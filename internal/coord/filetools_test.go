package coord

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/library"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// fsReq 是 fsMachines 收到的一次 orbd 请求。
type fsReq struct {
	orb, method, path, workdir, body string
	query                            map[string]string
}

// fsMachines 扮演 orbd 本机 API：按请求路径返回预置应答（files 是 orb 内的文件），记下请求、EnsureUsable 的特性与下发。
type fsMachines struct {
	fakeMachines
	mu       sync.Mutex
	files    map[string]string
	reqs     []fsReq
	features []string
	mux      *http.ServeMux
}

func newFSMachines() *fsMachines {
	m := &fsMachines{files: map[string]string{}, mux: http.NewServeMux()}
	fail := func(w http.ResponseWriter, status int, code, msg string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(apigen.Error{Code: code, Message: msg})
	}
	m.mux.HandleFunc("GET /fs/roots", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(apigen.FsRoots{Home: "/home/u", Workdir: "/home/u/repo"})
	})
	m.mux.HandleFunc("GET /fs/list", func(w http.ResponseWriter, r *http.Request) {
		l := apigen.FsListing{Path: r.URL.Query().Get("path")}
		for i := range 600 {
			l.Entries = append(l.Entries, apigen.FsEntry{Name: "f" + strings.Repeat("x", i%3), Type: apigen.FsEntryTypeFile, Size: int64(i)})
		}
		_ = json.NewEncoder(w).Encode(l)
	})
	m.mux.HandleFunc("GET /fs/stat", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		e := apigen.FsEntry{Path: p, Type: apigen.FsEntryTypeFile}
		switch p {
		case "/dir":
			e.Type = apigen.FsEntryTypeDir
		case "/big.txt":
			e.Size, e.Text = 2<<20, new(true)
		case "/bin.dat":
			e.Size, e.Text = 10, new(false)
		default:
			m.mu.Lock()
			c, ok := m.files[p]
			m.mu.Unlock()
			if !ok {
				fail(w, http.StatusNotFound, "not_found", p+" does not exist")
				return
			}
			e.Size, e.Text = int64(len(c)), new(true)
		}
		_ = json.NewEncoder(w).Encode(e)
	})
	m.mux.HandleFunc("GET /fs/file", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		c, ok := m.files[r.URL.Query().Get("path")]
		m.mu.Unlock()
		if !ok {
			fail(w, http.StatusNotFound, "not_found", "no such file")
			return
		}
		_, _ = io.WriteString(w, c)
	})
	m.mux.HandleFunc("PUT /fs/file", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		b, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		_, exists := m.files[p]
		if exists && r.URL.Query().Get("overwrite") != "true" {
			m.mu.Unlock()
			fail(w, http.StatusConflict, "exists", p+" already exists")
			return
		}
		m.files[p] = string(b)
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(apigen.FsEntry{Path: p, Name: p, Type: apigen.FsEntryTypeFile, Size: int64(len(b))})
	})
	m.mux.HandleFunc("GET /git/status", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(apigen.GitStatus{Repo: true, Files: []apigen.GitFileChange{{Path: "main.go"}}})
	})
	m.mux.HandleFunc("GET /git/diff", func(w http.ResponseWriter, r *http.Request) {
		patch := strings.Repeat("+ a line of the patch\n", 5000) // ≈105 KiB
		_ = json.NewEncoder(w).Encode(apigen.GitFileDiff{Path: r.URL.Query().Get("path"), Kind: apigen.Text, Patch: patch})
	})
	return m
}

func (m *fsMachines) EnsureUsable(_ context.Context, _, feature string, wake bool, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !wake {
		panic("文件工具应唤醒 orb")
	}
	m.features = append(m.features, feature)
	return nil
}

func (m *fsMachines) Do(_ context.Context, orbID string, req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	q := map[string]string{}
	for k, v := range req.URL.Query() {
		q[k] = v[0]
	}
	m.mu.Lock()
	m.reqs = append(m.reqs, fsReq{orb: orbID, method: req.Method, path: req.URL.Path, query: q, workdir: req.Header.Get(orbs.HeaderWorkdir), body: string(body)})
	m.mu.Unlock()
	in := req.Clone(req.Context())
	in.Body = io.NopCloser(bytes.NewReader(body))
	rec := httptest.NewRecorder()
	m.mux.ServeHTTP(rec, in)
	return rec.Result(), nil
}

// last 返回最近一次 orbd 请求。
func (m *fsMachines) last() fsReq {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reqs[len(m.reqs)-1]
}

// M19 2.4：list / read / write / get_thread_changes 经 orbd 本机 API 操作线程的 orb；读类结果带不可信包裹，写入写审计；
// Runner 线程带工作目录头；Space 协调者没有这些工具。
func TestThreadFileTools(t *testing.T) {
	e := newEnv(t, true)
	asst := e.assistant()
	w := e.work("go")
	orbID := deref(e.row(w.ID).OrbID)
	require.NotEmpty(t, orbID)

	assert.Equal(t, CodeCoordinatorUnavailable, errCodeOf(t, e.call(asst, ToolListThreadFiles, `{"threadId":"`+w.ID+`"}`, true)))
	m := newFSMachines()
	e.c.SetMachines(m)
	m.files["/home/u/repo/notes.txt"] = "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n"

	// list：缺省取工作目录，至多 500 项
	r := e.call(asst, ToolListThreadFiles, `{"threadId":"`+w.ID+`"}`, true)
	assert.True(t, strings.HasPrefix(r.Text, `<untrusted-thread-content thread="`+w.ID+`">`), r.Text)
	d := details(t, r)
	assert.Equal(t, "/home/u/repo", d["path"])
	assert.Len(t, d["entries"], threadListMax)
	assert.Equal(t, true, d["truncated"])
	assert.Equal(t, fsReq{orb: orbID, method: "GET", path: "/fs/list", query: map[string]string{"path": "/home/u/repo"}}, m.last())
	assert.Equal(t, "fs", m.features[0])
	details(t, e.call(asst, ToolListThreadFiles, `{"threadId":"`+w.ID+`","path":"~/x"}`, true))
	assert.Equal(t, "~/x", m.last().query["path"])

	// read：按行切片
	d = details(t, e.call(asst, ToolReadThreadFile, `{"threadId":"`+w.ID+`","path":"/home/u/repo/notes.txt","offset":3,"limit":2}`, true))
	assert.Equal(t, "l3\nl4", d["content"])
	assert.Equal(t, float64(10), d["totalLines"])
	assert.Equal(t, float64(5), d["nextOffset"])
	d = details(t, e.call(asst, ToolReadThreadFile, `{"threadId":"`+w.ID+`","path":"/home/u/repo/notes.txt","offset":9,"limit":5000}`, true))
	assert.Equal(t, "l9\nl10", d["content"])
	assert.NotContains(t, d, "nextOffset")
	d = details(t, e.call(asst, ToolReadThreadFile, `{"threadId":"`+w.ID+`","path":"/home/u/repo/notes.txt"}`, true))
	assert.Equal(t, strings.TrimSuffix(m.files["/home/u/repo/notes.txt"], "\n"), d["content"])
	for path, code := range map[string]string{
		"/bin.dat": CodeNotText, "/big.txt": CodeTooLarge, "/dir": CodeInvalid, "/missing": CodeNotFound,
	} {
		r := e.call(asst, ToolReadThreadFile, `{"threadId":"`+w.ID+`","path":"`+path+`"}`, true)
		assert.Equal(t, code, errCodeOf(t, r), path)
		if code == CodeNotText || code == CodeTooLarge {
			assert.Contains(t, r.Text, "fetch_thread_file", "提示改用 fetch_thread_file")
		}
	}
	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolReadThreadFile, `{"threadId":"`+w.ID+`","path":"/home/u/repo/notes.txt","offset":11}`, true)))

	// write：默认不覆盖；写审计
	d = details(t, e.call(asst, ToolWriteThreadFile, `{"threadId":"`+w.ID+`","path":"/home/u/new.md","content":"hello"}`, true))
	assert.Equal(t, float64(5), d["size"])
	last := m.last()
	assert.Equal(t, "PUT", last.method)
	assert.Equal(t, "hello", last.body)
	assert.Equal(t, "false", last.query["overwrite"])
	assert.Equal(t, "exists", errCodeOf(t, e.call(asst, ToolWriteThreadFile, `{"threadId":"`+w.ID+`","path":"/home/u/new.md","content":"x"}`, true)))
	details(t, e.call(asst, ToolWriteThreadFile, `{"threadId":"`+w.ID+`","path":"/home/u/new.md","content":"x","overwrite":true}`, true))
	assert.Equal(t, "x", m.files["/home/u/new.md"])
	big, _ := json.Marshal(strings.Repeat("a", threadTextMax+1))
	assert.Equal(t, CodeTooLarge, errCodeOf(t, e.call(asst, ToolWriteThreadFile, `{"threadId":"`+w.ID+`","path":"/home/u/big","content":`+string(big)+`}`, true)))
	acts := e.actions()
	require.Len(t, acts, 4)
	assert.Equal(t, ToolWriteThreadFile, acts[0].Action)
	assert.False(t, acts[0].Ok)
	assert.Equal(t, "too_large", deref(acts[0].Error))
	assert.True(t, acts[1].Ok)
	assert.Equal(t, w.ID, deref(acts[1].TargetThreadID))
	assert.Equal(t, "/home/u/new.md · 1 bytes", acts[1].Summary)
	assert.Equal(t, "exists", deref(acts[2].Error))

	// get_thread_changes：无 path 是 git 状态；有 path 是 diff，patch 截到 64 KiB
	d = details(t, e.call(asst, ToolGetThreadChanges, `{"threadId":"`+w.ID+`"}`, true))
	assert.Equal(t, true, d["status"].(map[string]any)["repo"])
	assert.Equal(t, fsReq{orb: orbID, method: "GET", path: "/git/status", query: map[string]string{"scope": "all"}}, m.last())
	assert.Equal(t, "git", m.features[len(m.features)-1])
	d = details(t, e.call(asst, ToolGetThreadChanges, `{"threadId":"`+w.ID+`","path":"main.go","scope":"uncommitted"}`, true))
	diff := d["diff"].(map[string]any)
	assert.LessOrEqual(t, len(diff["patch"].(string)), changesPatchMax)
	assert.True(t, strings.HasSuffix(diff["patch"].(string), "\n"), "在换行处截断")
	assert.Equal(t, true, diff["truncated"])
	assert.Equal(t, map[string]string{"scope": "uncommitted", "path": "main.go"}, m.last().query)
	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolGetThreadChanges, `{"threadId":"`+w.ID+`","scope":"commit"}`, true)))

	// Runner 线程带工作目录头
	_, err := e.st.DB.ExecContext(e.ctx, `UPDATE threads SET cwd = '/w/app' WHERE id = ?`, w.ID)
	require.NoError(t, err)
	details(t, e.call(asst, ToolListThreadFiles, `{"threadId":"`+w.ID+`"}`, true))
	assert.Equal(t, "/w/app", m.last().workdir)

	// 没有 orb、目标不是工作线程、Space 协调者
	_, err = e.st.DB.ExecContext(e.ctx, `UPDATE threads SET orb_id = NULL WHERE id = ?`, w.ID)
	require.NoError(t, err)
	assert.Equal(t, CodeNoOrb, errCodeOf(t, e.call(asst, ToolListThreadFiles, `{"threadId":"`+w.ID+`"}`, true)))
	assert.Equal(t, CodeInvalidTarget, errCodeOf(t, e.call(asst, ToolListThreadFiles, `{"threadId":"`+asst.ID+`"}`, true)))
	chat := e.spaceChat(e.space("Alpha", ""))
	assert.Equal(t, "unknown_tool", errCodeOf(t, e.call(chat, ToolReadThreadFile, `{"threadId":"`+w.ID+`","path":"/x"}`, true)))
}

// M19 2.4：fetch_thread_file 把线程 orb 里的文件存进文件库（source=assistant、sourceThreadId=该线程、属于本对话）并同步到
// 协调宿主；超过上限报 too_large。put_thread_file 把文件库文件或沙箱文件放进线程的 orb。
func TestThreadFileLibrary(t *testing.T) {
	e := newEnv(t, true)
	lib, _, host := e.withLibrary()
	asst := e.assistant()
	w := e.work("go")
	m := newFSMachines()
	e.c.SetMachines(m)
	m.files["/home/u/repo/shot.png"] = "\x89PNG-bytes"

	d := details(t, e.call(asst, ToolFetchThreadFile, `{"threadId":"`+w.ID+`","path":"/home/u/repo/shot.png"}`, true))
	f, err := lib.Get(e.ctx, d["id"].(string))
	require.NoError(t, err)
	assert.Equal(t, "shot.png", f.Name)
	assert.Equal(t, library.SourceAssistant, f.Source)
	assert.Equal(t, w.ID, f.SourceThreadID)
	assert.Equal(t, asst.ID, f.ThreadID)
	assert.Equal(t, library.ScopeConversation, f.Scope)
	assert.Equal(t, library.SandboxPath(f), d["path"])
	assert.Equal(t, []string{deref(e.row(asst.ID).OrbID)}, host.syncs, "存库后同步到协调宿主")
	d = details(t, e.call(asst, ToolFetchThreadFile, `{"threadId":"`+w.ID+`","path":"/home/u/repo/shot.png","name":"a.png","shared":true}`, true))
	assert.Equal(t, "a.png", d["name"])
	assert.Equal(t, library.ScopeShared, d["scope"])
	e.c.xferMax = 4
	assert.Equal(t, CodeTooLarge, errCodeOf(t, e.call(asst, ToolFetchThreadFile, `{"threadId":"`+w.ID+`","path":"/home/u/repo/shot.png"}`, true)))
	e.c.xferMax = 0

	// put：文件库文件
	details(t, e.call(asst, ToolPutThreadFile, `{"threadId":"`+w.ID+`","path":"/home/u/copy.png","file":"`+f.ID+`"}`, true))
	assert.Equal(t, "\x89PNG-bytes", m.files["/home/u/copy.png"])
	assert.Equal(t, "false", m.last().query["overwrite"])
	// put：沙箱文件
	host.files["~/library/out/plan.md"] = []byte("# plan")
	details(t, e.call(asst, ToolPutThreadFile, `{"threadId":"`+w.ID+`","path":"/home/u/plan.md","source":"~/library/out/plan.md","overwrite":true}`, true))
	assert.Equal(t, "# plan", m.files["/home/u/plan.md"])
	assert.Equal(t, "true", m.last().query["overwrite"])
	for _, args := range []string{
		`{"threadId":"` + w.ID + `","path":"/x"}`,
		`{"threadId":"` + w.ID + `","path":"/x","file":"` + f.ID + `","source":"~/tmp/a"}`,
		`{"threadId":"` + w.ID + `","path":"/x","source":"~/.omp/agent/models.yml"}`,
		`{"threadId":"` + w.ID + `","path":"/x","source":"~/library/out/../../.omp/agent/models.yml"}`,
	} {
		assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolPutThreadFile, args, true)), args)
	}
	assert.Equal(t, CodeNotFound, errCodeOf(t, e.call(asst, ToolPutThreadFile, `{"threadId":"`+w.ID+`","path":"/x","file":"afl_nope"}`, true)))
	acts := e.actions()
	var ok int
	for _, a := range acts {
		assert.Equal(t, ToolPutThreadFile, a.Action)
		if a.Ok {
			ok++
		}
	}
	assert.Equal(t, 2, ok)
	assert.Len(t, acts, 7)
}

// M19 2.4：整文件传输类工具与转发它们的 oryn 的调用上限 5 分钟，其余用缺省值；线程文件操作不在顶层工具表里。
func TestHostToolTimeoutValues(t *testing.T) {
	var s Service
	for _, name := range []string{ToolFetchThreadFile, ToolPutThreadFile, ToolExportArtifact, ToolImportArtifact, ToolOryn} {
		assert.Equal(t, 5*time.Minute, s.HostToolTimeout(name), name)
	}
	for _, name := range []string{ToolReadThreadFile, ToolWriteThreadFile, ToolListThreads, ToolCreateArtifact} {
		assert.Zero(t, s.HostToolTimeout(name), name)
	}
	names := func(o ToolSet) []string {
		var out []string
		for _, d := range ToolDefs(o) {
			out = append(out, d["name"].(string))
		}
		return out
	}
	assert.Contains(t, names(ToolSet{}), ToolOryn)
	assert.NotContains(t, names(ToolSet{}), ToolFetchThreadFile)
	assert.NotContains(t, names(ToolSet{InSpace: true}), ToolOryn)
}

// M19 2.4：文件类工具按对话合计限速（120 次 / 10 分钟）。
func TestThreadFileRateLimit(t *testing.T) {
	e := newEnv(t, true)
	asst := e.assistant()
	w := e.work("go")
	e.c.SetMachines(newFSMachines())
	for range 120 {
		details(t, e.call(asst, ToolGetThreadChanges, `{"threadId":"`+w.ID+`"}`, true))
	}
	assert.Equal(t, CodeRateLimited, errCodeOf(t, e.call(asst, ToolListThreadFiles, `{"threadId":"`+w.ID+`"}`, true)))
	e.clk.Add(10 * time.Minute)
	details(t, e.call(asst, ToolListThreadFiles, `{"threadId":"`+w.ID+`"}`, true))
}
