package repos

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/egress"
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
