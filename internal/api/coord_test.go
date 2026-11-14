package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/coord"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/schedule"
	"github.com/Lynricsy/Oryn/internal/share"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/threads/threadstest"
)

// coordHost 同时充当 coord.HostChecker 与 api.CoordinatorHost；err 可在测试中途切换。
type coordHost struct {
	mu  sync.Mutex
	err error
}

func (h *coordHost) set(err error) { h.mu.Lock(); h.err = err; h.mu.Unlock() }
func (h *coordHost) Check(context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}
func (h *coordHost) Status(ctx context.Context) apigen.CoordinatorStatus {
	if err := h.Check(ctx); err != nil {
		r := err.Error()
		return apigen.CoordinatorStatus{Available: false, Reason: &r}
	}
	return apigen.CoordinatorStatus{Available: true}
}

type nopCoordOrbs struct{}

func (nopCoordOrbs) Delete(context.Context, string) error { return nil }

type nopCoordNotifier struct{}

func (nopCoordNotifier) Post(context.Context, string, string, string) error { return nil }

// newCoordEnv 起 threads + schedule + coord + 分享服务的 REST 栈；新 orb 停在 requested（不拉 agent），
// 已有一台协调 orb，所以建 Space / 助手对话不经 orbs.Create。
func newCoordEnv(t *testing.T) (*threadsEnv, *coordHost) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "data"))
	require.NoError(t, err)
	o, err := threadstest.NewOrbs(st, fakeomp(t), filepath.Join(dir, "orbs"), "FAKEOMP_DELAY_MS=1")
	require.NoError(t, err)
	o.SetFeature("coordinator", true)
	o.Hold(true)
	svc := threads.New(threads.Config{DataDir: filepath.Join(dir, "data")}, st, o)
	sched := schedule.New(schedule.Config{}, st, svc, nopCoordOrbs{}, nopCoordNotifier{})
	host := &coordHost{}
	c := coord.New(coord.Config{PublicURL: h2PublicURL}, st, svc, nil, host, sched)
	svc.SetCoordinator(c)
	_, err = o.Create(ctx, threads.OrbCreate{Coordinator: true})
	require.NoError(t, err)

	cfg := config.DefaultOrynd()
	cfg.PublicURL, cfg.DataDir = h2PublicURL, filepath.Join(dir, "data")
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st}}
	s.Threads = &api.ThreadsDeps{Service: svc}
	s.Coord = &api.CoordDeps{Service: c, Host: host}
	s.Shares = &api.ShareDeps{Service: share.New(share.Config{PublicURL: h2PublicURL}, st, svc, nopShareHub{}, nil, nil, nil)}
	srv := httptest.NewServer(api.NewRouter(s))
	t.Cleanup(func() {
		srv.Close()
		o.Close()
		svc.Close()
		_ = st.Close()
	})
	return &threadsEnv{t: t, st: st, svc: svc, srv: srv, dir: dir}, host
}

// H6：@host 不进 provider 列表、不能删除、不能作项目默认 provider，也不能用来建工作线程。
func TestHostProviderHidden(t *testing.T) {
	e := newThreadsEnv(t)
	ctx := context.Background()
	require.NoError(t, e.st.Q.UpsertHostProvider(ctx, db.UpsertHostProviderParams{
		ID: ids.New("prov"), TokenHash: ids.Hash(ids.Token("opv")), Version: "t", Capabilities: "{}", OrbTypes: "[]", CreatedAt: 1,
	}))
	host, err := e.st.Q.GetProviderByName(ctx, "@host")
	require.NoError(t, err)

	code, body := e.do("GET", "/api/v1/providers", nil)
	require.Equal(t, 200, code)
	var list apigen.ProviderList
	require.NoError(t, json.Unmarshal(body, &list))
	for _, p := range list.Providers {
		assert.NotEqual(t, "@host", p.Name)
	}
	code, _ = e.do("DELETE", "/api/v1/providers/"+host.ID, nil)
	assert.Equal(t, 404, code)
	_, err = e.st.Q.GetProvider(ctx, host.ID)
	assert.NoError(t, err, "@host 没被删")

	code, _ = e.do("POST", "/api/v1/projects", map[string]any{"name": "p", "defaultProvider": "@host"})
	assert.Equal(t, 400, code)

	code, body = e.do("POST", "/api/v1/threads", map[string]any{"prompt": "x", "executor": map[string]any{"kind": "new_orb", "provider": "@host"}})
	assert.Equal(t, 400, code)
	assert.Equal(t, threads.CodeCoordinatorRestricted, errCode(body))
}

// 2.5：Space 五个接口、助手对话、协调宿主状态、操作记录、spaceId / kind / space 过滤、协调对话不可分享。
func TestCoordinatorREST(t *testing.T) {
	e, host := newCoordEnv(t)

	code, body := e.do("GET", "/api/v1/coordinator", nil)
	require.Equal(t, 200, code)
	var status apigen.CoordinatorStatus
	require.NoError(t, json.Unmarshal(body, &status))
	assert.True(t, status.Available)

	code, body = e.do("POST", "/api/v1/projects", map[string]any{"name": "alpha"})
	require.Equal(t, 201, code, string(body))
	var prj apigen.Project
	require.NoError(t, json.Unmarshal(body, &prj))

	// 宿主不可用：409 coordinator_unavailable，message 是原因，不留下 Space
	host.set(errors.New("landlock unavailable"))
	code, body = e.do("POST", "/api/v1/spaces", map[string]any{"name": "Alpha"})
	assert.Equal(t, 409, code)
	assert.Equal(t, coord.CodeCoordinatorUnavailable, errCode(body))
	assert.Contains(t, string(body), "landlock unavailable")
	code, body = e.do("POST", "/api/v1/assistant/conversations", nil)
	assert.Equal(t, 409, code, string(body))
	code, body = e.do("GET", "/api/v1/coordinator", nil)
	require.Equal(t, 200, code)
	require.NoError(t, json.Unmarshal(body, &status))
	assert.False(t, status.Available)
	host.set(nil)

	code, body = e.do("POST", "/api/v1/spaces", map[string]any{"name": "Alpha", "goal": "ship", "projectId": prj.ID})
	require.Equal(t, 201, code, string(body))
	var sp apigen.Space
	require.NoError(t, json.Unmarshal(body, &sp))
	code, body = e.do("POST", "/api/v1/spaces", map[string]any{"name": "Alpha"})
	assert.Equal(t, 409, code)
	assert.Equal(t, coord.CodeSpaceNameTaken, errCode(body))
	code, _ = e.do("POST", "/api/v1/spaces", map[string]any{"name": "Beta", "projectId": "prj_missing"})
	assert.Equal(t, 404, code)

	code, body = e.do("GET", "/api/v1/spaces/"+sp.ID, nil)
	require.Equal(t, 200, code)
	code, _ = e.do("GET", "/api/v1/spaces/spc_missing", nil)
	assert.Equal(t, 404, code)
	code, body = e.do("PATCH", "/api/v1/spaces/"+sp.ID, map[string]any{"goal": "ship M9"})
	require.Equal(t, 200, code, string(body))
	require.NoError(t, json.Unmarshal(body, &sp))
	assert.Equal(t, "ship M9", sp.Goal)
	code, body = e.do("GET", "/api/v1/spaces", nil)
	require.Equal(t, 200, code)
	var spaces apigen.SpaceList
	require.NoError(t, json.Unmarshal(body, &spaces))
	assert.Len(t, spaces.Spaces, 1)

	code, body = e.do("POST", "/api/v1/assistant/conversations", nil)
	require.Equal(t, 201, code, string(body))
	var asst apigen.ThreadEnvelope
	require.NoError(t, json.Unmarshal(body, &asst))
	require.NotNil(t, asst.Thread.Kind)
	assert.Equal(t, apigen.ThreadKind("assistant"), *asst.Thread.Kind)

	// 工作线程带 spaceId：未给项目时取 Space 的项目；Space 不存在 → 404
	code, body = e.do("POST", "/api/v1/threads", map[string]any{"prompt": "x", "spaceId": sp.ID, "executor": map[string]any{"kind": "new_orb"}})
	require.Equal(t, 201, code, string(body))
	var w apigen.ThreadEnvelope
	require.NoError(t, json.Unmarshal(body, &w))
	require.NotNil(t, w.Thread.SpaceID)
	assert.Equal(t, sp.ID, *w.Thread.SpaceID)
	require.NotNil(t, w.Thread.ProjectID)
	assert.Equal(t, prj.ID, *w.Thread.ProjectID)
	code, body = e.do("POST", "/api/v1/threads", map[string]any{"prompt": "x", "spaceId": "spc_missing", "executor": map[string]any{"kind": "new_orb"}})
	assert.Equal(t, 404, code)
	assert.Equal(t, "space_not_found", errCode(body))
	code, body = e.do("POST", "/api/v1/threads", map[string]any{"prompt": "x", "executor": map[string]any{"kind": "orb", "orbId": sp.ThreadID}})
	assert.NotEqual(t, 201, code, string(body))

	listIDs := func(q string) []string {
		t.Helper()
		code, body := e.do("GET", "/api/v1/threads"+q, nil)
		require.Equal(t, 200, code, string(body))
		var l apigen.ThreadList
		require.NoError(t, json.Unmarshal(body, &l))
		out := []string{}
		for _, th := range l.Threads {
			out = append(out, th.ID)
		}
		return out
	}
	assert.Equal(t, []string{w.Thread.ID}, listIDs(""), "缺省只列工作线程")
	assert.Equal(t, []string{w.Thread.ID}, listIDs("?space="+sp.ID))
	assert.Equal(t, []string{sp.ThreadID}, listIDs("?kind=space"))
	assert.Equal(t, []string{asst.Thread.ID}, listIDs("?kind=assistant"))
	assert.Len(t, listIDs("?kind=all"), 3)
	code, _ = e.do("GET", "/api/v1/threads?kind=bogus", nil)
	assert.Equal(t, 400, code)

	// 协调对话不可分享 / fork；工作线程可以
	for _, id := range []string{sp.ThreadID, asst.Thread.ID} {
		code, body = e.do("POST", "/api/v1/threads/"+id+"/shares", map[string]any{"mode": "snapshot", "acknowledgeFindings": true})
		assert.Equal(t, 400, code)
		assert.Equal(t, threads.CodeCoordinatorRestricted, errCode(body))
	}

	// 进度图参数校验
	code, body = e.do("GET", "/api/v1/spaces/"+sp.ID+"/progress?days=7&tz=Asia/Shanghai", nil)
	require.Equal(t, 200, code, string(body))
	var prog apigen.SpaceProgress
	require.NoError(t, json.Unmarshal(body, &prog))
	assert.Len(t, prog.Days, 7)
	for _, q := range []string{"?days=0", "?days=91", "?tz=Mars/Base"} {
		code, _ = e.do("GET", "/api/v1/spaces/"+sp.ID+"/progress"+q, nil)
		assert.Equal(t, 400, code, q)
	}

	// 操作记录：按线程过滤（此处无记录；写入路径见 internal/coord 与 e2e），limit 校验
	code, body = e.do("GET", "/api/v1/automation/actions?thread="+sp.ThreadID, nil)
	require.Equal(t, 200, code, string(body))
	var acts apigen.AutomationActionList
	require.NoError(t, json.Unmarshal(body, &acts))
	assert.Empty(t, acts.Actions)
	for _, q := range []string{"?limit=0", "?limit=201"} {
		code, _ = e.do("GET", "/api/v1/automation/actions"+q, nil)
		assert.Equal(t, 400, code, q)
	}

	// 协调线程不能直接删除；删除 Space 后工作线程移出
	code, _ = e.do("DELETE", "/api/v1/threads/"+sp.ThreadID, nil)
	assert.Equal(t, 409, code)
	code, _ = e.do("DELETE", "/api/v1/spaces/"+sp.ID, nil)
	assert.Equal(t, 204, code)
	got, err := e.svc.Get(context.Background(), w.Thread.ID)
	require.NoError(t, err)
	assert.Nil(t, got.SpaceID)
}

// 协调层未配置：Space / 助手 / 操作记录回 501，GET coordinator 回 available=false。
func TestCoordinatorRESTNotConfigured(t *testing.T) {
	e := newThreadsEnv(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/spaces"}, {"POST", "/api/v1/assistant/conversations"}, {"GET", "/api/v1/automation/actions"},
	} {
		code, _ := e.do(c.method, c.path, map[string]any{})
		assert.Equal(t, 501, code, c.path)
	}
	code, body := e.do("GET", "/api/v1/coordinator", nil)
	require.Equal(t, 200, code)
	var status apigen.CoordinatorStatus
	require.NoError(t, json.Unmarshal(body, &status))
	assert.False(t, status.Available)
	require.NotNil(t, status.Reason)
}
