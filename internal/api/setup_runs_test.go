package api_test

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func (e *orbsEnv) insertOrb(id, state string) {
	e.t.Helper()
	require.NoError(e.t, e.st.Q.CreateOrb(context.Background(), db.CreateOrbParams{
		ID: id, ProviderID: "prov_1", Type: "small", State: state, StateChangedAt: 1, TokenHash: "h-" + id, CreatedAt: 1,
	}))
}

func (e *orbsEnv) insertRun(id, orbID, phase string, finished int64, ok bool, log string) {
	e.t.Helper()
	okv := int64(0)
	if ok {
		okv = 1
	}
	require.NoError(e.t, e.st.Q.InsertOrbSetupRun(context.Background(), db.InsertOrbSetupRunParams{
		ID: id, OrbID: orbID, Phase: phase, Trigger: "auto", StartedAt: finished - 10, FinishedAt: finished,
		Ok: okv, SetupRan: 1, DurationMs: 10, HasRepoScript: 1, LogTail: []byte(log),
	}))
}

func (e *orbsEnv) getRaw(path string) (int, string, string) {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+e.dev)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
}

func TestOrbSetupRunsAPI(t *testing.T) {
	e := newOrbsEnv(t)
	ctx := context.Background()
	const a, b = "orb-aaaaaaaaaaaaaaaaaaaaaaaaaa", "orb-bbbbbbbbbbbbbbbbbbbbbbbbbb"
	e.insertOrb(a, "ready")
	e.insertOrb(b, "failed")
	e.insertRun("osr_1", a, "setup", 100, false, "boom\n")
	e.insertRun("osr_2", a, "resume", 200, true, "resumed\n")
	e.insertRun("osr_3", a, "setup", 300, true, "第二次 ok\n")
	require.NoError(t, e.st.Q.InsertOrbEvent(ctx, db.InsertOrbEventParams{OrbID: b, Ts: 5, FromState: "spawning", ToState: "failed", Detail: `{"job":"j","error":"docker run: no such image"}`}))

	var got apigen.Orb
	require.Equal(t, 200, e.do("GET", "/api/v1/orbs/"+a, e.dev, nil, &got))
	require.NotNil(t, got.LastSetup)
	assert.Equal(t, "osr_3", got.LastSetup.ID, "lastSetup 是最近一次 setup")
	assert.True(t, got.LastSetup.Ok)
	assert.True(t, got.LastSetup.HasRepoScript)
	require.NotNil(t, got.LastResume)
	assert.Equal(t, "osr_2", got.LastResume.ID)
	assert.Nil(t, got.FailureReason)

	// 失败原因：无 reason 时取任务错误；列表与单个一致
	var failed apigen.Orb
	require.Equal(t, 200, e.do("GET", "/api/v1/orbs/"+b, e.dev, nil, &failed))
	require.NotNil(t, failed.FailureReason)
	assert.Equal(t, "docker run: no such image", *failed.FailureReason)
	assert.Nil(t, failed.LastSetup)
	var list apigen.OrbList
	require.Equal(t, 200, e.do("GET", "/api/v1/orbs?state=failed", e.dev, nil, &list))
	require.Len(t, list.Orbs, 1)
	assert.Equal(t, "docker run: no such image", *list.Orbs[0].FailureReason)

	var runs apigen.OrbSetupRunList
	require.Equal(t, 200, e.do("GET", "/api/v1/orbs/"+a+"/setup-runs", e.dev, nil, &runs))
	require.Len(t, runs.Runs, 3)
	assert.Equal(t, []string{"osr_3", "osr_2", "osr_1"}, []string{runs.Runs[0].ID, runs.Runs[1].ID, runs.Runs[2].ID})
	assert.False(t, runs.Runs[2].Ok)
	status, _, body := e.getRaw("/api/v1/orbs/" + b + "/setup-runs")
	assert.Equal(t, 200, status)
	assert.JSONEq(t, `{"runs":[]}`, body, "空列表编码为 []")
	assert.Equal(t, 404, e.do("GET", "/api/v1/orbs/orb-nope/setup-runs", e.dev, nil, nil))

	var ct string
	status, ct, body = e.getRaw("/api/v1/orbs/" + a + "/setup-runs/osr_3/log")
	assert.Equal(t, 200, status)
	assert.Equal(t, "text/plain; charset=utf-8", ct)
	assert.Equal(t, "第二次 ok\n", body)

	// 运行记录不属于路径中的 orb → 404，不泄露别的 orb 的日志
	status, _, body = e.getRaw("/api/v1/orbs/" + b + "/setup-runs/osr_1/log")
	assert.Equal(t, 404, status)
	assert.NotContains(t, body, "boom")
	status, _, _ = e.getRaw("/api/v1/orbs/" + a + "/setup-runs/osr_nope/log")
	assert.Equal(t, 404, status)
}
