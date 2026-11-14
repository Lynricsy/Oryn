/** mock 模式的固定数据：项目、provider、orb、线程与一份信息量丰富的转录。 */
import type {
	Entry,
	Orb,
	Project,
	Provider,
	Schemas,
	Thread,
} from "../api/client";
import type { SessionState } from "../omp/types";

export const NOW = Date.now();
const MIN = 60_000;
const HOUR = 60 * MIN;

export const SESSION_FILE =
	"2026-11-12T10-00-00-000Z_0193a1b2-0000-7000-8000-000000000001.jsonl";

export const projects: Project[] = [
	{
		id: "prj_01JCZ0000000000000000ORYN",
		name: "Oryn",
		// 有图标的项目；oryn-core 留空（前端回退为名字首字）
		icon: "\u{F14DE}",
		repoUrl: "https://github.com/Lynricsy/Oryn",
		baseBranch: "main",
		defaultProvider: "local",
		defaultOrbType: "medium",
		setupScript: "bun install --cwd web",
		shallowClone: true,
		// 浏览器 Profile「工作账号」（M8；与 `BROWSER_PROFILE_IDS.work` 相同）
		defaultBrowserProfileId: "bpf_01JCZ8M3W4X5Y6Z7A8B9C0D1P1",
		createdAt: NOW - 30 * 24 * HOUR,
		updatedAt: NOW - 2 * HOUR,
	},
	{
		id: "prj_01JCZ00000000000000AGENT",
		name: "oryn-core",
		repoUrl: "https://github.com/Lynricsy/oryn-core",
		baseBranch: "oryn",
		shallowClone: false,
		gitAuthorName: "Oryn Agent",
		gitAuthorEmail: "agent@oryn.example",
		createdAt: NOW - 20 * 24 * HOUR,
		updatedAt: NOW - 5 * HOUR,
	},
];

export const providers: Provider[] = [
	{
		id: "prov_01JCZ000000000000000LOCAL",
		name: "local",
		driver: "docker",
		capabilities: { freeze: true, snapshot: true },
		orbTypes: [
			{ name: "small", cpus: 2, memoryMB: 4096 },
			{ name: "medium", cpus: 4, memoryMB: 8192 },
			{ name: "large", cpus: 8, memoryMB: 16384 },
		],
		defaultType: "medium",
		online: true,
		instanceId: "inst-mock",
		leaseUntil: NOW + 45_000,
		createdAt: NOW - 30 * 24 * HOUR,
	},
];

export function makeOrb(
	id: string,
	threadIds: string[],
	state: Orb["state"] = "ready",
): Orb {
	return {
		id,
		providerId: providers[0]?.id ?? "prov",
		providerName: "local",
		type: "medium",
		cpus: 4,
		memoryMB: 8192,
		image: "oryn-orb:dev",
		projectId: projects[0]?.id,
		state,
		stateChangedAt: NOW - 10 * MIN,
		orbdVersion: "0.1.0-mock",
		agentVersion: "omp/18.19.0",
		createdAt: NOW - 3 * HOUR,
		threadIds,
	};
}

function thread(p: Partial<Thread> & Pick<Thread, "id" | "title">): Thread {
	return {
		titleSource: "auto",
		state: "idle",
		stateChangedAt: NOW - 5 * MIN,
		pinned: false,
		labels: [],
		unread: false,
		createdAt: NOW - 2 * HOUR,
		updatedAt: NOW - 5 * MIN,
		lastMessageAt: NOW - 5 * MIN,
		...p,
	};
}

export const MAIN_THREAD = "T-0193a1b2-0000-7000-8000-00000000a001";
/** 工具样例线程：`tool-render/registry.ts` 注册的每个工具各一次调用（截图矩阵用）。 */
export const TOOL_ZOO_THREAD = "T-0193a1b2-0000-7000-8000-00000000a0aa";
const TOOL_ZOO_ORB = "orb-01jcz8m3w4x5y6z7a8b9c0d1ea";

// ---------------------------------------------------------------- orb 环境（M2.5）

type SetupRun = Schemas["OrbSetupRun"];

export const ORB_FAILED_REASON =
	"spawn 任务失败 3 次：docker: Error response from daemon: pull access denied for oryn-orb:dev, repository does not exist";

/** 各提示条对应的线程（mock 模式下可直接打开查看）。 */
export const ENV_THREADS = {
	setupFailed: "T-0193a1b2-0000-7000-8000-00000000a008",
	slowSetup: "T-0193a1b2-0000-7000-8000-00000000a003",
	missingConfig: "T-0193a1b2-0000-7000-8000-00000000a009",
	orbFailed: "T-0193a1b2-0000-7000-8000-00000000a005",
} as const;

function setupRun(
	orbId: string,
	n: number,
	p: Partial<SetupRun> & Pick<SetupRun, "phase">,
): SetupRun {
	const durationMs = p.durationMs ?? 42_000;
	const finishedAt = p.finishedAt ?? NOW - 9 * MIN;
	return {
		id: `osr_01jcz9${orbId.slice(-6)}${String(n).padStart(4, "0")}`,
		orbId,
		trigger: "auto",
		startedAt: finishedAt - durationMs,
		finishedAt,
		ok: true,
		setupRan: p.phase === "setup",
		durationMs,
		hasRepoScript: true,
		hasProjectScript: false,
		...p,
	};
}

const FAILED_SETUP_LOG = `${Array.from(
	{ length: 120 },
	(_, i) =>
		`[setup] ${String(i + 1).padStart(3, "0")} Progress: resolved ${400 + i * 3}, reused ${380 + i * 3}, downloaded ${i}, added ${i * 2}`,
).join("\n")}
[setup] + sudo apt-get install -y libpq-dev postgresql-16
[setup] Reading package lists...
[setup] E: Unable to locate package postgresql-16
[setup] .oryn/setup: line 14: exit status 100
`;

const SLOW_SETUP_LOG = `[setup] + rustup toolchain install 1.92.0 --profile minimal
[setup] info: downloading component 'rustc'
[setup] + cargo build --release --workspace
[setup]    Compiling proc-macro2 v1.0.101
[setup]    Compiling serde v1.0.228
[setup]    …
[setup]     Finished \`release\` profile [optimized] target(s) in 3m 41s
[setup] + bun install --cwd web
[setup] 1123 packages installed [18.42s]
`;

// ---------------------------------------------------------------- 受管服务与 portal（M4）

/** 主线程 orb 上有 portal 的服务端口。 */
export const PORTAL_PORT = 5173;

/**
 * mock 的 portal 地址模板（真实后端是 `https://t-<短码>-p{port}.<portal_domain>/`）：一张 `data:` 页面，
 * iframe 里能直接显示；颜色只用系统色（`color-scheme`），随明暗切换。
 */
export const MOCK_PORTAL_TEMPLATE = `data:text/html;charset=utf-8,${encodeURIComponent(
	`<!doctype html><meta charset="utf-8"><meta name="color-scheme" content="light dark"><title>web</title><style>body{font:14px/1.6 system-ui,sans-serif;margin:0;padding:28px 32px}h1{font-size:20px;margin:0 0 4px}p{margin:0 0 16px;opacity:.7}code{font-family:ui-monospace,monospace}ul{padding-left:18px}</style><h1>Oryn web (dev)</h1><p>Vite dev server · port <code>`,
)}{port}${encodeURIComponent(
	"</code></p><ul><li>/ — app shell</li><li>/docs — component docs</li><li>HMR connected</li></ul>",
)}`;

const MAIN_ORB_SERVICES: Schemas["OrbService"][] = [
	{
		name: "web",
		source: "file",
		command: 'bun run dev --port "$PORT" --host 127.0.0.1',
		cwd: "web",
		port: PORTAL_PORT,
		health: "/",
		links: [
			{ title: "web", path: "/" },
			{ title: "Docs", path: "/docs" },
		],
		threadId: MAIN_THREAD,
		publicUrl: `https://t-00000000a001-p${PORTAL_PORT}.orb.example.com`,
		status: "running",
		pid: 4121,
		restarts: 0,
		startedAt: NOW - 40 * MIN,
	},
	{
		name: "worker",
		source: "dynamic",
		command: "bun run worker",
		port: 20000,
		links: [],
		threadId: MAIN_THREAD,
		status: "failed",
		restarts: 5,
		exitCode: 1,
		// 与 orbd 的格式一致（`internal/orbdrt/services.go` 连续短命后的失败原因）
		error: "exited 5 times in a row within 1m0s (last exit code 1)",
		startedAt: NOW - 6 * MIN,
	},
];

/** 服务日志（服务名 → 文本）；`[orbd]` 行与 orbd 写入日志文件的状态行同形。 */
export const serviceLogs: Record<string, string> = {
	web: `[orbd] starting web (port ${PORTAL_PORT})
$ vite --port ${PORTAL_PORT} --host 127.0.0.1

  VITE v7.1.4  ready in 412 ms

  ➜  Local:   http://127.0.0.1:${PORTAL_PORT}/
[orbd] web is ready on port ${PORTAL_PORT}
10:41:07 [vite] hmr update /src/components/thread/RightPanel.tsx
10:42:31 [vite] hmr update /src/realtime/socket.ts
`,
	worker: Array.from(
		{ length: 5 },
		() => `[orbd] starting worker (port 20000)
error: Cannot find module "./jobs/index.ts" from "/home/user/workspace/repo/worker.ts"
[orbd] exited code=1`,
	)
		.join("\n[orbd] restarting in 2s\n")
		.concat("\n"),
};

// ---------------------------------------------------------------- 桌面与浏览器 Profile（M8）

/** 各桌面场景对应的线程（截图 `desktop-*` 直接打开）。 */
export const DESKTOP_THREADS = {
	/** 主线程：已连接、浏览器在跑、独占使用「工作账号」 */
	connected: MAIN_THREAD,
	/** 「GitHub 测试号」正被 DOCKER 线程的 orb 占用；本地已清空、浏览器未运行 */
	inUse: "T-0193a1b2-0000-7000-8000-00000000a003",
	/** 「工作账号」被主线程接管后降级为只读 */
	takenOver: "T-0193a1b2-0000-7000-8000-00000000a00b",
} as const;

const MAIN_ORB = "orb-01jcz8m3w4x5y6z7a8b9c0d1e2";
const DOCKER_ORB = "orb-01jcz8m3w4x5y6z7a8b9c0d1e4";
const TAKEN_OVER_ORB = "orb-01jcz8m3w4x5y6z7a8b9c0d1eb";
const MAIN_TITLE = "\u{F0668} WS: 重构 WebSocket 重连与 afterSeq 补拉";
const DOCKER_TITLE = "🐳 DOCKER: Docker provider 冻结后 orbd 重连";

export const BROWSER_PROFILE_IDS = {
	work: "bpf_01JCZ8M3W4X5Y6Z7A8B9C0D1P1",
	github: "bpf_01JCZ8M3W4X5Y6Z7A8B9C0D1P2",
	shop: "bpf_01JCZ8M3W4X5Y6Z7A8B9C0D1P3",
} as const;

/** 浏览器 Profile（与控制面的 `BrowserProfile` 同形）。 */
export const browserProfiles: Schemas["BrowserProfile"][] = [
	{
		id: BROWSER_PROFILE_IDS.work,
		name: "工作账号",
		revision: 3,
		sizeBytes: 18_874_368,
		savedAt: NOW - 12 * MIN,
		holder: {
			orbId: MAIN_ORB,
			threadIds: [MAIN_THREAD, "T-0193a1b2-0000-7000-8000-00000000a002"],
			threadId: MAIN_THREAD,
			threadTitle: MAIN_TITLE,
			since: NOW - 40 * MIN,
		},
		readers: [
			{
				orbId: TAKEN_OVER_ORB,
				threadIds: [DESKTOP_THREADS.takenOver],
				threadId: DESKTOP_THREADS.takenOver,
				threadTitle: "🛒 BUY: 比价并下单测试物料",
				since: NOW - 40 * MIN,
			},
		],
		createdAt: NOW - 9 * 24 * HOUR,
		updatedAt: NOW - 12 * MIN,
	},
	{
		id: BROWSER_PROFILE_IDS.github,
		name: "GitHub 测试号",
		revision: 1,
		sizeBytes: 4_718_592,
		savedAt: NOW - 2 * 24 * HOUR,
		holder: {
			orbId: DOCKER_ORB,
			threadIds: ["T-0193a1b2-0000-7000-8000-00000000a004"],
			threadId: "T-0193a1b2-0000-7000-8000-00000000a004",
			threadTitle: DOCKER_TITLE,
			since: NOW - 25 * MIN,
		},
		createdAt: NOW - 6 * 24 * HOUR,
		updatedAt: NOW - 25 * MIN,
	},
	{
		id: BROWSER_PROFILE_IDS.shop,
		name: "淘宝采购",
		revision: 2,
		sizeBytes: 271_581_184,
		savedAt: NOW - 3 * 24 * HOUR,
		saveError: "too_large",
		createdAt: NOW - 20 * 24 * HOUR,
		updatedAt: NOW - 3 * HOUR,
	},
];

/** 每个 orb 的环境字段（覆盖 `makeOrb` 的默认值）。 */
export const orbEnv: Record<string, Partial<Orb>> = {
	"orb-01jcz8m3w4x5y6z7a8b9c0d1e2": {
		desktop: {
			displayReady: true,
			browser: "running",
			profile: {
				id: BROWSER_PROFILE_IDS.work,
				name: "工作账号",
				status: "attached",
				mode: "exclusive",
				revision: 3,
			},
		},
		services: MAIN_ORB_SERVICES,
		portals: [
			{
				port: PORTAL_PORT,
				service: "web",
				links: MAIN_ORB_SERVICES[0]?.links ?? [],
			},
		],
		lastSetup: setupRun("orb-01jcz8m3w4x5y6z7a8b9c0d1e2", 1, {
			phase: "setup",
			durationMs: 48_300,
			finishedAt: NOW - 3 * HOUR + 2 * MIN,
		}),
		lastResume: setupRun("orb-01jcz8m3w4x5y6z7a8b9c0d1e2", 2, {
			phase: "resume",
			durationMs: 1_200,
			finishedAt: NOW - 12 * MIN,
		}),
	},
	"orb-01jcz8m3w4x5y6z7a8b9c0d1e3": {
		desktop: {
			displayReady: true,
			browser: "stopped",
			profile: {
				id: BROWSER_PROFILE_IDS.github,
				name: "GitHub 测试号",
				status: "in_use",
				mode: "exclusive",
				revision: 0,
				heldBy: browserProfiles[1]?.holder,
			},
		},
		lastSetup: setupRun("orb-01jcz8m3w4x5y6z7a8b9c0d1e3", 1, {
			phase: "setup",
			durationMs: 263_000,
		}),
	},
	[DOCKER_ORB]: {
		desktop: {
			displayReady: true,
			browser: "running",
			profile: {
				id: BROWSER_PROFILE_IDS.github,
				name: "GitHub 测试号",
				status: "attached",
				mode: "exclusive",
				revision: 1,
			},
		},
	},
	[TAKEN_OVER_ORB]: {
		desktop: {
			displayReady: true,
			browser: "running",
			profile: {
				id: BROWSER_PROFILE_IDS.work,
				name: "工作账号",
				status: "readonly",
				mode: "readonly",
				revision: 2,
				readonlyReason: "taken_over",
				takenOverBy: {
					threadId: MAIN_THREAD,
					threadTitle: MAIN_TITLE,
					at: NOW - 40 * MIN,
				},
			},
		},
	},
	"orb-01jcz8m3w4x5y6z7a8b9c0d1e5": {
		state: "failed",
		failureReason: ORB_FAILED_REASON,
	},
	"orb-01jcz8m3w4x5y6z7a8b9c0d1e8": {
		lastSetup: setupRun("orb-01jcz8m3w4x5y6z7a8b9c0d1e8", 1, {
			phase: "setup",
			ok: false,
			durationMs: 71_500,
			// 与 orbd 上报的格式一致（`<label>: exit status N`），原因在日志尾部
			error: ".oryn/setup: exit status 100",
		}),
		lastResume: setupRun("orb-01jcz8m3w4x5y6z7a8b9c0d1e8", 2, {
			phase: "resume",
			durationMs: 800,
			hasRepoScript: false,
		}),
	},
	"orb-01jcz8m3w4x5y6z7a8b9c0d1e9": {
		projectId: "prj_01JCZ00000000000000AGENT",
		lastSetup: setupRun("orb-01jcz8m3w4x5y6z7a8b9c0d1e9", 1, {
			phase: "setup",
			durationMs: 4_100,
			hasRepoScript: false,
		}),
	},
};

/** 运行记录 id → 日志全文。 */
export const setupLogs: Record<string, string> = Object.fromEntries(
	Object.values(orbEnv).flatMap((env) =>
		[env.lastSetup, env.lastResume]
			.filter((r): r is SetupRun => Boolean(r))
			.map((r) => [
				r.id,
				!r.ok
					? FAILED_SETUP_LOG
					: r.durationMs >= 120_000
						? SLOW_SETUP_LOG
						: `[${r.phase}] + ${r.phase === "setup" ? "bun install --cwd web" : "gh auth status"}\n[${r.phase}] done\n`,
			]),
	),
);

export const threads: Thread[] = [
	thread({
		id: MAIN_THREAD,
		title: "\u{F0668} WS: 重构 WebSocket 重连与 afterSeq 补拉",
		projectId: projects[0]?.id,
		orbId: "orb-01jcz8m3w4x5y6z7a8b9c0d1e2",
		model: { provider: "anthropic", id: "claude-sonnet-5" },
		thinkingLevel: "high",
		labels: ["realtime"],
		color: "blue",
		childCount: 2,
		childDoneCount: 1,
	}),
	thread({
		id: "T-0193a1b2-0000-7000-8000-00000000a002",
		title: "\u{F0453} RETRY: 共用 orb 试另一种退避策略",
		projectId: projects[0]?.id,
		orbId: "orb-01jcz8m3w4x5y6z7a8b9c0d1e2",
		parentThreadId: MAIN_THREAD,
		state: "working",
		lastMessageAt: NOW - MIN,
		activity: "读 socket.ts 定位重连逻辑",
	}),
	thread({
		id: "T-0193a1b2-0000-7000-8000-00000000a003",
		title: "🧪 SNAP: 连环境验证快照恢复",
		projectId: projects[0]?.id,
		parentThreadId: MAIN_THREAD,
		orbId: "orb-01jcz8m3w4x5y6z7a8b9c0d1e3",
	}),
	thread({
		id: "T-0193a1b2-0000-7000-8000-00000000a0f1",
		title: "fork 的 fork：快照恢复后再试冷启动",
		projectId: projects[0]?.id,
		parentThreadId: "T-0193a1b2-0000-7000-8000-00000000a003",
		orbId: "orb-01jcz8m3w4x5y6z7a8b9c0d1e3",
		lastMessageAt: NOW - 2 * MIN,
	}),
	thread({
		id: "T-0193a1b2-0000-7000-8000-00000000a004",
		title: "🐳 DOCKER: Docker provider 冻结后 orbd 重连",
		projectId: projects[0]?.id,
		pinned: true,
		state: "awaiting_input",
		unread: true,
		orbId: "orb-01jcz8m3w4x5y6z7a8b9c0d1e4",
	}),
	thread({
		id: "T-0193a1b2-0000-7000-8000-00000000a005",
		title: "同步上游 can1357/oh-my-pi",
		projectId: projects[1]?.id,
		state: "error",
		// 用户标的线程色；不用红色，免得和出错状态的红色图标叠成同一个信号
		color: "purple",
		orbId: "orb-01jcz8m3w4x5y6z7a8b9c0d1e5",
		error: { kind: "orb_failed", reason: ORB_FAILED_REASON },
	}),
	thread({
		id: ENV_THREADS.setupFailed,
		title: "🐘 PG: 接入 Postgres 测试数据库",
		projectId: projects[0]?.id,
		orbId: "orb-01jcz8m3w4x5y6z7a8b9c0d1e8",
		lastMessageAt: NOW - 8 * MIN,
	}),
	thread({
		id: ENV_THREADS.missingConfig,
		title: "补全 CLI 的 shell 补全脚本",
		projectId: projects[1]?.id,
		orbId: "orb-01jcz8m3w4x5y6z7a8b9c0d1e9",
		lastMessageAt: NOW - 20 * MIN,
	}),
	thread({
		id: DESKTOP_THREADS.takenOver,
		title: "🛒 BUY: 比价并下单测试物料",
		projectId: projects[0]?.id,
		orbId: TAKEN_OVER_ORB,
		lastMessageAt: NOW - 45 * MIN,
	}),
	thread({
		id: "T-0193a1b2-0000-7000-8000-00000000a006",
		title: "整理 models.yml 密钥抽取规则",
		lastMessageAt: NOW - 5 * 24 * HOUR,
		updatedAt: NOW - 5 * 24 * HOUR,
	}),
	thread({
		id: "T-0193a1b2-0000-7000-8000-00000000a007",
		title: "写部署文档（1Panel 泛域名证书）",
		snoozedUntil: NOW + 20 * HOUR,
	}),
	thread({
		id: TOOL_ZOO_THREAD,
		title: "工具样例",
		projectId: projects[0]?.id,
		orbId: TOOL_ZOO_ORB,
		model: { provider: "anthropic", id: "claude-sonnet-5" },
		thinkingLevel: "high",
		lastMessageAt: NOW - 30 * MIN,
	}),
];

// ---------------------------------------------------------------- 转录

let seq = 0;
let lastId: string | null = null;

function entry(
	type: string,
	body: Record<string, unknown>,
	ts: number,
	id: string,
): Entry {
	seq++;
	const e: Entry = {
		seq,
		file: SESSION_FILE,
		entryId: id,
		parentId: lastId ?? undefined,
		type,
		ts,
		payload: {
			type,
			id,
			parentId: lastId,
			timestamp: new Date(ts).toISOString(),
			...body,
		},
	};
	lastId = id;
	return e;
}

const usage = (input: number, output: number, cost: number) => ({
	input,
	output,
	cacheRead: Math.round(input * 3.2),
	cacheWrite: 0,
	totalTokens: input + output,
	cost: {
		input: cost * 0.4,
		output: cost * 0.6,
		cacheRead: 0,
		cacheWrite: 0,
		total: cost,
	},
});

export function assistant(
	content: unknown[],
	ts: number,
	extra: Record<string, unknown> = {},
) {
	return {
		role: "assistant",
		content,
		api: "anthropic-messages",
		provider: "anthropic",
		model: "claude-sonnet-5",
		usage: usage(18_400, 920, 0.0412),
		stopReason: "toolUse",
		timestamp: ts,
		...extra,
	};
}

const T0 = NOW - 40 * MIN;

const DIFF = `@@ -42,9 +42,14 @@ export class RealtimeClient {
-\tprivate scheduleReconnect() {
-\t\tsetTimeout(() => this.open(), 1000);
+\tprivate scheduleReconnect(reason: string) {
+\t\tconst attempts = connectionStore.getState().attempts;
+\t\tconnectionStore.setState({ status: "reconnecting", attempts: attempts + 1 });
+\t\tthis.reconnectTimer = setTimeout(() => {
+\t\t\tthis.reconnectTimer = null;
+\t\t\tvoid this.open();
+\t\t}, backoffMs(attempts));
 \t}`;

const FINAL_TEXT = `重连逻辑已改好，要点：

1. **指数退避**：\`0.5s → 1s → 2s … → 15s\` 封顶，成功后归零。
2. **补拉**：重连后对每个已订阅线程发 \`sub{afterSeq}\`，服务端先回 \`thread.snapshot\`，再分批补发条目。
3. **在途命令**：断线时全部以 \`disconnected\` 拒绝，由调用方决定是否重发。

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| 服务端重启 | 固定 1s 重试 | 退避 + 补拉 |
| 流式中断 | 叠加层残留 | \`live.reset\` 后按条目渲染 |

\`\`\`mermaid
sequenceDiagram
  participant C as 客户端
  participant S as orynd
  C->>S: POST ws-ticket
  C->>S: GET /api/v1/ws?ticket=…
  C->>S: sub {afterSeq: 128}
  S-->>C: thread.snapshot
  S-->>C: entries (seq > 128)
  S-->>C: frame …
\`\`\`

退避上限满足 $t_n = \\min(15, 0.5 \\cdot 2^n)$ 秒，最坏情况下恢复时间为

$$T = \\sum_{n=0}^{k} t_n \\le 0.5(2^{k+1}-1)$$
`;

export function buildMainTranscript(): Entry[] {
	seq = 0;
	lastId = null;
	const out: Entry[] = [];
	out.push({
		seq: ++seq,
		file: SESSION_FILE,
		entryId: "__title__",
		type: "title",
		payload: {
			type: "title",
			v: 1,
			title: "",
			updatedAt: new Date(T0).toISOString(),
		},
	});
	out.push({
		seq: ++seq,
		file: SESSION_FILE,
		entryId: "__session__",
		type: "session",
		payload: {
			type: "session",
			version: 3,
			id: "s1",
			cwd: "/home/user/workspace/repo",
		},
	});
	out.push(
		entry(
			"message",
			{
				message: {
					role: "user",
					content:
						"WebSocket 断线后重连太粗糙了：固定 1 秒重试，而且重连后不会补拉条目。帮我改成指数退避，并在重连后用 afterSeq 续传。",
					timestamp: T0,
				},
			},
			T0,
			"a1b2c3d4",
		),
	);
	out.push(
		entry(
			"message",
			{
				message: assistant(
					[
						{
							type: "thinking",
							thinking:
								"先看现有的 socket.ts：重连逻辑在 scheduleReconnect，订阅表在 subs。需要在 onopen 里对每个订阅线程重发 sub，并带上 store 里的 lastSeq。",
						},
						{ type: "text", text: "先看一下现有实现。" },
						{
							type: "toolCall",
							id: "toolu_read_1",
							name: "read",
							arguments: {
								path: "web/src/realtime/socket.ts",
								i: "Reading reconnect logic",
							},
						},
					],
					T0 + 4000,
				),
			},
			T0 + 4000,
			"b2c3d4e5",
		),
	);
	out.push(
		entry(
			"message",
			{
				message: {
					role: "toolResult",
					toolCallId: "toolu_read_1",
					toolName: "read",
					content: [
						{
							type: "text",
							text: "40|\tprivate scheduleReconnect() {\n41|\t\tsetTimeout(() => this.open(), 1000);\n42|\t}\n43|\n44|\tsubscribe(threadId: string) {\n45|\t\tthis.subs.add(threadId);\n46|\t}",
						},
					],
					details: {
						resolvedPath:
							"/home/user/workspace/repo/web/src/realtime/socket.ts",
					},
					isError: false,
					timestamp: T0 + 5000,
				},
			},
			T0 + 5000,
			"c3d4e5f6",
		),
	);
	out.push(
		entry(
			"message",
			{
				message: assistant(
					[
						{ type: "text", text: "改成退避重连，并把订阅表换成引用计数：" },
						{
							type: "toolCall",
							id: "toolu_edit_1",
							name: "edit",
							arguments: {
								path: "web/src/realtime/socket.ts",
								i: "Adding exponential backoff",
							},
						},
					],
					T0 + 20_000,
				),
			},
			T0 + 20_000,
			"d4e5f6a7",
		),
	);
	out.push(
		entry(
			"message",
			{
				message: {
					role: "toolResult",
					toolCallId: "toolu_edit_1",
					toolName: "edit",
					content: [
						{ type: "text", text: "Updated web/src/realtime/socket.ts" },
					],
					details: {
						path: "web/src/realtime/socket.ts",
						diff: DIFF,
						firstChangedLine: 42,
					},
					isError: false,
					timestamp: T0 + 21_000,
				},
			},
			T0 + 21_000,
			"e5f6a7b8",
		),
	);
	out.push(
		entry(
			"message",
			{
				message: assistant(
					[
						{
							type: "toolCall",
							id: "toolu_bash_1",
							name: "bash",
							arguments: {
								command: "cd web && bun run test -- socket",
								i: "Running socket tests",
							},
						},
					],
					T0 + 30_000,
				),
			},
			T0 + 30_000,
			"f6a7b8c9",
		),
	);
	out.push(
		entry(
			"message",
			{
				message: {
					role: "toolResult",
					toolCallId: "toolu_bash_1",
					toolName: "bash",
					content: [
						{
							type: "text",
							text: " ✓ src/realtime/socket.test.ts (6 tests) 41ms\n   ✓ 重连后以 afterSeq 补拉\n   ✓ 退避上限 15s\n\n Test Files  1 passed (1)\n      Tests  6 passed (6)",
						},
					],
					details: { exitCode: 0, wallTimeMs: 1830 },
					isError: false,
					timestamp: T0 + 33_000,
				},
			},
			T0 + 33_000,
			"a7b8c9d0",
		),
	);
	out.push(
		entry(
			"message",
			{
				message: assistant([{ type: "text", text: FINAL_TEXT }], T0 + 40_000, {
					stopReason: "stop",
					usage: usage(21_300, 1_480, 0.0583),
				}),
			},
			T0 + 40_000,
			"b8c9d0e1",
		),
	);
	return out;
}

/**
 * 各厂商的速度档位能力（与 omp 一致）：Anthropic 有 fast（speed: fast）与订阅全局慢速，没有 ultrafast；
 * OpenAI 有 priority / ultrafast / flex（会话级慢速）；其余厂商只有标准档。换模型时已开的档位一并清掉。
 */
export function mockSpeedCaps(
	provider: string,
): Pick<
	SessionState,
	| "fastModeSupported"
	| "fastModeEnabled"
	| "fastModeActive"
	| "ultrafastSupported"
	| "ultrafastEnabled"
	| "slowModeSupported"
	| "slowModeEnabled"
	| "slowModeScope"
> {
	const anthropic = provider === "anthropic";
	const openai = provider === "openai";
	return {
		fastModeSupported: anthropic || openai,
		fastModeEnabled: false,
		fastModeActive: false,
		ultrafastSupported: openai,
		ultrafastEnabled: false,
		slowModeSupported: anthropic || openai,
		slowModeEnabled: false,
		slowModeScope: anthropic ? "global" : openai ? "session" : undefined,
	};
}

export function mainState(): SessionState {
	return {
		model: {
			id: "claude-sonnet-5",
			name: "Claude Sonnet 5",
			provider: "anthropic",
			api: "anthropic-messages",
		} as unknown as SessionState["model"],
		thinkingLevel: "high" as SessionState["thinkingLevel"],
		thinkingConfigured: "high" as SessionState["thinkingConfigured"],
		availableThinkingLevels: [
			"off",
			"minimal",
			"low",
			"medium",
			"high",
			"xhigh",
		] as SessionState["availableThinkingLevels"],
		isStreaming: false,
		isCompacting: false,
		steeringMode: "all",
		followUpMode: "all",
		interruptMode: "immediate",
		cwd: "/home/user/workspace/repo",
		sessionId: "s1",
		autoCompactionEnabled: true,
		autoRetryEnabled: true,
		...mockSpeedCaps("anthropic"),
		tokensPerSecond: 64,
		messageCount: 9,
		queuedMessageCount: 0,
		hasPendingAsyncWork: false,
		isSettled: true,
		queuedMessages: { steering: [], followUp: [], liveSteered: 0 },
		todoPhases: [
			{
				name: "重连",
				tasks: [
					{ content: "指数退避（0.5s → 15s）", status: "completed" },
					{ content: "重连后 afterSeq 补拉", status: "completed" },
					{ content: "断线时拒绝在途命令", status: "in_progress" },
					{ content: "补端到端测试", status: "pending" },
				],
			},
		],
		contextUsage: { tokens: 48_200, contextWindow: 200_000, percent: 24.1 },
		planModeEnabled: false,
		agentsPaused: false,
		goal: null,
	};
}

// ---------------------------------------------------------------- 工具样例（TOOL_ZOO_THREAD）

/** 96×64 的小图（圆环品牌标），供 browser / generate_image 的结果图片。 */
const ZOO_PNG =
	"iVBORw0KGgoAAAANSUhEUgAAAGAAAABACAIAAABqVuVZAAAB10lEQVR42u3XWVICQQyA4ZzAU3sE76YoqAMMsy+ABxBLH7RmJr0m6SlT9T92N8nHgwjN+KEhQT1cNSQFUqBAoGq4akhQ9VcNSYFMQGV/0ZAUyAjUXTQkKLqLhqRAawO6u39IC+jUngW7cdgkOCEkTiPOBKugEWSCFdGIMEHennmKqPMdz9iQN2eGouv8GNFPDuI61NdDgY7NSBqym8g7rokByT7lAlSPdC2ulMyDxuBQj3TNLpPgm0iwLh1+I1agtTz+B2hfDRTNfsmWF0nfd40PKPrC/wgI+Y0jD5RVA0XT6e1Pel+kWASycqBoZnrrk94XKRZRIAkg+z0t/2sP/JQgoPeyp2g6uv1J74sUiyiQEajoKZqZ3vqk90WKReCt6CmaTu902O8WxSJJAC0Zxf2I1IEsF7A/xgT0euqImi6wlsd/xwoUaw26l1mBiDbh1PkC2p06upb+NqXzoDHY5R1piyuJPmWfGJDrYrHecQba5i11+A9l6uuBcQAZl/SOYXLYHlue4uuwjM0HFNeIbWZ4ObbMBdIwTysA5M0kMqcYkBOT4ITwfGiS6saR1DzJAaWWApmANodGQ4LNvtGQFMgE9LSvNSQFMgJltYYEj1mtISmQAoX1CdOvjQj2s62vAAAAAElFTkSuQmCC";

const REPO_DIR = "/home/user/workspace/repo";

interface ZooCall {
	name: string;
	args: Record<string, unknown>;
	text?: string;
	details?: unknown;
	isError?: boolean;
	image?: boolean;
}

/** 一组工具调用：一条 assistant 消息（文字 + 多个 toolCall），随后逐个 toolResult。 */
const ZOO_GROUPS: { text: string; calls: ZooCall[] }[] = [
	{
		text: "先读代码、搜索调用点：",
		calls: [
			{
				name: "read",
				args: { path: "web/src/realtime/socket.ts:40-52", i: "读取重连逻辑" },
				text: '40|\tprivate scheduleReconnect() {\n41|\t\tsetTimeout(() => this.open(), 1000);\n42|\t}\n43|\n44|\tsubscribe(threadId: string) {\n45|\t\tthis.subs.add(threadId);\n46|\t\tthis.send({ type: "sub", threadId });\n47|\t}',
				details: { resolvedPath: `${REPO_DIR}/web/src/realtime/socket.ts` },
			},
			{
				name: "grep",
				args: { pattern: "afterSeq", path: "web/src", type: "ts" },
				text: 'web/src/realtime/socket.ts:52:\t\tthis.send({ type: "sub", threadId, afterSeq });\nweb/src/state/thread-reducer.ts:955:\tconst keepEntries = !snap.fullReplay && snap.lastSeq >= s.lastSeq;\nweb/src/mock/server.ts:1093:\t\tconst after = typeof msg.afterSeq === "number" ? msg.afterSeq : 0;',
				details: { matchCount: 3, fileCount: 3 },
			},
			{
				name: "search",
				args: { pattern: "scheduleReconnect", paths: ["web/src", "internal"] },
				text: "web/src/realtime/socket.ts:40:\tprivate scheduleReconnect() {\nweb/src/realtime/socket.ts:88:\t\tthis.scheduleReconnect();",
				details: { matchCount: 2, fileCount: 1 },
			},
			{
				name: "glob",
				args: { path: "web/src/realtime/**/*.ts" },
				text: "web/src/realtime/socket.ts\nweb/src/realtime/socket.test.ts\nweb/src/realtime/backoff.ts",
				details: { fileCount: 3 },
			},
			{
				name: "find",
				args: { paths: ["internal/**/*_test.go"], limit: 4 },
				text: "internal/realtime/hub_test.go\ninternal/realtime/sub_test.go\ninternal/orb/spawn_test.go\ninternal/webui/webui_test.go",
				details: { fileCount: 4, resultLimitReached: 4 },
			},
			{
				name: "ast_grep",
				args: {
					pat: "setTimeout($FN, $MS)",
					paths: ["web/src/realtime"],
					lang: "typescript",
				},
				text: "web/src/realtime/socket.ts\n  41:\t\tsetTimeout(() => this.open(), 1000);\n  97:\t\tsetTimeout(() => this.ping(), 25_000);",
				details: { matchCount: 2, fileCount: 1, filesSearched: 6 },
			},
			{
				name: "lsp",
				args: { action: "diagnostics", file: "web/src/realtime/socket.ts" },
				text: "web/src/realtime/socket.ts:57:9 [error] Property 'reconnectTimer' does not exist on type 'RealtimeClient'.\nweb/src/realtime/socket.ts:61:7 [warning] 'attempt' is declared but its value is never read.\n1 error(s), 1 warning(s)",
				details: {
					serverName: "typescript-language-server",
					action: "diagnostics",
				},
			},
		],
	},
	{
		text: "改文件：",
		calls: [
			{
				name: "write",
				args: {
					path: "web/src/realtime/backoff.ts",
					content:
						"/** 指数退避：0.5s 起步，每次翻倍，15s 封顶。 */\nexport function backoffMs(attempt: number): number {\n\tconst base = Math.min(15_000, 500 * 2 ** attempt);\n\treturn Math.round(base * (0.8 + Math.random() * 0.4));\n}\n",
				},
				text: "Wrote 5 lines to web/src/realtime/backoff.ts",
				details: {},
			},
			{
				name: "edit",
				args: { path: "web/src/realtime/socket.ts" },
				text: "Updated web/src/realtime/socket.ts",
				details: {
					path: "web/src/realtime/socket.ts",
					diff: DIFF,
					firstChangedLine: 42,
				},
			},
			{
				name: "apply_patch",
				args: {
					input:
						'*** Begin Patch\n*** Update File: web/src/realtime/pending.ts\n@@\n-\tpending.clear();\n+\tfor (const p of pending.values()) p.reject(new Error("disconnected"));\n+\tpending.clear();\n*** End Patch',
				},
				text: "Updated web/src/realtime/pending.ts",
				details: {
					path: "web/src/realtime/pending.ts",
					diff: '@@ -18,3 +18,4 @@ export function failAll() {\n-\tpending.clear();\n+\tfor (const p of pending.values()) p.reject(new Error("disconnected"));\n+\tpending.clear();\n }',
					firstChangedLine: 18,
				},
			},
			{
				name: "ast_edit",
				args: {
					paths: ["web/src/realtime"],
					ops: [{ pat: "console.log($A)", out: "log.debug($A)" }],
				},
				text: "3 replacements in 2 files (preview)",
				details: {
					totalReplacements: 3,
					filesTouched: 2,
					filesSearched: 6,
					fileReplacements: [
						{ path: "web/src/realtime/socket.ts", count: 2 },
						{ path: "web/src/realtime/pending.ts", count: 1 },
					],
					displayContent:
						'--- a/web/src/realtime/socket.ts\n+++ b/web/src/realtime/socket.ts\n@@ -70,1 +70,1 @@\n-\t\tconsole.log("reconnect", attempt);\n+\t\tlog.debug("reconnect", attempt);',
				},
			},
			{
				name: "resolve",
				args: { action: "apply", reason: "预览确认无误，应用替换" },
				text: "Applied 3 replacements.",
				details: {
					action: "apply",
					sourceToolName: "ast_edit",
					label: "3 replacements in 2 files",
				},
			},
			{
				name: "reject",
				args: { reason: "改动波及测试快照，放弃" },
				text: "Discarded preview.",
				details: {
					action: "discard",
					sourceToolName: "ast_edit",
					label: "1 replacement in 1 file",
				},
			},
		],
	},
	{
		text: "跑测试与脚本：",
		calls: [
			{
				name: "bash",
				args: {
					command: "cd web && bun run test -- realtime",
					cwd: REPO_DIR,
					timeout: 120,
				},
				text: " ✓ src/realtime/socket.test.ts (6 tests) 41ms\n ✓ src/realtime/backoff.test.ts (3 tests) 4ms\n\n Test Files  2 passed (2)\n      Tests  9 passed (9)",
				details: { wallTimeMs: 2140 },
			},
			{
				name: "bash",
				args: { command: "cd web && bun run lint" },
				text: 'src/realtime/socket.ts:61:7 lint/correctness/noUnusedVariables  ━━━━━━━━━━\n  ✖ This variable attempt is unused.\n\nFound 1 error.\nerror: script "lint" exited with code 1',
				details: { exitCode: 1, wallTimeMs: 870 },
				isError: true,
			},
			{
				name: "terminal",
				args: { action: "run", command: "bun run dev:mock" },
				text: "  VITE v8.0.0  ready in 412 ms\n\n  ➜  Local:   http://localhost:5173/",
				details: {
					terminal: {
						terminalId: "t3",
						kind: "command",
						title: "bun run dev:mock",
						cwd: `${REPO_DIR}/web`,
						running: true,
						state: "running",
					},
				},
			},
			{
				name: "eval",
				args: {
					cells: [
						{
							language: "py",
							title: "退避序列",
							code: "[min(15, 0.5 * 2**n) for n in range(7)]",
						},
					],
				},
				details: {
					cells: [
						{
							index: 0,
							title: "退避序列",
							code: "[min(15, 0.5 * 2**n) for n in range(7)]",
							language: "py",
							output: "[0.5, 1.0, 2.0, 4.0, 8.0, 15, 15]",
							status: "ok",
							durationMs: 38,
						},
					],
				},
				text: "[0.5, 1.0, 2.0, 4.0, 8.0, 15, 15]",
			},
			{
				name: "js",
				args: {
					language: "js",
					code: "const total = [500, 1000, 2000, 4000].reduce((a, b) => a + b, 0);\ntotal / 1000;",
				},
				details: {
					cells: [
						{
							index: 0,
							code: "",
							language: "js",
							output: "7.5",
							status: "ok",
							durationMs: 6,
						},
					],
				},
				text: "7.5",
			},
			{
				name: "python",
				args: {
					language: "py",
					code: "import statistics\nstatistics.mean([0.41, 0.52, 0.47])",
				},
				details: {
					cells: [
						{
							index: 0,
							code: "",
							language: "py",
							output: "0.4666666666666666",
							status: "ok",
							durationMs: 12,
						},
					],
				},
				text: "0.4666666666666666",
			},
			{
				name: "notebook",
				args: {
					cells: [
						{
							language: "py",
							title: "加载",
							code: 'import json\nrows = json.load(open("latency.json"))',
						},
						{
							language: "py",
							title: "P95",
							code: 'sorted(r["ms"] for r in rows)[int(len(rows) * 0.95)]',
						},
					],
				},
				details: {
					cells: [
						{ index: 0, output: "", status: "ok", durationMs: 21 },
						{ index: 1, output: "184", status: "ok", durationMs: 3 },
					],
				},
				text: "184",
			},
			{
				name: "wait",
				args: {},
				text: "1 job completed.",
				details: {
					jobs: [
						{
							id: "bg_3",
							type: "bash",
							status: "completed",
							label: "go test ./internal/realtime/...",
							intent: "后台跑 Go 端测试",
							durationMs: 48_200,
							resultText: "ok  \toryn/internal/realtime\t1.92s",
						},
					],
				},
			},
		],
	},
	{
		text: "查资料、看页面：",
		calls: [
			{
				name: "fetch",
				args: { url: "https://pkg.go.dev/nhooyr.io/websocket", timeout: 20 },
				text: "# websocket\n\nPackage websocket implements the RFC 6455 WebSocket protocol.\n\n## Deprecated\n\nUse github.com/coder/websocket instead.",
				details: {
					url: "https://pkg.go.dev/nhooyr.io/websocket",
					finalUrl: "https://pkg.go.dev/github.com/coder/websocket",
					contentType: "text/html; charset=utf-8",
					method: "readability",
					notes: ["已跟随 1 次重定向"],
				},
			},
			{
				name: "web_search",
				args: {
					query: "websocket reconnect exponential backoff jitter",
					recency: "year",
					limit: 3,
				},
				text: "常见做法是**指数退避 + 全抖动**：等待时间取 `random(0, min(cap, base·2^n))`，避免大量客户端同时重连。",
				details: {
					response: {
						provider: "exa",
						model: "exa-research",
						sources: [
							{
								url: "https://aws.amazon.com/blogs/architecture/exponential-backoff-and-jitter/",
								title: "Exponential Backoff And Jitter",
								ageSeconds: 86_400 * 200,
							},
							{
								url: "https://developer.mozilla.org/en-US/docs/Web/API/WebSocket",
								title: "WebSocket - Web APIs | MDN",
								ageSeconds: 86_400 * 12,
							},
						],
						usage: { inputTokens: 1840, outputTokens: 212, searchRequests: 1 },
					},
				},
			},
			{
				name: "github",
				args: {
					op: "search_issues",
					query: "reconnect afterSeq",
					repo: "can1357/oh-my-pi",
				},
				text: "#412 rpc: resume subscription after reconnect (open)\n#388 ws: replay entries after seq (closed)",
			},
			{
				name: "browser",
				args: {
					action: "open",
					url: "http://localhost:5173/t/demo",
					viewport: { width: 1280, height: 800 },
				},
				text: "Opened tab main at http://localhost:5173/t/demo",
				details: {
					action: "open",
					name: "main",
					url: "http://localhost:5173/t/demo",
					browser: "chromium 140",
				},
				image: true,
			},
			{
				name: "puppeteer",
				args: {
					action: "run",
					name: "main",
					code: 'await page.getByRole("button", { name: "断开" }).click();\nreturn await page.title();',
				},
				text: '"重构 WebSocket 重连与 afterSeq 补拉 · Oryn"',
				details: { action: "run", name: "main" },
			},
			{
				name: "generate_image",
				args: {
					subject: "Oryn 品牌标：青碧色圆环与圆点",
					style: "flat vector",
					aspect_ratio: "3:2",
				},
				text: "Generated 1 image.",
				details: {
					provider: "openai",
					model: "gpt-image-2",
					imagePaths: [`${REPO_DIR}/assets/mark.png`],
				},
				image: true,
			},
		],
	},
	{
		text: "分派子任务、记录进度：",
		calls: [
			{
				name: "task",
				args: {
					agent: "explore",
					context: "重连改造：确认服务端补拉与客户端订阅表一致。",
					tasks: [
						{
							name: "HubAudit",
							task: "审计 internal/realtime/hub.go 的 afterSeq 补拉边界",
						},
						{ name: "QueueCheck", task: "检查断线时在途命令是否全部被拒绝" },
					],
				},
				text: "2 agents finished.",
				details: {
					results: [
						{
							id: "HubAudit",
							description: "afterSeq 补拉边界",
							exitCode: 0,
							output: "补拉从 seq+1 开始，批大小 200，无遗漏。",
							tokens: 12_400,
							requests: 6,
							durationMs: 41_000,
							resolvedModel: "claude-haiku-5",
						},
						{
							id: "QueueCheck",
							description: "在途命令拒绝",
							exitCode: 0,
							output: "pending.ts 的 failAll 覆盖全部在途命令。",
							tokens: 8_900,
							requests: 4,
							durationMs: 27_000,
							resolvedModel: "claude-haiku-5",
						},
					],
					totalDurationMs: 43_000,
				},
			},
			{
				name: "todo",
				args: {
					ops: [
						{ op: "done", task: "指数退避（0.5s → 15s）" },
						{ op: "start", task: "断线时拒绝在途命令" },
					],
				},
				text: "Updated 2 tasks.",
				details: {
					phases: [
						{
							name: "重连",
							tasks: [
								{ content: "指数退避（0.5s → 15s）", status: "completed" },
								{ content: "重连后 afterSeq 补拉", status: "completed" },
								{ content: "断线时拒绝在途命令", status: "in_progress" },
								{ content: "补端到端测试", status: "pending" },
							],
						},
					],
				},
			},
			{
				name: "ask",
				args: {
					html: "<p>要保留旧的固定间隔作为降级开关吗？</p><label><input type=radio name=q1 value=keep>保留</label><label><input type=radio name=q1 value=drop>删除</label>",
				},
				text: "User answered.",
				details: {
					answers: [
						{
							name: "q1",
							question: "要保留旧的固定间隔作为降级开关吗？",
							answer: "保留，默认关闭",
						},
					],
				},
			},
			{
				name: "goal",
				args: {
					op: "create",
					objective: "完成重连改造并补齐端到端测试",
					token_budget: 400_000,
				},
				text: "Goal set.",
				details: {
					op: "create",
					goal: {
						objective: "完成重连改造并补齐端到端测试",
						status: "active",
						tokenBudget: 400_000,
						tokensUsed: 86_000,
						timeUsedSeconds: 1260,
					},
				},
			},
			{
				name: "recall",
				args: { query: "WebSocket 重连的既有约定" },
				text: "Found 2 relevant memories (as of 2026-11-12 09:00 UTC)\n\n- 重连退避上限 15s，带 ±20% 抖动 [decision] (2026-11-02)\n\n- 订阅恢复用 afterSeq，不重放全量 [convention] (2026-10-28)",
			},
			{
				name: "reflect",
				args: {
					query: "这次重连改造有哪些值得记住的坑？",
					context:
						"改造过程中 lint 报了未使用变量，ast_edit 预览曾被放弃一次。",
				},
				text: "1. 断线时必须先拒绝在途命令再重置订阅表，否则会重复发送。\n2. 退避计数要在 onopen 成功后归零。",
			},
			{
				name: "retain",
				args: {
					items: [
						{
							content: "重连成功后在 onopen 里把退避计数归零",
							context: "web/src/realtime/socket.ts",
						},
					],
				},
				text: "1 memory retained.",
				details: { count: 1 },
			},
			{
				name: "debug",
				args: {
					action: "launch",
					program: "bin/orynd",
					args: ["serve", "--dev"],
					adapter: "dlv",
				},
				text: "Stopped at breakpoint in realtime.(*Hub).Resume",
				details: {
					snapshot: {
						id: "1",
						adapter: "dlv",
						status: "stopped",
						program: "bin/orynd",
						stopReason: "breakpoint",
						frameName: "realtime.(*Hub).Resume",
						source: { path: "internal/realtime/hub.go" },
						line: 118,
						column: 2,
					},
				},
			},
			{
				name: "report_tool_issue",
				args: { tool: "grep", report: "type=ts 过滤漏掉了 .tsx 文件" },
				text: "Reported.",
			},
			{
				name: "propose",
				args: {
					title: "重连改造计划",
					planFilePath: "local://reconnect-plan.md",
				},
				text: "Plan proposed.",
			},
			{
				name: "yield",
				args: {
					data: {
						summary: "重连改造完成",
						files: [
							"web/src/realtime/socket.ts",
							"web/src/realtime/backoff.ts",
						],
					},
				},
				text: "Result submitted.",
			},
		],
	},
];

/** 工具样例线程里仍在运行的工具调用（快照的 `agent.live` 帧与之对应）。 */
const ZOO_LIVE_CALL = "toolu_zoo_live";
const ZOO_LIVE_ARGS = {
	command: "cd web && bun run test -- --watch=false e2e",
	i: "跑端到端测试",
};

/** 工具样例转录：每个注册工具一次调用 + 一次失败 + 一次运行中，外加 bashExecution、compaction、model_change。 */
export function buildToolZooTranscript(): Entry[] {
	seq = 0;
	lastId = null;
	const out: Entry[] = [];
	const t0 = NOW - 35 * MIN;
	let ts = t0;
	let n = 0;
	const id = () => `z${(++n).toString(16).padStart(7, "0")}`;
	const push = (type: string, body: Record<string, unknown>) => {
		ts += 4000;
		out.push(entry(type, body, ts, id()));
	};
	push("message", {
		message: {
			role: "user",
			content: "把工具渲染器挨个跑一遍，看看每种卡片长什么样。",
			timestamp: ts + 4000,
		},
	});
	push("model_change", { model: "anthropic/claude-sonnet-5" });
	ZOO_GROUPS.forEach((group, gi) => {
		const calls = group.calls.map((c, ci) => ({
			...c,
			toolCallId: `toolu_zoo_${gi}_${ci}`,
		}));
		push("message", {
			message: assistant(
				[
					{ type: "text", text: group.text },
					...calls.map((c) => ({
						type: "toolCall",
						id: c.toolCallId,
						name: c.name,
						arguments: c.args,
					})),
				],
				ts + 4000,
			),
		});
		for (const c of calls) {
			const content: unknown[] = c.text ? [{ type: "text", text: c.text }] : [];
			if (c.image)
				content.push({ type: "image", data: ZOO_PNG, mimeType: "image/png" });
			push("message", {
				message: {
					role: "toolResult",
					toolCallId: c.toolCallId,
					toolName: c.name,
					content,
					details: c.details ?? {},
					isError: c.isError === true,
					timestamp: ts + 4000,
				},
			});
		}
		if (gi === 0) {
			push("message", {
				message: {
					role: "bashExecution",
					command: "git status --short",
					output:
						" M web/src/realtime/socket.ts\n?? web/src/realtime/backoff.ts",
					exitCode: 0,
					cancelled: false,
					truncated: false,
					timestamp: ts + 4000,
				},
			});
		}
		if (gi === 1) {
			push("compaction", {
				summary:
					"## 目标\n重构 WebSocket 重连。\n\n## 进展\n- 已加入指数退避与 afterSeq 补拉\n- ast_edit 预览一次应用、一次放弃",
				shortSummary: "重连改造：退避与补拉已完成，正在验证。",
				firstKeptEntryId: out[0]?.entryId,
				tokensBefore: 142_000,
				tokensAfter: 31_500,
			});
		}
	});
	push("message", {
		message: assistant(
			[
				{ type: "text", text: "最后跑一遍端到端测试：" },
				{
					type: "toolCall",
					id: ZOO_LIVE_CALL,
					name: "bash",
					arguments: ZOO_LIVE_ARGS,
				},
			],
			ts + 4000,
		),
	});
	return out;
}

/** 工具样例线程快照里的 `agent.live`：一个仍在运行、已有部分输出的 bash 调用。 */
export function toolZooLiveFrames(): Record<string, unknown>[] {
	return [
		{
			type: "tool_execution_start",
			toolCallId: ZOO_LIVE_CALL,
			toolName: "bash",
			args: ZOO_LIVE_ARGS,
			intent: ZOO_LIVE_ARGS.i,
		},
		{
			type: "tool_execution_update",
			toolCallId: ZOO_LIVE_CALL,
			toolName: "bash",
			partialResult: {
				content: [
					{
						type: "text",
						text: " RUN  v4.0.0 /home/user/workspace/repo/web\n\n ✓ e2e/reconnect.spec.ts > 断线后自动重连 1.2s\n ✓ e2e/reconnect.spec.ts > 重连后补拉缺失条目 0.9s\n ⠙ e2e/reconnect.spec.ts > 断线期间命令被拒绝",
					},
				],
			},
		},
	];
}

/** 工具样例线程首次订阅时按位置插入的帧（`afterSeq` 之后投递）：一条 warning 提示。 */
export function toolZooReplayFrames(
	entries: readonly Entry[],
): { afterSeq: number; frame: Record<string, unknown> }[] {
	const compaction = entries.find((e) => e.type === "compaction");
	return [
		{
			afterSeq: (compaction?.seq ?? 1) - 1,
			frame: {
				type: "notice",
				level: "warning",
				message: "上下文已用 71%，即将自动压缩。",
			},
		},
	];
}
