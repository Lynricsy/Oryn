/** mock 模式的固定数据：项目、provider、orb、线程与一份信息量丰富的转录。 */
import type { Entry, Orb, Project, Provider, Thread } from "../api/client";
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
		repoUrl: "https://github.com/Lynricsy/Oryn",
		baseBranch: "main",
		defaultProvider: "local",
		defaultOrbType: "medium",
		setupScript: "bun install --cwd web",
		shallowClone: true,
		createdAt: NOW - 30 * 24 * HOUR,
		updatedAt: NOW - 2 * HOUR,
	},
	{
		id: "prj_01JCZ00000000000000AGENT",
		name: "oryn-agent",
		repoUrl: "https://github.com/Lynricsy/oryn-agent",
		baseBranch: "oryn",
		shallowClone: false,
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

export const threads: Thread[] = [
	thread({
		id: MAIN_THREAD,
		title: "重构 WebSocket 重连与 afterSeq 补拉",
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
		title: "fork：共用 orb 试另一种退避策略",
		projectId: projects[0]?.id,
		orbId: "orb-01jcz8m3w4x5y6z7a8b9c0d1e2",
		parentThreadId: MAIN_THREAD,
		state: "working",
		lastMessageAt: NOW - MIN,
	}),
	thread({
		id: "T-0193a1b2-0000-7000-8000-00000000a003",
		title: "fork：连环境验证快照恢复",
		projectId: projects[0]?.id,
		parentThreadId: MAIN_THREAD,
		orbId: "orb-01jcz8m3w4x5y6z7a8b9c0d1e3",
	}),
	thread({
		id: "T-0193a1b2-0000-7000-8000-00000000a004",
		title: "Docker provider 冻结后 orbd 重连",
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
		color: "red",
		orbId: "orb-01jcz8m3w4x5y6z7a8b9c0d1e5",
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
		fastModeEnabled: false,
		fastModeActive: false,
		slowModeSupported: false,
		slowModeEnabled: false,
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
