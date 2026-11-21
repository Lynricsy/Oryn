package repos

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/egress"
)

// fakeShips 记录 MarkShipped 的调用（线程 id）。
type fakeShips struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeShips) MarkShipped(_ context.Context, id string, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, id)
	return nil
}

func (f *fakeShips) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// hostedPushServer 是托管项目的 ServeHostedGit 测试服务：/owner/r.git/<端点> 是主人入口，/orb/r.git/<端点> 是
// orb-1（线程 thread）的入口，/gone/r.git/<端点> 同 orb 入口，但首次写响应时取消请求的 ctx（模拟客户端先断开）。
type hostedPushServer struct {
	*httptest.Server
	env     *testEnv
	id      string
	dir     string
	thread  string
	ships   *fakeShips
	mu      sync.Mutex
	changed []string
}

func newHostedPushServer(t *testing.T) *hostedPushServer {
	t.Helper()
	s := &hostedPushServer{ships: &fakeShips{}}
	s.env = newTestEnv(t, func(c *Config) {
		c.Ships = s.ships
		c.Changed = func(id string) {
			s.mu.Lock()
			s.changed = append(s.changed, id)
			s.mu.Unlock()
		}
	}, nil, nil)
	ctx := context.Background()
	s.id = s.env.project(t, "")
	s.env.exec(t, `UPDATE projects SET hosted = 1, base_branch = 'main' WHERE id = ?`, s.id)
	_, err := s.env.svc.InitHosted(ctx, s.id, "main", true, testIdentity)
	require.NoError(t, err)
	require.NoError(t, s.env.svc.RecordHosted(ctx, s.id, "main"))
	s.dir = s.env.svc.dir(s.id)
	s.thread = s.env.thread(t, s.id)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/r.git/")
		var pusher egress.HostedPusher
		switch who {
		case "orb":
			pusher = egress.HostedPusher{OrbID: "orb-1", ThreadID: s.thread}
		case "gone":
			pusher = egress.HostedPusher{OrbID: "orb-1", ThreadID: s.thread}
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			r = r.WithContext(ctx)
			w = &cancelOnWrite{ResponseWriter: w, cancel: cancel}
		}
		s.env.svc.ServeHostedGit(w, r, s.id, rest, pusher)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *hostedPushServer) url(who string) string { return s.URL + "/" + who + "/r.git" }

func (s *hostedPushServer) changedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, id := range s.changed {
		if id == s.id {
			n++
		}
	}
	return n
}

func (s *hostedPushServer) pushRows(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, s.env.st.DB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM repo_pushes WHERE project_id = ?`, s.id).Scan(&n))
	return n
}

// cancelOnWrite 在第一次写响应时取消请求的 ctx。
type cancelOnWrite struct {
	http.ResponseWriter
	cancel context.CancelFunc
}

func (c *cancelOnWrite) Write(b []byte) (int, error) {
	c.cancel()
	return c.ResponseWriter.Write(b)
}

func (c *cancelOnWrite) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func TestHostedPushOrbAccounting(t *testing.T) {
	s := newHostedPushServer(t)
	tmp := t.TempDir()
	mustClientGit(t, tmp, "clone", "-q", s.url("orb"), "w")
	work := filepath.Join(tmp, "w")
	sizeBefore := s.env.row(t, s.id).SizeBytes

	// 推基线：记推送、展开成提交 → 线程、广播、自动交付
	c1 := commitFile(t, work, "a.txt", "a\n", "c1")
	mustClientGit(t, work, "push", "-q", "origin", "main")
	assert.Equal(t, map[string]string{"refs/heads/main": ""}, s.env.pushes(t, s.id))
	assert.Equal(t, map[string]string{c1: s.thread + "|push"}, s.env.links(t, s.id))
	assert.Equal(t, []string{s.thread}, s.ships.got())
	assert.Equal(t, 1, s.changedCount())
	assert.Greater(t, s.env.row(t, s.id).SizeBytes, sizeBefore)

	// 只推 feat：记推送，不交付
	mustClientGit(t, work, "checkout", "-q", "-b", "feat")
	f1 := commitFile(t, work, "f.txt", "f\n", "f1")
	mustClientGit(t, work, "push", "-q", "origin", "feat")
	assert.Equal(t, 2, s.pushRows(t))
	assert.Equal(t, s.thread+"|push", s.env.links(t, s.id)[f1])
	assert.Len(t, s.ships.got(), 1)

	// 强推 main：再次交付（MarkShipped 自己幂等），旧值留存
	mustClientGit(t, work, "checkout", "-q", "main")
	mustClientGit(t, work, "commit", "-q", "--amend", "-m", "c1 (amended)")
	mustClientGit(t, work, "push", "-q", "--force", "origin", "main")
	assert.Len(t, s.ships.got(), 2)
	assert.Equal(t, []string{c1}, savedFor(t, s.dir, "heads/main"))

	// 删 feat：有变化（广播）但不写推送记录，旧值留存
	rows := s.pushRows(t)
	n := s.changedCount()
	mustClientGit(t, work, "push", "-q", "origin", ":feat")
	assert.Equal(t, rows, s.pushRows(t))
	assert.Equal(t, n+1, s.changedCount())
	assert.Equal(t, []string{f1}, savedFor(t, s.dir, "heads/feat"))

	// 留存的 ref 对客户端不可见、不可改
	out := mustClientGit(t, tmp, "ls-remote", s.url("owner"))
	assert.NotContains(t, out, "refs/oryn")
	saved := gitCmd(t, s.dir, "for-each-ref", "--format=%(refname)", "refs/oryn/saved/")
	out, err := clientGit(t, work, "push", "origin", ":"+strings.Fields(saved)[0])
	require.Error(t, err, out)
}

// savedFor 返回 refs/oryn/saved/<毫秒>/<rest> 留存的对象 id（按 ref 名排序）。
func savedFor(t *testing.T, dir, rest string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(gitCmd(t, dir, "for-each-ref", "--format=%(objectname) %(refname)", "refs/oryn/saved/"), "\n") {
		oid, ref, ok := strings.Cut(line, " ")
		if ok && strings.HasSuffix(ref, "/"+rest) {
			out = append(out, oid)
		}
	}
	return out
}

func TestHostedPushOwner(t *testing.T) {
	s := newHostedPushServer(t)
	tmp := t.TempDir()
	mustClientGit(t, tmp, "clone", "-q", s.url("owner"), "w")
	work := filepath.Join(tmp, "w")
	commitFile(t, work, "a.txt", "a\n", "owner commit")
	mustClientGit(t, work, "push", "-q", "origin", "main")

	assert.Zero(t, s.pushRows(t))
	assert.Empty(t, s.env.links(t, s.id))
	assert.Empty(t, s.ships.got())
	assert.Equal(t, 1, s.changedCount())

	// 被拒的推送（非快进且不 --force）没有 ref 变化：不广播
	mustClientGit(t, work, "commit", "-q", "--amend", "-m", "rewritten")
	out, err := clientGit(t, work, "push", "origin", "main")
	require.Error(t, err, out)
	assert.Equal(t, 1, s.changedCount())
}

func TestHostedPushClientGone(t *testing.T) {
	s := newHostedPushServer(t)
	tmp := t.TempDir()
	mustClientGit(t, tmp, "clone", "-q", s.url("orb"), "w")
	work := filepath.Join(tmp, "w")
	c1 := commitFile(t, work, "a.txt", "a\n", "c1")
	mustClientGit(t, work, "push", "-q", s.url("gone"), "main")

	assert.Equal(t, c1, gitCmd(t, s.dir, "rev-parse", "refs/heads/main"))
	assert.Equal(t, 1, s.changedCount())
	assert.Equal(t, []string{s.thread}, s.ships.got())
	assert.Equal(t, s.thread+"|push", s.env.links(t, s.id)[c1])
}

func TestDiffRefs(t *testing.T) {
	z := zeroOID
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	got := diffRefs(
		map[string]string{"refs/heads/main": a, "refs/heads/old": b, "refs/tags/v1": c},
		map[string]string{"refs/heads/main": b, "refs/heads/new": a, "refs/tags/v1": c},
	)
	assert.Equal(t, []egress.RefUpdate{
		{Ref: "refs/heads/main", Old: a, New: b},
		{Ref: "refs/heads/new", Old: z, New: a},
		{Ref: "refs/heads/old", Old: b, New: z},
	}, got)
	assert.Empty(t, diffRefs(map[string]string{"refs/heads/main": a}, map[string]string{"refs/heads/main": a}))
}

func TestHostedSavedRefsSweep(t *testing.T) {
	s := newHostedPushServer(t)
	main := gitCmd(t, s.dir, "rev-parse", "refs/heads/main")
	now := s.env.svc.cfg.Now()
	old := strconv.FormatInt(now.Add(-31*24*time.Hour).UnixMilli(), 10)
	recent := strconv.FormatInt(now.Add(-29*24*time.Hour).UnixMilli(), 10)
	gitCmd(t, s.dir, "update-ref", "refs/oryn/saved/"+old+"/heads/a", main)
	gitCmd(t, s.dir, "update-ref", "refs/oryn/saved/"+recent+"/heads/b", main)

	s.env.svc.sweepSavedRefs(context.Background())
	assert.Equal(t, "refs/oryn/saved/"+recent+"/heads/b", gitCmd(t, s.dir, "for-each-ref", "--format=%(refname)", "refs/oryn/saved/"))
	assert.Equal(t, main, gitCmd(t, s.dir, "rev-parse", "refs/heads/main"))
}
