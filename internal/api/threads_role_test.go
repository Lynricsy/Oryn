package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/threads/threadstest"
)

// createThread 带 role：首批命令（落库的 startup frames）在 prompt 之前有 oryn_select_model{role}。
// orb 停在 requested，首批命令不会被下发清掉。
func TestCreateThreadWithRole(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "data"))
	require.NoError(t, err)
	orbs, err := threadstest.NewOrbs(st, fakeomp(t), filepath.Join(dir, "orbs"))
	require.NoError(t, err)
	orbs.Hold(true)
	svc := threads.New(threads.Config{DataDir: filepath.Join(dir, "data")}, st, orbs)
	cfg := config.DefaultOrynd()
	cfg.DataDir = filepath.Join(dir, "data")
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st}}
	s.Threads = &api.ThreadsDeps{Service: svc}
	srv := httptest.NewServer(api.NewRouter(s))
	t.Cleanup(func() {
		srv.Close()
		orbs.Close()
		svc.Close()
		_ = st.Close()
	})
	e := &threadsEnv{t: t, st: st, svc: svc, srv: srv, dir: dir}

	code, body := e.do("POST", "/api/v1/threads", map[string]any{
		"prompt": "role hello", "executor": map[string]any{"kind": "new_orb"}, "role": "Cyrene", "thinkingLevel": "high",
	})
	require.Equal(t, http.StatusCreated, code, string(body))
	var env apigen.ThreadEnvelope
	require.NoError(t, json.Unmarshal(body, &env))
	row, err := st.Q.GetThread(context.Background(), env.Thread.ID)
	require.NoError(t, err)
	frames := string(row.StartupFrames)
	sel := strings.Index(frames, `"oryn_select_model"`)
	prompt := strings.Index(frames, `role hello`)
	require.GreaterOrEqual(t, sel, 0, frames)
	assert.Contains(t, frames, `"role":"Cyrene"`)
	assert.Less(t, sel, prompt, "oryn_select_model 排在 prompt 之前")

	// 不带 role：首批命令里没有 oryn_select_model
	code, body = e.do("POST", "/api/v1/threads", map[string]any{"prompt": "plain", "executor": map[string]any{"kind": "new_orb"}})
	require.Equal(t, http.StatusCreated, code, string(body))
	require.NoError(t, json.Unmarshal(body, &env))
	row, err = st.Q.GetThread(context.Background(), env.Thread.ID)
	require.NoError(t, err)
	assert.NotContains(t, string(row.StartupFrames), "oryn_select_model")

	// 非法 role → 400
	code, body = e.do("POST", "/api/v1/threads", map[string]any{"prompt": "x", "executor": map[string]any{"kind": "new_orb"}, "role": "a\nb"})
	assert.Equal(t, http.StatusBadRequest, code, string(body))
}
