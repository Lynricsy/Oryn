package artifacts

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// fetchCaller 是带 source 取回的线程调用方：srcs 是 source 路径 → 字节，fetched 记下被取回的路径。
func fetchCaller(thread string, srcs map[string][]byte, fetched *[]string) Caller {
	c := threadCaller(thread)
	c.Fetch = func(_ context.Context, path string) (io.ReadCloser, int64, error) {
		b, ok := srcs[path]
		if !ok {
			return nil, 0, opErr(CodeInvalid, "取不到 source %s", path)
		}
		if fetched != nil {
			*fetched = append(*fetched, path)
		}
		return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
	}
	return c
}

// M31 C3：二进制类型只能用 source 写入口这一个文件；html、content、capabilities、多文件、错路径都回 invalid 并指明写法。
func TestAgentOfficeCreate(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	c := fetchCaller("thr_a", map[string][]byte{"/w/a.docx": docxBytes(t), "/w/a.xlsx": xlsxBytes(t), "/w/a.pdf": pdfBytes()}, nil)

	r := mustCall(t, e.s, c, OpCreate, `{"title":"季度报告","type":"docx","files":[{"path":"document.docx","source":"/w/a.docx"}]}`)
	out := textObj(t, r)
	assert.Equal(t, KindDocument, out["kind"])
	assert.EqualValues(t, 1, out["pages"])
	a, err := e.s.Get(context.Background(), out["id"].(string))
	require.NoError(t, err)
	assert.Equal(t, TypeDocx, a.Type)
	assert.Equal(t, string(docxBytes(t)), readFile(t, e.s, a.ID, 0, EntryDocx))

	for name, c2 := range map[string]struct{ params, want string }{
		"content":      {`{"title":"x","type":"docx","files":[{"path":"document.docx","content":"hi"}]}`, "不能用 content 内联"},
		"html":         {`{"title":"x","type":"docx","html":"<p>"}`, "Word 文档 artifact 不接受 html"},
		"capabilities": {`{"title":"x","type":"docx","capabilities":["db"],"files":[{"path":"document.docx","source":"/w/a.docx"}]}`, "不接受 capabilities"},
		"two files":    {`{"title":"x","type":"docx","files":[{"path":"document.docx","source":"/w/a.docx"},{"path":"a.png","source":"/w/a.pdf"}]}`, "只能有 document.docx 这一个文件"},
		"wrong path":   {`{"title":"x","type":"docx","files":[{"path":"report.docx","source":"/w/a.docx"}]}`, "只能有 document.docx 这一个文件"},
		"no files":     {`{"title":"x","type":"pdf"}`, `{path: "document.pdf", source`},
		"wrong kind":   {`{"title":"x","type":"docx","files":[{"path":"document.docx","source":"/w/a.xlsx"}]}`, "内容是 Excel 表格（xlsx）"},
	} {
		_, err := agentCall(t, e.s, c, OpCreate, c2.params)
		require.Equal(t, CodeInvalid, code(err), name)
		assert.Contains(t, err.Error(), c2.want, name)
		if name != "wrong kind" {
			assert.Contains(t, err.Error(), "office 主题", name)
		}
	}
}

// M31 C3：Office 入口的 source 上限是 MaxOfficeFileBytes，普通文件仍是 MaxFileBytes。
func TestAgentOfficeSourceLimit(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	big := append(pdfBytes(), make([]byte, MaxFileBytes)...)
	c := fetchCaller("thr_a", map[string][]byte{"/w/big.pdf": big, "/w/big.css": big}, nil)
	out := textObj(t, mustCall(t, e.s, c, OpCreate, `{"title":"大 PDF","type":"pdf","files":[{"path":"document.pdf","source":"/w/big.pdf"}]}`))
	f, err := e.s.Files(context.Background(), out["id"].(string), 0)
	require.NoError(t, err)
	assert.EqualValues(t, len(big), f[0].Size)

	_, err = agentCall(t, e.s, c, OpCreate, `{"title":"页","html":"<p>","files":[{"path":"big.css","source":"/w/big.css"}]}`)
	require.Equal(t, CodeTooLarge, code(err))
	assert.Contains(t, err.Error(), fmt.Sprintf("%d MiB", MaxFileBytes>>20))
}

// M31 C3：read_artifact 对二进制类型只回元数据与 binary / hint，不带正文；update_artifact 只接受 title、note 与入口的 source。
func TestAgentOfficeReadUpdate(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	v2 := ooxmlZip(t, partPpt, ctPptx, "ppt/presentation.xml", "ppt/slides/slide1.xml", "ppt/slides/slide2.xml", "ppt/slides/slide3.xml", "docProps/app.xml")
	c := fetchCaller("thr_a", map[string][]byte{"/w/a.pptx": pptxBytes(t), "/w/b.pptx": v2}, nil)
	id := textObj(t, mustCall(t, e.s, c, OpCreate, `{"title":"路演","type":"pptx","files":[{"path":"presentation.pptx","source":"/w/a.pptx"}]}`))["id"].(string)

	r := mustCall(t, e.s, c, OpRead, `{"id":"`+id+`"}`)
	assert.NotContains(t, r.Text, "\n\n", "不带正文")
	var h map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.Text), &h))
	assert.Equal(t, true, h["binary"])
	assert.Equal(t, "这是二进制 Office 文件；用 pull_artifact 取回到本机后再处理。", h["hint"])
	assert.Equal(t, TypePptx, h["type"])
	assert.Equal(t, EntryPptx, h["path"])

	for name, c2 := range map[string]struct{ params, want string }{
		"edits":        {`{"id":"` + id + `","baseVersion":1,"edits":[{"oldText":"a","newText":"b"}]}`, "不能用 edits"},
		"html":         {`{"id":"` + id + `","baseVersion":1,"html":"<p>"}`, "不接受 html"},
		"capabilities": {`{"id":"` + id + `","baseVersion":1,"capabilities":[]}`, "不接受 capabilities"},
		"canvas":       {`{"id":"` + id + `","baseVersion":1,"canvas":[{"op":"remove","id":"x"}]}`, "不能用 canvas 操作"},
		"remove":       {`{"id":"` + id + `","baseVersion":1,"files":[{"path":"presentation.pptx","remove":true}]}`, "不能删除 presentation.pptx"},
		"content":      {`{"id":"` + id + `","baseVersion":1,"files":[{"path":"presentation.pptx","content":"x"}]}`, "不能用 content 内联"},
		"other file":   {`{"id":"` + id + `","baseVersion":1,"files":[{"path":"notes.txt","source":"/w/b.pptx"}]}`, "只能有 presentation.pptx 这一个文件"},
	} {
		_, err := agentCall(t, e.s, c, OpUpdate, c2.params)
		require.Equal(t, CodeInvalid, code(err), name)
		assert.Contains(t, err.Error(), c2.want, name)
	}

	out := textObj(t, mustCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","baseVersion":1,"note":"改了第二页","files":[{"path":"presentation.pptx","source":"/w/b.pptx"}]}`))
	assert.EqualValues(t, 2, out["version"])
	assert.EqualValues(t, 3, out["pages"])
	assert.Equal(t, string(v2), readFile(t, e.s, id, 0, EntryPptx))
	out = textObj(t, mustCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","title":"路演 v2"}`))
	assert.EqualValues(t, 2, out["version"], "改名不产生版本")
}

// M31 C3：pull_artifact 按 PullPageBytes 分页，拼回的字节与 sha256 等于原文件；cursor 与 path 的错误回 invalid。
func TestAgentPull(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	mkThread(t, e.st, "thr_b", "")
	src := append(pdfBytes(), make([]byte, 2*PullPageBytes+12345)...)
	_, _ = rand.Read(src[len(pdfBytes()):])
	c := fetchCaller("thr_a", map[string][]byte{"/w/a.pdf": src}, nil)
	id := textObj(t, mustCall(t, e.s, c, OpCreate, `{"title":"手册","type":"pdf","files":[{"path":"document.pdf","source":"/w/a.pdf"}]}`))["id"].(string)
	sum := sha256.Sum256(src)

	var got []byte
	cursor, pages := "", 0
	for {
		params := `{"id":"` + id + `"}`
		if cursor != "" {
			params = `{"id":"` + id + `","version":1,"path":"document.pdf","cursor":"` + cursor + `"}`
		}
		r := mustCall(t, e.s, c, OpPull, params)
		var pg struct {
			ID, Path, Sha256, Data, Next string
			Version                      int
			Size, Offset                 int64
		}
		require.NoError(t, json.Unmarshal([]byte(r.Text), &pg))
		assert.Equal(t, id, pg.ID)
		assert.Equal(t, 1, pg.Version)
		assert.Equal(t, EntryPDF, pg.Path)
		assert.EqualValues(t, len(src), pg.Size)
		assert.Equal(t, hex.EncodeToString(sum[:]), pg.Sha256)
		assert.EqualValues(t, len(got), pg.Offset)
		data, err := base64.StdEncoding.DecodeString(pg.Data)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(data), PullPageBytes)
		got = append(got, data...)
		d := r.Details.(map[string]any)
		assert.Equal(t, OpPull, d["op"])
		assert.NotContains(t, d, "data", "转录卡片不带字节")
		pages++
		if pg.Next == "" {
			break
		}
		cursor = pg.Next
	}
	assert.Equal(t, 3, pages)
	assert.Equal(t, sum, sha256.Sum256(got))

	for name, c2 := range map[string]struct{ params, want string }{
		"bad cursor":    {`{"id":"` + id + `","cursor":"abc"}`, "cursor"},
		"neg cursor":    {`{"id":"` + id + `","cursor":"-1"}`, "cursor"},
		"cursor beyond": {`{"id":"` + id + `","cursor":"` + fmt.Sprint(len(src)+1) + `"}`, "超出了文件长度"},
		"missing path":  {`{"id":"` + id + `","path":"nope.txt"}`, "没有文件 nope.txt"},
		"neg version":   {`{"id":"` + id + `","version":-1}`, "version"},
		"unknown field": {`{"id":"` + id + `","to":"/tmp/x"}`, "to"},
	} {
		_, err := agentCall(t, e.s, c, OpPull, c2.params)
		require.Equal(t, CodeInvalid, code(err), name)
		assert.Contains(t, err.Error(), c2.want, name)
	}
	// 读到末尾的 cursor 给出空页；任何类型的文件都能取；访问范围同 read_artifact
	r := mustCall(t, e.s, c, OpPull, `{"id":"`+id+`","cursor":"`+fmt.Sprint(len(src))+`"}`)
	end := textObj(t, r)
	assert.Equal(t, "", end["data"])
	assert.Equal(t, "", end["next"])
	page := textObj(t, mustCall(t, e.s, threadCaller("thr_a"), OpCreate, `{"title":"页","html":"<h1>hi</h1>"}`))["id"].(string)
	r = mustCall(t, e.s, c, OpPull, `{"id":"`+page+`"}`)
	assert.Contains(t, r.Text, `"data":"`+base64.StdEncoding.EncodeToString([]byte("<h1>hi</h1>"))+`"`)
	_, err := agentCall(t, e.s, threadCaller("thr_b"), OpPull, `{"id":"`+id+`"}`)
	assert.Equal(t, CodeNotFound, code(err))
}

// M31：read_artifact_guide 有 office 主题；未知操作的可用操作列表里有 pull_artifact。
func TestAgentOfficeGuide(t *testing.T) {
	e := newEnv(t)
	r := mustCall(t, e.s, Caller{Scope: ScopeAll}, OpReadGuide, `{"topic":"office"}`)
	assert.Contains(t, r.Text, "oryn-office 技能")
	assert.Contains(t, r.Text, "pull_artifact")
	_, err := agentCall(t, e.s, Caller{Scope: ScopeAll}, "nope", `{}`)
	assert.Contains(t, err.Error(), OpPull)
	_, err = agentCall(t, e.s, Caller{Scope: ScopeAll}, OpReadGuide, `{"topic":"nope"}`)
	assert.Contains(t, err.Error(), "office")
}

// M31 4.2：orb 的 source 经 FetchLimit 以 Office 上限取回；普通文件超过 MaxFileBytes 仍是 too_large。
func TestAgentOrbOfficeSource(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	require.NoError(t, e.st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	mkOrb(t, e, "orb_a", "")
	mkOrbThread(t, e, "thr_a", "orb_a", "")
	big := string(append(pdfBytes(), make([]byte, MaxFileBytes)...))
	links := fakeLinks{}
	e.s.RegisterOrbMethod(links, fakeSource{"orb_a:/w/big.pdf": big, "orb_a:/w/big.css": big})
	fn := links[string(apigen.MethodArtifactCall)]
	call := func(op, params string) (any, error) {
		raw, err := json.Marshal(map[string]any{"threadId": "thr_a", "op": op, "params": json.RawMessage(params)})
		require.NoError(t, err)
		return fn(ctx, "orb_a", raw)
	}
	res, err := call(OpCreate, `{"title":"大 PDF","type":"pdf","files":[{"path":"document.pdf","source":"/w/big.pdf"}]}`)
	require.NoError(t, err)
	assert.Equal(t, TypePDF, mustGet(t, e, res.(apigen.LinkCtlArtifactCallResult).Details["id"].(string)).Type)
	_, err = call(OpCreate, `{"title":"页","html":"<p>","files":[{"path":"big.css","source":"/w/big.css"}]}`)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "超过单文件上限 20 MiB"), err.Error())
}

func mustGet(t *testing.T, e *env, id string) Artifact {
	t.Helper()
	a, err := e.s.Get(context.Background(), id)
	require.NoError(t, err)
	return a
}
