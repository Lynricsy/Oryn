package threads

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// M13 4.5：协调线程每次 agent.start 之前先以 AgentProfile.Setup 对协调宿主重跑 setup.run、再同步文件库；
// 两次启动之间沙箱里被改动的 config.yml 被还原。
func TestCoordinatorStartRestoresManagedFiles(t *testing.T) {
	f := newFixture(t, Config{})
	o := newCtlOrbs(t, f.orbs)
	o.home = t.TempDir()
	o.features["coordinator"] = true
	f.s.orbs = o
	lib := &fakeLibrary{}
	f.s.SetLibrary(lib)
	coord := newFakeCoord()
	const cfg = "modelRoles:\n  default: fake/model\n"
	coord.prof.Setup = &apigen.LinkCtlSetupRunParams{
		GitHosts: []string{}, Env: map[string]string{},
		OmpHome: []apigen.LinkCtlOmpHomeFile{{Path: "config.yml", Mode: 0o644, ContentB64: base64.StdEncoding.EncodeToString([]byte(cfg))}},
	}
	f.s.SetCoordinator(coord)
	th := f.coordThread(f.orb("ready"), KindAssistant, StateProvisioning)
	path := filepath.Join(o.home, ".omp", "agent", "config.yml")

	start := func() {
		t.Helper()
		f.s.startAgent(f.ctx, th.id)
		c := o.nextCall(t)
		require.Equal(t, "setup.run", c.method, "agent.start 之前先还原受管文件")
		var p apigen.LinkCtlSetupRunParams
		require.NoError(t, json.Unmarshal(c.params, &p))
		assert.Equal(t, *coord.prof.Setup, p)
		require.Equal(t, "agent.start", o.nextCall(t).method)
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, cfg, string(got))
	}
	start()
	// 助手在这次运行里用 write 改了 config.yml；下次启动前被还原
	require.NoError(t, os.WriteFile(path, []byte("modelRoles:\n  default: evil/model\n"), 0o644))
	start()
	assert.Equal(t, []string{th.orbID, th.orbID}, lib.syncs, "每次启动前都同步文件库")
}

// setup.run 失败时不带着可能被改过的配置启动：线程进入 error、不发 agent.start。
func TestCoordinatorStartFailsWhenRestoreFails(t *testing.T) {
	f := newFixture(t, Config{})
	o := newCtlOrbs(t, f.orbs)
	o.features["coordinator"] = true
	o.setupErr = &link.CtlError{Code: "failed", Message: "omp home: disk full"}
	f.s.orbs = o
	coord := newFakeCoord()
	coord.prof.Setup = &apigen.LinkCtlSetupRunParams{GitHosts: []string{}, Env: map[string]string{}, OmpHome: []apigen.LinkCtlOmpHomeFile{}}
	f.s.SetCoordinator(coord)
	th := f.coordThread(f.orb("ready"), KindAssistant, StateProvisioning)

	f.s.startAgent(f.ctx, th.id)
	require.Equal(t, "setup.run", o.nextCall(t).method)
	th.mu.Lock()
	assert.Equal(t, StateError, th.state)
	th.mu.Unlock()
	o.mu.Lock()
	assert.Len(t, o.calls, 1, "没有 agent.start")
	o.mu.Unlock()
}
