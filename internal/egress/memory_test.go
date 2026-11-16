package egress

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/memory"
)

// memFake 是假的记忆通道：callers 是允许的线程 → 调用方身份，网关只记录收到的请求。
type memFake struct {
	callers map[string]memory.Caller

	mu   sync.Mutex
	seen []memSeen
}

type memSeen struct {
	path, rawPath, auth string
	caller              memory.Caller
	ok                  bool
}

func (m *memFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, ok := memory.CallerFrom(r.Context())
	m.mu.Lock()
	m.seen = append(m.seen, memSeen{path: r.URL.Path, rawPath: r.URL.EscapedPath(), auth: r.Header.Get("Authorization"), caller: c, ok: ok})
	m.mu.Unlock()
	_, _ = io.WriteString(w, `{"ok":true}`)
}

func (m *memFake) take() []memSeen {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.seen
	m.seen = nil
	return out
}

func (f fakeR) Memory() http.Handler {
	if f.mem == nil {
		return nil
	}
	return f.mem
}

func (f fakeR) Caller(threadID string) (memory.Caller, error) {
	if f.mem == nil {
		return memory.Caller{}, errors.New("没有记忆通道")
	}
	c, ok := f.mem.callers[threadID]
	if !ok {
		return memory.Caller{}, errors.New("线程不属于该 orb")
	}
	return c, nil
}

func memDo(t *testing.T, h http.Handler, method, target, auth string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(`{}`))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// /memory/t/<threadId>/…：线程号只从路径取，经 Caller 校验后把剩余路径与调用方身份交给网关，客户端的 Authorization 不转发。
func TestMemoryRouteDispatchesWithCaller(t *testing.T) {
	prj := "prj_A"
	mem := &memFake{callers: map[string]memory.Caller{"T-1": {OrbID: "orb-1", ThreadID: "T-1", ProjectID: &prj}}}
	h := NewHandler("orb-1", fakeR{mem: mem})

	code, body := memDo(t, h, http.MethodPost, "/memory/t/T-1/v1/default/banks/omp%2Fx/memories/recall", "Bearer oryn-ph-anything")
	require.Equal(t, http.StatusOK, code, body)
	seen := mem.take()
	require.Len(t, seen, 1)
	assert.Equal(t, "/v1/default/banks/omp/x/memories/recall", seen[0].path)
	assert.Equal(t, "/v1/default/banks/omp%2Fx/memories/recall", seen[0].rawPath, "保留原始转义，网关按转义路径分段")
	assert.Empty(t, seen[0].auth, "客户端的 Authorization 不进网关")
	require.True(t, seen[0].ok)
	assert.Equal(t, "orb-1", seen[0].caller.OrbID)
	assert.Equal(t, "T-1", seen[0].caller.ThreadID)
	require.NotNil(t, seen[0].caller.ProjectID)
	assert.Equal(t, prj, *seen[0].caller.ProjectID)
}

// R14：线程不属于这台 orb、路径形状不对、没有记忆通道，一律 403，不进网关。
func TestMemoryRouteForbidden(t *testing.T) {
	mem := &memFake{callers: map[string]memory.Caller{"T-1": {OrbID: "orb-1", ThreadID: "T-1"}}}
	h := NewHandler("orb-1", fakeR{mem: mem})
	for _, target := range []string{
		"/memory/t/T-other/v1/default/banks/oryn/memories/recall",
		"/memory/t/T-1",
		"/memory/t//v1/default/banks/oryn",
		"/memory/x/T-1/v1/default/banks/oryn",
		"/memory/t/T-1%2FT-2/v1/default/banks/oryn",
		"/memory/v1/default/banks/oryn",
	} {
		code, body := memDo(t, h, http.MethodPost, target, "")
		assert.Equal(t, http.StatusForbidden, code, target)
		assert.Contains(t, body, "memory_forbidden", target)
	}
	assert.Empty(t, mem.take())

	code, _ := memDo(t, NewHandler("orb-1", fakeR{}), http.MethodPost, "/memory/t/T-1/v1/default/banks/oryn/memories/recall", "")
	assert.Equal(t, http.StatusForbidden, code, "没有记忆通道的 Resolver")
}
