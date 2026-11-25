package artifacts

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// mkDesignService 往 orb 的服务镜像里写一条服务（doc 同 orbs.applyServices 写入的 apigen.OrbService）。
func mkDesignService(t *testing.T, e *env, orbID, name string, port int, portal bool, status apigen.OrbServiceStatus, thread string) {
	t.Helper()
	sv := apigen.OrbService{Name: name, Command: "node vite.js", Source: "dynamic", Status: status, Port: &port, Links: []apigen.OrbPortalLink{}}
	if thread != "" {
		sv.ThreadID = &thread
	}
	doc, err := json.Marshal(sv)
	require.NoError(t, err)
	p := int64(port)
	hp := int64(0)
	if portal {
		hp = 1
	}
	require.NoError(t, e.st.Q.InsertOrbService(context.Background(), db.InsertOrbServiceParams{
		OrbID: orbID, Name: name, Port: &p, HasPortal: hp, Doc: string(doc), UpdatedAt: 1,
	}))
}

// 服务名与 orbd 侧同一算法：dsn- + sha256(id) 前 10 位 hex。
func TestDesignLiveServiceName(t *testing.T) {
	assert.Equal(t, "dsn-1d171b1a70", DesignServiceName("art_x"), "printf art_x | sha256sum")
	assert.Regexp(t, `^dsn-[0-9a-f]{10}$`, DesignServiceName("art_01932f6e"))
	assert.NotEqual(t, DesignServiceName("a"), DesignServiceName("b"))
}

// M29 T6：live 只在设计画布、创建线程的 orb 为 ready、该画布的 dsn 服务 running 且有 portal 端口时非 null；
// threadId 取服务所属线程。别的服务、非 running、无 portal、orb 冻结、回收站里都回 null。
func TestDesignLive(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	require.NoError(t, e.st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	mkOrb(t, e, "orb_a", "")
	mkOrbThread(t, e, "thr_a", "orb_a", "")
	id := mkEmptyDesign(t, e, "thr_a")
	name := DesignServiceName(id)

	live := func() *DesignLive {
		t.Helper()
		d, err := e.s.Design(ctx, id)
		require.NoError(t, err)
		require.True(t, d.Design)
		return d.Live
	}
	assert.Nil(t, live(), "没有服务")

	mkDesignService(t, e, "orb_a", "web", 3000, true, apigen.OrbServiceStatusRunning, "thr_a")
	assert.Nil(t, live(), "别的服务不算")

	mkDesignService(t, e, "orb_a", name, 5173, true, apigen.OrbServiceStatusStarting, "thr_a")
	assert.Nil(t, live(), "未就绪")

	require.NoError(t, e.st.Q.DeleteOrbServices(ctx, "orb_a"))
	mkDesignService(t, e, "orb_a", name, 5173, false, apigen.OrbServiceStatusRunning, "thr_a")
	assert.Nil(t, live(), "没有 portal")

	require.NoError(t, e.st.Q.DeleteOrbServices(ctx, "orb_a"))
	mkDesignService(t, e, "orb_a", name, 5173, true, apigen.OrbServiceStatusRunning, "thr_b")
	got := live()
	require.NotNil(t, got)
	assert.Equal(t, DesignLive{ThreadID: "thr_b", Port: 5173}, *got, "threadId 取服务所属线程")
	api := e.s.mustDesignAPI(t, id)
	require.NotNil(t, api.Live)
	assert.Equal(t, 5173, api.Live.Port)
	assert.Equal(t, "thr_b", api.Live.ThreadID)

	require.NoError(t, e.st.Q.SetOrbState(ctx, db.SetOrbStateParams{ID: "orb_a", State: orbs.StateFrozen, StateChangedAt: 2}))
	assert.Nil(t, live(), "orb 冻结时镜像不可信")
	require.NoError(t, e.st.Q.SetOrbState(ctx, db.SetOrbStateParams{ID: "orb_a", State: orbs.StateReady, StateChangedAt: 3}))
	assert.NotNil(t, live())

	_, err := e.s.Delete(ctx, id)
	require.NoError(t, err)
	assert.Nil(t, live(), "回收站里的不开实时预览")
}

// 非设计画布（HTML 画布）即使碰巧有同名服务也不报 live；没有创建线程的画布同样为 null。
func TestDesignLiveNotDesign(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	require.NoError(t, e.st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	mkOrb(t, e, "orb_a", "")
	mkOrbThread(t, e, "thr_a", "orb_a", "")
	a, err := e.s.Create(ctx, CreateParams{Title: "画布", ThreadID: "thr_a", Files: []FileChange{
		put(EntryCanvas, `{"format":1,"canvases":[{"id":"c1","name":"首页"}],"pages":[{"id":"p1","name":"一","file":"a.html"}]}`),
		put("a.html", "<p>a</p>"),
	}})
	require.NoError(t, err)
	mkDesignService(t, e, "orb_a", DesignServiceName(a.ID), 5173, true, apigen.OrbServiceStatusRunning, "thr_a")
	d, err := e.s.Design(ctx, a.ID)
	require.NoError(t, err)
	assert.Nil(t, d.Live)

	id := mkEmptyDesign(t, e, "")
	d, err = e.s.Design(ctx, id)
	require.NoError(t, err)
	assert.True(t, d.Design)
	assert.Nil(t, d.Live)
}

func (s *Service) mustDesignAPI(t *testing.T, id string) apigen.ArtifactDesign {
	t.Helper()
	d, err := s.Design(context.Background(), id)
	require.NoError(t, err)
	return d.API()
}
