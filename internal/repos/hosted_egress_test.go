package repos

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/lfs"
)

// hostedEgressResolver 是只有托管仓库通道的出站代理 Resolver：策略把 oryn.test 上的项目仓库交给真 *Service 在本地服务，
// 推送方固定为 orb-1 与一个线程。
type hostedEgressResolver struct {
	egress.NoGateLookup
	egress.NoMemory
	pol    egress.Policy
	svc    *Service
	thread string
}

func (r hostedEgressResolver) Policy() egress.Policy                          { return r.pol }
func (hostedEgressResolver) SecretByID(string) (string, []string, bool)       { return "", nil, false }
func (hostedEgressResolver) GitToken(string) (string, bool)                   { return "", false }
func (hostedEgressResolver) GitHubToken() (string, bool)                      { return "", false }
func (hostedEgressResolver) GitHubTokenRejected(string)                       {}
func (hostedEgressResolver) GitHubWriteBegin(egress.GitHubWrite) string       { return "" }
func (hostedEgressResolver) GitHubWriteEnd(egress.GitHubWrite, int, []string) {}

// ThreadGates：没有线程限制（NoGateLookup 的「一律不放行」会让推送按只读评审被拒）。
func (hostedEgressResolver) ThreadGates() (egress.ThreadGates, error) {
	return egress.ThreadGates{}, nil
}

func (r hostedEgressResolver) HostedGit() (egress.HostedGitServer, egress.HostedPusher, error) {
	return r.svc, egress.HostedPusher{OrbID: "orb-1", ThreadID: r.thread}, nil
}

// M24 阶段 ③：orb 内的 git 以代理形式经出站代理克隆、推送本项目的托管仓库（真 git、真 smart-HTTP），推送记到线程；
// 别的托管项目被代理拒绝。
func TestHostedEgressCloneAndPush(t *testing.T) {
	requireGit(t)
	e := newTestEnv(t, nil, nil, nil)
	ctx := context.Background()
	mk := func() string {
		id := e.hostedProject(t, "main")
		_, err := e.svc.InitHosted(ctx, id, "main", true, testIdentity)
		require.NoError(t, err)
		require.NoError(t, e.svc.RecordHosted(ctx, id, "main"))
		return id
	}
	a, b := mk(), mk()
	thread := e.thread(t, a)
	route := egress.HostedGitRoute{Host: "oryn.test", Repo: egress.NormalizeRepo("/git/" + a + ".git"), ProjectID: a}
	px := httptest.NewServer(egress.NewHandler("orb-1", hostedEgressResolver{
		pol: egress.Policy{HostedGit: &route}, svc: e.svc, thread: thread,
	}))
	t.Cleanup(px.Close)
	proxy := "http.proxy=" + px.URL

	tmp := t.TempDir()
	mustClientGit(t, tmp, "clone", "-q", "-c", proxy, "http://oryn.test/git/"+a+".git", "w")
	work := filepath.Join(tmp, "w")
	assert.Equal(t, "Initial commit", mustClientGit(t, work, "log", "-1", "--format=%s"))
	c1 := commitFile(t, work, "a.txt", "a\n", "from orb")
	mustClientGit(t, work, "push", "-q", "origin", "main")
	assert.Equal(t, c1, gitCmd(t, e.svc.dir(a), "rev-parse", "refs/heads/main"))

	var tid string
	require.NoError(t, e.st.DB.QueryRowContext(ctx, `SELECT thread_id FROM repo_pushes WHERE project_id = ?`, a).Scan(&tid))
	assert.Equal(t, thread, tid)

	out, err := clientGit(t, tmp, "clone", "-q", "-c", proxy, "http://oryn.test/git/"+b+".git", "other")
	require.Error(t, err, out)
	assert.Contains(t, out, "这个 orb 只能访问本项目的托管仓库")
}

// requireGitLFS：找不到 git-lfs 时，ORYN_REQUIRE_GIT_LFS=1 则失败，否则跳过。
func requireGitLFS(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git-lfs"); err != nil {
		if os.Getenv("ORYN_REQUIRE_GIT_LFS") == "1" {
			t.Fatalf("找不到 git-lfs：%v", err)
		}
		t.Skip("找不到 git-lfs")
	}
}

// lfsClientGit 以 orb 内 git 的方式执行带 Git LFS 的 git：HOME 固定（git lfs install 写在里面），不读系统配置、
// 不带环境里的代理变量（代理只经 http.proxy 指向出站代理）。
func lfsClientGit(t *testing.T, home string) func(dir string, args ...string) string {
	return func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		for _, kv := range os.Environ() {
			if k, _, _ := strings.Cut(kv, "="); !strings.HasSuffix(strings.ToLower(k), "_proxy") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "HOME="+home, "XDG_CONFIG_HOME="+home, "GIT_TERMINAL_PROMPT=0",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
		return strings.TrimSpace(string(out))
	}
}

// M24 步骤 3.14：orb 内的 git-lfs 经出站代理用托管仓库的 LFS 服务：batch 与对象 PUT / GET 都在本地服务，
// 推送后对象落在托管库里、推送记到线程；另起的克隆检出真实内容。
func TestHostedEgressLFS(t *testing.T) {
	requireGit(t)
	requireGitLFS(t)
	e := newTestEnv(t, func(c *Config) { c.PublicURL = "https://oryn.test" }, nil, nil)
	ctx := context.Background()
	a := e.hostedProject(t, "main")
	_, err := e.svc.InitHosted(ctx, a, "main", true, testIdentity)
	require.NoError(t, err)
	require.NoError(t, e.svc.RecordHosted(ctx, a, "main"))
	thread := e.thread(t, a)
	route := egress.HostedGitRoute{Host: "oryn.test", Repo: egress.NormalizeRepo("/git/" + a + ".git"), ProjectID: a}
	px := httptest.NewServer(egress.NewHandler("orb-1", hostedEgressResolver{
		pol: egress.Policy{HostedGit: &route}, svc: e.svc, thread: thread,
	}))
	t.Cleanup(px.Close)
	proxy := "http.proxy=" + px.URL
	cloneURL := "http://oryn.test/git/" + a + ".git"

	home, tmp := t.TempDir(), t.TempDir()
	git := lfsClientGit(t, home)
	git(tmp, "lfs", "install", "--skip-repo")
	git(tmp, "clone", "-q", "-c", proxy, cloneURL, "w")
	work := filepath.Join(tmp, "w")
	git(work, "lfs", "install", "--local")
	git(work, "lfs", "track", "*.bin")
	content := []byte(strings.Repeat("oryn hosted lfs\n", 64))
	require.NoError(t, os.WriteFile(filepath.Join(work, "a.bin"), content, 0o644))
	git(work, "add", ".gitattributes", "a.bin")
	git(work, "commit", "-q", "-m", "add lfs file")
	git(work, "push", "-q", "origin", "main")

	sum := sha256.Sum256(content)
	oid := hex.EncodeToString(sum[:])
	got, err := os.ReadFile(lfs.ObjectPath(filepath.Join(e.svc.dir(a), "lfs", "objects"), oid))
	require.NoError(t, err, "LFS 对象经代理上传到托管库")
	assert.Equal(t, content, got)
	p, ok := lfs.ParsePointer([]byte(gitCmd(t, e.svc.dir(a), "cat-file", "blob", "main:a.bin") + "\n"))
	require.True(t, ok, "仓库里存的是指针")
	assert.Equal(t, lfs.Pointer{OID: oid, Size: int64(len(content))}, p)

	var tid string
	require.NoError(t, e.st.DB.QueryRowContext(ctx, `SELECT thread_id FROM repo_pushes WHERE project_id = ?`, a).Scan(&tid))
	assert.Equal(t, thread, tid)

	git(tmp, "clone", "-q", "-c", proxy, cloneURL, "w2")
	got, err = os.ReadFile(filepath.Join(tmp, "w2", "a.bin"))
	require.NoError(t, err)
	assert.Equal(t, content, got, "另起的克隆经代理取到真实内容")
}
