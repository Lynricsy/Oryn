package orbdrt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M29 T5：oryn-orb.ts 注册 6 个设计工具（写法同 deskrecord_tool_test.go），始终激活（不调用 setActiveTools），
// 经 orbd.sock 发 design.open / design.check / design.status / design.turn / design.seal，发牌与便签经 artifact.call；
// 回合钩子挂在 input、agent_start、agent_end、session_stop 上；artifact.call 外壳带 turn；Runner 分支注册发牌、便签与 check_artifact。
func TestDesignToolsRegistered(t *testing.T) {
	src, err := os.ReadFile("assets/oryn-orb.ts")
	require.NoError(t, err)
	ext := string(src)
	var names []string
	for _, m := range regexp.MustCompile(`name:\s*"([a-z_]+)"`).FindAllStringSubmatch(ext, -1) {
		names = append(names, m[1])
	}
	for _, n := range []string{"design_open", "design_check", "design_directions", "design_direction", "design_notes", "design_resolve_note", "check_artifact"} {
		assert.Contains(t, names, n)
	}
	for _, s := range []string{
		`"design.open"`, `"design.check"`, `"design.status"`, `"design.turn"`, `"design.seal"`, "no_design",
		`pi.on("input"`, `pi.on("agent_start"`, `pi.on("agent_end"`, `pi.on("session_stop"`, `pi.on("tool_call"`,
		"turnBarrier", "event.willContinue", "event.stop_hook_active", "crypto.randomUUID()", `startsWith("[Oryn ")`,
		"turn: design.turn", "registerDesign(pi)", "registerRunnerDesign(pi)", "oryn-design",
		"只用于 Oryn 画布 artifact；在项目仓库里写前端不要用。", "设计检查未通过：",
	} {
		assert.Contains(t, ext, s)
	}
	// 探针 P6：设计工具与回合钩子始终激活，不在设计路径里调用 setActiveTools（registerRoleTools 的显式角色白名单只在 session_start/session_switch 生效）。
	designStart := strings.Index(ext, "function registerDesign(")
	designEnd := strings.Index(ext, "function registerArtifacts(")
	require.True(t, designStart >= 0 && designEnd > designStart, "应能定位 registerDesign..registerRunnerDesign 代码段")
	assert.NotContains(t, ext[designStart:designEnd], "setActiveTools(", "探针 P6：设计工具与回合钩子不切换工具集")
}

// M29 C9：oryn-design 技能按 8 节组织；Runner 版去掉第 3、8 节（画布契约、自动封存），换成「用 HTML 画框」；NOTICE 列全部出处。
func TestDesignSkillAssets(t *testing.T) {
	skill := string(designSkill)
	assert.True(t, strings.HasPrefix(skill, "---\nname: oryn-design\n"), "技能 frontmatter")
	heads := []string{"## 1. ", "## 2. ", "## 3. 画布契约", "## 4. ", "## 5. ", "## 6. ", "## 7. ", "## 8. 自动发生的事"}
	last := -1
	for _, h := range heads {
		i := strings.Index(skill, "\n"+h)
		require.GreaterOrEqual(t, i, 0, h)
		assert.Greater(t, i, last, "%s 的顺序", h)
		last = i
	}
	for _, s := range []string{"design_open", "~/designs/<id>/", "src/frames/<帧 id>.tsx", "^[a-z0-9][a-z0-9-]{0,63}$",
		"export const frame", "_layout.tsx", `import { Link } from "oryn:design"`, "src/theme.css", "--color-on-accent",
		"--font-display", "icon-[lucide--mail]", "lucide-react", "src/package.json", "src/DESIGN.md", "update_artifact"} {
		assert.Contains(t, skill, s)
	}

	runner := string(runnerDesignSkill)
	assert.True(t, strings.HasPrefix(runner, "---\nname: oryn-design\n"), "技能 frontmatter")
	assert.NotContains(t, runner, "画布契约")
	assert.NotContains(t, runner, "自动发生的事")
	assert.NotContains(t, runner, "design_open")
	assert.Contains(t, runner, "## 3. Runner 上用 HTML 画框做设计稿")
	assert.Contains(t, runner, "check_artifact")

	notice := string(designNotice)
	for _, s := range []string{"oil-ui", "HyperSkills", "frontend-design", "taste-skill", "Vetta", "MIT License"} {
		assert.Contains(t, notice, s)
	}
}

// M29 C9：orb 把 oryn-design 写到 ~/.agents/skills/oryn-design/。
func TestDesignSkillWrittenOnOrb(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	dir := filepath.Join(rt.home, ".agents", "skills", "oryn-design")
	assert.Equal(t, dir, rt.designSkillDir())
	assert.Equal(t, string(designSkill), readFile(t, filepath.Join(dir, "SKILL.md")))
	assert.Equal(t, string(designNotice), readFile(t, filepath.Join(dir, "NOTICE.md")))
}

// M29 C9：runner 把 Runner 版 oryn-design 写到 ~/.oryn/omp/agent/skills/oryn-design/（不含第 3 节画布契约）。
func TestRunnerDesignSkillWritten(t *testing.T) {
	f := newRunnerHome(t)
	startRunnerRT(t, f, nil)
	dir := filepath.Join(f.home, ".oryn", "omp", "agent", "skills", "oryn-design")
	assert.Equal(t, dir, runnerDesignSkillDir(f.home))
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(dir, "SKILL.md")); return err == nil }, 5*time.Second, 10*time.Millisecond)
	got := readFile(t, filepath.Join(dir, "SKILL.md"))
	assert.Equal(t, string(runnerDesignSkill), got)
	assert.NotContains(t, got, "## 3. 画布契约")
	assert.Equal(t, string(designNotice), readFile(t, filepath.Join(dir, "NOTICE.md")))
	assert.NoDirExists(t, filepath.Join(f.home, ".agents"), "runner 不写 orb 的技能目录")
}

// M29 K5：真跑 oryn-orb.ts 的钩子顺序。agent_start 不 await 就来 tool_call 时，tool_call 必须等到 design.turn
// 收到回执才放行，保证 bash/read 不会抢在 orbd 回合交界对账完成前读到恢复前的旧源码；回执之后的工具不再等。
func TestDesignTurnBarrier(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("没有 bun，跳过扩展钩子实跑检查")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bun, "testdata/turn_barrier.ts", "assets/oryn-orb.ts")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "turn_barrier.ts 失败：\n%s", string(out))
	assert.Equal(t, "OK", strings.TrimSpace(string(out)))
}
