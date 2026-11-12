package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

type orbsEnv struct {
	t     *testing.T
	srv   *httptest.Server
	st    *store.Store
	svc   *orbs.Service
	dev   string
	provT string
}

func newOrbsEnv(t *testing.T) *orbsEnv {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	q := jobs.New(st)
	svc := orbs.New(orbs.Config{
		Idle:      config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(time.Hour)},
		PublicURL: "http://x", TokenKey: make([]byte, 32),
	}, st, q, nil, nil)
	cfg := config.DefaultOrynd()
	cfg.PublicURL, cfg.DataDir = "https://oryn.test", dir
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st, AuthMiddleware: auth.Middleware(st)}}
	s.Orbs = &api.OrbsDeps{Service: svc}
	e := &orbsEnv{t: t, st: st, svc: svc, srv: httptest.NewServer(api.NewRouter(s))}
	t.Cleanup(func() {
		e.srv.Close()
		svc.Close()
		_ = st.Close()
	})
	_, e.dev, err = auth.CreateDevice(ctx, st, "t", "cli", "")
	require.NoError(t, err)
	e.provT = ids.Token("opv")
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: ids.Hash(e.provT), CreatedAt: 1}))
	return e
}

func (e *orbsEnv) do(method, path, token string, body any, out any) int {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		require.NoError(e.t, json.Unmarshal(raw, out), string(raw))
	}
	return resp.StatusCode
}

func TestOrbsAndStatsAPI(t *testing.T) {
	e := newOrbsEnv(t)
	ctx := context.Background()

	// provider 协议走自己的 opv_ 令牌，绕过设备 / 会话鉴权。
	hello := apigen.ProviderHelloRequest{InstanceID: "i1", Driver: apigen.Docker, Types: []apigen.OrbType{{Name: "small", Cpus: 2, MemoryMB: 4096}}, DefaultType: "small"}
	var hr apigen.ProviderHelloResponse
	require.Equal(t, 200, e.do("POST", "/provider/v1/hello", e.provT, hello, &hr))
	assert.Equal(t, "prov_1", hr.ProviderID)
	assert.Equal(t, 401, e.do("POST", "/provider/v1/hello", e.dev, hello, nil))
	assert.Equal(t, 401, e.do("GET", "/api/v1/orbs", e.provT, nil, nil), "provider 令牌不能访问客户端 API")

	orb, err := e.svc.Create(ctx, orbs.CreateParams{})
	require.NoError(t, err)

	var list apigen.OrbList
	require.Equal(t, 200, e.do("GET", "/api/v1/orbs", e.dev, nil, &list))
	require.Len(t, list.Orbs, 1)
	assert.Equal(t, orb.ID, list.Orbs[0].ID)
	assert.Equal(t, "local", *list.Orbs[0].ProviderName)
	assert.Equal(t, apigen.OrbStateRequested, list.Orbs[0].State)
	assert.NotNil(t, list.Orbs[0].ThreadIds)
	require.Equal(t, 200, e.do("GET", "/api/v1/orbs?state=ready", e.dev, nil, &list))
	assert.Empty(t, list.Orbs)

	// 认领 spawn 任务 → spawning。
	var cr apigen.ProviderClaimResponse
	require.Equal(t, 200, e.do("POST", "/provider/v1/jobs/claim", e.provT, apigen.ProviderClaimRequest{InstanceID: "i1", WaitSeconds: 1}, &cr))
	assert.Equal(t, apigen.Spawn, cr.Job.Op)
	assert.Equal(t, orb.ID, cr.Job.OrbID)
	var got apigen.Orb
	require.Equal(t, 200, e.do("GET", "/api/v1/orbs/"+orb.ID, e.dev, nil, &got))
	assert.Equal(t, apigen.OrbStateSpawning, got.State)
	assert.EqualValues(t, 2, got.Cpus)

	// spawning 时不能冻结；唤醒为无操作。
	assert.Equal(t, 409, e.do("POST", "/api/v1/orbs/"+orb.ID+"/pause", e.dev, map[string]string{"mode": "freeze"}, nil))
	assert.Equal(t, 400, e.do("POST", "/api/v1/orbs/"+orb.ID+"/pause", e.dev, map[string]string{"mode": "nap"}, nil))
	assert.Equal(t, 202, e.do("POST", "/api/v1/orbs/"+orb.ID+"/resume", e.dev, nil, &got))
	assert.Equal(t, apigen.OrbStateSpawning, got.State)
	assert.Equal(t, 404, e.do("GET", "/api/v1/orbs/orb-nope", e.dev, nil, nil))

	require.Equal(t, 204, e.do("POST", "/provider/v1/jobs/"+cr.Job.ID+"/complete", e.provT, apigen.ProviderCompleteRequest{Ok: true}, nil))
	require.Equal(t, 200, e.do("GET", "/api/v1/orbs/"+orb.ID, e.dev, nil, &got))
	assert.Equal(t, apigen.OrbStateConnecting, got.State)

	// 采样与统计。
	for i := range 4 {
		require.NoError(t, e.st.Q.InsertOrbSample(ctx, db.InsertOrbSampleParams{OrbID: orb.ID, Ts: 1_000_000 + int64(i)*15000, CpuPct: float64(i), MemUsedMb: 10, MemLimitMb: 100}))
	}
	var m apigen.OrbMetrics
	require.Equal(t, 200, e.do("GET", "/api/v1/orbs/"+orb.ID+"/metrics?from=0&to=2000000&step=60", e.dev, nil, &m))
	assert.Equal(t, 60, m.Step)
	require.NotEmpty(t, m.Samples)
	require.Equal(t, 200, e.do("GET", "/api/v1/orbs/"+orb.ID+"/metrics?from=0&to=2000000", e.dev, nil, &m))
	assert.Len(t, m.Samples, 4)
	assert.Equal(t, 404, e.do("GET", "/api/v1/orbs/orb-nope/metrics", e.dev, nil, nil))

	require.NoError(t, e.st.Q.InsertUsageRecord(ctx, db.InsertUsageRecordParams{EntrySeq: 1, Ts: time.Now().UnixMilli(), Provider: "anthropic", Model: "opus", InputTokens: 5, OutputTokens: 7}))
	var u apigen.UsageStats
	require.Equal(t, 200, e.do("GET", "/api/v1/stats/usage?groupBy=model", e.dev, nil, &u))
	require.Len(t, u.Groups, 1)
	assert.EqualValues(t, 5, u.Groups[0].InputTokens)
	assert.EqualValues(t, 7, u.Groups[0].OutputTokens)
	assert.Equal(t, 400, e.do("GET", "/api/v1/stats/usage?groupBy=nope", e.dev, nil, nil))

	var os apigen.OrbStats
	require.Equal(t, 200, e.do("GET", "/api/v1/stats/orbs", e.dev, nil, &os))
	require.Len(t, os.Types, 1)
	assert.Equal(t, "small", os.Types[0].Type)
	assert.Equal(t, 1, os.Types[0].Spawns)

	// 删除 → deleting，并入队 delete 任务。
	require.Equal(t, 202, e.do("DELETE", "/api/v1/orbs/"+orb.ID, e.dev, nil, &got))
	assert.Equal(t, apigen.OrbStateDeleting, got.State)
	require.Eventually(t, func() bool {
		var c apigen.ProviderClaimResponse
		return e.do("POST", "/provider/v1/jobs/claim", e.provT, apigen.ProviderClaimRequest{InstanceID: "i1", WaitSeconds: 1}, &c) == 200 && c.Job.Op == apigen.Delete
	}, 5*time.Second, 50*time.Millisecond)

	require.Equal(t, 204, e.do("POST", "/provider/v1/bye", e.provT, map[string]string{"instanceId": "i1"}, nil))
}
