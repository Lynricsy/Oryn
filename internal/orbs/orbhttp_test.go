package orbs

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M3 §3.2：fs 流上的 HTTP 通道与 EnsureUsable 的可用性判定。
func TestOrbHTTPChannel(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	drv := newFakeDriver()
	drv.features = []string{FeatureFS, FeatureGit}
	drv.fs = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		_, _ = io.WriteString(w, "ok "+r.URL.RequestURI()+" "+string(body))
	})
	h := newHarness(t, drv, fakeSetup{upstream: "http://127.0.0.1:1"}, nil)
	ctx := context.Background()
	orb, err := h.svc.Create(ctx, CreateParams{})
	require.NoError(t, err)
	h.waitState(orb.ID, StateReady, 10*time.Second)

	require.NoError(t, h.svc.EnsureUsable(ctx, orb.ID, FeatureFS, false, 0))
	assert.ErrorIs(t, h.svc.EnsureUsable(ctx, orb.ID, FeatureAttachments, false, 0), ErrOrbdOutdated, "hello 未声明的特性")

	// 每个请求一条新流：连发两次都成功（DisableKeepAlives，流随请求结束释放）
	for i := range 2 {
		req, _ := http.NewRequest(http.MethodPut, "http://ignored.example/fs/file?path=%7E%2Fa.txt", strings.NewReader("data"))
		resp, err := h.svc.Do(ctx, orb.ID, req)
		require.NoError(t, err, "request %d", i)
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "ok /fs/file?path=%7E%2Fa.txt data", string(b))
	}
	mu.Lock()
	assert.Equal(t, []string{"PUT /fs/file?path=%7E%2Fa.txt", "PUT /fs/file?path=%7E%2Fa.txt"}, seen)
	mu.Unlock()

	// 冻结后：后台请求不唤醒，orb 保持 frozen；前台请求唤醒、记用户交互并等到 ready
	require.NoError(t, h.svc.Pause(ctx, orb.ID, PauseFreeze))
	h.waitState(orb.ID, StateFrozen, 10*time.Second)
	assert.ErrorIs(t, h.svc.EnsureUsable(ctx, orb.ID, FeatureFS, false, 0), ErrOrbNotReady)
	cur, err := h.svc.Get(ctx, orb.ID)
	require.NoError(t, err)
	assert.Equal(t, StateFrozen, cur.State)
	assert.Nil(t, cur.LastUserInteractionAt)

	require.NoError(t, h.svc.EnsureUsable(ctx, orb.ID, FeatureFS, true, 10*time.Second))
	cur, err = h.svc.Get(ctx, orb.ID)
	require.NoError(t, err)
	assert.Equal(t, StateReady, cur.State)
	assert.NotNil(t, cur.LastUserInteractionAt)

	// 唤醒等待超时：orb 回不来时返回 ErrOrbNotReady 而不是一直挂着
	drv.mu.Lock()
	drv.noConnect = true
	drv.mu.Unlock()
	require.NoError(t, h.svc.Pause(ctx, orb.ID, PauseFreeze))
	h.waitState(orb.ID, StateFrozen, 10*time.Second)
	assert.ErrorIs(t, h.svc.EnsureUsable(ctx, orb.ID, FeatureFS, true, 300*time.Millisecond), ErrOrbNotReady)

	require.NoError(t, h.svc.Delete(ctx, orb.ID))
	assert.ErrorIs(t, h.svc.EnsureUsable(ctx, orb.ID, FeatureFS, true, time.Second), ErrOrbGone)
	assert.ErrorIs(t, h.svc.EnsureUsable(ctx, "orb-missing", FeatureFS, false, 0), ErrNotFound)
}

// 旧版 orbd（hello 无 features）：任何特性都是 ErrOrbdOutdated，拨号同样拒绝，不会把请求写进它不认识的流。
func TestOrbHTTPOutdatedOrbd(t *testing.T) {
	drv := newFakeDriver()
	h := newHarness(t, drv, fakeSetup{upstream: "http://127.0.0.1:1"}, nil)
	ctx := context.Background()
	orb, err := h.svc.Create(ctx, CreateParams{})
	require.NoError(t, err)
	h.waitState(orb.ID, StateReady, 10*time.Second)

	assert.ErrorIs(t, h.svc.EnsureUsable(ctx, orb.ID, FeatureFS, true, time.Second), ErrOrbdOutdated)
	req, _ := http.NewRequest(http.MethodGet, "/fs/roots", nil)
	_, err = h.svc.Do(ctx, orb.ID, req)
	assert.ErrorIs(t, err, ErrOrbdOutdated)
}
