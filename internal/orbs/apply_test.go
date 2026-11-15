package orbs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// memHome 是内存里的 omp-home（实现 Home）。
type memHome struct {
	mu    sync.Mutex
	files map[string][]byte
}

func (h *memHome) LoadAll() (map[string][]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string][]byte, len(h.files))
	for p, b := range h.files {
		out[p] = slices.Clone(b)
	}
	return out, nil
}

func (h *memHome) Modes() (map[string]os.FileMode, error) { return map[string]os.FileMode{}, nil }

func (h *memHome) put(p, content string) {
	h.mu.Lock()
	h.files[p] = []byte(content)
	h.mu.Unlock()
}

// modelVault 对任意 omp/models/<key>/apiKey 都返回一个密钥（实现 Vault）。
type modelVault struct{}

func (modelVault) Lookup(name string) (string, string, bool) {
	if strings.HasPrefix(name, "omp/models/") {
		return "sid-" + strings.Split(name, "/")[2], "real-key", true
	}
	return "", "", false
}
func (modelVault) SecretByID(string) (string, []string, bool) { return "real-key", nil, true }
func (modelVault) GitToken(string) (string, bool)             { return "", false }
func (modelVault) EnvSecrets() (map[string]string, error)     { return map[string]string{}, nil }
func (modelVault) GitHosts() ([]string, error)                { return nil, nil }

func modelsYml(key, base string) string {
	return "providers:\n  " + key + ":\n    baseUrl: " + base + "\n    apiKey: ${oryn:secret:omp/models/" + key + "/apiKey}\n    api: openai-completions\n"
}

// 两台 ready 的 orb 与一台 frozen 的 orb：只有前两台收到 omphome.apply；不带 omphome-apply 特性的 orb 不发。
func TestApplyOmpHomeOnlyReadyOrbs(t *testing.T) {
	drv := newFakeDriver()
	drv.features = []string{FeatureOmpHomeApply}
	h := newHarness(t, drv, fakeSetup{upstream: "https://up.example.com"}, nil)
	h.quiet.Store(false)
	ctx := context.Background()
	home := &memHome{files: map[string][]byte{"config.yml": []byte("a: 1\n")}}
	require.NoError(t, h.svc.EnableOmpApply(OmpApplyConfig{Home: home, Dir: t.TempDir()}))

	var ids []string
	for range 3 {
		orb, err := h.svc.Create(ctx, CreateParams{})
		require.NoError(t, err)
		h.waitState(orb.ID, StateReady, 10*time.Second)
		ids = append(ids, orb.ID)
	}
	h.quiet.Store(true)
	require.NoError(t, h.svc.Pause(ctx, ids[2], PauseFreeze))
	h.waitState(ids[2], StateFrozen, 10*time.Second)

	var got []ApplyReport
	h.svc.OnOmpApplied(func(r ApplyReport) { got = append(got, r) })
	home.put("config.yml", "a: 2\n")
	home.put("skills/x/SKILL.md", "x")
	rep, err := h.svc.ApplyOmpHome(ctx)
	require.NoError(t, err)

	var applied []string
	for _, o := range rep.Orbs {
		assert.True(t, o.OK, o.Err)
		applied = append(applied, o.OrbID)
	}
	assert.ElementsMatch(t, ids[:2], applied)
	for i, id := range ids {
		_, ok := drv.orbd(id).call(string(apigen.MethodOmpHomeApply))
		assert.Equal(t, i < 2, ok, "orb %d", i)
	}
	assert.Equal(t, []string{"config.yml", "skills/x/SKILL.md"}, rep.Changed)
	assert.Equal(t, "a: 1\n", string(rep.ConfigBefore))
	assert.Equal(t, "a: 2\n", string(rep.ConfigAfter))
	require.Len(t, got, 1, "订阅方收到一次")

	// 再下发一次：基线已更新，没有变化
	rep, err = h.svc.ApplyOmpHome(ctx)
	require.NoError(t, err)
	assert.Empty(t, rep.Changed)
	assert.Equal(t, "a: 2\n", string(rep.ConfigBefore))
}

// models.yml 变化并下发后，policyFor 返回新路由；下发参数里是渲染后的 models.yml。
func TestApplyOmpHomeRefreshesEgress(t *testing.T) {
	drv := newFakeDriver()
	drv.features = []string{FeatureOmpHomeApply}
	home := &memHome{files: map[string][]byte{"models.yml": []byte(modelsYml("p1", "https://a.example.com/v1"))}}
	ds := &DefaultSetup{Home: home, Vault: modelVault{}}
	h := newHarness(t, drv, ds, nil)
	ds.Store = h.st
	ctx := context.Background()
	dir := t.TempDir()
	require.NoError(t, h.svc.EnableOmpApply(OmpApplyConfig{Home: home, Dir: dir, Debounce: 50 * time.Millisecond}))

	orb, err := h.svc.Create(ctx, CreateParams{})
	require.NoError(t, err)
	h.waitState(orb.ID, StateReady, 10*time.Second)
	pol := h.svc.policyFor(orb.ID)
	require.Contains(t, pol.Routes, "p1")
	assert.Equal(t, "https://a.example.com", pol.Routes["p1"].Origin)

	home.put("models.yml", modelsYml("p2", "https://b.example.com/v2"))
	assert.False(t, h.svc.OmpApplyState("").Pending)
	done := make(chan ApplyReport, 1)
	// 订阅方处理时这次写入仍算待下发：读到 Pending=false 时会话级分类一定已落定
	pendingInSub := make(chan bool, 1)
	h.svc.OnOmpApplied(func(r ApplyReport) { pendingInSub <- h.svc.OmpApplyState("").Pending; done <- r })
	h.svc.ScheduleOmpApply("") // 防抖后在后台下发
	h.svc.ScheduleOmpApply("")
	assert.True(t, h.svc.OmpApplyState("").Pending, "已接受的写入在下发完成前是待下发")
	var rep ApplyReport
	select {
	case rep = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("防抖后没有下发")
	}
	assert.True(t, <-pendingInSub)
	require.Eventually(t, func() bool { return !h.svc.OmpApplyState("").Pending }, 5*time.Second, 10*time.Millisecond)
	st := h.svc.OmpApplyState("")
	assert.False(t, st.AppliedAt.IsZero())
	assert.Empty(t, st.Failed)
	assert.Empty(t, st.Err)
	assert.Equal(t, []string{"models.yml"}, rep.Changed)
	require.Len(t, rep.Orbs, 1)
	assert.True(t, rep.Orbs[0].OK, rep.Orbs[0].Err)

	pol = h.svc.policyFor(orb.ID)
	assert.NotContains(t, pol.Routes, "p1")
	require.Contains(t, pol.Routes, "p2")
	assert.Equal(t, "https://b.example.com", pol.Routes["p2"].Origin)

	raw, ok := drv.orbd(orb.ID).call(string(apigen.MethodOmpHomeApply))
	require.True(t, ok)
	var p apigen.LinkCtlOmpHomeApplyParams
	require.NoError(t, json.Unmarshal(raw, &p))
	require.Len(t, p.OmpHome, 1)
	assert.Equal(t, "models.yml", p.OmpHome[0].Path)

	// 已下发记录落盘
	m, err := os.ReadFile(filepath.Join(dir, appliedManifest))
	require.NoError(t, err)
	assert.Contains(t, string(m), "models.yml")
}

func TestChangedPaths(t *testing.T) {
	assert.Equal(t, []string{"a", "c"}, changedPaths(nil, map[string]string{"a": "1", "c": "2"}))
	assert.Equal(t, []string{"b", "c", "d"}, changedPaths(
		map[string]string{"a": "1", "b": "2", "d": "4"},
		map[string]string{"a": "1", "b": "3", "c": "5"},
	))
}
