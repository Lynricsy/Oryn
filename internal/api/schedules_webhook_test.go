package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/schedule"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
)

// M31：定时的入站 Webhook 走公开路径（不带凭据），管理接口要登录；签名、未知密钥、过大请求体按契约回状态码。
func TestScheduleWebhookAPI(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.LoadOrCreate(dir)
	require.NoError(t, err)
	svc, err := auth.NewService(st, dir, h2PublicURL)
	require.NoError(t, err)
	cfg := config.DefaultOrynd()
	cfg.PublicURL, cfg.DataDir = h2PublicURL, dir
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st, AuthMiddleware: auth.Middleware(st)}}
	s.Auth = &api.AuthDeps{Service: svc}
	s.Schedules = &api.ScheduleDeps{Service: schedule.New(schedule.Config{PublicURL: h2PublicURL, Box: box}, st, schedThreads{st}, nopOrbs{}, nil)}
	srv := httptest.NewServer(api.NewRouter(s))
	t.Cleanup(srv.Close)
	_, tok, err := auth.CreateDevice(ctx, st, "t", "cli", "")
	require.NoError(t, err)

	send := func(method, path string, body []byte, header map[string]string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
		require.NoError(t, err)
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}
	owner := func(method, path string, v any) (int, []byte) {
		b, _ := json.Marshal(v)
		return send(method, path, b, map[string]string{"Authorization": "Bearer " + tok, "Content-Type": "application/json"})
	}

	code, b := owner(http.MethodPost, "/api/v1/schedules", map[string]any{"mode": "new_thread", "name": "alerts", "kind": "manual", "prompt": "p"})
	require.Equal(t, http.StatusCreated, code, string(b))
	sc := decode[apigen.Schedule](t, b)

	code, b = send(http.MethodPut, "/api/v1/schedules/"+sc.ID+"/webhook", []byte(`{}`), map[string]string{"Content-Type": "application/json"})
	assert.Equal(t, http.StatusUnauthorized, code, "管理接口要登录")

	code, b = owner(http.MethodPut, "/api/v1/schedules/"+sc.ID+"/webhook", map[string]any{})
	require.Equal(t, http.StatusOK, code, string(b))
	res := decode[apigen.ScheduleWebhookResult](t, b)
	require.NotNil(t, res.URL)
	path, ok := strings.CutPrefix(*res.URL, strings.TrimRight(h2PublicURL, "/"))
	require.True(t, ok, *res.URL)

	code, b = send(http.MethodPost, path+"?source=test", []byte(`{"alert":"disk"}`), map[string]string{"Content-Type": "application/json"})
	require.Equal(t, http.StatusAccepted, code, string(b))
	acc := decode[apigen.ScheduleWebhookAccepted](t, b)
	assert.NotEmpty(t, acc.RunID)

	code, b = send(http.MethodPost, "/api/v1/public/hooks/owh_unknown", []byte(`x`), nil)
	assert.Equal(t, http.StatusNotFound, code)
	assert.Equal(t, "not_found", errCode(b))

	code, b = send(http.MethodPost, path, bytes.Repeat([]byte("a"), schedule.WebhookMaxBody+1), nil)
	assert.Equal(t, http.StatusRequestEntityTooLarge, code, string(b))

	code, b = owner(http.MethodPut, "/api/v1/schedules/"+sc.ID+"/webhook", map[string]any{"secret": "s3cret"})
	require.Equal(t, http.StatusOK, code, string(b))
	assert.Nil(t, decode[apigen.ScheduleWebhookResult](t, b).URL, "只改签名不换地址")
	code, b = send(http.MethodPost, path, []byte(`x`), nil)
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Equal(t, "bad_signature", errCode(b))

	code, _ = owner(http.MethodDelete, "/api/v1/schedules/"+sc.ID+"/webhook", nil)
	assert.Equal(t, http.StatusNoContent, code)
	code, _ = send(http.MethodPost, path, []byte(`x`), nil)
	assert.Equal(t, http.StatusNotFound, code)
}
