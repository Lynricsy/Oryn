package orbs

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/memory"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// memoryEgressEnv 是出站代理 → 记忆网关 → 假 Hindsight 的端到端环境：项目 A / B 各一台 orb，一台 runner 上跑着
// A、B 与无项目的线程。
type memoryEgressEnv struct {
	s                *Service
	prjA, prjB       string
	orbA, orbB, rnr  string
	tA, tB           string
	tRA, tRB, tRNone string

	mu   sync.Mutex
	key  string
	tags [][]string // 假 Hindsight 收到的每次召回的 tags
}

func newMemoryEgressEnv(t *testing.T) *memoryEgressEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	e := &memoryEgressEnv{prjA: ids.New("prj"), prjB: ids.New("prj")}

	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.key == "" || r.Header.Get("Authorization") != "Bearer "+e.key || r.URL.Path != "/v1/default/banks/oryn/memories/recall" {
			http.Error(w, `{"detail":"no"}`, http.StatusUnauthorized)
			return
		}
		var body struct {
			Tags      []string `json:"tags"`
			TagsMatch string   `json:"tags_match"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.TagsMatch != "any_strict" {
			http.Error(w, `{"detail":"tags_match"}`, http.StatusBadRequest)
			return
		}
		e.tags = append(e.tags, body.Tags)
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(hs.Close)

	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	for _, p := range []string{e.prjA, e.prjB} {
		require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{ID: p, Name: p, CreatedAt: 1, UpdatedAt: 1}))
	}
	mkOrb := func(project *string, kind *string) string {
		id := "orb-" + strings.ToLower(ids.New("x")[2:])
		require.NoError(t, st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: id, ProviderID: "prov_1", Type: "small", Image: "img", State: StateReady,
			TokenHash: ids.Token("h"), CreatedAt: 1, ProjectID: project, Kind: kind}))
		return id
	}
	e.orbA, e.orbB, e.rnr = mkOrb(&e.prjA, nil), mkOrb(&e.prjB, nil), mkOrb(nil, new(KindRunner))
	mkThread := func(orbID string, project *string) string {
		tid := ids.Thread()
		require.NoError(t, st.Q.CreateThread(ctx, db.CreateThreadParams{ID: tid, ProjectID: project, OrbID: &orbID, Title: "t", TitleSource: "auto",
			State: "idle", SessionDir: "d", CreatedAt: 1, UpdatedAt: 1, Kind: "work"}))
		return tid
	}
	e.tA, e.tB = mkThread(e.orbA, &e.prjA), mkThread(e.orbB, &e.prjB)
	e.tRA, e.tRB, e.tRNone = mkThread(e.rnr, &e.prjA), mkThread(e.rnr, &e.prjB), mkThread(e.rnr, nil)

	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	svc := memory.New(memory.Config{Q: st.Q, Box: box, PublicURL: "https://oryn.test"})
	u := hs.URL
	_, err = svc.PutSettings(ctx, apigen.MemorySettingsUpdate{Enabled: true, HindsightURL: &u})
	require.NoError(t, err)
	out, err := svc.GenerateEnvFile(ctx)
	require.NoError(t, err)
	for _, l := range strings.Split(out.Content, "\n") {
		if v, ok := strings.CutPrefix(l, "HINDSIGHT_API_TENANT_API_KEY="); ok {
			e.key = v
		}
	}
	require.NotEmpty(t, e.key)
	e.s = &Service{st: st, policies: map[string]egress.Policy{}}
	e.s.SetMemory(memory.NewGateway(svc))
	return e
}

// recall 以 orbID 的出站代理身份对线程 threadID 发一次召回（请求里伪造了标签与 bank），返回状态码与假 Hindsight 收到的 tags。
func (e *memoryEgressEnv) recall(t *testing.T, orbID, threadID string) (int, []string) {
	t.Helper()
	h := egress.NewHandler(orbID, egressResolver{s: e.s, orbID: orbID})
	req := httptest.NewRequest(http.MethodPost, "/memory/t/"+threadID+"/v1/default/banks/omp-evil/memories/recall",
		strings.NewReader(`{"query":"q","tags":["project:other"],"tags_match":"any"}`))
	req.Header.Set("Authorization", "Bearer forged")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.NotContains(t, rec.Body.String(), e.key)
	e.mu.Lock()
	defer e.mu.Unlock()
	var got []string
	if len(e.tags) > 0 {
		got = e.tags[len(e.tags)-1]
		e.tags = nil
	}
	return rec.Code, got
}

// R5、R13、R14：普通 orb 的项目取 orb 的项目，runner 按线程取 threads.project_id；线程不属于发出请求的 orb / runner 时 403。
func TestMemoryEgressCallerScopes(t *testing.T) {
	e := newMemoryEgressEnv(t)
	tagA, tagB := memory.ProjectTag(e.prjA), memory.ProjectTag(e.prjB)

	for _, c := range []struct {
		name, orb, thread string
		want              []string
	}{
		{"orb A 的线程", e.orbA, e.tA, []string{tagA, memory.GlobalTag}},
		{"orb B 的线程", e.orbB, e.tB, []string{tagB, memory.GlobalTag}},
		{"runner 上项目 A 的线程", e.rnr, e.tRA, []string{tagA, memory.GlobalTag}},
		{"runner 上项目 B 的线程", e.rnr, e.tRB, []string{tagB, memory.GlobalTag}},
		{"runner 上无项目的线程", e.rnr, e.tRNone, []string{memory.GlobalTag}},
	} {
		code, tags := e.recall(t, c.orb, c.thread)
		assert.Equal(t, http.StatusOK, code, c.name)
		assert.Equal(t, c.want, tags, c.name)
	}

	for _, c := range []struct{ name, orb, thread string }{
		{"orb A 冒用 orb B 的线程", e.orbA, e.tB},
		{"orb A 冒用 runner 的线程", e.orbA, e.tRA},
		{"runner 冒用 orb 的线程", e.rnr, e.tA},
		{"不存在的线程", e.orbA, "T-nope"},
	} {
		code, tags := e.recall(t, c.orb, c.thread)
		assert.Equal(t, http.StatusForbidden, code, c.name)
		assert.Nil(t, tags, c.name+"：不转发")
	}

	c, err := e.s.memoryCaller(context.Background(), e.rnr, e.tRB)
	require.NoError(t, err)
	assert.True(t, c.Runner)
	require.NotNil(t, c.ProjectID)
	assert.Equal(t, e.prjB, *c.ProjectID)

	// 没有设置记忆网关：/memory/ 一律 403
	e.s.SetMemory(nil)
	code, _ := e.recall(t, e.orbA, e.tA)
	assert.Equal(t, http.StatusForbidden, code)
}
