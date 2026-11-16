package artifacts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// fakeOrbFS 模拟 orb 本机 API 的 /fs/roots、/fs/list 与 /fs/file，并记下收到的请求。
type fakeOrbFS struct {
	mu      sync.Mutex
	ensure  error
	workdir string
	// files：绝对路径 → 内容；sized 里的路径应答带 Content-Length。
	files map[string]string
	sized map[string]bool
	dirs  map[string][]apigen.FsEntry
	reqs  []*http.Request
	// old 模拟不认 confine 的旧 orbd：应答不带 confinedHeader。
	old bool
	// started 非 nil 时，每个 /fs/file 先报到再等 block 关闭。
	started chan struct{}
	block   chan struct{}
}

func (f *fakeOrbFS) EnsureUsable(_ context.Context, _, feature string, wake bool, _ time.Duration) error {
	if feature != orbs.FeatureFS || !wake {
		return orbs.ErrOrbdOutdated
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ensure
}

func (f *fakeOrbFS) Do(ctx context.Context, _ string, req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req.Clone(ctx))
	started, block, old := f.started, f.block, f.old
	f.mu.Unlock()
	rec := httptest.NewRecorder()
	p := req.URL.Query().Get("path")
	if req.URL.Query().Get("confine") != "" && !old {
		rec.Header().Set(confinedHeader, "1")
	}
	notFound := func() {
		rec.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(rec).Encode(apigen.Error{Code: "not_found", Message: p})
	}
	switch req.URL.Path {
	case "/fs/roots":
		_ = json.NewEncoder(rec).Encode(apigen.FsRoots{Home: "/home/u", Workdir: f.workdir})
	case "/fs/list":
		entries, ok := f.dirs[p]
		if !ok {
			notFound()
			break
		}
		_ = json.NewEncoder(rec).Encode(apigen.FsListing{Path: p, Entries: entries})
	case "/fs/file":
		if started != nil {
			started <- struct{}{}
			<-block
		}
		if _, ok := f.dirs[p]; ok {
			rec.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(rec).Encode(apigen.Error{Code: "is_a_directory", Message: p})
			break
		}
		body, ok := f.files[p]
		if !ok {
			notFound()
			break
		}
		if f.sized[p] {
			rec.Header().Set("Content-Length", strconv.Itoa(len(body)))
		}
		_, _ = rec.WriteString(body)
	default:
		notFound()
	}
	res := rec.Result()
	if !f.sized[p] {
		res.ContentLength = -1
	}
	return res, nil
}

// requests 返回收到的请求（路径、path 与 confine 参数、工作目录头）。
func (f *fakeOrbFS) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.reqs))
	for i, r := range f.reqs {
		q := r.URL.Query()
		out[i] = r.URL.Path + " " + q.Get("path") + " confine=" + q.Get("confine") + " wd=" + r.Header.Get(orbs.HeaderWorkdir)
	}
	return out
}

func (f *fakeOrbFS) reset() {
	f.mu.Lock()
	f.reqs = nil
	f.mu.Unlock()
}

func sym(name string, target apigen.FsEntryTargetType, size int64) apigen.FsEntry {
	e := apigen.FsEntry{Name: name, Type: apigen.FsEntryTypeSymlink, Size: size}
	if target != "" {
		e.TargetType = &target
	}
	return e
}

type filesEnv struct {
	*rtEnv
	fake *fakeOrbFS
}

// newFilesEnv 建一个声明了 files 的页面：threadID 是创建线程（空 = 导入的），consent 为真时已获同意。
func newFilesEnv(t *testing.T, threadID string, consent bool) *filesEnv {
	t.Helper()
	ctx := context.Background()
	e := newEnv(t)
	require.NoError(t, e.st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	mkProject(t, e, "prj_f")
	mkOrb(t, e, "orb_f", "")
	mkOrb(t, e, "orb_r", orbs.KindRunner)
	mkOrbThread(t, e, "thr_f", "orb_f", "prj_f")
	mkOrbThread(t, e, "thr_r", "orb_r", "prj_f")
	_, err := e.st.DB.ExecContext(ctx, `UPDATE threads SET cwd = '/srv/proj' WHERE id = 'thr_r'`)
	require.NoError(t, err)
	h := newHost(t, e, "localhost", fakeViewerAssets)
	a, err := e.s.Create(ctx, CreateParams{
		Title: "仓库浏览", ProjectID: "prj_f", ThreadID: threadID, Files: []FileChange{put("index.html", page)}, Capabilities: []string{"files"},
	})
	require.NoError(t, err)
	if consent {
		require.NoError(t, e.st.Q.UpsertArtifactConsent(ctx, db.UpsertArtifactConsentParams{ArtifactID: a.ID, Capability: "files", GrantedAt: 1}))
	}
	big := strings.Repeat("x", MaxProjectFileBytes+1)
	fake := &fakeOrbFS{
		workdir: "/home/u/repo",
		files: map[string]string{
			"/home/u/repo/a.txt":     "hello",
			"/home/u/repo/src/x.go":  "package x",
			"/home/u/repo/bin.dat":   "\xff\xfe",
			"/home/u/repo/big":       big,
			"/home/u/repo/big2":      big,
			"/home/u/repo/limit.txt": big[:MaxProjectFileBytes],
			"/srv/proj/a.txt":        "runner",
		},
		sized: map[string]bool{"/home/u/repo/big": true},
		dirs: map[string][]apigen.FsEntry{
			"/home/u/repo": {
				{Name: "src", Type: apigen.FsEntryTypeDir, Size: 4096},
				{Name: ".git", Type: apigen.FsEntryTypeDir, Size: 4096},
				sym("lib", apigen.FsEntryTargetTypeDir, 3),
				{Name: "a.txt", Type: apigen.FsEntryTypeFile, Size: 5},
				{Name: "bad:name", Type: apigen.FsEntryTypeFile, Size: 1},
				{Name: "trail.", Type: apigen.FsEntryTypeFile, Size: 1},
				sym("ln", apigen.FsEntryTargetTypeFile, 9),
				sym("dangling", "", 7),
				{Name: "sock", Type: apigen.FsEntryTypeOther},
			},
			"/home/u/repo/src": {{Name: "x.go", Type: apigen.FsEntryTypeFile, Size: 9}},
		},
	}
	e.s.AttachOrbFS(fake)
	r := &rtEnv{e: e, h: h, mw: h.Middleware(http.NotFoundHandler()), art: a}
	r.host = r.frame(t, 0)
	return &filesEnv{rtEnv: r, fake: fake}
}

func (f *filesEnv) use(t *testing.T) any {
	t.Helper()
	return f.send(t, f.host, map[string]any{"kind": "use", "cap": "files"})["value"]
}

func (f *filesEnv) files(t *testing.T, op string, args ...any) map[string]any {
	t.Helper()
	if args == nil {
		args = []any{}
	}
	return f.send(t, f.host, map[string]any{"kind": "call", "cap": "files", "op": op, "args": args})
}

func (f *filesEnv) value(t *testing.T, op string, args ...any) any {
	t.Helper()
	rep := f.files(t, op, args...)
	require.Equal(t, true, rep["ok"], rep)
	return rep["value"]
}

// list 与 read：经 /fs/list、/fs/file 取，总是带 confine=<工作目录>；工作目录取自 orbd 的 /fs/roots。
func TestCapFilesListRead(t *testing.T) {
	f := newFilesEnv(t, "thr_f", true)
	assert.Equal(t, map[string]any{"ops": []any{"list", "read"}}, f.use(t))

	f.fake.reset()
	got := f.value(t, "list")
	assert.Equal(t, []any{
		map[string]any{"name": "src", "path": "src", "type": "folder", "size": float64(0)},
		map[string]any{"name": "lib", "path": "lib", "type": "folder", "size": float64(0)},
		map[string]any{"name": "a.txt", "path": "a.txt", "type": "file", "size": float64(5)},
		map[string]any{"name": "ln", "path": "ln", "type": "file", "size": float64(9)},
	}, got, ".git、读不到的名字、悬空链接与其它类型不列出")
	assert.Equal(t, []string{
		"/fs/roots  confine= wd=",
		"/fs/list /home/u/repo confine=/home/u/repo wd=",
	}, f.fake.requests())
	assert.Equal(t, []any{map[string]any{"name": "x.go", "path": "src/x.go", "type": "file", "size": float64(9)}}, f.value(t, "list", "src"))
	assert.Equal(t, CodeNotFound, replyCode(t, f.files(t, "list", "nope")))

	assert.Equal(t, "hello", f.value(t, "read", "a.txt"))
	assert.Equal(t, "package x", f.value(t, "read", "src/x.go", map[string]any{"as": "text"}))
	assert.Equal(t, "//4=", f.value(t, "read", "bin.dat", map[string]any{"as": "bytes"}))
	assert.Equal(t, CodeInvalid, replyCode(t, f.files(t, "read", "bin.dat")), "非 UTF-8 按文本读")
	assert.Equal(t, CodeInvalid, replyCode(t, f.files(t, "read", "src")), "文件夹")
	assert.Equal(t, CodeNotFound, replyCode(t, f.files(t, "read", "missing.txt")))
	assert.Equal(t, CodeInvalid, replyCode(t, f.files(t, "read", "a.txt", map[string]any{"as": "blob"})))
	assert.Equal(t, CodeInvalid, replyCode(t, f.files(t, "read", "a.txt", map[string]any{"bogus": 1})))
	assert.Equal(t, CodeInvalid, replyCode(t, f.files(t, "read")))
	assert.Equal(t, CodeInvalid, replyCode(t, f.files(t, "read", "")))

	// 20 MiB 恰好可读；多一个字节就 too_large（带不带 Content-Length 都一样）
	assert.Len(t, f.value(t, "read", "limit.txt"), MaxProjectFileBytes)
	assert.Equal(t, CodeTooLarge, replyCode(t, f.files(t, "read", "big")))
	assert.Equal(t, CodeTooLarge, replyCode(t, f.files(t, "read", "big2")))

	// 路径规则在控制面先挡下，不发给 orb
	f.fake.reset()
	for _, p := range []string{".git/config", "a/../b", "A/.GIT/x", "/etc/passwd", "a\\b", "c:x"} {
		assert.Equal(t, CodeInvalid, replyCode(t, f.files(t, "read", p)), p)
	}
	assert.Equal(t, CodeInvalid, replyCode(t, f.files(t, "list", "../x")))
	assert.Empty(t, f.fake.requests())
}

// Runner 线程：工作目录取线程的 cwd，请求带 X-Oryn-Workdir，不问 /fs/roots。
func TestCapFilesRunnerWorkdir(t *testing.T) {
	f := newFilesEnv(t, "thr_r", true)
	f.fake.reset()
	assert.Equal(t, "runner", f.value(t, "read", "a.txt"))
	assert.Equal(t, []string{"/fs/file /srv/proj/a.txt confine=/srv/proj wd=/srv/proj"}, f.fake.requests())
}

// 不可用：导入的（没有创建线程）、orb 不可用、没接 orb 出口时 use 回 null，call 回 unsupported。
func TestCapFilesUnavailable(t *testing.T) {
	imported := newFilesEnv(t, "", true)
	assert.Nil(t, imported.use(t))
	assert.Equal(t, codeUnsupported, replyCode(t, imported.files(t, "read", "a.txt")))
	assert.Empty(t, imported.fake.requests())

	down := newFilesEnv(t, "thr_f", true)
	down.fake.ensure = orbs.ErrOrbNotReady
	assert.Nil(t, down.use(t))
	assert.Equal(t, codeUnsupported, replyCode(t, down.files(t, "list")))

	detached := newFilesEnv(t, "thr_f", true)
	detached.e.s.AttachOrbFS(nil)
	assert.Nil(t, detached.use(t))

	// 不认 confine 的旧 orbd：应答不用，回 unsupported（不回落到不受限的读取）
	old := newFilesEnv(t, "thr_f", true)
	old.fake.old = true
	assert.Equal(t, codeUnsupported, replyCode(t, old.files(t, "read", "a.txt")))
	assert.Equal(t, codeUnsupported, replyCode(t, old.files(t, "list")))
}

// 未获同意时 call 回 not_allowed，不访问 orb。
func TestCapFilesConsent(t *testing.T) {
	f := newFilesEnv(t, "thr_f", false)
	assert.Nil(t, f.use(t), "没有观看者：按拒绝处理")
	assert.Equal(t, codeNotAllowed, replyCode(t, f.files(t, "read", "a.txt")))
	assert.Empty(t, f.fake.requests())
}

// 每个 artifact 同时最多 MaxProjectReads 个读取，多出的回 rate_limited；完成后名额归还。
func TestCapFilesConcurrency(t *testing.T) {
	f := newFilesEnv(t, "thr_r", true)
	f.fake.started = make(chan struct{}, MaxProjectReads+1)
	f.fake.block = make(chan struct{})
	results := make(chan map[string]any, MaxProjectReads)
	for i := range MaxProjectReads {
		go func() {
			rec := postRuntime(f.mw, f.host, "/_oryn/v1", "null", `{"v":1,"id":"r`+strconv.Itoa(i)+`","kind":"call","cap":"files","op":"read","args":["a.txt"]}`)
			var rep map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &rep)
			results <- rep
		}()
	}
	for range MaxProjectReads {
		select {
		case <-f.fake.started:
		case <-time.After(5 * time.Second):
			t.Fatal("读取没有到达 orb")
		}
	}
	assert.Equal(t, CodeRateLimited, replyCode(t, f.files(t, "read", "a.txt")))
	assert.Equal(t, CodeRateLimited, replyCode(t, f.files(t, "list")), "list 与 read 共用名额")
	f.fake.mu.Lock()
	f.fake.started = nil
	f.fake.mu.Unlock()
	close(f.fake.block)
	for range MaxProjectReads {
		select {
		case rep := <-results:
			assert.Equal(t, true, rep["ok"], rep)
		case <-time.After(5 * time.Second):
			t.Fatal("读取没有返回")
		}
	}
	assert.Equal(t, "runner", f.value(t, "read", "a.txt"))
}

func TestCapFilesPathRules(t *testing.T) {
	for _, p := range []string{
		"", "/abs", ".git", ".git/config", "A/.GIT/x", "x/.Git", "a/../b", "..", "a/./b", ".",
		"a//b", "a/", "a\\b", "c:x", "a\x00b", "a\nb", "a\x7fb", "a.", "a/b ", "a/.../b",
		strings.Repeat("x", MaxProjectPathChars+1),
	} {
		assert.NotEmpty(t, projectPathProblem(p), "%q", p)
	}
	for _, p := range []string{
		"a.txt", "src/main.go", ".env", ".github/workflows/ci.yml", "x.git", ".gitignore", "目录/文件.txt", "a b/c",
		strings.Repeat("字", MaxProjectPathChars),
	} {
		assert.Empty(t, projectPathProblem(p), "%q", p)
	}
}
