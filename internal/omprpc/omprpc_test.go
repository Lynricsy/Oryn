package omprpc

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chunkLines 模拟 omp encodeChunkedRpcFrames：把 frame 切成 count 块。
func chunkLines(t *testing.T, id string, frame []byte, count int) [][]byte {
	t.Helper()
	size := (len(frame) + count - 1) / count
	var out [][]byte
	for i := range count {
		lo, hi := i*size, min((i+1)*size, len(frame))
		line, err := json.Marshal(ChunkFrame{
			Type: TypeChunk, ChunkID: id, Index: i, Count: count, ByteLength: len(frame),
			Data: base64.StdEncoding.EncodeToString(frame[lo:hi]),
		})
		require.NoError(t, err)
		out = append(out, line)
	}
	return out
}

func TestReaderPlainFrames(t *testing.T) {
	in := "{\"type\":\"ready\"}\n\n  \r\n{\"type\":\"agent_start\"}\r\n"
	r := NewReader(strings.NewReader(in), 0)
	l1, err := r.ReadLine()
	require.NoError(t, err)
	assert.Equal(t, `{"type":"ready"}`, string(l1))
	l2, err := r.ReadLine()
	require.NoError(t, err)
	assert.Equal(t, `{"type":"agent_start"}`, string(l2))
	_, err = r.ReadLine()
	assert.ErrorIs(t, err, io.EOF)
}

func TestReaderLineCap(t *testing.T) {
	ok := `{"a":"` + strings.Repeat("x", MaxFrameBytes-9) + `"}` // 恰好 MaxFrameBytes-1 字节 + 换行
	require.Len(t, ok, MaxFrameBytes-1)
	big := `{"a":"` + strings.Repeat("y", MaxFrameBytes) + `"}`
	in := ok + "\n" + big + "\n" + `{"type":"after"}` + "\n" + `{"partial"`
	r := NewReader(strings.NewReader(in), 0)

	l, err := r.ReadLine()
	require.NoError(t, err)
	assert.Len(t, l, MaxFrameBytes-1)

	_, err = r.ReadLine()
	assert.ErrorIs(t, err, ErrLineTooLong)

	l, err = r.ReadLine()
	require.NoError(t, err, "溢出行之后应能继续读取")
	assert.Equal(t, `{"type":"after"}`, string(l))

	_, err = r.ReadLine()
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestWriter(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, 64)
	require.NoError(t, w.WriteFrame(map[string]string{"type": "x", "s": "<&>"}))
	assert.Equal(t, "{\"s\":\"<&>\",\"type\":\"x\"}\n", buf.String())
	assert.ErrorIs(t, w.WriteFrame(map[string]string{"s": strings.Repeat("z", 100)}), ErrLineTooLong)
	assert.Error(t, w.WriteRaw([]byte("{}\n{}")))
	require.NoError(t, w.WriteRaw([]byte(`{"type":"y"}`)))
	assert.True(t, strings.HasSuffix(buf.String(), "{\"type\":\"y\"}\n"))
}

func TestReassemblePlainFrame(t *testing.T) {
	var ra Reassembler
	f, err := ra.Push([]byte(`{"type":"agent_start"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"agent_start"}`, string(f))

	_, err = ra.Push([]byte(`[1,2]`))
	assert.Error(t, err)
	_, err = ra.Push([]byte(`not json`))
	assert.Error(t, err)
}

func TestReassembleThreeChunks(t *testing.T) {
	frame := []byte(fmt.Sprintf(`{"type":"message_end","text":%q}`, strings.Repeat("数据", 5000)))
	lines := chunkLines(t, "rpc-1", frame, 3)
	require.Len(t, lines, 3)

	var ra Reassembler
	for i, l := range lines[:2] {
		f, err := ra.Push(l)
		require.NoError(t, err, "chunk %d", i)
		assert.Nil(t, f)
	}
	f, err := ra.Push(lines[2])
	require.NoError(t, err)
	assert.Equal(t, frame, f)

	// 经 Decoder 走完整流（前后夹普通帧）。
	var in bytes.Buffer
	in.WriteString(`{"type":"ready"}` + "\n")
	for _, l := range lines {
		in.Write(l)
		in.WriteByte('\n')
	}
	in.WriteString(`{"type":"agent_end"}` + "\n")
	d := NewDecoder(&in)
	var got []string
	for {
		f, err := d.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		e, err := ParseEnvelope(f)
		require.NoError(t, err)
		got = append(got, e.Type)
	}
	assert.Equal(t, []string{"ready", "message_end", "agent_end"}, got)
}

func TestReassembleOverCap(t *testing.T) {
	var ra Reassembler
	line, _ := json.Marshal(ChunkFrame{Type: TypeChunk, ChunkID: "big", Index: 0, Count: 2, ByteLength: MaxReassembledBytes + 1, Data: "e30="})
	_, err := ra.Push(line)
	var ce *ChunkError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "big", ce.ChunkID)

	// 被丢弃的 chunkId 的后续分块静默忽略。
	line, _ = json.Marshal(ChunkFrame{Type: TypeChunk, ChunkID: "big", Index: 1, Count: 2, ByteLength: 10, Data: "e30="})
	f, err := ra.Push(line)
	assert.NoError(t, err)
	assert.Nil(t, f)

	// 实际数据超过声明长度。
	lines := chunkLines(t, "over", []byte(`{"type":"x","pad":"0123456789"}`), 2)
	var c ChunkFrame
	require.NoError(t, json.Unmarshal(lines[0], &c))
	c.ByteLength = 5
	l0, _ := json.Marshal(c)
	_, err = ra.Push(l0)
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "over", ce.ChunkID)
}

func TestReassembleMissingChunk(t *testing.T) {
	frame := []byte(`{"type":"tool_execution_end","result":"` + strings.Repeat("r", 300) + `"}`)
	lines := chunkLines(t, "rpc-7", frame, 3)

	t.Run("out of order", func(t *testing.T) {
		var ra Reassembler
		_, err := ra.Push(lines[0])
		require.NoError(t, err)
		_, err = ra.Push(lines[2])
		var ce *ChunkError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, "rpc-7", ce.ChunkID)
		// 迟到的块属于已丢弃的 chunkId，忽略。
		f, err := ra.Push(lines[1])
		assert.NoError(t, err)
		assert.Nil(t, f)
		// 之后的普通帧照常通过。
		f, err = ra.Push([]byte(`{"type":"agent_end"}`))
		require.NoError(t, err)
		assert.NotNil(t, f)
	})

	t.Run("interrupted by plain frame", func(t *testing.T) {
		d := NewDecoder(strings.NewReader(string(lines[0]) + "\n" + string(lines[1]) + "\n" + `{"type":"agent_end"}` + "\n"))
		_, err := d.Next()
		require.Error(t, err)
		assert.True(t, IsRecoverable(err))
		var ce *ChunkError
		require.ErrorAs(t, err, &ce)
		f, err := d.Next()
		require.NoError(t, err)
		assert.JSONEq(t, `{"type":"agent_end"}`, string(f))
		_, err = d.Next()
		assert.ErrorIs(t, err, io.EOF)
	})

	t.Run("interrupted by new sequence", func(t *testing.T) {
		other := chunkLines(t, "rpc-8", []byte(`{"type":"ok"}`), 2)
		var ra Reassembler
		_, err := ra.Push(lines[0])
		require.NoError(t, err)
		_, err = ra.Push(other[0])
		var ce *ChunkError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, "rpc-7", ce.ChunkID)
		f, err := ra.Push(other[1])
		require.NoError(t, err)
		assert.JSONEq(t, `{"type":"ok"}`, string(f))
	})
}

func TestEnvelope(t *testing.T) {
	e, err := ParseEnvelope([]byte(`{"id":"7","type":"response","command":"prompt","success":false,"error":"boom","code":"x"}`))
	require.NoError(t, err)
	assert.True(t, e.IsResponse())
	assert.False(t, e.OK())
	assert.Equal(t, "boom", e.ErrorText())
	assert.Equal(t, "x", e.Code)
	assert.Equal(t, "7", e.ID)

	e, err = ParseEnvelope([]byte(`{"type":"prompt_result","status":"error","error":{"message":"rate limited"}}`))
	require.NoError(t, err)
	assert.Nil(t, e.Success)
	assert.Equal(t, "rate limited", e.ErrorText())

	_, err = ParseEnvelope([]byte(`"x"`))
	assert.Error(t, err)
}

func TestSetID(t *testing.T) {
	out, err := SetID([]byte(`{"type":"get_state"}`), "c-1")
	require.NoError(t, err)
	assert.Equal(t, `{"id":"c-1","type":"get_state"}`, string(out))

	out, err = SetID([]byte(`{}`), "c-2")
	require.NoError(t, err)
	assert.Equal(t, `{"id":"c-2"}`, string(out))

	out, err = SetID([]byte(`{"id":"old","type":"prompt","message":"<hi>"}`), "c-3")
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"c-3","type":"prompt","message":"<hi>"}`, string(out))
}

func TestEncodeChunksRoundTrip(t *testing.T) {
	frame := []byte(`{"type":"agent_end","blob":"` + strings.Repeat("q", 2*MaxFrameBytes) + `"}`)
	lines, err := EncodeChunks(frame, "rpc-1")
	require.NoError(t, err)
	require.Greater(t, len(lines), 8)
	var ra Reassembler
	var out []byte
	for _, l := range lines {
		assert.LessOrEqual(t, len(l)+1, MaxFrameBytes)
		f, err := ra.Push(l)
		require.NoError(t, err)
		if f != nil {
			out = f
		}
	}
	assert.Equal(t, frame, out)

	_, err = EncodeChunks(make([]byte, MaxReassembledBytes+1), "x")
	assert.Error(t, err)
}
