package orbs

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

type pushSinkStub struct {
	mu  sync.Mutex
	got []PushedRefs
}

func (p *pushSinkStub) GitPushed(_ context.Context, r PushedRefs) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, r)
}

func (p *pushSinkStub) take() []PushedRefs {
	p.mu.Lock()
	defer p.mu.Unlock()
	g := p.got
	p.got = nil
	return g
}

// M24：出站代理观察到的推送按 orb 的项目与唯一未归档工作线程归属；无项目、没有或不止一个工作线程时不记。
func TestEgressGitPushedSink(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	prj := ids.New("prj")
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{ID: prj, Name: prj, CreatedAt: 1, UpdatedAt: 1}))
	mkOrb := func(project *string) string {
		id := "orb-" + strings.ToLower(ids.New("x")[2:])
		require.NoError(t, st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: id, ProviderID: "prov_1", Type: "small", Image: "img", State: StateReady,
			TokenHash: ids.Token("h"), CreatedAt: 1, ProjectID: project}))
		return id
	}
	mkThread := func(orbID string, kind string) string {
		tid := ids.Thread()
		require.NoError(t, st.Q.CreateThread(ctx, db.CreateThreadParams{ID: tid, ProjectID: &prj, OrbID: &orbID, Title: "t", TitleSource: "auto",
			State: "idle", SessionDir: "d", CreatedAt: 1, UpdatedAt: 1, Kind: kind}))
		return tid
	}
	orbA, orbNoPrj, orbTwo, orbNone := mkOrb(&prj), mkOrb(nil), mkOrb(&prj), mkOrb(&prj)
	tA := mkThread(orbA, "work")
	mkThread(orbNoPrj, "work")
	mkThread(orbTwo, "work")
	mkThread(orbTwo, "work")
	mkThread(orbNone, "space")

	sink := &pushSinkStub{}
	s := &Service{st: st, policies: map[string]egress.Policy{}}
	push := egress.GitPush{Host: "git.example", Repo: "acme/widgets", Refs: []egress.RefUpdate{{Old: "a", New: "b", Ref: "refs/heads/x"}}}

	egressResolver{s: s, orbID: orbA}.GitPushed(push) // 没有 sink：不记
	s.SetPushSink(sink)
	egressResolver{s: s, orbID: orbA}.GitPushed(push)
	assert.Equal(t, []PushedRefs{{OrbID: orbA, ProjectID: prj, ThreadID: tA, Host: "git.example", Repo: "acme/widgets", Refs: push.Refs}}, sink.take())

	for name, orb := range map[string]string{"无项目": orbNoPrj, "两个工作线程": orbTwo, "没有工作线程": orbNone, "不存在的 orb": "orb-gone"} {
		egressResolver{s: s, orbID: orb}.GitPushed(push)
		assert.Empty(t, sink.take(), name)
	}
}
