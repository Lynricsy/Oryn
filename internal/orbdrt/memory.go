package orbdrt

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// M16：agent.start 带 memory 时启用内置 Hindsight 记忆（计划步骤 2.3 / 2.4）。omp 经出站通道访问 orynd 的记忆网关
// （/memory/t/<threadId>/…），网关按 orb 与线程强制 bank 和项目标签；Hindsight 的 key 只在控制面，这里只给地址。
// orb 与 runner 都走常驻的出站通道（M21 起 runner 也常驻，控制面对 runner 只放行 /llm/ 与 /memory/）。

// memoryOverlay 是记忆覆盖层：memory.backend 没有对应的环境变量，只能经 --config 设置。
const memoryOverlay = `# 由 orbd 生成（M16）：启用 Oryn 内置的 Hindsight 记忆。
memory:
  backend: hindsight
hindsight:
  globalScope: true
`

// memoryNoProxy 是必须绕过代理的地址：Bun 的 fetch 读代理变量，发往本机出站通道的记忆请求也会被送进代理。
var memoryNoProxy = []string{"127.0.0.1", "localhost"}

// memoryOverlayPath 返回记忆覆盖层的路径（~/.oryn/memory-overlay.yml）。
func (r *Runtime) memoryOverlayPath() string {
	return filepath.Join(r.home, ".oryn", "memory-overlay.yml")
}

// applyMemory 为一次 agent.start 启用记忆：写覆盖层并设置环境变量，返回要追加的 --config 文件。
// 在 p.Env 之后调用，env 模式的密钥覆盖不了这些变量。m 为 nil、或覆盖层写不了时不改 env、返回空串
// （记录日志，agent 照常以不带记忆的方式启动）。
func (r *Runtime) applyMemory(env map[string]string, threadID string, m *apigen.LinkCtlAgentMemory) string {
	if m == nil {
		return ""
	}
	overlay := r.memoryOverlayPath()
	if err := writeFileAtomic(overlay, []byte(memoryOverlay), 0o644); err != nil {
		r.log.Warn("write memory overlay failed; starting the agent without memory", "thread", threadID, "err", err)
		return ""
	}
	memoryEnv(env, r.EgressAddr(), threadID, m)
	return overlay
}

// memoryEnv 写入记忆相关的环境变量（addr 是出站通道的实际地址）。
func memoryEnv(env map[string]string, addr, threadID string, m *apigen.LinkCtlAgentMemory) {
	env["HINDSIGHT_API_URL"] = fmt.Sprintf("http://%s/memory/t/%s", addr, threadID)
	env["HINDSIGHT_BANK_ID"] = "oryn"
	// 网关依据 items[].tags 整体缺失判断全局写入，只在 per-project-tagged 下成立。
	env["HINDSIGHT_SCOPING"] = "per-project-tagged"
	if m.Project != nil && *m.Project != "" {
		env["ORYN_HINDSIGHT_PROJECT"] = *m.Project
	} else {
		delete(env, "ORYN_HINDSIGHT_PROJECT")
	}
	if !m.AutoRetain {
		env["HINDSIGHT_AUTO_RETAIN"] = "false"
	}
	// 鉴权由网关注入，orb 里不带任何 token。
	delete(env, "HINDSIGHT_API_TOKEN")
	noProxy := mergeNoProxy(env["NO_PROXY"], env["no_proxy"])
	env["NO_PROXY"], env["no_proxy"] = noProxy, noProxy
}

// mergeNoProxy 合并大小写两份 NO_PROXY 的已有条目（保持顺序、去重），再追加缺少的本机地址。
func mergeNoProxy(values ...string) string {
	var out []string
	for _, v := range values {
		for e := range strings.SplitSeq(v, ",") {
			if e = strings.TrimSpace(e); e != "" && !slices.Contains(out, e) {
				out = append(out, e)
			}
		}
	}
	for _, e := range memoryNoProxy {
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return strings.Join(out, ",")
}
