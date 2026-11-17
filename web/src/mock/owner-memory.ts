/**
 * mock 后端的 M17（全局助手的主人记忆）部分：`/api/v1/owner-memory/*` 全部操作，格式与 orynd 一致，时间戳是 Unix 毫秒。
 * `MockServer.handleFetch` 在 runner 路由之前调 `handle`，不归这里的请求返回 null。
 * 初始：工程记忆可用、助手记忆已开启、主人名字两个、判断模型接口已配置（key 已设置，从不回显）、回填进行中、
 * 三个主题（含固定种子与一个已归档的）、world 与 observation 事实若干、四个来源（一个写入失败、一个线程已删除）、
 * 一个排除项、一条待确认与一条已生效的明确记忆。
 * 与服务端一致的行为：事实失效或按来源遗忘后全部活跃主题标为等待刷新（stale），刷新后恢复；observation 不能直接失效；
 * 批准待确认的明确记忆后状态变 active，已生效的再批准回 409 `not_pending`；助手记忆关闭时需要 bank 的操作回 409
 * `owner_memory_disabled`。`localStorage["oryn.mock.owner-memory"]` 设为 `unavailable` 时视为工程记忆未启用，
 * 设为 `error` 时状态为连不上 Hindsight（截图与验收用）。
 */
import type { Schemas } from "../api/client";
import { M9_IDS, MAIN_THREAD, NOW, projects } from "./fixtures";
import { insecureUrl } from "./memory";

type Json = Record<string, unknown>;
type Topic = Schemas["OwnerMemoryTopic"];
type Fact = Schemas["OwnerMemoryFact"];
type Explicit = Schemas["OwnerMemoryExplicit"];
type ExclusionKind = Schemas["OwnerMemoryExclusionKind"];
type Provenance = Schemas["OwnerMemoryProvenance"];

const MOCK_KEY = "oryn.mock.owner-memory";
const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;
/** 与服务端 `normalizeNames`、`MemoryContent` 的上限一致。 */
const MAX_NAMES = 16;
const MAX_NAME_CHARS = 64;
const MAX_CONTENT = 4000;
const DEFAULT_LIMIT = 30;
const MAX_LIMIT = 100;
/** 已删除的线程（来源列表里显示「线程已删除」）。 */
const DELETED_THREAD = "T-0193a1b2-0000-7000-8000-00000000dead";

function json(body: unknown, status = 200): Response {
	return new Response(body === undefined ? null : JSON.stringify(body), {
		status,
		headers: { "Content-Type": "application/json" },
	});
}

function error(status: number, code: string, message: string): Response {
	return json({ code, message }, status);
}

function noContent(status = 204): Response {
	return new Response(null, { status });
}

/** 来源（provenance 里的线程标题、类型在应答时按线程表补）。 */
interface Source {
	threadId: string;
	projectId?: string;
	kind: "conv" | "thread";
	chunks: number;
	retainedAt?: number;
	attempts: number;
	nextAttemptAt?: number;
	lastError?: string;
}

/** 事实（出处的线程标题在应答时补）。 */
interface StoredFact extends Omit<Fact, "provenance"> {
	provenance: Omit<Provenance, "threadTitle" | "threadKind">;
}

export interface OwnerMemoryDeps {
	/** 工程记忆（M16）已启用且已配置 */
	engineeringReady(): boolean;
	/** 线程的标题、类型与项目；线程不存在时为 undefined */
	thread(
		id: string,
	): { title: string; kind?: string; projectId?: string } | undefined;
	/** 项目名；项目不存在时为 undefined */
	projectName(id: string): string | undefined;
}

export class MockOwnerMemory {
	private readonly deps: OwnerMemoryDeps;
	private enabled = true;
	private ownerNames = ["Lynricsy", "林"];
	private decide: { baseUrl: string; model: string; key?: string } | undefined =
		{
			baseUrl: "https://generativelanguage.googleapis.com",
			model: "gemini-decision-1",
			key: "AIzaSy-mock-decide-key",
		};
	private backfillSince: number | undefined = NOW - 30 * DAY;
	private lastCuratedAt: number | undefined = NOW - 20 * HOUR;
	private lastCuration: Schemas["OwnerMemoryCuration"] | undefined = {
		at: NOW - 20 * HOUR,
		created: ["工作习惯"],
		kept: ["关于主人"],
		archived: ["旧机器迁移"],
	};
	private readonly topics: Topic[] = [
		{
			id: "owner-profile",
			name: "关于主人",
			scope: "主人的身份、称呼、长期偏好与沟通方式",
			seed: true,
			status: "active",
			stale: false,
			content:
				"- 主人叫 Lynricsy，也叫「林」。\n- 回复用简体中文，先给结论再列理由，不用表情符号。\n- 常在深夜工作，周末会集中处理 Oryn 的大改动。",
			lastRefreshedAt: NOW - 3 * HOUR,
		},
		{
			id: "omt_work_habits",
			name: "工作习惯",
			scope: "主人怎么安排开发、评审与发布",
			seed: false,
			status: "active",
			stale: false,
			content:
				"- 前端用 bun，不用 npm。\n- 发版前先在 mock 环境跑一遍截图矩阵。\n- 喜欢小步提交，每步都过检查。",
			lastRefreshedAt: NOW - 5 * HOUR,
		},
		{
			id: "omt_old_machine",
			name: "旧机器迁移",
			scope: "从旧工作站迁到新机器的过程",
			seed: false,
			status: "archived",
			stale: false,
			content: "- 旧工作站的数据已全部迁到新机器。",
			lastRefreshedAt: NOW - 9 * DAY,
		},
	];
	private readonly facts: StoredFact[] = [
		{
			id: "omf_01",
			type: "world",
			text: "主人的回复偏好：先给结论，再列理由，不用表情符号。",
			state: "valid",
			mentionedAt: "2026-11-10",
			provenance: {
				kind: "owner",
				threadId: M9_IDS.assistant,
				seqFrom: 3,
				seqTo: 5,
				documentId: `owner-conv-${M9_IDS.assistant}-0`,
			},
		},
		{
			id: "omf_02",
			type: "world",
			text: "主人周末会集中处理 Oryn 的大改动。",
			state: "valid",
			mentionedAt: "2026-11-08",
			provenance: {
				kind: "mixed",
				threadId: MAIN_THREAD,
				seqFrom: 12,
				seqTo: 30,
				documentId: `owner-thread-${MAIN_THREAD}-1`,
			},
		},
		{
			id: "omf_03",
			type: "world",
			text: "主人在 Oryn 前端项目里用 bun，不用 npm。",
			state: "valid",
			mentionedAt: "2026-11-05",
			provenance: {
				kind: "explicit",
				documentId: "owner-explicit-ome_02",
			},
		},
		{
			id: "omf_04",
			type: "world",
			text: "主人以前用一台旧工作站开发。",
			state: "invalidated",
			mentionedAt: "2026-10-20",
			provenance: { kind: "unknown" },
		},
		{
			id: "omf_05",
			type: "observation",
			text: "主人倾向于小步提交，每步都跑完检查再继续。",
			state: "valid",
			provenance: {
				kind: "mixed",
				threadId: MAIN_THREAD,
				seqFrom: 40,
				seqTo: 40,
			},
		},
	];
	private readonly sources: Source[] = [
		{
			threadId: M9_IDS.assistant,
			kind: "conv",
			chunks: 3,
			retainedAt: NOW - 2 * HOUR,
			attempts: 0,
		},
		{
			threadId: MAIN_THREAD,
			projectId: projects[0]?.id,
			kind: "thread",
			chunks: 5,
			retainedAt: NOW - 6 * HOUR,
			attempts: 0,
		},
		{
			threadId: M9_IDS.scheduledThread,
			projectId: projects[1]?.id,
			kind: "thread",
			chunks: 0,
			attempts: 2,
			nextAttemptAt: NOW + 25 * MIN,
			lastError: "写入 Hindsight 失败：502 Bad Gateway",
		},
		{
			threadId: DELETED_THREAD,
			kind: "conv",
			chunks: 2,
			retainedAt: NOW - 4 * DAY,
			attempts: 0,
		},
	];
	private readonly exclusions: {
		kind: ExclusionKind;
		targetId: string;
		createdAt: number;
	}[] = [
		{
			kind: "thread",
			targetId: M9_IDS.spaceIdle,
			createdAt: NOW - 3 * DAY,
		},
	];
	private readonly explicit: Explicit[] = [
		{
			id: "ome_01",
			content: "主人的生日是 3 月 14 日。",
			ownerQuote: "记一下，我生日是 3 月 14 号",
			sourceThreadId: M9_IDS.assistant,
			status: "pending",
			createdAt: NOW - 40 * MIN,
			updatedAt: NOW - 40 * MIN,
		},
		{
			id: "ome_02",
			content: "Oryn 前端项目用 bun，不用 npm。",
			status: "active",
			createdAt: NOW - 12 * DAY,
			updatedAt: NOW - 12 * DAY,
		},
	];
	private seq = 3;

	constructor(deps: OwnerMemoryDeps) {
		this.deps = deps;
	}

	private flag(): string | null {
		try {
			return globalThis.localStorage?.getItem(MOCK_KEY) ?? null;
		} catch {
			return null;
		}
	}

	private available(): boolean {
		return this.flag() !== "unavailable" && this.deps.engineeringReady();
	}

	/** 需要 bank 的操作：助手记忆没在运行时与服务端一样回 409。 */
	private bankBlocked(): Response | null {
		return this.available() && this.enabled
			? null
			: error(409, "owner_memory_disabled", "助手记忆未开启");
	}

	private settings(): Schemas["OwnerMemorySettings"] {
		return {
			enabled: this.enabled,
			available: this.available(),
			ownerNames: [...this.ownerNames],
			...(this.decide
				? {
						decide: {
							baseUrl: this.decide.baseUrl,
							model: this.decide.model,
							hasKey: Boolean(this.decide.key),
						},
					}
				: {}),
		};
	}

	private status(): Schemas["OwnerMemoryStatus"] {
		const base = {
			backfillTotal: this.backfillSince === undefined ? 0 : 42,
			backfillDone: this.backfillSince === undefined ? 0 : 37,
			failingSources: this.sources.filter((s) => s.lastError).length,
			pendingExplicit: this.explicit.filter((e) => e.status === "pending")
				.length,
			...(this.backfillSince === undefined
				? {}
				: { backfillSince: this.backfillSince }),
			...(this.lastCuratedAt === undefined
				? {}
				: { lastCuratedAt: this.lastCuratedAt }),
		};
		if (!this.available()) return { ...base, state: "unavailable" };
		if (!this.enabled) return { ...base, state: "disabled" };
		if (this.flag() === "error")
			return {
				...base,
				state: "error",
				error:
					'读取 bank 统计失败：连不上 Hindsight：Get "http://127.0.0.1:8888/v1/default/banks/oryn-owner/stats": connect: connection refused',
			};
		return {
			...base,
			state: "ok",
			memoryCount: this.facts.filter((f) => f.state === "valid").length * 9,
			documentCount:
				this.sources.reduce((n, s) => n + s.chunks, 0) +
				this.explicit.filter((e) => e.status === "active").length,
			usageDay: { calls: 18, inputTokens: 52_300, outputTokens: 6_120 },
			usageWeek: { calls: 131, inputTokens: 418_900, outputTokens: 47_350 },
		};
	}

	/** 遗忘保护：遗忘或失效后全部活跃主题在刷新前不进入注入。 */
	private markStale(): void {
		for (const t of this.topics) if (t.status === "active") t.stale = true;
	}

	private provenance(p: StoredFact["provenance"]): Provenance {
		const th = p.threadId ? this.deps.thread(p.threadId) : undefined;
		return {
			...p,
			...(th ? { threadTitle: th.title, threadKind: th.kind ?? "thread" } : {}),
		};
	}

	private brief(threadId: string | null): Schemas["OwnerMemoryBrief"] {
		if (!this.available() || !this.enabled)
			return { content: "", generatedAt: Date.now(), cached: false };
		const topics = this.topics
			.filter((t) => t.status === "active" && !t.stale && t.content)
			.map((t) => `### ${t.name}\n${t.content}`);
		const content = [
			"## 关于主人（助手记忆）",
			"以下内容来自过往对话，可能过时；与主人当前的说法冲突时以当前为准。",
			...topics,
			"### 最近在做的事",
			"- 11-17 「今天有哪些线程在等我」（助手对话，空闲）",
		].join("\n\n");
		const cached =
			threadId !== null && this.deps.thread(threadId)?.kind === "assistant";
		return {
			content,
			generatedAt: cached ? Date.now() - 4 * MIN : Date.now(),
			cached,
		};
	}

	/** 删除一个来源写入的全部 document（与它派生的事实）；线程已删除时来源记录随之清掉。 */
	private forget(threadId: string): Schemas["MemoryForgetResult"] {
		const i = this.sources.findIndex((s) => s.threadId === threadId);
		const src = this.sources[i];
		if (!src) return { deleted: 0, pending: 0 };
		const deleted = src.chunks;
		for (let j = this.facts.length - 1; j >= 0; j--)
			if (this.facts[j]?.provenance.threadId === threadId)
				this.facts.splice(j, 1);
		if (this.deps.thread(threadId)) {
			src.chunks = 0;
			src.retainedAt = Date.now();
		} else {
			this.sources.splice(i, 1);
		}
		if (deleted > 0) this.markStale();
		return { deleted, pending: 0 };
	}

	private explicitView(e: Explicit): Explicit {
		const title = e.sourceThreadId
			? this.deps.thread(e.sourceThreadId)?.title
			: undefined;
		return { ...e, ...(title ? { sourceThreadTitle: title } : {}) };
	}

	async handle(
		p: string,
		m: string,
		body: () => Promise<Json>,
		url: URL,
	): Promise<Response | null> {
		if (!p.startsWith("owner-memory/")) return null;
		const sub = p.slice("owner-memory/".length);

		if (sub === "settings" && m === "GET") return json(this.settings());
		if (sub === "settings" && m === "PUT") {
			const b = await body();
			if (typeof b.enabled !== "boolean" || !Array.isArray(b.ownerNames))
				return error(400, "invalid_request", "enabled 与 ownerNames 必填");
			const names: string[] = [];
			for (const raw of b.ownerNames) {
				const n = String(raw).trim();
				if (!n || names.includes(n)) continue;
				if ([...n].length > MAX_NAME_CHARS || /[\r\n]/.test(n))
					return error(400, "invalid_request", "名字或别名过长或含换行");
				names.push(n);
			}
			if (names.length > MAX_NAMES)
				return error(400, "invalid_request", "名字与别名最多 16 个");
			this.enabled = b.enabled;
			this.ownerNames = names;
			if (b.enabled && this.backfillSince === undefined)
				this.backfillSince = Date.now() - 30 * DAY;
			return json(this.settings());
		}
		if (sub === "decide-endpoint" && m === "PUT") {
			const b = await body();
			const baseUrl = String(b.baseUrl ?? "")
				.trim()
				.replace(/\/+$/, "");
			if (!baseUrl || insecureUrl(baseUrl))
				return error(400, "insecure_url", "远端地址必须用 https://");
			const model = String(b.model ?? "").trim();
			if (!model || /[/\s]/.test(model))
				return error(400, "invalid_request", "模型名无效");
			// apiKey 省略 = 保留原值，空串 = 清除
			const key =
				typeof b.apiKey === "string" ? b.apiKey || undefined : this.decide?.key;
			this.decide = { baseUrl, model, ...(key ? { key } : {}) };
			return json(this.settings());
		}
		if (sub === "decide-endpoint" && m === "DELETE") {
			this.decide = undefined;
			return json(this.settings());
		}
		if (sub === "status" && m === "GET") return json(this.status());

		if (sub === "topics" && m === "GET")
			return json({
				items: this.topics,
				...(this.lastCuration ? { lastCuration: this.lastCuration } : {}),
			});
		const refresh = /^topics\/([^/]+)\/refresh$/.exec(sub);
		if (refresh && m === "POST") {
			const topic = this.topics.find(
				(t) => t.id === decodeURIComponent(refresh[1] ?? ""),
			);
			if (!topic) return error(404, "not_found", "主题不存在");
			const blocked = this.bankBlocked();
			if (blocked) return blocked;
			topic.stale = false;
			topic.lastRefreshedAt = Date.now();
			return noContent(202);
		}
		if (sub === "curate" && m === "POST") {
			const blocked = this.bankBlocked();
			if (blocked) return blocked;
			const created: string[] = [];
			if (!this.topics.some((t) => t.id === "omt_recent")) {
				this.topics.push({
					id: "omt_recent",
					name: "近期在做的事",
					scope: "主人最近一两周在推进的工作",
					seed: false,
					status: "active",
					stale: false,
					content: "- 在给全局助手做主人记忆（M17）。",
					lastRefreshedAt: Date.now(),
				});
				created.push("近期在做的事");
			}
			const at = Date.now();
			this.lastCuratedAt = at;
			this.lastCuration = {
				at,
				created,
				kept: this.topics
					.filter((t) => t.status === "active" && !created.includes(t.name))
					.map((t) => t.name),
				archived: [],
			};
			return json(this.lastCuration);
		}
		if (sub === "brief" && m === "GET")
			return json(this.brief(url.searchParams.get("threadId")));

		if (sub === "facts" && m === "GET") {
			const blocked = this.bankBlocked();
			if (blocked) return blocked;
			const type = url.searchParams.get("type");
			const q = url.searchParams.get("q")?.trim() ?? "";
			const limit = Math.min(
				Math.max(Number(url.searchParams.get("limit") ?? DEFAULT_LIMIT), 1),
				MAX_LIMIT,
			);
			const offset = Math.max(Number(url.searchParams.get("offset") ?? 0), 0);
			const all = this.facts.filter((f) => !type || f.type === type);
			const view = (f: StoredFact): Fact => ({
				...f,
				provenance: this.provenance(f.provenance),
			});
			if (q) {
				// 召回的近似：按关键词命中，不分页
				const terms = q.toLowerCase().split(/\s+/);
				const hits = all.filter((f) =>
					terms.some((t) => f.text.toLowerCase().includes(t)),
				);
				return json({ items: hits.slice(0, limit).map(view), hasMore: false });
			}
			return json({
				items: all.slice(offset, offset + limit).map(view),
				hasMore: all.length > offset + limit,
			});
		}
		const factState = /^facts\/([^/]+)\/state$/.exec(sub);
		if (factState && m === "PUT") {
			const b = await body();
			if (b.state !== "valid" && b.state !== "invalidated")
				return error(
					400,
					"invalid_request",
					"state 只能是 invalidated 或 valid",
				);
			const blocked = this.bankBlocked();
			if (blocked) return blocked;
			const fact = this.facts.find(
				(f) => f.id === decodeURIComponent(factState[1] ?? ""),
			);
			if (!fact) return error(404, "not_found", "事实不存在");
			if (fact.type !== "world")
				return error(
					400,
					"invalid_request",
					"只有 world 事实可以直接失效；observation 请失效它的来源事实",
				);
			fact.state = b.state;
			this.markStale();
			return noContent();
		}

		if (sub === "sources" && m === "GET") {
			const limit = Math.min(
				Math.max(Number(url.searchParams.get("limit") ?? DEFAULT_LIMIT), 1),
				MAX_LIMIT,
			);
			const offset = Math.max(Number(url.searchParams.get("offset") ?? 0), 0);
			const items = this.sources
				.slice(offset, offset + limit)
				.map((s): Schemas["OwnerMemorySource"] => {
					const th = this.deps.thread(s.threadId);
					return {
						...s,
						missing: th === undefined,
						...(th
							? { threadTitle: th.title, threadKind: th.kind ?? "thread" }
							: {}),
					};
				});
			return json({ items, hasMore: this.sources.length > offset + limit });
		}
		const source = /^sources\/([^/]+)$/.exec(sub);
		if (source && m === "DELETE") {
			const id = decodeURIComponent(source[1] ?? "");
			if (!this.sources.some((s) => s.threadId === id))
				return error(404, "not_found", "这个对话或线程没有写入助手记忆");
			const blocked = this.bankBlocked();
			if (blocked) return blocked;
			return json(this.forget(id));
		}

		if (sub === "exclusions" && m === "GET")
			return json({
				items: this.exclusions.map((e): Schemas["OwnerMemoryExclusion"] => {
					const name =
						e.kind === "project"
							? this.deps.projectName(e.targetId)
							: this.deps.thread(e.targetId)?.title;
					return { ...e, ...(name ? { name } : {}) };
				}),
			});
		const exclusion = /^exclusions\/([^/]+)\/([^/]+)$/.exec(sub);
		if (exclusion && (m === "PUT" || m === "DELETE")) {
			const kind = exclusion[1];
			const targetId = decodeURIComponent(exclusion[2] ?? "");
			if (kind !== "thread" && kind !== "project")
				return error(400, "invalid_request", "kind 只能是 thread 或 project");
			const i = this.exclusions.findIndex(
				(e) => e.kind === kind && e.targetId === targetId,
			);
			if (m === "DELETE") {
				if (i >= 0) this.exclusions.splice(i, 1);
				return noContent();
			}
			const exists =
				kind === "project"
					? this.deps.projectName(targetId) !== undefined
					: this.deps.thread(targetId) !== undefined;
			if (!exists) return error(404, "not_found", "对象不存在");
			if (i < 0)
				this.exclusions.push({ kind, targetId, createdAt: Date.now() });
			const result = { deleted: 0, pending: 0 };
			if (this.available() && this.enabled) {
				const ids =
					kind === "thread"
						? [targetId]
						: this.sources
								.filter((s) => s.projectId === targetId)
								.map((s) => s.threadId);
				for (const id of ids) result.deleted += this.forget(id).deleted;
			}
			return json(result);
		}

		if (sub === "explicit" && m === "GET") {
			const items = [...this.explicit]
				.sort(
					(a, b) =>
						Number(b.status === "pending") - Number(a.status === "pending") ||
						b.updatedAt - a.updatedAt,
				)
				.map((e) => this.explicitView(e));
			return json({ items });
		}
		if (sub === "explicit" && m === "POST") {
			const content = String((await body()).content ?? "").trim();
			if (!content || content.length > MAX_CONTENT)
				return error(400, "invalid_request", "内容不能为空，且不超过 4000 字");
			const blocked = this.bankBlocked();
			if (blocked) return blocked;
			const now = Date.now();
			const e: Explicit = {
				id: `ome_${String(this.seq++).padStart(2, "0")}`,
				content,
				status: "active",
				createdAt: now,
				updatedAt: now,
			};
			this.explicit.push(e);
			return json(this.explicitView(e));
		}
		const approve = /^explicit\/([^/]+)\/approve$/.exec(sub);
		if (approve && m === "POST") {
			const approveId = decodeURIComponent(approve[1] ?? "");
			const e = this.explicit.find((x) => x.id === approveId);
			if (!e) return error(404, "not_found", "明确记忆不存在");
			if (e.status !== "pending")
				return error(409, "not_pending", "这条记忆已经生效");
			const b = await body();
			let content = e.content;
			if (typeof b.content === "string") {
				content = b.content.trim();
				if (!content || content.length > MAX_CONTENT)
					return error(
						400,
						"invalid_request",
						"内容不能为空，且不超过 4000 字",
					);
			}
			const blocked = this.bankBlocked();
			if (blocked) return blocked;
			e.content = content;
			e.status = "active";
			e.updatedAt = Date.now();
			return json(this.explicitView(e));
		}
		const one = /^explicit\/([^/]+)$/.exec(sub);
		if (one && (m === "PATCH" || m === "DELETE")) {
			const id = decodeURIComponent(one[1] ?? "");
			const e = this.explicit.find((x) => x.id === id);
			if (!e) return error(404, "not_found", "明确记忆不存在");
			if (m === "DELETE") {
				if (e.status === "active") {
					const blocked = this.bankBlocked();
					if (blocked) return blocked;
				}
				this.explicit.splice(this.explicit.indexOf(e), 1);
				return noContent();
			}
			const content = String((await body()).content ?? "").trim();
			if (!content || content.length > MAX_CONTENT)
				return error(400, "invalid_request", "内容不能为空，且不超过 4000 字");
			if (e.status === "active" && content !== e.content) {
				const blocked = this.bankBlocked();
				if (blocked) return blocked;
			}
			e.content = content;
			e.updatedAt = Date.now();
			return json(this.explicitView(e));
		}
		return null;
	}
}
