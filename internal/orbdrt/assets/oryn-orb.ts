// Oryn orb 环境说明（由 orbd 写出并以 `omp -e` 加载；每次 orbd 启动覆盖，请勿手改）。
// 在 before_agent_start 向系统提示词追加一段 orb 生命周期约定，并附上最近一次 setup / resume 的异常现状。
import type { BeforeAgentStartEvent, BeforeAgentStartEventResult, ExtensionAPI } from "@oh-my-pi/pi-coding-agent";
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

const GUIDE = `# Oryn orb 环境
你运行在 Oryn orb 中：一台 Debian 13 隔离机器，用户 \`user\`（免密 sudo），仓库默认在 ~/workspace/repo。
orb 的环境由这些生命周期文件配置（仓库内 \`.agents/setup\`、\`.agents/resume\` 作为回退也会被读取）：
- \`.oryn/setup\`：每台新 orb 首次就绪时运行一次。必须非交互、可重复执行（幂等）、结束时不留常驻进程，且不做任何认证。
- \`.oryn/resume\`：首次激活与每次唤醒时运行，须在 10 秒内完成，只做快速、幂等的认证 / 重连 / 自检。
- 项目脚本：存放在 Oryn 项目设置中，先于仓库内脚本运行，适合不能或不想提交进仓库的步骤。
试跑：\`oryn orb setup run\`（setup）、\`oryn orb setup run --resume\`（resume）；查看日志：\`oryn orb setup logs [--resume]\`；项目脚本：\`oryn orb setup project-script get|set\`。
需要持久的 PATH / 环境变量写进 \`~/.profile\`：agent 在启动时读取登录 shell 环境，所以只对之后启动的 agent 生效。
仓库内脚本的改动只有合入项目基线分支后才对新 orb 生效。
用户要求配置 orb 环境、或 setup / resume 失败时，使用 \`oryn-setup\` 技能。`;

interface SetupResult {
	ok: boolean;
	error?: string;
	durationMs: number;
	setupRan: boolean;
}

function statusLine(): string | undefined {
	let st: { setup?: SetupResult | null; resume?: SetupResult | null };
	try {
		st = JSON.parse(readFileSync(join(homedir(), ".oryn", "setup-status.json"), "utf8"));
	} catch {
		return undefined;
	}
	for (const [phase, r] of [["setup", st.setup], ["resume", st.resume]] as const) {
		if (r && !r.ok) {
			return `当前现状：最近一次 ${phase} 失败（${r.error ?? "未知错误"}），用 \`oryn orb setup logs${phase === "resume" ? " --resume" : ""}\` 查看日志。`;
		}
	}
	if (st.setup?.setupRan && st.setup.durationMs >= 120_000) {
		return `当前现状：最近一次 setup 耗时 ${Math.round(st.setup.durationMs / 60_000)} 分钟，可考虑优化（\`oryn orb setup logs\`）。`;
	}
	return undefined;
}

export default function orynOrbExtension(pi: ExtensionAPI) {
	pi.on("before_agent_start", async (event: BeforeAgentStartEvent): Promise<BeforeAgentStartEventResult> => {
		const line = statusLine();
		return { systemPrompt: [...event.systemPrompt, line ? `${GUIDE}\n${line}` : GUIDE] };
	});
}
