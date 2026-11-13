/**
 * 浏览器内 mock 服务端：实现足够的 REST 与 WS（计划 C3）让界面无需后端即可体验。
 * 发送提示词时按 fakeomp 的帧序列回放：agent_start → 用户消息 → 流式 assistant（思考 / 文本 / 工具调用）→
 * bash 工具生命周期 → 最终回答 → agent_end → prompt_result → session_settled，期间按序复制条目。
 * 提示词含 `ask` 时发一个 ask 对话框并等待作答；含 `plan` 时发计划提案并等待审批。
 * 环境（M2.5）：orb 带 `lastSetup` / `lastResume`，提供 setup-runs 与日志接口；failed orb 的线程 `wake` 后恢复；
 * 提示词点名 `oryn-setup` 技能时，回合结束后补一条手动重跑成功的记录（模拟 agent 修好环境）。
 * 设置：Git 身份（GET / PUT / DELETE）与项目的新建 / 修改（含 git 身份覆盖，空串清除）。
 * 终端（M3，同 fakeomp 2.4）：`terminal_*` 命令的替身 shell（回显；`flood <n>` 洪泛、`count <n>` 逐行输出、`echo …`、`exit`），
 * `term.watch` 开关、`term.resized` 广播、按单连接 1s 窗口的输出量模拟背压（`term.lagged`）；提示词含 `pty` 时
 * bash 工具以 `pty:true` 在 `kind:"command"` 终端里等用户输入 `y`。orb 的 fs / git REST 由 `./orbfs` 应答。
 * `localStorage["oryn.debug"]==="1"` 时挂 `window.__orynMock`（验收脚本用：断线、重启 agent 等）。
 */
import type { Entry, Orb, Project, Schemas, Thread } from "../api/client";
import type { RpcTerminalInfo, SessionState } from "../omp/types";
import {
	assistant,
	buildMainTranscript,
	buildToolZooTranscript,
	projects as fixtureProjects,
	threads as fixtureThreads,
	MAIN_THREAD,
	mainState,
	makeOrb,
	NOW,
	orbEnv,
	providers,
	SESSION_FILE,
	setupLogs,
	TOOL_ZOO_THREAD,
	toolZooLiveFrames,
	toolZooReplayFrames,
} from "./fixtures";
import { handleOrbFetch } from "./orbfs";

type Json = Record<string, unknown>;

export interface MockSocket {
	deliver(msg: Json): void;
	close?(): void;
}

/** omp 快照返回的回滚上限（字符）。 */
const SNAPSHOT_CHARS = 262_144;
const FLOOD_FRAME = 32_768;
const PROMPT = "fake$ ";
/** CSI 序列与其它 ESC 双字节序列。 */
// biome-ignore lint/suspicious/noControlCharactersInRegex: 匹配终端转义序列必须用 ESC
const ESCAPE_SEQ = /\u001b\[[0-9;]*[A-Za-z~]|\u001b./g;

interface MockTerm {
	info: RpcTerminalInfo;
	data: string;
	seq: number;
	line: string;
	/** 正在执行的命令被 ^C 中断。 */
	aborted: boolean;
	/** `kind:"command"` 终端的输入回调（pty 命令等待作答）。 */
	onInput?: (data: string) => void;
}

function json(body: unknown, status = 200): Response {
	return new Response(body === undefined ? null : JSON.stringify(body), {
		status,
		headers: { "Content-Type": "application/json" },
	});
}

/** 项目 git 身份覆盖须两项同时给出（与服务端一致）；两项都为空串表示清除。 */
function badGitPair(name?: string, email?: string): Response | null {
	if (name === undefined && email === undefined) return null;
	const n = name?.trim() ?? "";
	const e = email?.trim() ?? "";
	if ((n === "" && e === "") || (n !== "" && e.includes("@"))) return null;
	return json(
		{
			code: "invalid_request",
			message: "gitAuthorName 与 gitAuthorEmail 须同时给出",
		},
		400,
	);
}

function sleep(ms: number) {
	return new Promise((r) => setTimeout(r, ms));
}

export class MockServer {
	readonly threads = new Map<string, Thread>();
	readonly entries = new Map<string, Entry[]>();
	readonly states = new Map<string, SessionState>();
	readonly orbs = new Map<string, Orb>();
	readonly pendingUi = new Map<string, Json[]>();
	/** setup / resume 运行记录（按 orb，新的在前）与日志。 */
	readonly setupRuns = new Map<string, Schemas["OrbSetupRun"][]>();
	readonly setupLogs = new Map<string, string>(Object.entries(setupLogs));
	readonly planProposals = new Map<string, Json>();
	/** 快照 `agent.live` 里的在途帧（按线程）。 */
	readonly liveFrames = new Map<string, Json[]>();
	/** 首次订阅（afterSeq=0）时按位置插在条目之间投递的帧（按线程）。 */
	readonly replayFrames = new Map<
		string,
		{ afterSeq: number; frame: Json }[]
	>();
	readonly projects: Project[] = fixtureProjects.map((p) => ({ ...p }));
	gitIdentity: Schemas["GitIdentity"] | undefined = {
		name: "Oryn Owner",
		email: "owner@oryn.example",
	};
	readonly queues = new Map<
		string,
		{ steering: string[]; followUp: string[] }
	>();
	private readonly sockets = new Set<MockSocket>();
	private readonly subs = new Map<MockSocket, Set<string>>();
	private readonly waiters = new Map<string, (value: Json) => void>();
	private rev = 1;
	private idSeq = 100;
	private readonly terms = new Map<string, Map<string, MockTerm>>();
	private readonly termWatch = new Map<MockSocket, Set<string>>();
	private readonly termWindow = new Map<
		MockSocket,
		{ start: number; chars: number }
	>();
	private termSeq = 0;
	/** 消息附件（控制面副本）：id → 元数据与内容。 */
	readonly attachments = new Map<
		string,
		{ meta: Schemas["Attachment"]; data: Blob }
	>();
	/** 单连接 1s 内可收的终端输出（字符）；超出即模拟服务端丢帧并发 `term.lagged`。 */
	termLag = 2 << 20;
	/** 帧间隔（毫秒）；测试里设为 0。 */
	delay = 45;
	files: Record<string, string> = {
		"config.yml": "theme: dark\ndefaultThinkingLevel: high\n",
		"models.yml": `providers:\n  anthropic:\n    apiKey: \${oryn:secret:omp/models/anthropic/apiKey}\n  openrouter:\n    baseUrl: https://openrouter.ai/api/v1\n    apiKey: \${oryn:secret:omp/models/openrouter/apiKey}\n`,
		"AGENTS.md": "# 约定\n\n- 中文注释\n- 先写测试\n",
		"skills/review/SKILL.md": "---\nname: review\n---\n审查改动。\n",
	};

	constructor() {
		for (const t of fixtureThreads) {
			this.threads.set(t.id, { ...t });
			this.entries.set(t.id, []);
			if (t.orbId && !this.orbs.has(t.orbId)) {
				const ids = fixtureThreads
					.filter((x) => x.orbId === t.orbId)
					.map((x) => x.id);
				this.orbs.set(t.orbId, {
					...makeOrb(t.orbId, ids),
					...orbEnv[t.orbId],
				});
			}
		}
		for (const orb of this.orbs.values())
			this.setupRuns.set(
				orb.id,
				[orb.lastResume, orb.lastSetup]
					.filter((r): r is Schemas["OrbSetupRun"] => Boolean(r))
					.sort((a, b) => b.finishedAt - a.finishedAt),
			);
		this.entries.set(MAIN_THREAD, buildMainTranscript());
		const zoo = buildToolZooTranscript();
		this.entries.set(TOOL_ZOO_THREAD, zoo);
		this.liveFrames.set(TOOL_ZOO_THREAD, toolZooLiveFrames());
		this.replayFrames.set(TOOL_ZOO_THREAD, toolZooReplayFrames(zoo));
		for (const t of fixtureThreads)
			this.states.set(t.id, {
				...mainState(),
				todoPhases: t.id === MAIN_THREAD ? mainState().todoPhases : [],
			});
		try {
			if (globalThis.localStorage?.getItem("oryn.debug") === "1")
				(globalThis as { __orynMock?: MockServer }).__orynMock = this;
		} catch {
			// 无 localStorage
		}
		this.pendingUi.set("T-0193a1b2-0000-7000-8000-00000000a004", [
			{
				type: "extension_ui_request",
				id: "ui-mock-1",
				method: "select",
				title: "允许执行 `docker exec orb-… pkill -HUP -x orbd`？",
				options: ["允许一次", "本会话总是允许", "拒绝"],
			},
		]);
	}

	// ------------------------------------------------------------ REST

	async handleFetch(req: Request): Promise<Response | null> {
		const url = new URL(req.url, location.origin);
		const path = url.pathname;
		if (path === "/healthz") return json({ ok: true, version: "0.1.0-mock" });
		if (!path.startsWith("/api/v1/")) return null;
		const orbRes = await handleOrbFetch(req);
		if (orbRes) return orbRes;
		const p = path.slice("/api/v1/".length);
		const m = req.method.toUpperCase();
		const body = async (): Promise<Json> => {
			try {
				return (await req.json()) as Json;
			} catch {
				return {};
			}
		};
		if (p === "me")
			return json({
				displayName: "owner",
				createdAt: NOW - 90 * 86_400_000,
				auth: { kind: "web" },
				passkeyCount: 1,
			});
		if (p === "ws-ticket")
			return json({ ticket: "mock-ticket", expiresIn: 30 });
		if (p === "projects" && m === "GET")
			return json({
				projects: this.projects.filter((x) => !x.archivedAt),
			});
		if (p === "projects" && m === "POST") {
			const b = (await body()) as Schemas["ProjectCreate"];
			const bad = badGitPair(b.gitAuthorName, b.gitAuthorEmail);
			if (bad) return bad;
			const created: Project = {
				// 新建时空串等同缺省
				...(Object.fromEntries(
					Object.entries(b).filter(([, v]) => v !== ""),
				) as Schemas["ProjectCreate"]),
				id: `prj_mock${this.idSeq++}`,
				shallowClone: Boolean(b.shallowClone),
				createdAt: Date.now(),
				updatedAt: Date.now(),
			};
			this.projects.push(created);
			return json(created, 201);
		}
		const projectMatch = /^projects\/([^/]+)$/.exec(p);
		if (projectMatch) {
			const i = this.projects.findIndex((x) => x.id === projectMatch[1]);
			const cur = this.projects[i];
			if (!cur) return json({ code: "not_found", message: "project" }, 404);
			if (m === "PATCH") {
				const { archived, ...b } = (await body()) as Schemas["ProjectPatch"];
				const bad = badGitPair(b.gitAuthorName, b.gitAuthorEmail);
				if (bad) return bad;
				const next: Project = { ...cur, updatedAt: Date.now() };
				for (const [k, v] of Object.entries(b)) {
					const key = k as keyof Project;
					if (v === "") delete next[key];
					else Object.assign(next, { [key]: v });
				}
				if (archived !== undefined)
					next.archivedAt = archived ? Date.now() : undefined;
				this.projects[i] = next;
				return json(next);
			}
			if (m === "DELETE") {
				this.projects.splice(i, 1);
				return new Response(null, { status: 204 });
			}
			return json(cur);
		}
		if (p === "settings/git-identity") {
			if (m === "PUT") {
				const b = (await body()) as Schemas["GitIdentity"];
				const name = String(b.name ?? "").trim();
				const email = String(b.email ?? "").trim();
				if (!name || !email.includes("@"))
					return json(
						{ code: "invalid_request", message: "git 名字与邮箱无效" },
						400,
					);
				this.gitIdentity = { name, email };
			} else if (m === "DELETE") {
				this.gitIdentity = undefined;
				return new Response(null, { status: 204 });
			}
			return json(this.gitIdentity ? { identity: this.gitIdentity } : {});
		}
		if (p === "providers" && m === "GET") return json({ providers });
		if (p === "providers" && m === "POST") {
			const b = await body();
			return json(
				{
					provider: {
						...providers[0],
						id: `prov_${this.idSeq++}`,
						name: String(b.name),
					},
					token: "opv_mock_ONLY_SHOWN_ONCE",
				},
				201,
			);
		}
		if (p === "devices" && m === "GET") {
			return json({
				devices: [
					{
						id: "dev_1",
						name: "laptop-cli",
						kind: "cli",
						platform: "linux-x64",
						createdAt: NOW - 86_400_000,
						lastSeenAt: NOW - 600_000,
						current: false,
					},
					{
						id: "dev_2",
						name: "iPad",
						kind: "mobile",
						platform: "ios",
						createdAt: NOW - 7 * 86_400_000,
						lastSeenAt: NOW - 3 * 3_600_000,
					},
				],
			});
		}
		const pairMatch = /^devices\/pair\/([A-Z0-9-]+)$/.exec(p);
		if (pairMatch && m === "GET") {
			if (pairMatch[1] !== "ABCD-EFGH")
				return json({ code: "not_found", message: "unknown pairing" }, 404);
			return json({
				code: "ABCD-EFGH",
				name: "laptop-cli",
				kind: "cli",
				platform: "linux-x64",
				ip: "203.0.113.7",
				userAgent: "oryn/0.1.0 (linux; amd64)",
				createdAt: Date.now() - 30_000,
				expiresAt: Date.now() + 570_000,
			});
		}
		if (p === "devices/pair/approve")
			return new Response(null, { status: 204 });
		if (p === "secrets" && m === "GET") {
			return json({
				secrets: [
					{
						name: "omp/models/anthropic/apiKey",
						mode: "proxy",
						createdAt: NOW,
						updatedAt: NOW - 3_600_000,
					},
					{
						name: "git/github.com/token",
						mode: "proxy",
						hosts: ["github.com"],
						createdAt: NOW,
						updatedAt: NOW - 86_400_000,
					},
					{
						name: "NPM_TOKEN",
						mode: "env",
						createdAt: NOW,
						updatedAt: NOW - 2 * 86_400_000,
					},
				],
			});
		}
		if (p.startsWith("secrets/")) return new Response(null, { status: 204 });
		if (p === "agent-config/files") {
			return json({
				files: Object.entries(this.files).map(([path, content]) => ({
					path,
					size: content.length,
					mode: 420,
					sha256: "0".repeat(64),
					updatedAt: NOW - 3_600_000,
				})),
			});
		}
		if (p.startsWith("agent-config/files/")) {
			const fp = decodeURIComponent(p.slice("agent-config/files/".length));
			if (m === "GET")
				return new Response(this.files[fp] ?? "", {
					status: this.files[fp] === undefined ? 404 : 200,
				});
			if (m === "PUT") {
				this.files[fp] = await req.text();
				return json({
					file: {
						path: fp,
						size: this.files[fp]?.length ?? 0,
						mode: 420,
						sha256: "0".repeat(64),
						updatedAt: Date.now(),
					},
					extractedSecrets: [],
					warnings: [],
				});
			}
			if (m === "DELETE") {
				delete this.files[fp];
				return new Response(null, { status: 204 });
			}
		}
		if (p === "orbs" && m === "GET")
			return json({ orbs: [...this.orbs.values()] });
		const metricsMatch = /^orbs\/([^/]+)\/metrics$/.exec(p);
		if (metricsMatch) {
			const samples = Array.from({ length: 60 }, (_, i) => {
				const ts = Date.now() - (59 - i) * 30_000;
				return {
					ts,
					cpuPct: 18 + 30 * Math.abs(Math.sin(i / 6)) + (i % 7) * 2,
					memUsedMB: 2100 + 400 * Math.sin(i / 11) + i * 6,
					memLimitMB: 8192,
					diskUsedMB: 5400,
					netRxBytes: i * 120_000,
					netTxBytes: i * 40_000,
				};
			});
			return json({ orbId: metricsMatch[1], step: 30, samples });
		}
		const runsMatch = /^orbs\/([^/]+)\/setup-runs(?:\/([^/]+)\/log)?$/.exec(p);
		if (runsMatch && m === "GET") {
			const runs = this.setupRuns.get(runsMatch[1] ?? "") ?? [];
			if (!runsMatch[2]) return json({ runs: runs.slice(0, 20) });
			const log = runs.some((r) => r.id === runsMatch[2])
				? this.setupLogs.get(runsMatch[2])
				: undefined;
			if (log === undefined)
				return json({ code: "not_found", message: "setup run" }, 404);
			return new Response(log, {
				headers: { "Content-Type": "text/plain; charset=utf-8" },
			});
		}
		const orbAction = /^orbs\/([^/]+)\/(pause|resume)$/.exec(p);
		if (orbAction) {
			const orb = this.orbs.get(orbAction[1] ?? "");
			if (!orb) return json({ code: "not_found", message: "orb" }, 404);
			const next = {
				...orb,
				state:
					orbAction[2] === "pause" ? ("frozen" as const) : ("ready" as const),
				stateChangedAt: Date.now(),
			};
			this.orbs.set(orb.id, next);
			this.broadcast({ type: "orb", orb: next });
			return json(next, 202);
		}
		if (p === "stats/usage") {
			const groups = [...this.threads.values()].map((t) => ({
				key: t.id,
				label: t.title,
				requests: 12,
				inputTokens: 120_000,
				outputTokens: 9_000,
				cacheReadTokens: 300_000,
				cacheWriteTokens: 0,
				costUsd: t.id === MAIN_THREAD ? 0.2381 : 0.05,
				subagentCostUsd: 0,
				tokensPerSecond: 64,
				toolCalls: 3,
				toolErrors: 0,
			}));
			return json({
				groupBy: url.searchParams.get("groupBy") ?? "thread",
				from: 0,
				to: Date.now(),
				groups,
			});
		}
		if (p === "threads" && m === "GET") {
			const archived = url.searchParams.get("archived") === "true";
			return json({
				threads: [...this.threads.values()].filter(
					(t) => Boolean(t.archivedAt) === archived,
				),
			});
		}
		const attRes = await this.handleAttachmentFetch(req, p, m, url);
		if (attRes) return attRes;
		if (p === "threads" && m === "POST") {
			const b = await body();
			const ids = Array.isArray(b.attachments)
				? (b.attachments as string[])
				: [];
			if (ids.some((a) => this.attachments.get(a)?.meta.threadId))
				return json(
					{ code: "attachment_not_found", message: "attachment" },
					404,
				);
			return json({ thread: this.createThread(b) }, 201);
		}
		const threadMatch = /^threads\/([^/]+)(?:\/(fork|wake))?$/.exec(p);
		if (threadMatch) {
			const id = threadMatch[1] ?? "";
			const t = this.threads.get(id);
			if (!t) return json({ code: "not_found", message: "thread" }, 404);
			if (threadMatch[2] === "fork") {
				const b = await body();
				const copy = this.createThread({
					prompt: "",
					title: `fork：${t.title}`,
					parent: id,
					env: b.env,
				});
				return json({ thread: copy }, 201);
			}
			if (threadMatch[2] === "wake") {
				const orb = t.orbId ? this.orbs.get(t.orbId) : undefined;
				if (orb?.state === "failed") void this.recoverOrb(orb);
				else if (orb?.state === "frozen" || orb?.state === "stopped")
					void this.wakeOrb(orb);
				return new Response(null, { status: 202 });
			}
			if (m === "PATCH") {
				const b = await body();
				const next: Thread = { ...t, updatedAt: Date.now() };
				if (typeof b.title === "string")
					Object.assign(next, { title: b.title, titleSource: "user" });
				if (typeof b.pinned === "boolean") next.pinned = b.pinned;
				if (typeof b.color === "string") next.color = b.color || undefined;
				if (Array.isArray(b.labels)) next.labels = b.labels as string[];
				if (typeof b.archived === "boolean")
					next.archivedAt = b.archived ? Date.now() : undefined;
				if (typeof b.snoozedUntil === "number")
					next.snoozedUntil = b.snoozedUntil || undefined;
				this.upsertThread(next);
				return json(next);
			}
			if (m === "DELETE") {
				this.threads.delete(id);
				for (const [aid, a] of this.attachments)
					if (a.meta.threadId === id) this.attachments.delete(aid);
				this.broadcast({
					type: "index.patch",
					upsert: [],
					remove: [id],
					rev: ++this.rev,
				});
				return new Response(null, { status: 204 });
			}
			return json(t);
		}
		return json({ code: "not_found", message: `mock: ${m} ${p}` }, 404);
	}

	private setOrb(orb: Orb) {
		this.orbs.set(orb.id, orb);
		this.broadcast({ type: "orb", orb });
	}

	/** failed orb 重试：waking → connecting → resuming → ready，其线程回到 idle。 */
	private async recoverOrb(orb: Orb) {
		const { failureReason: _, ...rest } = orb;
		for (const state of [
			"waking",
			"connecting",
			"resuming",
			"ready",
		] as const) {
			await sleep(this.delay * 8);
			this.setOrb({ ...rest, state, stateChangedAt: Date.now() });
			if (state === "waking")
				for (const tid of orb.threadIds) {
					const t = this.threads.get(tid);
					if (!t) continue;
					const { error: __, ...clean } = t;
					this.upsertThread({ ...clean, state: "provisioning" });
				}
		}
		for (const tid of orb.threadIds) {
			const t = this.threads.get(tid);
			if (t) this.upsertThread({ ...t, state: "idle" });
		}
	}

	/** 冻结 / 停机的 orb 唤醒：waking → connecting → resuming → ready（终端随 omp 进程保留）。 */
	private async wakeOrb(orb: Orb) {
		for (const state of [
			"waking",
			"connecting",
			"resuming",
			"ready",
		] as const) {
			await sleep(this.delay * 6);
			const cur = this.orbs.get(orb.id) ?? orb;
			this.setOrb({ ...cur, state, stateChangedAt: Date.now() });
		}
	}

	/** 记一条手动重跑成功的 setup 运行并推送 orb 更新。 */
	private recordManualSetup(orbId: string) {
		const orb = this.orbs.get(orbId);
		if (!orb) return;
		const now = Date.now();
		const run: Schemas["OrbSetupRun"] = {
			id: `osr_mock${(this.idSeq++).toString(16)}`,
			orbId,
			phase: "setup",
			trigger: "manual",
			startedAt: now - 38_000,
			finishedAt: now,
			ok: true,
			setupRan: true,
			durationMs: 38_000,
			hasRepoScript: true,
			hasProjectScript: orb.lastSetup?.hasProjectScript ?? false,
		};
		this.setupRuns.set(orbId, [run, ...(this.setupRuns.get(orbId) ?? [])]);
		this.setupLogs.set(run.id, "[setup] + oryn orb setup run\n[setup] ok\n");
		this.setOrb({ ...orb, lastSetup: run, stateChangedAt: now });
	}

	private upsertThread(t: Thread) {
		this.threads.set(t.id, t);
		this.broadcast({
			type: "index.patch",
			upsert: [t],
			remove: [],
			rev: ++this.rev,
		});
	}

	// ------------------------------------------------------------ 附件

	private async handleAttachmentFetch(
		req: Request,
		p: string,
		m: string,
		url: URL,
	): Promise<Response | null> {
		if (p === "attachments" && m === "POST") {
			const data = await req.blob();
			if (data.size > 100 * 1024 * 1024)
				return json({ code: "too_large", message: "attachment" }, 413);
			const name = (url.searchParams.get("name") ?? "file")
				.split("/")
				.pop() as string;
			const threadId = url.searchParams.get("threadId") ?? undefined;
			const ext = name.slice(name.lastIndexOf(".") + 1).toLowerCase();
			const mimeByExt: Record<string, string> = {
				png: "image/png",
				jpg: "image/jpeg",
				jpeg: "image/jpeg",
				gif: "image/gif",
				webp: "image/webp",
				pdf: "application/pdf",
				txt: "text/plain; charset=utf-8",
				md: "text/markdown; charset=utf-8",
			};
			const meta: Schemas["Attachment"] = {
				id: `att_${(this.idSeq++).toString(36).toUpperCase().padStart(26, "0")}`,
				name,
				mime: mimeByExt[ext] ?? "application/octet-stream",
				size: data.size,
				sha256: "0".repeat(64),
				createdAt: Date.now(),
				...(threadId ? { threadId } : {}),
			};
			this.attachments.set(meta.id, { meta, data });
			return json(meta, 201);
		}
		const listMatch = /^threads\/([^/]+)\/attachments$/.exec(p);
		if (listMatch && m === "GET") {
			if (!this.threads.has(listMatch[1] ?? ""))
				return json({ code: "not_found", message: "thread" }, 404);
			return json({
				attachments: [...this.attachments.values()]
					.map((a) => a.meta)
					.filter((a) => a.threadId === listMatch[1]),
			});
		}
		const attMatch = /^attachments\/([^/]+)(\/content)?$/.exec(p);
		if (!attMatch) return null;
		const a = this.attachments.get(attMatch[1] ?? "");
		if (!a) return json({ code: "not_found", message: "attachment" }, 404);
		if (attMatch[2] && m === "GET")
			return new Response(a.data, {
				headers: { "Content-Type": "application/octet-stream" },
			});
		if (!attMatch[2] && m === "DELETE") {
			this.attachments.delete(a.meta.id);
			return new Response(null, { status: 204 });
		}
		return null;
	}

	/**
	 * 模拟控制面投递：绑定到线程并在消息末尾追加 `<oryn-attachments>` 块（路径形如 orb 内的投递位置）。
	 * 有附件属于别的线程时返回 null（投递失败）。
	 */
	private withAttachments(
		threadId: string,
		message: string,
		ids: unknown,
	): string | null {
		if (!Array.isArray(ids) || ids.length === 0) return message;
		const lines: string[] = [];
		for (const id of ids) {
			const a = this.attachments.get(String(id));
			if (!a || (a.meta.threadId && a.meta.threadId !== threadId)) return null;
			a.meta = { ...a.meta, threadId };
			lines.push(
				`- /home/user/.oryn/attachments/${threadId}/${a.meta.id}/${a.meta.name} (${a.meta.mime}, ${a.meta.size} bytes)`,
			);
		}
		const block = `<oryn-attachments>\nThe user attached these files. They are stored outside the repository; read them when relevant and do not copy them into the repository unless asked.\n${lines.join("\n")}\n</oryn-attachments>`;
		return message ? `${message}\n\n${block}` : block;
	}

	private createThread(b: Json): Thread {
		const id = `T-mock-${(this.idSeq++).toString(16)}`;
		const orbId = `orb-mock${this.idSeq.toString().padStart(22, "0")}`;
		const prompt =
			this.withAttachments(
				id,
				typeof b.prompt === "string" ? b.prompt : "",
				b.attachments,
			) ?? "";
		const t: Thread = {
			id,
			title:
				typeof b.title === "string" && b.title
					? b.title
					: prompt.slice(0, 40) || "新线程",
			titleSource: "auto",
			state: "provisioning",
			stateChangedAt: Date.now(),
			pinned: false,
			labels: [],
			unread: false,
			createdAt: Date.now(),
			updatedAt: Date.now(),
			orbId,
			projectId: typeof b.projectId === "string" ? b.projectId : undefined,
			parentThreadId: typeof b.parent === "string" ? b.parent : undefined,
		};
		this.entries.set(id, []);
		this.states.set(id, {
			...mainState(),
			todoPhases: [],
			contextUsage: { tokens: 0, contextWindow: 200_000, percent: 0 },
		});
		const orb = makeOrb(orbId, [id], "spawning");
		this.orbs.set(orbId, orb);
		this.upsertThread(t);
		void (async () => {
			for (const state of ["connecting", "setting_up", "ready"] as const) {
				await sleep(this.delay * 8);
				const next = { ...orb, state, stateChangedAt: Date.now() };
				this.orbs.set(orbId, next);
				this.broadcast({ type: "orb", orb: next });
			}
			this.upsertThread({ ...t, state: "idle" });
			if (prompt) await this.runPrompt(id, prompt);
		})();
		return t;
	}

	// ------------------------------------------------------------ WS

	connect(socket: MockSocket) {
		this.sockets.add(socket);
		this.subs.set(socket, new Set());
		socket.deliver({
			type: "index",
			threads: [...this.threads.values()].filter((t) => !t.archivedAt),
			rev: this.rev,
		});
	}

	disconnect(socket: MockSocket) {
		this.sockets.delete(socket);
		this.subs.delete(socket);
		this.termWatch.delete(socket);
		this.termWindow.delete(socket);
	}

	/** 调试：断开全部连接（客户端会重连并补拉）。 */
	dropConnections() {
		for (const s of [...this.sockets]) s.close?.();
	}

	/** 调试：模拟 omp 进程重启——其终端全部销毁，线程收到 `live.reset`。 */
	restartAgent(threadId: string) {
		this.terms.delete(threadId);
		this.broadcast({ type: "live.reset", threadId }, threadId);
	}

	/** 调试：某线程的终端列表。 */
	terminalInfos(threadId: string): RpcTerminalInfo[] {
		return [...(this.terms.get(threadId)?.values() ?? [])].map((t) => t.info);
	}

	// ------------------------------------------------------------ 终端替身

	private termsOf(threadId: string): Map<string, MockTerm> {
		let m = this.terms.get(threadId);
		if (!m) {
			m = new Map();
			this.terms.set(threadId, m);
		}
		return m;
	}

	private openTerm(
		threadId: string,
		kind: RpcTerminalInfo["kind"],
		title: string,
		size: { cols?: unknown; rows?: unknown },
		reveal?: boolean,
	): MockTerm {
		const terminalId = `term-fake-${++this.termSeq}`;
		const term: MockTerm = {
			info: {
				terminalId,
				kind,
				title,
				cwd: "/home/user/workspace",
				running: true,
				pid: 4000 + this.termSeq,
				cols: typeof size.cols === "number" ? size.cols : 100,
				rows: typeof size.rows === "number" ? size.rows : 30,
			},
			data: "",
			seq: 0,
			line: "",
			aborted: false,
		};
		this.termsOf(threadId).set(terminalId, term);
		this.frame(threadId, {
			type: "terminal_opened",
			terminal: term.info,
			...(reveal ? { reveal: true } : {}),
		});
		return term;
	}

	private termOut(threadId: string, term: MockTerm, data: string) {
		if (!this.terms.get(threadId)?.has(term.info.terminalId)) return;
		term.seq += data.length;
		term.data = (term.data + data).slice(-SNAPSHOT_CHARS);
		const frame = {
			type: "terminal_output",
			terminalId: term.info.terminalId,
			data,
			seq: term.seq,
		};
		const now = Date.now();
		for (const s of this.sockets) {
			if (!this.subs.get(s)?.has(threadId)) continue;
			const watching = this.termWatch.get(s);
			if (!watching?.has(threadId)) continue;
			let w = this.termWindow.get(s);
			if (!w || now - w.start > 1000) {
				w = { start: now, chars: 0 };
				this.termWindow.set(s, w);
			}
			w.chars += data.length;
			if (w.chars > this.termLag) {
				watching.delete(threadId);
				s.deliver({ type: "term.lagged", threadId });
				continue;
			}
			s.deliver({ type: "frame", threadId, frame });
		}
	}

	private termExit(threadId: string, term: MockTerm, exitCode: number) {
		term.info = { ...term.info, running: false, exitCode };
		this.frame(threadId, {
			type: "terminal_exit",
			terminalId: term.info.terminalId,
			exitCode,
			cancelled: false,
			timedOut: false,
		});
	}

	private termInput(threadId: string, term: MockTerm, data: string) {
		if (!term.info.running) return;
		if (term.onInput) {
			term.onInput(data);
			return;
		}
		// 方向键等转义序列不回显
		const text = data.replace(ESCAPE_SEQ, "");
		for (const ch of text) {
			if (ch === "\r") {
				const line = term.line;
				term.line = "";
				this.termOut(threadId, term, "\r\n");
				void this.runLine(threadId, term, line.trim());
			} else if (ch === "\x7f") {
				if (term.line) {
					term.line = term.line.slice(0, -1);
					this.termOut(threadId, term, "\b \b");
				}
			} else if (ch === "\x03") {
				term.line = "";
				term.aborted = true;
				this.termOut(threadId, term, `^C\r\n${PROMPT}`);
			} else if (ch >= " ") {
				term.line += ch;
				this.termOut(threadId, term, ch);
			}
		}
	}

	private async runLine(threadId: string, term: MockTerm, line: string) {
		term.aborted = false;
		const flood = /^flood (\d+)$/.exec(line);
		const count = /^count (\d+)$/.exec(line);
		if (line === "exit") {
			this.termExit(threadId, term, 0);
			return;
		}
		if (flood) {
			let left = Number(flood[1]);
			let frames = 0;
			while (left > 0 && !term.aborted) {
				const n = Math.min(FLOOD_FRAME, left);
				this.termOut(threadId, term, "x".repeat(n));
				left -= n;
				if (++frames % 8 === 0) await sleep(0);
			}
			this.termOut(threadId, term, `\r\n${PROMPT}`);
			return;
		}
		if (count) {
			const n = Number(count[1]);
			for (let i = 1; i <= n && !term.aborted; i++) {
				this.termOut(threadId, term, `line-${i}\r\n`);
				await sleep(20);
			}
			if (!term.aborted) this.termOut(threadId, term, PROMPT);
			return;
		}
		const echo = /^echo (.*)$/.exec(line);
		const out = echo
			? `${(echo[1] ?? "")
					.replace(/^(['"])(.*)\1$/, "$2")
					.replace(/\$TERM\b/g, "xterm-256color")
					.replace(/\$\(\((\d+)\*(\d+)\)\)/g, (_, a, b) =>
						String(Number(a) * Number(b)),
					)}\r\n`
			: "";
		this.termOut(threadId, term, `${out}${PROMPT}`);
	}

	/** 终端命令的替身；不是终端命令时返回 false。 */
	private termCmd(
		socket: MockSocket,
		threadId: string,
		id: string,
		type: string,
		frame: Json,
	): boolean {
		const terms = this.termsOf(threadId);
		const term =
			typeof frame.terminalId === "string"
				? terms.get(frame.terminalId)
				: undefined;
		const fail = () =>
			this.respond(
				socket,
				threadId,
				id,
				type,
				undefined,
				`unknown terminal ${String(frame.terminalId)}`,
			);
		switch (type) {
			case "terminal_configure":
				this.respond(socket, threadId, id, type, {
					interactiveCommands: frame.interactiveCommands === true,
					agentTools: frame.agentTools === true,
				});
				return true;
			case "terminal_open": {
				const t = this.openTerm(threadId, "shell", "bash", frame);
				this.respond(socket, threadId, id, type, t.info);
				this.termOut(threadId, t, PROMPT);
				return true;
			}
			case "terminal_input":
				if (!term) fail();
				else {
					this.respond(socket, threadId, id, type);
					this.termInput(threadId, term, String(frame.data ?? ""));
				}
				return true;
			case "terminal_resize":
				if (!term) {
					fail();
					return true;
				}
				term.info = {
					...term.info,
					cols: Number(frame.cols),
					rows: Number(frame.rows),
				};
				this.respond(socket, threadId, id, type);
				this.broadcast(
					{
						type: "term.resized",
						threadId,
						terminalId: term.info.terminalId,
						cols: term.info.cols,
						rows: term.info.rows,
					},
					threadId,
				);
				return true;
			case "terminal_close":
				if (!term) {
					fail();
					return true;
				}
				terms.delete(term.info.terminalId);
				term.aborted = true;
				this.frame(threadId, {
					type: "terminal_closed",
					terminalId: term.info.terminalId,
				});
				this.respond(socket, threadId, id, type);
				return true;
			case "terminal_list":
				this.respond(socket, threadId, id, type, {
					terminals: [...terms.values()].map((t) => t.info),
				});
				return true;
			case "terminal_snapshot":
				if (!term) fail();
				else
					this.respond(socket, threadId, id, type, {
						terminal: term.info,
						data: term.data,
						seq: term.seq,
					});
				return true;
			default:
				return false;
		}
	}

	private broadcast(msg: Json, threadId?: string) {
		for (const s of this.sockets) {
			if (threadId && !this.subs.get(s)?.has(threadId)) continue;
			s.deliver(msg);
		}
	}

	private frame(threadId: string, frame: Json) {
		this.broadcast({ type: "frame", threadId, frame }, threadId);
	}

	private appendEntry(threadId: string, message: Json) {
		const list = this.entries.get(threadId) ?? [];
		const last = [...list].reverse().find((e) => !e.entryId.startsWith("__"));
		const id = (this.idSeq++).toString(16).padStart(8, "0");
		const ts =
			typeof message.timestamp === "number" ? message.timestamp : Date.now();
		const e: Entry = {
			seq: (list[list.length - 1]?.seq ?? 0) + 1,
			file: SESSION_FILE,
			entryId: id,
			parentId: last?.entryId,
			type: "message",
			ts,
			payload: {
				type: "message",
				id,
				parentId: last?.entryId ?? null,
				timestamp: new Date(ts).toISOString(),
				message,
			},
		};
		list.push(e);
		this.entries.set(threadId, list);
		this.broadcast(
			{
				type: "entries",
				threadId,
				entries: [e],
				leafEntryId: id,
				lastSeq: e.seq,
			},
			threadId,
		);
	}

	onClient(socket: MockSocket, msg: Json) {
		const threadId = typeof msg.threadId === "string" ? msg.threadId : "";
		switch (msg.type) {
			case "ping":
				socket.deliver({ type: "pong" });
				return;
			case "focus":
				return;
			case "sub": {
				this.subs.get(socket)?.add(threadId);
				const t = this.threads.get(threadId);
				if (!t) {
					socket.deliver({
						type: "error",
						code: "not_found",
						message: "thread",
						threadId,
					});
					return;
				}
				const list = this.entries.get(threadId) ?? [];
				const after = typeof msg.afterSeq === "number" ? msg.afterSeq : 0;
				const orb = t.orbId ? this.orbs.get(t.orbId) : undefined;
				const last = list[list.length - 1];
				socket.deliver({
					type: "thread.snapshot",
					threadId,
					thread: t,
					orb,
					agent: {
						status: orb?.state === "ready" ? "ready" : "starting",
						state: this.states.get(threadId),
						pendingUi: this.pendingUi.get(threadId) ?? [],
						planProposal: this.planProposals.get(threadId),
						subagents: [],
						live: this.liveFrames.get(threadId) ?? [],
					},
					leafEntryId: last?.entryId,
					lastSeq: last?.seq ?? 0,
					transcriptRev: 0,
					fullReplay: false,
				});
				const rest = list.filter((e) => e.seq > after);
				// 重放帧只在首次订阅时投递，续传（afterSeq>0）不重复
				const frames =
					after === 0 ? (this.replayFrames.get(threadId) ?? []) : [];
				let fi = 0;
				let batch: Entry[] = [];
				const flush = () => {
					if (batch.length === 0) return;
					socket.deliver({
						type: "entries",
						threadId,
						entries: batch,
						leafEntryId: last?.entryId,
						// 本批覆盖到的最大 seq（可续传游标）
						lastSeq: batch[batch.length - 1]?.seq ?? 0,
					});
					batch = [];
				};
				const replayUpTo = (seq: number) => {
					for (
						;
						fi < frames.length && (frames[fi]?.afterSeq ?? 0) < seq;
						fi++
					) {
						flush();
						socket.deliver({
							type: "frame",
							threadId,
							frame: frames[fi]?.frame,
						});
					}
				};
				for (const e of rest) {
					replayUpTo(e.seq);
					batch.push(e);
					if (batch.length === 200) flush();
				}
				flush();
				replayUpTo(Number.POSITIVE_INFINITY);
				return;
			}
			case "unsub":
				this.subs.get(socket)?.delete(threadId);
				this.termWatch.get(socket)?.delete(threadId);
				return;
			case "ui.response": {
				const frame = msg.frame as Json;
				if (frame?.type !== "extension_ui_response") {
					socket.deliver({
						type: "error",
						code: "invalid_frame",
						message: "ui.response only accepts extension_ui_response",
						threadId,
					});
					return;
				}
				const reqId = String(frame.id ?? "");
				const list = this.pendingUi.get(threadId) ?? [];
				if (!list.some((r) => r.id === reqId)) return; // 已被别人先答
				this.pendingUi.set(
					threadId,
					list.filter((r) => r.id !== reqId),
				);
				this.broadcast(
					{ type: "ui.resolved", threadId, requestId: reqId },
					threadId,
				);
				this.waiters.get(reqId)?.(frame);
				this.waiters.delete(reqId);
				this.refreshThreadState(threadId);
				return;
			}
			case "term.watch": {
				if (!this.subs.get(socket)?.has(threadId)) {
					socket.deliver({
						type: "error",
						code: "not_subscribed",
						message: "term.watch before sub",
						threadId,
					});
					return;
				}
				let w = this.termWatch.get(socket);
				if (!w) {
					w = new Set();
					this.termWatch.set(socket, w);
				}
				if (msg.on === true) {
					w.add(threadId);
					this.termWindow.delete(socket);
				} else w.delete(threadId);
				return;
			}
			case "cmd":
				void this.handleCmd(
					socket,
					threadId,
					String(msg.id),
					msg.frame as Json,
				);
				return;
		}
	}

	private refreshThreadState(threadId: string) {
		const t = this.threads.get(threadId);
		if (!t) return;
		const waiting =
			(this.pendingUi.get(threadId)?.length ?? 0) > 0 ||
			this.planProposals.has(threadId);
		if (waiting && t.state !== "awaiting_input")
			this.upsertThread({ ...t, state: "awaiting_input" });
		if (!waiting && t.state === "awaiting_input")
			this.upsertThread({ ...t, state: "working" });
	}

	private respond(
		socket: MockSocket,
		threadId: string,
		id: string,
		command: string,
		data?: unknown,
		error?: string,
	) {
		socket.deliver({
			type: "res",
			threadId,
			id,
			response: error
				? { type: "response", command, success: false, error }
				: { type: "response", command, success: true, data },
		});
	}

	private async handleCmd(
		socket: MockSocket,
		threadId: string,
		id: string,
		frame: Json,
	) {
		const type = String(frame.type);
		if (this.termCmd(socket, threadId, id, type, frame)) return;
		const state = this.states.get(threadId) ?? mainState();
		const list = this.entries.get(threadId) ?? [];
		switch (type) {
			case "get_state":
				return this.respond(socket, threadId, id, type, state);
			case "get_available_commands":
				return this.respond(socket, threadId, id, type, {
					commands: [
						{
							name: "compact",
							description: "Manually compact the session context",
							source: "builtin",
							allowArgs: true,
							subcommands: [
								{ name: "snap", description: "Snapshot compaction" },
								{ name: "remote", description: "Remote compaction" },
							],
						},
						{
							name: "review",
							description: "Review the current changes",
							source: "skill",
							allowArgs: true,
							hasDynamicArgCompletion: true,
						},
						{
							name: "settings",
							description: "Open settings",
							source: "builtin",
						},
					],
				});
			case "get_command_arg_completions":
				return this.respond(socket, threadId, id, type, {
					items: ["HEAD~1", "main", "origin/main"]
						.filter((v) => v.startsWith(String(frame.prefix ?? "")))
						.map((value) => ({ value, description: "git ref" })),
				});
			case "get_queue": {
				const q = this.queues.get(threadId) ?? { steering: [], followUp: [] };
				const items = (lane: string[], p: string) =>
					lane.map((text, i) => ({
						id: `${p}${i}`,
						text,
						editable: true,
						timestamp: Date.now(),
					}));
				return this.respond(socket, threadId, id, type, {
					steering: items(q.steering, "s"),
					followUp: items(q.followUp, "f"),
				});
			}
			case "steer":
			case "follow_up": {
				const q = this.queues.get(threadId) ?? { steering: [], followUp: [] };
				(type === "steer" ? q.steering : q.followUp).push(
					String(frame.message ?? ""),
				);
				this.queues.set(threadId, q);
				this.frame(threadId, {
					type: "queue_update",
					steering: q.steering,
					followUp: q.followUp,
					liveSteered: 0,
				});
				return this.respond(socket, threadId, id, type);
			}
			case "queue_clear":
				this.queues.set(threadId, { steering: [], followUp: [] });
				this.frame(threadId, {
					type: "queue_update",
					steering: [],
					followUp: [],
					liveSteered: 0,
				});
				return this.respond(socket, threadId, id, type, { cleared: 0 });
			case "get_vibe_mode":
				return this.respond(socket, threadId, id, type, { enabled: false });
			case "get_loop_mode":
				return this.respond(socket, threadId, id, type, {
					enabled: false,
					state: "off",
				});
			case "get_subagents":
				return this.respond(socket, threadId, id, type, { subagents: [] });
			case "get_goal":
				return this.respond(socket, threadId, id, type, { enabled: false });
			case "get_btw_history":
				return this.respond(socket, threadId, id, type, { records: [] });
			case "get_available_models":
				return this.respond(socket, threadId, id, type, {
					models: [
						{
							id: "claude-sonnet-5",
							name: "Claude Sonnet 5",
							provider: "anthropic",
						},
						{
							id: "claude-opus-5",
							name: "Claude Opus 5",
							provider: "anthropic",
						},
						{ id: "gpt-5.5", name: "GPT-5.5", provider: "openai" },
						{ id: "deepseek-v4", name: "DeepSeek V4", provider: "deepseek" },
					],
					discoveryStates: [],
					refreshPending: false,
					generation: 1,
				});
			case "get_session_tree": {
				const tree = list
					.filter((e) => e.type === "message" && !e.entryId.startsWith("__"))
					.map((e) => {
						const m = (e.payload as Json).message as Json;
						const content = m.content;
						const text =
							typeof content === "string"
								? content
								: Array.isArray(content)
									? content
											.map((b) =>
												b && typeof b === "object" && "text" in b
													? String(b.text)
													: "",
											)
											.join(" ")
									: "";
						return {
							entryId: e.entryId,
							parentId: e.parentId ?? null,
							role:
								m.role === "user"
									? "user"
									: m.role === "assistant"
										? "assistant"
										: "system",
							textPreview: text.slice(0, 80),
							timestamp: e.ts ?? 0,
							onActiveBranch: true,
							isLeaf: e === list[list.length - 1],
						};
					});
				return this.respond(socket, threadId, id, type, {
					tree,
					activeLeafId: list[list.length - 1]?.entryId ?? null,
				});
			}
			case "get_context_report":
				return this.respond(socket, threadId, id, type, {
					model: "claude-sonnet-5",
					contextWindow: 200_000,
					breakdown: {
						contextWindow: 200_000,
						anchored: true,
						usedTokens: 48_200,
						systemPromptTokens: 6_100,
						systemToolsTokens: 9_800,
						systemContextTokens: 2_300,
						instructionFilesTokens: 1_400,
						skillsTokens: 900,
						messagesTokens: 27_700,
					},
				});
			case "get_session_stats":
				return this.respond(socket, threadId, id, type, {
					sessionId: "s1",
					userMessages: 1,
					assistantMessages: 5,
					toolCalls: 3,
					toolResults: 3,
					totalMessages: 9,
					tokens: {
						input: 98_000,
						output: 4_200,
						reasoning: 800,
						cacheRead: 310_000,
						cacheWrite: 0,
						total: 413_000,
					},
					premiumRequests: 0,
					cost: 0.2381,
				});
			case "plan_approval":
				this.planProposals.delete(threadId);
				this.waiters.get(`plan:${threadId}`)?.(frame);
				this.waiters.delete(`plan:${threadId}`);
				this.broadcast({ type: "plan.resolved", threadId }, threadId);
				this.refreshThreadState(threadId);
				return this.respond(socket, threadId, id, type, {
					approved: frame.approved === true,
					dispatched: true,
				});
			case "set_plan_mode":
				this.states.set(threadId, {
					...state,
					planModeEnabled: frame.enabled === true,
				});
				return this.respond(socket, threadId, id, type, {
					enabled: frame.enabled === true,
				});
			case "set_agents_paused":
				this.states.set(threadId, {
					...state,
					agentsPaused: frame.enabled === true,
				});
				return this.respond(socket, threadId, id, type);
			case "set_todos":
				this.states.set(threadId, {
					...state,
					todoPhases: frame.phases as SessionState["todoPhases"],
				});
				return this.respond(socket, threadId, id, type, {
					todoPhases: frame.phases,
				});
			case "prompt":
			case "abort_and_prompt": {
				const message = this.withAttachments(
					threadId,
					String(frame.message ?? ""),
					frame.orynAttachments,
				);
				if (message === null)
					return this.respond(
						socket,
						threadId,
						id,
						type,
						undefined,
						"attachment delivery failed: attachment not found",
					);
				this.respond(socket, threadId, id, type, { agentInvoked: true });
				await this.runPrompt(threadId, message);
				return;
			}
			case "bash": {
				this.appendEntry(threadId, {
					role: "bashExecution",
					command: String(frame.command),
					output: "Linux orb-mock 6.12.0 #1 SMP x86_64 GNU/Linux\n",
					exitCode: 0,
					cancelled: false,
					truncated: false,
					excludeFromContext: frame.excludeFromContext === true,
					timestamp: Date.now(),
				});
				return this.respond(socket, threadId, id, type, { exitCode: 0 });
			}
			case "fork":
			case "fork_from":
				return this.respond(
					socket,
					threadId,
					id,
					type,
					undefined,
					"use_oryn_fork",
				);
			default:
				return this.respond(socket, threadId, id, type, {});
		}
	}

	private waitFor(key: string): Promise<Json> {
		return new Promise((resolve) => this.waiters.set(key, resolve));
	}

	/** fakeomp 风格的一轮：流式帧 + 条目复制。 */
	async runPrompt(threadId: string, text: string) {
		const d = this.delay;
		const t = this.threads.get(threadId);
		if (!t) return;
		this.upsertThread({ ...t, state: "working", lastMessageAt: Date.now() });
		this.frame(threadId, { type: "agent_start" });
		const userMsg = { role: "user", content: text, timestamp: Date.now() };
		this.frame(threadId, {
			type: "message_start",
			messageId: `m${this.idSeq++}`,
			message: userMsg,
		});
		this.frame(threadId, {
			type: "message_end",
			messageId: `m${this.idSeq++}`,
			message: userMsg,
		});
		await sleep(d);
		this.appendEntry(threadId, userMsg);

		const ts1 = Date.now();
		const mid1 = `m${this.idSeq++}`;
		const thinking =
			"用户要我在 orb 里执行命令并汇报结果。先跑 uname -a 看看内核与架构。";
		const intro = "我先在 orb 里执行一下命令。";
		const pty = /\bpty\b/i.test(text);
		const ptyCommand = "read -p 'Continue? [y/N] ' a; echo $a";
		const callArgs = JSON.stringify(
			pty
				? { command: ptyCommand, pty: true, i: "Asking in terminal" }
				: { command: "uname -a", i: "Checking kernel" },
		);
		const toolCallId = `toolu_${this.idSeq++}`;
		this.frame(threadId, {
			type: "message_start",
			messageId: mid1,
			message: assistant([], ts1, { stopReason: undefined }),
		});
		const upd = (ev: Json) =>
			this.frame(threadId, {
				type: "message_update",
				messageId: mid1,
				message: { role: "assistant" },
				assistantMessageEvent: ev,
			});
		upd({
			type: "thinking_start",
			contentIndex: 0,
			block: { type: "thinking", thinking: "" },
		});
		for (const chunk of thinking.match(/.{1,6}/gu) ?? []) {
			upd({ type: "thinking_delta", contentIndex: 0, delta: chunk });
			await sleep(d);
		}
		upd({ type: "thinking_end", contentIndex: 0, content: thinking });
		upd({
			type: "text_start",
			contentIndex: 1,
			block: { type: "text", text: "" },
		});
		for (const chunk of intro.match(/.{1,3}/gu) ?? []) {
			upd({ type: "text_delta", contentIndex: 1, delta: chunk });
			await sleep(d);
		}
		upd({ type: "text_end", contentIndex: 1, content: intro });
		upd({
			type: "toolcall_start",
			contentIndex: 2,
			block: { type: "toolCall", id: toolCallId, name: "bash", arguments: {} },
		});
		for (const chunk of callArgs.match(/.{1,8}/gu) ?? []) {
			upd({ type: "toolcall_delta", contentIndex: 2, delta: chunk });
			await sleep(d);
		}
		const call = {
			type: "toolCall",
			id: toolCallId,
			name: "bash",
			arguments: JSON.parse(callArgs) as Json,
		};
		upd({ type: "toolcall_end", contentIndex: 2, toolCall: call });
		const msg1 = assistant(
			[{ type: "thinking", thinking }, { type: "text", text: intro }, call],
			ts1,
		);
		this.frame(threadId, {
			type: "message_end",
			messageId: mid1,
			message: msg1,
		});
		this.appendEntry(threadId, msg1);

		this.frame(threadId, {
			type: "tool_execution_start",
			toolCallId,
			toolName: "bash",
			args: call.arguments,
			intent: "Checking kernel",
		});
		let out = "";
		if (pty) {
			// pty:true：在 orb 的终端里运行，等用户作答
			const term = this.openTerm(threadId, "command", ptyCommand, {}, true);
			const ask = "Continue? [y/N] ";
			this.termOut(threadId, term, ask);
			const answer = await new Promise<string>((resolve) => {
				let typed = "";
				term.onInput = (data) => {
					typed += data;
					this.termOut(threadId, term, data.replace(/\r/g, "\r\n"));
					if (/y/i.test(data)) resolve(typed.replace(/\r/g, ""));
				};
			});
			term.onInput = undefined;
			this.termOut(threadId, term, `\r\n${answer}\r\n`);
			this.termExit(threadId, term, 0);
			out = `${ask}${answer}\n${answer}`;
		}
		for (const line of pty
			? []
			: [
					"Linux orb-01jcz8m3 6.12.0-oryn #1 SMP PREEMPT_DYNAMIC",
					" x86_64 GNU/Linux",
				]) {
			await sleep(d * 4);
			out += line;
			this.frame(threadId, {
				type: "tool_execution_update",
				toolCallId,
				toolName: "bash",
				partialResult: { content: [{ type: "text", text: out }] },
			});
		}
		const result = {
			content: [{ type: "text", text: out }],
			details: { exitCode: 0, wallTimeMs: 210 },
		};
		this.frame(threadId, {
			type: "tool_execution_end",
			toolCallId,
			toolName: "bash",
			result,
			isError: false,
		});
		await sleep(d);
		this.appendEntry(threadId, {
			role: "toolResult",
			toolCallId,
			toolName: "bash",
			...result,
			isError: false,
			timestamp: Date.now(),
		});

		if (/ask/i.test(text)) {
			const reqId = `ui-${this.idSeq++}`;
			const req = {
				type: "extension_ui_request",
				id: reqId,
				method: "ask",
				questions: [
					{
						id: "q1",
						question: "输出要包含主机名吗？",
						options: [
							{ label: "包含", description: "原样输出 uname -a" },
							{ label: "去掉主机名" },
						],
						recommended: 0,
					},
				],
				timeout: 120_000,
			};
			this.pendingUi.set(threadId, [
				...(this.pendingUi.get(threadId) ?? []),
				req,
			]);
			this.frame(threadId, req);
			this.refreshThreadState(threadId);
			await this.waitFor(reqId);
		}
		if (/plan|计划/i.test(text)) {
			const proposal = {
				type: "plan_proposal",
				planFilePath: "/home/user/.omp/plans/reconnect.md",
				title: "重连改造",
				suggestedFileName: "reconnect-plan.md",
				planContent:
					"## 步骤\n\n1. 指数退避\n2. afterSeq 补拉\n3. 端到端测试\n",
				options: ["execute", "compact", "keep_context", "save", "refine"],
			};
			this.planProposals.set(threadId, proposal);
			this.frame(threadId, proposal);
			this.refreshThreadState(threadId);
			await this.waitFor(`plan:${threadId}`);
		}

		const ts2 = Date.now();
		const mid2 = `m${this.idSeq++}`;
		const answer = `命令输出：\n\n\`\`\`text\n${out}\n\`\`\`\n\n内核是 **6.12**，架构 \`x86_64\`。`;
		this.frame(threadId, {
			type: "message_start",
			messageId: mid2,
			message: assistant([], ts2, { stopReason: undefined }),
		});
		const upd2 = (ev: Json) =>
			this.frame(threadId, {
				type: "message_update",
				messageId: mid2,
				message: { role: "assistant" },
				assistantMessageEvent: ev,
			});
		upd2({
			type: "text_start",
			contentIndex: 0,
			block: { type: "text", text: "" },
		});
		for (const chunk of answer.match(/[\s\S]{1,4}/gu) ?? []) {
			upd2({ type: "text_delta", contentIndex: 0, delta: chunk });
			await sleep(d);
		}
		upd2({ type: "text_end", contentIndex: 0, content: answer });
		const msg2 = assistant([{ type: "text", text: answer }], ts2, {
			stopReason: "stop",
		});
		this.frame(threadId, {
			type: "message_end",
			messageId: mid2,
			message: msg2,
		});
		this.appendEntry(threadId, msg2);
		this.frame(threadId, {
			type: "agent_end",
			messages: [],
			isTerminal: true,
			yielded: true,
		});
		this.frame(threadId, {
			type: "prompt_result",
			agentInvoked: true,
			status: "completed",
			sessionSettled: true,
		});
		this.frame(threadId, { type: "session_settled" });
		const cur = this.threads.get(threadId);
		if (cur)
			this.upsertThread({ ...cur, state: "idle", lastMessageAt: Date.now() });
		if (cur?.orbId && text.includes("oryn-setup"))
			this.recordManualSetup(cur.orbId);
	}
}
