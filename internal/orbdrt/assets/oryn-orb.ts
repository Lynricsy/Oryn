// Oryn orb 环境说明（由 orbd 写出并以 `omp -e` 加载；每次 orbd 启动覆盖，请勿手改）。
// 在 before_agent_start 向系统提示词追加一段 orb 生命周期约定，并附上最近一次 setup / resume 与受管服务的异常现状。
import type { BeforeAgentStartEvent, BeforeAgentStartEventResult, ExtensionAPI } from "@oh-my-pi/pi-coding-agent";
import { existsSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

const GUIDE = `# Oryn orb 环境
你运行在 Oryn orb 中：一台 Arch Linux 系隔离机器（x64 官方 Arch，ARM64 为 Arch Linux ARM），用户 \`user\`（免密 sudo），仓库默认在 ~/workspace/repo。
常见语言、编译工具、网络诊断与数据处理 CLI 已预装，先检查现有命令；系统包用 \`sudo pacman -Syu --needed --noconfirm <包名>\`，不要使用 apt 或 \`pacman -Sy\` 部分升级。包版本清单在 \`/usr/local/share/oryn/packages.txt\`。保留标准命令，不配置替换别名。
orb 的环境由这些生命周期文件配置（仓库内 \`.agents/setup\`、\`.agents/resume\` 作为回退也会被读取）：
- \`.oryn/setup\`：每台新 orb 首次就绪时运行一次。必须非交互、可重复执行（幂等）、结束时不留常驻进程，且不做任何认证。
- \`.oryn/resume\`：首次激活与每次唤醒时运行，须在 10 秒内完成，只做快速、幂等的认证 / 重连 / 自检。
- 项目脚本：存放在 Oryn 项目设置中，先于仓库内脚本运行，适合不能或不想提交进仓库的步骤。
试跑：\`oryn orb setup run\`（setup）、\`oryn orb setup run --resume\`（resume）；查看日志：\`oryn orb setup logs [--resume]\`；项目脚本：\`oryn orb setup project-script get|set\`。
需要持久的 PATH / 环境变量写进 \`~/.profile\`：agent 在启动时读取登录 shell 环境，所以只对之后启动的 agent 生效。
仓库内脚本的改动只有合入项目基线分支后才对新 orb 生效。
git 提交身份（user.name / user.email）由 Oryn 配置；git 报缺少身份时，请用户到 Oryn 设置（设置 → Git 身份，或项目设置）填写，不要自己编一个，也不要借用仓库历史里的作者。
用户要求配置 orb 环境、或 setup / resume 失败时，使用 \`oryn-setup\` 技能。
长驻进程（dev server、watcher、队列消费者）用受管服务，不要用 nohup / setsid / & / tmux 保活：
- 仓库里的服务写进 \`.oryn/services.yaml\`（\`services.<名字>: {command, cwd, port, env, health, portal, portals}\`），用 \`oryn orb service start\` 按文件确保全部运行；临时服务用 \`oryn orb service start <名字> --command '<命令>' --portal\`。
- 服务必须监听 \`$PORT\`（127.0.0.1 或 0.0.0.0）；对外地址只从 \`$PUBLIC_URL\` 或命令打印的 URL 取，不要自己拼域名，也不要把 localhost 地址给用户。
- dev server 若校验 Host / Origin，要放行 \`$PUBLIC_URL\` 的主机名（如 Vite 的 \`server.allowedHosts\`）。前端需要调另一个服务时在 dev server 里做代理，不要跨 portal 主机发请求。
- portal 默认只有所有者能打开；限时公开只能由用户在 Portal 面板里设置。应用可读请求头 \`X-Oryn-Authenticated: owner=yes|no\` 区分访问者。
- 在 orb 内自测用 \`http://127.0.0.1:<端口>\`（portal URL 需要登录，orb 内 curl 只会得到 302）。WebSocket 可用，但控制面重启或 orb 冻结会断开，客户端要能重连。
- 查看：\`oryn orb service ls\`、\`oryn orb service logs <名字>\`；停止：\`oryn orb service stop <名字>\`。服务在 setup 之后、每次唤醒后由 Oryn 自动确保运行，不要在 setup / resume 里启动它们。
与其它线程协作（独立、产出明确的工作才外包；需要反复协调或改同一批文件的留在当前线程）：
- \`oryn thread new "<完整、自包含的提示>"\` 在本项目开一个新线程（自带新 orb，不共享未提交的文件），打印线程 id。
- \`oryn thread wait <id>\` 等它静止并打印最后的回复（退出码 0 完成 / 3 在等主人回答 / 4 出错 / 124 超时）；\`oryn thread send <id> "<消息>"\` 追加指示；\`oryn thread show <id>\` 看状态。只能联系自己开的线程和开了自己的那个线程。
- \`oryn schedule set --cron "0 9 * * 1-5" --tz Asia/Shanghai --prompt "<到点要做的事>"\` 给当前线程设定时（另有 \`--every 30m\`、\`--at 2026-01-02T09:00:00+08:00\`）；\`oryn schedule show|pause|resume|rm\`。每个线程只有一个定时，到点时提示会作为新消息出现在本线程。`;

// 桌面说明只在 orbd 启用了桌面时追加（orbd 此时写出 ~/.oryn/core/desktop.yml 覆盖层）。
const DESKTOP_GUIDE = `桌面与浏览器：这台 orb 有一块 X11 桌面（DISPLAY=:1），用户能在 Oryn 的 Desktop 面板里看到并随时接管。
- browser 工具默认附着到桌面上的那个 Chromium（用户看得见）；不要传 headed。它第一次使用时才启动，若报 CDP 端点超时，重试一次即可。需要一个与桌面无关的无头浏览器时传 app: "own"。
- computer 工具与任何 GUI 程序都作用于同一块桌面。
- 浏览器里可能带着用户的登录态（浏览器 Profile）：不要把 Cookie、令牌或 tab.cookies() / tab.saveState() 的结果写进文件、日志或回复；不要退出用户已登录的账号，除非用户要求。
- 需要用户登录或过验证码时，请用户在 Desktop 面板里操作，然后继续。
- 查看状态：oryn orb desktop status。`;

function desktopEnabled(): boolean {
	return existsSync(join(homedir(), ".oryn", "core", "desktop.yml"));
}

interface SetupResult {
	ok: boolean;
	error?: string;
	durationMs: number;
	setupRan: boolean;
}

interface ServiceStatus {
	name: string;
	status: string;
	error?: string;
}

function readOryn<T>(name: string): T | undefined {
	try {
		return JSON.parse(readFileSync(join(homedir(), ".oryn", name), "utf8")) as T;
	} catch {
		return undefined;
	}
}

function setupLine(): string | undefined {
	const st = readOryn<{ setup?: SetupResult | null; resume?: SetupResult | null }>("setup-status.json");
	if (!st) {
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

function serviceLines(): string[] {
	const st = readOryn<{ services?: ServiceStatus[] }>("services-status.json");
	return (st?.services ?? [])
		.filter((s) => s.status === "failed")
		.map((s) => `当前现状：服务 ${s.name} 启动失败（${s.error ?? "未知错误"}），用 \`oryn orb service logs ${s.name}\` 查看。`);
}

function statusLines(): string[] {
	const setup = setupLine();
	return [...(setup ? [setup] : []), ...serviceLines()];
}

export default function orynOrbExtension(pi: ExtensionAPI) {
	pi.on("before_agent_start", async (event: BeforeAgentStartEvent): Promise<BeforeAgentStartEventResult> => {
		const guide = desktopEnabled() ? [GUIDE, DESKTOP_GUIDE] : [GUIDE];
		return { systemPrompt: [...event.systemPrompt, [...guide, ...statusLines()].join("\n")] };
	});
}
