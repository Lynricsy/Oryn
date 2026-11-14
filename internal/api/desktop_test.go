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
	"github.com/Lynricsy/Oryn/internal/desktop"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

type desktopEnv struct {
	t    *testing.T
	ctx  context.Context
	srv  *httptest.Server
	st   *store.Store
	orbs *orbs.Service
	dev  string
}

func newDesktopEnv(t *testing.T) *desktopEnv {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	osvc := orbs.New(orbs.Config{
		Idle:      config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(time.Hour)},
		PublicURL: "http://x", TokenKey: make([]byte, 32),
	}, st, jobs.New(st), nil, nil)
	box, err := secrets.New(bytes.Repeat([]byte{3}, 32))
	require.NoError(t, err)
	dsvc, err := desktop.New(desktop.Config{DataDir: dir, Box: box}, st, osvc, auth.NewTickets())
	require.NoError(t, err)
	cfg := config.DefaultOrynd()
	cfg.PublicURL, cfg.DataDir = "https://oryn.test", dir
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st, AuthMiddleware: auth.Middleware(st)}}
	s.Orbs = &api.OrbsDeps{Service: osvc}
	s.Desktop = &api.DesktopDeps{Service: dsvc}
	e := &desktopEnv{t: t, ctx: ctx, st: st, orbs: osvc, srv: httptest.NewServer(api.NewRouter(s))}
	t.Cleanup(func() {
		e.srv.Close()
		dsvc.Close()
		osvc.Close()
		_ = st.Close()
	})
	_, e.dev, err = auth.CreateDevice(ctx, st, "t", "cli", "")
	require.NoError(t, err)
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: ids.Hash(ids.Token("opv")), CreatedAt: 1}))
	return e
}

func (e *desktopEnv) do(method, path string, body any) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+e.dev)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func codeOf(raw []byte) string {
	var b struct{ Code string }
	_ = json.Unmarshal(raw, &b)
	return b.Code
}

func (e *desktopEnv) orb(state, title string) string {
	e.t.Helper()
	id := ids.Orb()
	require.NoError(e.t, e.st.Q.CreateOrb(e.ctx, db.CreateOrbParams{ID: id, ProviderID: "prov_1", Type: "small", State: state, StateChangedAt: 1, TokenHash: ids.Hash(id), CreatedAt: 1}))
	if title != "" {
		require.NoError(e.t, e.st.Q.CreateThread(e.ctx, db.CreateThreadParams{Kind: "work", ID: ids.Thread(), OrbID: &id, Title: title, TitleSource: "user", State: "idle", SessionDir: "/s", CreatedAt: 1, UpdatedAt: 1}))
	}
	return id
}

func TestBrowserProfilesAPI(t *testing.T) {
	e := newDesktopEnv(t)
	code, raw := e.do("POST", "/api/v1/browser-profiles", apigen.BrowserProfileCreate{Name: " 工作 "})
	require.Equal(t, 201, code, string(raw))
	var p apigen.BrowserProfile
	require.NoError(t, json.Unmarshal(raw, &p))
	assert.Equal(t, "工作", p.Name)
	assert.Regexp(t, `^bpf_[0-9A-Z]{26}$`, p.ID)

	code, raw = e.do("POST", "/api/v1/browser-profiles", apigen.BrowserProfileCreate{Name: "工作"})
	assert.Equal(t, 409, code)
	assert.Equal(t, "profile_name_taken", codeOf(raw))
	code, raw = e.do("POST", "/api/v1/browser-profiles", apigen.BrowserProfileCreate{Name: "a\tb"})
	assert.Equal(t, 400, code)
	assert.Equal(t, "invalid_request", codeOf(raw))

	code, raw = e.do("PATCH", "/api/v1/browser-profiles/"+p.ID, apigen.BrowserProfilePatch{Name: "个人"})
	require.Equal(t, 200, code, string(raw))
	missing := "bpf_01JCZ8M3W4X5Y6Z7A8B9C0D1E2"
	code, raw = e.do("PATCH", "/api/v1/browser-profiles/"+missing, apigen.BrowserProfilePatch{Name: "x"})
	assert.Equal(t, 404, code)
	assert.Equal(t, "profile_not_found", codeOf(raw))

	// 被独占持有：列表带 holder（代表线程标题），删除 409 ProfileInUseConflict
	holder := e.orb(orbs.StateReady, "正在登录")
	_, err := e.st.Q.AcquireBrowserProfile(e.ctx, db.AcquireBrowserProfileParams{OrbID: holder, Now: 42, ID: p.ID})
	require.NoError(t, err)
	code, raw = e.do("GET", "/api/v1/browser-profiles", nil)
	require.Equal(t, 200, code)
	var list apigen.BrowserProfileList
	require.NoError(t, json.Unmarshal(raw, &list))
	require.Len(t, list.Profiles, 1)
	require.NotNil(t, list.Profiles[0].Holder)
	assert.Equal(t, "正在登录", *list.Profiles[0].Holder.ThreadTitle)
	code, raw = e.do("DELETE", "/api/v1/browser-profiles/"+p.ID, nil)
	assert.Equal(t, 409, code)
	var conflict apigen.ProfileInUseConflict
	require.NoError(t, json.Unmarshal(raw, &conflict))
	assert.Equal(t, apigen.ProfileInUseConflictCodeProfileInUse, conflict.Code)
	assert.Equal(t, p.ID, conflict.ProfileID)
	assert.Equal(t, "个人", conflict.ProfileName)
	assert.Equal(t, holder, conflict.Holder.OrbID)
	assert.EqualValues(t, 42, conflict.Holder.Since)
	assert.Equal(t, "正在登录", *conflict.Holder.ThreadTitle)

	_, err = e.st.Q.ReleaseBrowserProfileByOrb(e.ctx, holder)
	require.NoError(t, err)
	code, _ = e.do("DELETE", "/api/v1/browser-profiles/"+p.ID, nil)
	assert.Equal(t, 204, code)
	code, raw = e.do("DELETE", "/api/v1/browser-profiles/"+p.ID, nil)
	assert.Equal(t, 404, code)
	assert.Equal(t, "profile_not_found", codeOf(raw))
}

// orb 桌面接口经 orb 通道的错误映射：不存在 404、已删除 410。
func TestOrbDesktopAPIErrors(t *testing.T) {
	e := newDesktopEnv(t)
	gone := e.orb(orbs.StateDeleted, "")
	for _, c := range []struct{ method, path string }{
		{"PUT", "/desktop/profile"}, {"POST", "/desktop/profile/save"}, {"POST", "/desktop/browser/start"}, {"POST", "/desktop/browser/stop"},
	} {
		var body any
		if c.method == "PUT" {
			body = apigen.OrbDesktopProfilePut{ProfileID: ""}
		}
		code, raw := e.do(c.method, "/api/v1/orbs/"+gone+c.path, body)
		assert.Equal(t, 410, code, c.path)
		assert.Equal(t, "orb_deleted", codeOf(raw), c.path)
		code, raw = e.do(c.method, "/api/v1/orbs/orb-01jcz8m3w4x5y6z7a8b9c0d1e2"+c.path, body)
		assert.Equal(t, 404, code, c.path)
		assert.Equal(t, "not_found", codeOf(raw), c.path)
	}
	bad := apigen.BrowserProfileMode("bogus")
	code, raw := e.do("PUT", "/api/v1/orbs/"+gone+"/desktop/profile", apigen.OrbDesktopProfilePut{ProfileID: "", ProfileMode: &bad})
	assert.Equal(t, 400, code)
	assert.Equal(t, "invalid_request", codeOf(raw))
}

// 项目默认 Profile：建 / 改读写；不存在 404；PATCH 不给该字段时保留旧值（S0 回归），空串清除。
func TestProjectDefaultBrowserProfile(t *testing.T) {
	e := newDesktopEnv(t)
	_, raw := e.do("POST", "/api/v1/browser-profiles", apigen.BrowserProfileCreate{Name: "work"})
	var p apigen.BrowserProfile
	require.NoError(t, json.Unmarshal(raw, &p))
	missing := "bpf_01JCZ8M3W4X5Y6Z7A8B9C0D1E2"

	code, raw := e.do("POST", "/api/v1/projects", map[string]any{"name": "x", "defaultBrowserProfileId": missing})
	assert.Equal(t, 404, code)
	assert.Equal(t, "profile_not_found", codeOf(raw))
	code, raw = e.do("POST", "/api/v1/projects", map[string]any{"name": "demo", "defaultBrowserProfileId": p.ID})
	require.Equal(t, 201, code, string(raw))
	var prj apigen.Project
	require.NoError(t, json.Unmarshal(raw, &prj))
	require.NotNil(t, prj.DefaultBrowserProfileID)
	assert.Equal(t, p.ID, *prj.DefaultBrowserProfileID)

	// 只改别的字段：默认 Profile 保留
	code, raw = e.do("PATCH", "/api/v1/projects/"+prj.ID, map[string]any{"name": "demo2", "color": "red"})
	require.Equal(t, 200, code, string(raw))
	require.NoError(t, json.Unmarshal(raw, &prj))
	require.NotNil(t, prj.DefaultBrowserProfileID, "未指定时保留旧值")
	assert.Equal(t, p.ID, *prj.DefaultBrowserProfileID)
	row, err := e.st.Q.GetProject(e.ctx, prj.ID)
	require.NoError(t, err)
	require.NotNil(t, row.DefaultBrowserProfileID)

	code, raw = e.do("PATCH", "/api/v1/projects/"+prj.ID, map[string]any{"defaultBrowserProfileId": missing})
	assert.Equal(t, 404, code)
	assert.Equal(t, "profile_not_found", codeOf(raw))
	code, raw = e.do("PATCH", "/api/v1/projects/"+prj.ID, map[string]any{"defaultBrowserProfileId": ""})
	require.Equal(t, 200, code, string(raw))
	prj = apigen.Project{}
	require.NoError(t, json.Unmarshal(raw, &prj))
	assert.Nil(t, prj.DefaultBrowserProfileID, "空串清除")
	code, raw = e.do("GET", "/api/v1/projects/"+prj.ID, nil)
	require.Equal(t, 200, code)
	prj = apigen.Project{}
	require.NoError(t, json.Unmarshal(raw, &prj))
	assert.Nil(t, prj.DefaultBrowserProfileID)
}

// stubProfiles 让建线程的 Profile 解析返回预设错误。
type stubProfiles struct{ err error }

func (p stubProfiles) ResolveForNewOrb(context.Context, *string, string, apigen.BrowserProfileMode) (string, error) {
	return "", p.err
}

// POST threads：profile_in_use → 409 ProfileInUseConflict；profile_not_found → 404；profileMode 非法 → 400。
func TestCreateThreadProfileErrors(t *testing.T) {
	e := newThreadsEnv(t)
	conflict := &apigen.ProfileInUseConflict{
		Code: apigen.ProfileInUseConflictCodeProfileInUse, Message: "in use", ProfileID: "bpf_X", ProfileName: "work",
		Holder: apigen.BrowserProfileHolder{OrbID: "orb-x", ThreadIds: []string{}, Since: 7},
	}
	body := map[string]any{"prompt": "hi", "executor": map[string]any{"kind": "new_orb"}, "browserProfileId": "bpf_X"}

	e.svc.SetProfiles(stubProfiles{err: &desktop.Error{Code: "profile_in_use", Status: 409, Message: "in use", Conflict: conflict}})
	code, raw := e.do("POST", "/api/v1/threads", body)
	assert.Equal(t, 409, code)
	var got apigen.ProfileInUseConflict
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, *conflict, got)

	e.svc.SetProfiles(stubProfiles{err: &desktop.Error{Code: "profile_not_found", Status: 404, Message: "missing"}})
	code, raw = e.do("POST", "/api/v1/threads", body)
	assert.Equal(t, 404, code)
	assert.Equal(t, "profile_not_found", codeOf(raw))

	body["profileMode"] = "bogus"
	code, raw = e.do("POST", "/api/v1/threads", body)
	assert.Equal(t, 400, code)
	assert.Equal(t, "invalid_request", codeOf(raw))
}
