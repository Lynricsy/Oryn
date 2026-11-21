package repos

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// requireGit 在找不到 git 时跳过；ORYN_REQUIRE_GIT=1（CI）时改为失败。
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		if os.Getenv("ORYN_REQUIRE_GIT") == "1" {
			t.Fatal("ORYN_REQUIRE_GIT=1，但找不到 git")
		}
		t.Skip("没有 git")
	}
}

type fakeSecrets map[string]string

func (f fakeSecrets) GitToken(host string) (string, bool) {
	v, ok := f[host]
	return v, ok
}

type fakeGitHub struct{ tok string }

func (f fakeGitHub) UserToken(context.Context) (string, error) {
	if f.tok == "" {
		return "", errors.New("没有连接 GitHub 身份")
	}
	return f.tok, nil
}

// testClock 是可拨快的时钟。
type testClock struct{ offset atomic.Int64 }

func (c *testClock) Now() time.Time          { return time.Now().Add(time.Duration(c.offset.Load())) }
func (c *testClock) Advance(d time.Duration) { c.offset.Add(int64(d)) }

// traceLog 收集执行器的 argv 与环境。
type traceLog struct {
	mu    sync.Mutex
	calls [][2][]string
}

func (l *traceLog) record(argv, env []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, [2][]string{argv, env})
}

// forDir 返回 --git-dir 指向 projectID 的镜像（含临时目录）的调用。
func (l *traceLog) forDir(projectID string) [][2][]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out [][2][]string
	for _, c := range l.calls {
		if len(c[0]) > 2 && strings.Contains(filepath.Base(c[0][2]), projectID+".git") {
			out = append(out, c)
		}
	}
	return out
}

type testEnv struct {
	st    *store.Store
	svc   *Service
	fake  *githubtest.Server
	clock *testClock
	trace *traceLog
}

// newTestEnv 起假 GitHub（带 acme/widgets 裸仓库）、临时库与 Service；mut 可改配置。
func newTestEnv(t *testing.T, mut func(*Config), sec Secrets, gh GitHub) *testEnv {
	t.Helper()
	requireGit(t)
	fake := githubtest.New()
	t.Cleanup(fake.Close)
	fake.GitRoot = t.TempDir()
	if err := fake.InitRepo("acme/widgets"); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	st, err := store.Open(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clock := &testClock{}
	cfg := Config{Dir: filepath.Join(data, "repos"), GitHubWebURL: fake.URL(), Now: clock.Now}
	if mut != nil {
		mut(&cfg)
	}
	svc, err := New(cfg, st, sec, gh)
	if err != nil {
		t.Fatal(err)
	}
	trace := &traceLog{}
	svc.trace = trace.record
	t.Cleanup(svc.Close)
	return &testEnv{st: st, svc: svc, fake: fake, clock: clock, trace: trace}
}

// project 建一个项目，repoURL 为空表示没有仓库。
func (e *testEnv) project(t *testing.T, repoURL string) string {
	t.Helper()
	id := ids.New("prj")
	now := time.Now().UnixMilli()
	p := db.CreateProjectParams{ID: id, Name: strings.ToLower(id), CreatedAt: now, UpdatedAt: now}
	if repoURL != "" {
		p.RepoUrl = &repoURL
	}
	if err := e.st.Q.CreateProject(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *testEnv) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.st.DB.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}

// waitIdle 等项目的飞行（含 again 轮次）结束。
func (e *testEnv) waitIdle(t *testing.T, projectID string) {
	t.Helper()
	e.svc.mu.Lock()
	f := e.svc.flights[projectID]
	e.svc.mu.Unlock()
	if f == nil {
		return
	}
	select {
	case <-f.done:
	case <-time.After(2 * time.Minute):
		t.Fatalf("项目 %s 的拉取没有结束", projectID)
	}
}

func (e *testEnv) status(t *testing.T, projectID string) RepoStatus {
	t.Helper()
	st, err := e.svc.Status(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// settle 读一次状态（可能触发后台克隆或拉取），等它结束后再读一次。
func (e *testEnv) settle(t *testing.T, projectID string) RepoStatus {
	t.Helper()
	e.status(t, projectID)
	e.waitIdle(t, projectID)
	return e.status(t, projectID)
}

func (e *testEnv) sync(t *testing.T, projectID string) RepoStatus {
	t.Helper()
	if _, err := e.svc.Sync(context.Background(), projectID); err != nil {
		t.Fatal(err)
	}
	e.waitIdle(t, projectID)
	return e.status(t, projectID)
}

func (e *testEnv) row(t *testing.T, projectID string) *db.ProjectRepo {
	t.Helper()
	r, err := e.st.Q.GetProjectRepo(context.Background(), projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return &r
}

func (e *testEnv) infoRefs() int { return len(e.fake.CallsTo("GET", "/acme/widgets.git/info/refs")) }

// gitCmd 在 dir 里执行宿主 git（不读系统与用户配置）。
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// commitTo 在裸仓库 bare 的 branch 上提交 files 并推送（分支不存在时从 main 分出）。
func commitTo(t *testing.T, bare, branch string, files map[string]string, msg string) {
	t.Helper()
	work := t.TempDir()
	gitCmd(t, work, "clone", "-q", bare, ".")
	if strings.Contains(gitCmd(t, work, "branch", "-r"), "origin/"+branch) {
		gitCmd(t, work, "checkout", "-q", "-B", branch, "origin/"+branch)
	} else {
		gitCmd(t, work, "checkout", "-q", "-B", branch)
	}
	for name, body := range files {
		p := filepath.Join(work, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitCmd(t, work, "add", "-A")
	gitCmd(t, work, "commit", "-q", "-m", msg)
	gitCmd(t, work, "push", "-q", "origin", branch)
}
