package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestBox(t *testing.T) *Box {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	b, err := New(key)
	require.NoError(t, err)
	return b
}

// seal 以 7 字节为步长写入（覆盖跨块缓冲），返回完整密文。
func seal(t *testing.T, b *Box, name string, plain []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := b.SealStream(&out, name)
	require.NoError(t, err)
	for p := plain; len(p) > 0; {
		n := min(len(p), 7)
		if len(p) > 3*streamChunk {
			n = streamChunk + 13 // 大输入用大步长，免得测试太慢
		}
		k, err := w.Write(p[:n])
		require.NoError(t, err)
		require.Equal(t, n, k)
		p = p[n:]
	}
	require.NoError(t, w.Close())
	return out.Bytes()
}

func open(b *Box, name string, sealed []byte) ([]byte, error) {
	r, err := b.OpenStream(bytes.NewReader(sealed), name)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// frames 把密文拆成文件头与各帧（含 4 字节长度）。
func frames(t *testing.T, sealed []byte) (head []byte, fs [][]byte) {
	t.Helper()
	head, rest := sealed[:len(streamMagic)+streamSaltLen], sealed[len(streamMagic)+streamSaltLen:]
	for len(rest) > 0 {
		require.GreaterOrEqual(t, len(rest), 4)
		n := 4 + int(binary.BigEndian.Uint32(rest[:4]))
		require.GreaterOrEqual(t, len(rest), n)
		fs = append(fs, rest[:n])
		rest = rest[n:]
	}
	return head, fs
}

func join(head []byte, fs ...[]byte) []byte {
	out := append([]byte(nil), head...)
	for _, f := range fs {
		out = append(out, f...)
	}
	return out
}

func TestStreamRoundTrip(t *testing.T) {
	b := newTestBox(t)
	for _, size := range []int{0, 1, streamChunk - 1, streamChunk, streamChunk + 1, 200 << 10} {
		plain := make([]byte, size)
		_, err := rand.Read(plain)
		require.NoError(t, err)

		sealed := seal(t, b, "browser-profile/bpf_X/1", plain)
		assert.Equal(t, streamMagic, string(sealed[:4]))
		_, fs := frames(t, sealed)
		assert.Len(t, fs, max(1, (size+streamChunk-1)/streamChunk), "size=%d", size)

		got, err := open(b, "browser-profile/bpf_X/1", sealed)
		require.NoError(t, err, "size=%d", size)
		assert.True(t, bytes.Equal(plain, got), "size=%d", size)
	}
}

func TestStreamHidesPlaintextAndUsesFreshSalt(t *testing.T) {
	b := newTestBox(t)
	plain := bytes.Repeat([]byte("cookie-marker-0123456789 "), 1000)
	s1 := seal(t, b, "n", plain)
	s2 := seal(t, b, "n", plain)
	assert.False(t, bytes.Contains(s1, []byte("cookie-marker")))
	assert.NotEqual(t, s1, s2, "每条流的 salt 随机")
}

func TestStreamRejectsTampering(t *testing.T) {
	b := newTestBox(t)
	plain := make([]byte, 200<<10)
	_, err := rand.Read(plain)
	require.NoError(t, err)
	sealed := seal(t, b, "n", plain)
	head, fs := frames(t, sealed)
	require.Len(t, fs, 4)

	flipped := append([]byte(nil), sealed...)
	flipped[len(head)+4+100] ^= 0x01

	cases := map[string][]byte{
		"截掉末帧":    join(head, fs[:3]...),
		"末帧截断一半":  join(head, fs[0], fs[1], fs[2], fs[3][:len(fs[3])/2]),
		"翻转一位":    flipped,
		"交换两帧":    join(head, fs[1], fs[0], fs[2], fs[3]),
		"末帧后多余数据": append(append([]byte(nil), sealed...), 0),
		"重复末帧":    join(head, fs[0], fs[1], fs[2], fs[3], fs[3]),
		"只有文件头":   head,
	}
	for name, data := range cases {
		_, err := open(b, "n", data)
		assert.ErrorIs(t, err, ErrStreamCorrupt, name)
	}

	_, err = open(b, "other", sealed)
	assert.ErrorIs(t, err, ErrStreamCorrupt, "name 不符")

	_, err = open(newTestBox(t), "n", sealed)
	assert.ErrorIs(t, err, ErrStreamCorrupt, "主密钥不符")

	bad := append([]byte("XXXX"), sealed[4:]...)
	_, err = open(b, "n", bad)
	assert.ErrorIs(t, err, ErrStreamCorrupt, "文件头不符")
}

func TestStreamWithoutCloseFailsToOpen(t *testing.T) {
	b := newTestBox(t)
	for _, size := range []int{1, streamChunk + 10} {
		var out bytes.Buffer
		w, err := b.SealStream(&out, "n")
		require.NoError(t, err)
		_, err = w.Write(make([]byte, size))
		require.NoError(t, err)
		_, err = open(b, "n", out.Bytes())
		assert.ErrorIs(t, err, ErrStreamCorrupt, "size=%d", size)
	}
}

func TestStreamWriteAfterClose(t *testing.T) {
	b := newTestBox(t)
	var out bytes.Buffer
	w, err := b.SealStream(&out, "n")
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, w.Close(), "重复 Close 无操作")
	_, err = w.Write([]byte("x"))
	assert.Error(t, err)
}
