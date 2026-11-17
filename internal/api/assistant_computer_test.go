package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/coord"
	"github.com/Lynricsy/Oryn/internal/schedule"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/threads/threadstest"
)

// computerMachines 是只扮演助手的电脑的 coord.Machines：创建即就绪，暂停即冻结，删除即消失。
type computerMachines struct {
	mu  sync.Mutex
	orb *apigen.Orb
	// stuck 为真时创建出来的电脑一直停在 provisioning。
	stuck bool
}

func (m *computerMachines) ListRunners(context.Context) ([]apigen.Runner, error) { return nil, nil }
func (m *computerMachines) ListOrbs(context.Context) ([]apigen.Orb, error)       { return nil, nil }
func (m *computerMachines) Pause(_ context.Context, _, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orb.State, m.orb.Features = apigen.OrbStateFrozen, nil
	return nil
}
func (m *computerMachines) Wake(context.Context, string) error { return nil }
func (m *computerMachines) Delete(context.Context, string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orb = nil
	return nil
}
func (m *computerMachines) EnsureUsable(context.Context, string, string, bool, time.Duration) error {
	return nil
}
func (m *computerMachines) Do(context.Context, string, *http.Request) (*http.Response, error) {
	return nil, http.ErrNotSupported
}
func (m *computerMachines) ScheduleOmpApply(string) {}
func (m *computerMachines) CreateComputer(_ context.Context, provider, orbType string) (apigen.Orb, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orb = &apigen.Orb{ID: "orb_comp", State: apigen.OrbStateReady, Type: "small", ProviderName: &provider,
		Features: []string{"fs", "computer"}, AgentVersion: new("v1")}
	if m.stuck {
		m.orb.State, m.orb.Features = apigen.OrbStateSpawning, nil
	}
	return *m.orb, nil
}
func (m *computerMachines) Computer(context.Context) (apigen.Orb, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.orb == nil {
		return apigen.Orb{}, false, nil
	}
	return *m.orb, true, nil
}
func (m *computerMachines) TouchAgent(context.Context, string) {}
func (m *computerMachines) ComputerCall(_ context.Context, _, _ string, _, result any) error {
	b, _ := json.Marshal(apigen.LinkCtlComputerStartResult{AgentVersion: "v1",
		Tools: []apigen.OmpFrame{{"name": "bash", "description": "Run.", "parameters": map[string]any{}}}})
	return json.Unmarshal(b, result)
}
func (m *computerMachines) OnComputerUpdate(func(string, string, json.RawMessage)) {}

// M19 4.3：助手的电脑的 REST：还没有时 orbId 省略；唤醒即创建并等到就绪；暂停冻结（没有 → 404）；重置删除且幂等。
func TestAssistantComputerREST(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "data"))
	require.NoError(t, err)
	o, err := threadstest.NewOrbs(st, fakeomp(t), filepath.Join(dir, "orbs"), "FAKEOMP_DELAY_MS=1")
	require.NoError(t, err)
	o.Hold(true)
	svc := threads.New(threads.Config{DataDir: filepath.Join(dir, "data")}, st, o)
	sched := schedule.New(schedule.Config{}, st, svc, nopCoordOrbs{}, nopCoordNotifier{})
	c := coord.New(coord.Config{PublicURL: h2PublicURL}, st, svc, nil, &coordHost{}, sched)
	m := &computerMachines{}
	c.SetMachines(m)
	cfg := config.DefaultOrynd()
	cfg.PublicURL, cfg.DataDir = h2PublicURL, filepath.Join(dir, "data")
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st}}
	s.Threads = &api.ThreadsDeps{Service: svc}
	s.Coord = &api.CoordDeps{Service: c, Host: &coordHost{}}
	srv := httptest.NewServer(api.NewRouter(s))
	t.Cleanup(func() {
		srv.Close()
		o.Close()
		svc.Close()
		_ = st.Close()
	})
	e := &threadsEnv{t: t, st: st, svc: svc, srv: srv, dir: dir}
	get := func(method, path string) (int, apigen.AssistantComputer, []byte) {
		code, body := e.do(method, path, nil)
		var out apigen.AssistantComputer
		_ = json.Unmarshal(body, &out)
		return code, out, body
	}

	code, out, body := get("GET", "/api/v1/assistant/computer")
	require.Equal(t, 200, code, string(body))
	assert.Nil(t, out.OrbID)
	assert.False(t, out.ToolsReady)
	code, _, body = get("POST", "/api/v1/assistant/computer/pause")
	assert.Equal(t, 404, code, string(body))
	assert.Equal(t, "not_found", errCode(body))

	code, out, body = get("POST", "/api/v1/assistant/computer/wake")
	require.Equal(t, 200, code, string(body))
	assert.Equal(t, "orb_comp", *out.OrbID)
	assert.Equal(t, apigen.OrbStateReady, *out.State)
	assert.True(t, out.ToolsReady, "唤醒后工具定义已缓存")

	code, out, body = get("POST", "/api/v1/assistant/computer/pause")
	require.Equal(t, 200, code, string(body))
	assert.Equal(t, apigen.OrbStateFrozen, *out.State)

	code, out, body = get("POST", "/api/v1/assistant/computer/reset")
	require.Equal(t, 200, code, string(body))
	assert.Nil(t, out.OrbID)
	code, _, body = get("POST", "/api/v1/assistant/computer/reset")
	assert.Equal(t, 200, code, string(body), "幂等")

	m.mu.Lock()
	m.stuck = true
	m.mu.Unlock()
	ctx2, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	_, err = c.WakeComputer(ctx2)
	assert.Error(t, err, "迟迟不就绪时不返回成功")
}
