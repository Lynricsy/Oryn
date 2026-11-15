package coord

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/attachments"
	"github.com/Lynricsy/Oryn/internal/library"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// fakeLibHost 扮演 *library.Syncer：PutFrom 从 files（orb 内路径 → 内容）取字节存进真实的文件库。
type fakeLibHost struct {
	lib   *library.Service
	mu    sync.Mutex
	files map[string][]byte
	calls []string // orbID + " " + path
}

func (h *fakeLibHost) PutFrom(ctx context.Context, orbID, path string, p library.PutParams) (library.File, error) {
	h.mu.Lock()
	h.calls = append(h.calls, orbID+" "+path)
	b, ok := h.files[path]
	h.mu.Unlock()
	if !ok {
		return library.File{}, &link.CtlError{Code: "not_found", Message: path + " does not exist"}
	}
	p.R = bytes.NewReader(b)
	return h.lib.Put(ctx, p)
}

// withLibrary 给 env 接上真实的文件库与附件服务（建线程时绑定 create_thread{files} 复制出的附件）。
func (e *env) withLibrary() (*library.Service, *attachments.Service, *fakeLibHost) {
	e.t.Helper()
	dir := e.t.TempDir()
	att, err := attachments.New(e.st, dir, nil)
	require.NoError(e.t, err)
	e.th.SetAttachments(att)
	lib, err := library.New(e.st, filepath.Join(dir, "lib"), att)
	require.NoError(e.t, err)
	host := &fakeLibHost{lib: lib, files: map[string][]byte{}}
	e.c.SetLibrary(lib, host)
	return lib, att, host
}

func errText(t *testing.T, r threads.HostToolResult, code string) {
	t.Helper()
	assert.True(t, r.IsError, r.Text)
	assert.True(t, strings.HasPrefix(r.Text, code+":"), r.Text)
}

// M13 4.4：save_file 经协调 orb 取回 ~/library/out/ 下的文件存库（source=assistant，属于本对话）；越界路径在
// 发起取回之前就被拒；list_files 只列本对话与共享的文件（不可信包裹）；share_file 切换 scope；Space 协调者没有这些工具。
func TestLibraryTools(t *testing.T) {
	e := newEnv(t, true)
	lib, _, host := e.withLibrary()
	asst := e.assistant()
	other := e.assistant()
	host.files["~/library/out/report.md"] = []byte("# Q3\n")

	r := e.call(asst, ToolSaveFile, `{"path":"~/library/out/report.md"}`, true)
	saved := details(t, r)
	id := saved["id"].(string)
	assert.Equal(t, library.ScopeConversation, saved["scope"])
	assert.Equal(t, "~/library/threads/"+asst.ID+"/"+id+"/report.md", saved["path"])
	f, err := lib.Get(e.ctx, id)
	require.NoError(t, err)
	assert.Equal(t, library.SourceAssistant, f.Source)
	assert.Equal(t, asst.ID, f.ThreadID)
	assert.Equal(t, int64(5), f.Size)
	assert.Equal(t, []string{e.coordOrb + " ~/library/out/report.md"}, host.calls, "从对话所在的协调 orb 取回")

	r = e.call(asst, ToolSaveFile, `{"path":"~/library/out/report.md","name":"q3.md","shared":true}`, true)
	shared := details(t, r)
	assert.Equal(t, library.ScopeShared, shared["scope"])
	assert.Equal(t, "q3.md", shared["name"])

	// 越界路径：不发起取回
	for _, args := range []string{`{"path":"../shared/x"}`, `{"path":"~/library/out/../shared/x"}`, `{"path":"~/library/shared/afl_x/a.txt"}`, `{"path":"~/.omp/agent/config.yml"}`, `{}`} {
		errText(t, e.call(asst, ToolSaveFile, args, true), CodeInvalid)
	}
	errText(t, e.call(asst, ToolSaveFile, `{"path":"~/library/out/missing.md"}`, true), "not_found")
	assert.Len(t, host.calls, 3)

	// list_files：本对话的与共享的；另一个对话只看到共享的
	r = e.call(asst, ToolListFiles, `{}`, true)
	assert.True(t, strings.HasPrefix(r.Text, "<untrusted-thread-content"), r.Text)
	assert.Len(t, details(t, r)["files"], 2)
	assert.Len(t, details(t, e.call(other, ToolListFiles, `{}`, true))["files"], 1)
	assert.Len(t, details(t, e.call(asst, ToolListFiles, `{"scope":"conversation"}`, true))["files"], 1)

	// share_file：自己的文件可以共享再改回私有；别的对话不能把它的私有文件改掉，也不能把共享文件改成私有
	r = e.call(asst, ToolShareFile, `{"id":"`+id+`","shared":true}`, true)
	assert.Equal(t, "~/library/shared/"+id+"/report.md", details(t, r)["path"])
	errText(t, e.call(other, ToolShareFile, `{"id":"`+id+`","shared":false}`, true), CodeForbidden)
	details(t, e.call(asst, ToolShareFile, `{"id":"`+id+`","shared":false}`, true))
	errText(t, e.call(other, ToolShareFile, `{"id":"`+id+`","shared":true}`, true), CodeNotFound)
	f, err = lib.Get(e.ctx, id)
	require.NoError(t, err)
	assert.Equal(t, library.ScopeConversation, f.Scope)

	chat := e.spaceChat(e.space("Alpha", ""))
	errText(t, e.call(chat, ToolListFiles, `{}`, true), "unknown_tool")
	names := map[string]bool{}
	for _, d := range ToolDefs(true) {
		names[d["name"].(string)] = true
	}
	assert.False(t, names[ToolSaveFile] || names[ToolListFiles] || names[ToolShareFile], "Space 协调者没有文件工具")
}

// M13 4.4：create_thread{files} 把库文件复制成附件，新线程的首条提示词带 orynAttachments，附件 sha256 与库文件一致；
// 别的对话的私有文件不能带。
func TestCreateThreadWithFiles(t *testing.T) {
	e := newEnv(t, true)
	lib, att, _ := e.withLibrary()
	asst := e.assistant()
	other := e.assistant()
	f, err := lib.Put(e.ctx, library.PutParams{Name: "brief.pdf", ThreadID: asst.ID, Source: library.SourceUpload, R: strings.NewReader("%PDF-1.4 brief")})
	require.NoError(t, err)

	r := e.call(asst, ToolCreateThread, `{"prompt":"read the brief","files":["`+f.ID+`"]}`, true)
	newID := details(t, r)["threadId"].(string)
	row := e.row(newID)
	var frames []map[string]any
	require.NoError(t, json.Unmarshal(row.StartupFrames, &frames))
	var attIDs []any
	for _, fr := range frames {
		if fr["type"] == "prompt" {
			attIDs, _ = fr["orynAttachments"].([]any)
		}
	}
	require.Len(t, attIDs, 1, "首条提示词带 orynAttachments：%v", frames)
	a, err := att.Get(e.ctx, attIDs[0].(string))
	require.NoError(t, err)
	assert.Equal(t, f.Sha256, a.Sha256)
	assert.Equal(t, f.Name, a.Name)
	require.NotNil(t, a.ThreadID)
	assert.Equal(t, newID, *a.ThreadID, "附件绑定到新线程")

	errText(t, e.call(other, ToolCreateThread, `{"prompt":"steal","files":["`+f.ID+`"]}`, true), CodeNotFound)
}

// M13 4.6：orb 内 `oryn library put` 经 library.put 存入的文件是 scope=shared、source=orb、sourceThreadId=调用方；
// 调用方须属于发起的 orb；路径须是绝对路径。
func TestOrbLibraryPut(t *testing.T) {
	e := newEnv(t, true)
	lib, _, host := e.withLibrary()
	w := e.work("make a report")
	orbID := deref(e.row(w.ID).OrbID)
	host.files["/home/user/workspace/out.csv"] = []byte("a,b\n")

	params, _ := json.Marshal(apigen.LinkCtlLibraryPutParams{ThreadID: w.ID, Path: "/home/user/workspace/out.csv"})
	res, err := e.c.onLibraryPut(e.ctx, orbID, params)
	require.NoError(t, err)
	af := res.(apigen.AssistantFile)
	assert.Equal(t, apigen.Shared, af.Scope)
	assert.Equal(t, apigen.AssistantFileSourceOrb, af.Source)
	require.NotNil(t, af.SourceThreadID)
	assert.Equal(t, w.ID, *af.SourceThreadID)
	assert.Equal(t, "out.csv", af.Name)
	assert.Nil(t, af.ThreadID, "不属于任何助手对话")
	f, err := lib.Get(e.ctx, af.ID)
	require.NoError(t, err)
	assert.Equal(t, library.ScopeShared, f.Scope)
	assert.Equal(t, library.SourceOrb, f.Source)
	assert.Equal(t, w.ID, f.SourceThreadID)
	assert.Equal(t, []string{orbID + " /home/user/workspace/out.csv"}, host.calls, "从发起请求的 orb 取回")

	asst := e.assistant()
	assert.Len(t, details(t, e.call(asst, ToolListFiles, `{}`, true))["files"], 1, "所有助手对话都能读到")

	_, err = e.c.onLibraryPut(e.ctx, "orb-other", params)
	var ce *link.CtlError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, CodeForbidden, ce.Code)
	rel, _ := json.Marshal(apigen.LinkCtlLibraryPutParams{ThreadID: w.ID, Path: "out.csv"})
	_, err = e.c.onLibraryPut(e.ctx, orbID, rel)
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "invalid_params", ce.Code)
}

// M13 4.3：助手的系统提示写明可读目录、唯一可写目录与存库方法；Space 协调者没有文件库。
func TestSystemPromptFiles(t *testing.T) {
	p := SystemPrompt(nil, "", "T-1")
	assert.Contains(t, p, "~/library/threads/T-1/")
	assert.Contains(t, p, "~/library/shared/")
	assert.Contains(t, p, "The only place you may write is ~/library/out/")
	assert.Contains(t, p, "save_file")
	assert.NotContains(t, p, "<FILES>")
	sp := SystemPrompt(&apigen.Space{Name: "Alpha"}, "", "T-2")
	assert.NotContains(t, sp, "~/library")
	assert.Contains(t, sp, "do not use the read or write tools")
}
