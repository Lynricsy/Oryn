package orbs

import (
	"context"
	"net/http"
	"net/http/httptest"
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

// hostedSrv 记录出站代理交给本地服务的托管仓库请求。
type hostedSrv struct {
	mu   sync.Mutex
	seen []hostedCall
}

type hostedCall struct {
	project, endpoint string
	pusher            egress.HostedPusher
}

func (h *hostedSrv) ServeHostedGit(w http.ResponseWriter, _ *http.Request, projectID, endpoint string, pusher egress.HostedPusher) {
	h.mu.Lock()
	h.seen = append(h.seen, hostedCall{projectID, endpoint, pusher})
	h.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// M24 阶段 ③：托管项目的 orb 经出站代理访问本项目的托管仓库，策略来自 setup 计划（public_url），
// 本地服务方收到 orb 与它唯一的工作线程；别的项目的托管仓库被拒。
func TestHostedEgressPusher(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	prjA, prjB := ids.New("prj"), ids.New("prj")
	for _, p := range []string{prjA, prjB} {
		require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{ID: p, Name: p, Hosted: true, CreatedAt: 1, UpdatedAt: 1}))
	}
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	orbID := "orb-" + strings.ToLower(ids.New("x")[2:])
	require.NoError(t, st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: orbID, ProviderID: "prov_1", Type: "small", Image: "img", State: StateReady,
		TokenHash: ids.Token("h"), CreatedAt: 1, ProjectID: &prjA}))
	mkThread := func() string {
		tid := ids.Thread()
		require.NoError(t, st.Q.CreateThread(ctx, db.CreateThreadParams{ID: tid, ProjectID: &prjA, OrbID: &orbID, Title: "t", TitleSource: "auto",
			State: "idle", SessionDir: "d", CreatedAt: 1, UpdatedAt: 1, Kind: "work"}))
		return tid
	}
	tid := mkThread()

	srv := &hostedSrv{}
	s := &Service{st: st, setup: &DefaultSetup{Store: st, PublicURL: "https://oryn.test"}, policies: map[string]egress.Policy{}}
	s.SetHostedGit(srv)
	do := func(target string) int {
		t.Helper()
		rec := httptest.NewRecorder()
		egress.NewHandler(orbID, egressResolver{s: s, orbID: orbID}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, target, nil))
		return rec.Code
	}

	require.Equal(t, http.StatusOK, do("http://oryn.test/git/"+prjA+".git/git-receive-pack"))
	require.Len(t, srv.seen, 1)
	assert.Equal(t, hostedCall{prjA, "git-receive-pack", egress.HostedPusher{OrbID: orbID, ThreadID: tid}}, srv.seen[0])

	assert.Equal(t, http.StatusForbidden, do("http://oryn.test/git/"+prjB+".git/git-receive-pack"), "别的项目的托管仓库")
	assert.Len(t, srv.seen, 1)

	mkThread()
	require.Equal(t, http.StatusOK, do("http://oryn.test/git/"+prjA+".git/git-upload-pack"))
	assert.Equal(t, egress.HostedPusher{OrbID: orbID}, srv.seen[1].pusher, "不止一个工作线程：不关联线程")

	s.SetHostedGit(nil)
	assert.Equal(t, http.StatusForbidden, do("http://oryn.test/git/"+prjA+".git/git-upload-pack"), "没有通道时失败关闭")
}
