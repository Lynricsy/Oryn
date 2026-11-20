package threads

import (
	"encoding/base64"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// projLayers 是 orbs.ProjectConfigs 的假实现：按项目 id 返回固定的项目层（未登记的项目没有文件）。
type projLayers map[string]map[string][]byte

func (p projLayers) Load(id string) (map[string][]byte, map[string]os.FileMode, error) {
	return p[id], map[string]os.FileMode{}, nil
}

// M21：runner 线程的 agent.start 带线程所属项目的 projectId 与项目层（与 orb 收到的相同，原样不经变换）；两个项目的线程
// 各带各的；无项目线程、项目没有项目层、orb 线程与旧版 runner（hello 不含 omphome-apply）都不带。
func TestRunnerAgentStartProjectConfig(t *testing.T) {
	layers := projLayers{
		"prj_a": {"AGENTS.md": []byte("A rules"), "skills/x/SKILL.md": []byte("x")},
		"prj_b": {"RULES.md": []byte("B ${oryn:secret:x}")},
	}
	f := newFixture(t, Config{Projects: layers})
	for _, id := range []string{"prj_a", "prj_b", "prj_empty"} {
		f.projectWithID(id)
	}
	o := newCtlOrbs(t, f.orbs)
	mo := &memOrbs{Orbs: o, kinds: map[string]string{}, feats: map[string][]string{}}
	f.s.orbs = mo
	runner := f.orb("ready")
	mo.kinds[runner], mo.feats[runner] = orbs.KindRunner, []string{"fs", orbs.FeatureOmpHomeApply, "ssh"}
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	p := startParams(t, f, o, f.threadIn(runner, "prj_a"))
	require.NotNil(t, p.ProjectID)
	assert.Equal(t, "prj_a", *p.ProjectID)
	require.NotNil(t, p.ProjectConfig)
	assert.Equal(t, []apigen.LinkCtlOmpHomeFile{
		{Path: "AGENTS.md", Mode: 0o644, ContentB64: b64("A rules")},
		{Path: "skills/x/SKILL.md", Mode: 0o644, ContentB64: b64("x")},
	}, p.ProjectConfig.Files)

	p = startParams(t, f, o, f.threadIn(runner, "prj_b"))
	require.NotNil(t, p.ProjectID)
	assert.Equal(t, "prj_b", *p.ProjectID)
	require.NotNil(t, p.ProjectConfig)
	assert.Equal(t, []apigen.LinkCtlOmpHomeFile{{Path: "RULES.md", Mode: 0o644, ContentB64: b64("B ${oryn:secret:x}")}},
		p.ProjectConfig.Files, "原样交付，不解析密钥引用")

	for name, th := range map[string]*thread{
		"无项目线程":     f.threadIn(runner, ""),
		"项目没有项目层":   f.threadIn(runner, "prj_empty"),
		"orb 线程":    f.threadIn(f.orbIn("prj_a"), "prj_a"),
		"旧版 runner": f.threadIn(func() string { id := f.orb("ready"); mo.kinds[id] = orbs.KindRunner; return id }(), "prj_a"),
	} {
		p = startParams(t, f, o, th)
		assert.Nil(t, p.ProjectID, name)
		assert.Nil(t, p.ProjectConfig, name)
	}
}

// M21：项目层变化对 runner 上属于该项目、agent 在跑的线程按 orb 的分类生效，但 reload 类也记为需重启（文件要到下次
// agent.start 才交付）；项目主机合成的 ssh.json 同样记为需重启（runner 线程的项目主机随项目层交付）。别的项目的 runner
// 线程不受影响；同项目 orb 上的线程照旧 reload。
func TestConfigAppliedRunnerProjectLayer(t *testing.T) {
	f := newFixture(t, Config{})
	f.projectWithID("prj_a")
	f.projectWithID("prj_b")
	o := newCtlOrbs(t, f.orbs)
	mo := &memOrbs{Orbs: o, kinds: map[string]string{}, feats: map[string][]string{}}
	f.s.orbs = mo
	runner := f.orb("ready")
	mo.kinds[runner], mo.feats[runner] = orbs.KindRunner, []string{"fs", orbs.FeatureOmpHomeApply}
	ra, rb := f.threadIn(runner, "prj_a"), f.threadIn(runner, "prj_b")
	orbA := f.orbIn("prj_a")
	oa := f.threadIn(orbA, "prj_a")
	for _, th := range []*thread{ra, rb, oa} {
		attach(th, fakeAgent(8), apigen.AgentStatusReady)
	}

	f.s.ConfigApplied(f.ctx, orbs.ApplyReport{ProjectID: "prj_a", Orbs: []orbs.OrbApply{{OrbID: orbA, OK: true}}, Changed: []string{"ssh.json"}})
	ra.mu.Lock()
	assert.Equal(t, []string{"ssh.json"}, ra.restartReasons, "本项目 runner 线程的项目主机要到下次 agent.start 才交付")
	ra.restartReasons = nil
	ra.mu.Unlock()
	rb.mu.Lock()
	assert.Empty(t, rb.restartReasons)
	rb.mu.Unlock()

	f.s.ConfigApplied(f.ctx, orbs.ApplyReport{
		ProjectID: "prj_a", Orbs: []orbs.OrbApply{{OrbID: orbA, OK: true}},
		Changed: []string{"hooks/pre.sh", "skills/x/SKILL.md", "ssh.json"},
	})
	ra.mu.Lock()
	assert.Equal(t, []string{"hooks/pre.sh", "skills/x/SKILL.md", "ssh.json"}, ra.restartReasons)
	assert.False(t, ra.reloadPending, "runner 线程不发 reload_plugins：文件还没交付")
	ra.mu.Unlock()
	rb.mu.Lock()
	assert.Empty(t, rb.restartReasons, "别的项目的 runner 线程不受影响")
	rb.mu.Unlock()
	oa.mu.Lock()
	assert.Equal(t, []string{"hooks/pre.sh"}, oa.restartReasons, "orb 线程照旧分类")
	oa.mu.Unlock()
}
