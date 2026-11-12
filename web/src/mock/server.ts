/**
 * 浏览器内 mock 服务端：实现足够的 REST 与 WS（计划 C3）让界面无需后端即可体验。
 * 发送提示词时按 fakeomp 的帧序列回放：agent_start → 用户消息 → 流式 assistant（思考 / 文本 / 工具调用）→
 * bash 工具生命周期 → 最终回答 → agent_end → prompt_result → session_settled，期间按序复制条目。
 * 提示词含 `ask` 时发一个 ask 对话框并等待作答；含 `plan` 时发计划提案并等待审批。
 * 环境（M2.5）：orb 带 `lastSetup` / `lastResume`，提供 setup-runs 与日志接口；failed orb 的线程 `wake` 后恢复；
 * 提示词点名 `oryn-setup` 技能时，回合结束后补一条手动重跑成功的记录（模拟 agent 修好环境）。
 */
import type { Entry, Orb, Schemas, Thread } from "../api/client";
import type { SessionState } from "../omp/types";
import {
	assistant,
	buildMainTranscript,
	threads as fixtureThreads,
	MAIN_THREAD,
	mainState,
	makeOrb,
	NOW,
	orbEnv,
	projects,
	providers,
	SESSION_FILE,
	setupLogs,
} from "./fixtures";

type Json = Record<string, unknown>;

export interface MockSocket {
	deliver(msg: Json): void;
}

function json(body: unknown, status = 200): Response {
	return new Response(body === undefined ? null : JSON.stringify(body), {
		status,
		headers: { "Content-Type": "application/json" },
	});
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
	readonly queues = new Map<
		string,
		{ steering: string[]; followUp: string[] }
	>();
	private readonly sockets = new Set<MockSocket>();
	private readonly subs = new Map<MockSocket, Set<string>>();
	private readonly waiters = new Map<string, (value: Json) => void>();
	private rev = 1;
	private idSeq = 100;
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
		for (const t of fixtureThreads)
			this.states.set(t.id, {
				...mainState(),
				todoPhases: t.id === MAIN_THREAD ? mainState().todoPhases : [],
			});
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
		if (p === "projects" && m === "GET") return json({ projects });
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
		if (p === "threads" && m === "POST")
			return json({ thread: this.createThread(await body()) }, 201);
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

	private createThread(b: Json): Thread {
		const id = `T-mock-${(this.idSeq++).toString(16)}`;
		const orbId = `orb-mock${this.idSeq.toString().padStart(22, "0")}`;
		const prompt = typeof b.prompt === "string" ? b.prompt : "";
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
						live: [],
					},
					leafEntryId: last?.entryId,
					lastSeq: last?.seq ?? 0,
				});
				const rest = list.filter((e) => e.seq > after);
				for (let i = 0; i < rest.length; i += 200) {
					socket.deliver({
						type: "entries",
						threadId,
						entries: rest.slice(i, i + 200),
						leafEntryId: last?.entryId,
						lastSeq: last?.seq ?? 0,
					});
				}
				return;
			}
			case "unsub":
				this.subs.get(socket)?.delete(threadId);
				return;
			case "ui.response": {
				const frame = msg.frame as Json;
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
			case "abort_and_prompt":
				this.respond(socket, threadId, id, type, { agentInvoked: true });
				await this.runPrompt(threadId, String(frame.message ?? ""));
				return;
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
		const callArgs = JSON.stringify({
			command: "uname -a",
			i: "Checking kernel",
		});
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
		for (const line of [
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
