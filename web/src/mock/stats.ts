/**
 * mock 后端的统计部分（M10）：`stats/*` 与 `orbs/{id}/timeline`，格式与 orynd 一致。
 * 用量 / 工具 / 回合 / orb / 产出都由固定种子的伪随机数生成（近 120 天，相对页面加载时刻），每次加载形状相同；
 * 聚合在内存里按浏览器本地时区切桶（页面传来的 `tz` 就是它），时间粒度的 `groups` / `series` 稠密，
 * 非时间分组按成本降序取前 `limit` 个、其余并成 `__other__`。
 */
import type { Orb, Project, Schemas, Thread } from "../api/client";
import { MAIN_THREAD, NOW } from "./fixtures";

type UsageStat = Schemas["UsageStat"];
type OrbStat = Schemas["OrbStat"];
type OutputStat = Schemas["OutputStat"];
type Grain = Schemas["StatsGrain"];
type Json = Record<string, unknown>;

const DAY = 86_400_000;
const HOUR = 3_600_000;
const HISTORY_DAYS = 120;
const OTHER = "__other__";

export interface StatsDeps {
	threads(): Thread[];
	projects(): Project[];
	spaces(): Schemas["Space"][];
	orbs(): Orb[];
}

function json(body: unknown, status = 200, headers?: HeadersInit): Response {
	return Response.json(body, { status, headers });
}

/** mulberry32：固定种子的伪随机数。 */
function rng(seed: number): () => number {
	let a = seed >>> 0;
	return () => {
		a = (a + 0x6d2b79f5) >>> 0;
		let t = a;
		t = Math.imul(t ^ (t >>> 15), t | 1);
		t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
		return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
	};
}

/** 模型与每百万 token 的大致标价（输入, 输出, 缓存读, 缓存写），权重越大越常用。 */
const MODELS: {
	key: string;
	price: [number, number, number, number];
	w: number;
}[] = [
	{ key: "anthropic/claude-sonnet-5", price: [3, 15, 0.3, 3.75], w: 30 },
	{ key: "anthropic/claude-opus-5", price: [15, 75, 1.5, 18.75], w: 8 },
	{ key: "openai/gpt-6.1", price: [2.5, 10, 0.25, 0], w: 16 },
	{ key: "google/gemini-3-pro", price: [1.25, 10, 0.31, 0], w: 10 },
	{ key: "deepseek/deepseek-v4", price: [0.27, 1.1, 0.07, 0], w: 9 },
	{ key: "moonshot/kimi-k3", price: [0.6, 2.5, 0.15, 0], w: 6 },
	{ key: "zhipu/glm-5", price: [0.5, 2, 0.1, 0], w: 5 },
	{ key: "xai/grok-5", price: [3, 15, 0.75, 0], w: 4 },
	{ key: "qwen/qwen3-max", price: [1.2, 6, 0.24, 0], w: 3 },
	{ key: "mistral/mistral-large-3", price: [2, 6, 0.2, 0], w: 2 },
];
const PURPOSES = ["title", "cache-warm", "memory", "auto-thinking"];
const TOOLS = [
	"bash",
	"read",
	"edit",
	"grep",
	"write",
	"web_search",
	"task",
	"lsp",
];

interface Fact {
	ts: number;
	thread: string;
	project: string;
	space: string;
	model: string;
	purpose: string;
	input: number;
	output: number;
	cacheRead: number;
	cacheWrite: number;
	cost: number;
	ttft: number;
	duration: number;
	error: boolean;
	subagent: boolean;
}
interface ToolFact {
	ts: number;
	thread: string;
	project: string;
	space: string;
	tool: string;
	error: boolean;
}
interface TurnFact {
	ts: number;
	thread: string;
	project: string;
	space: string;
}

function pick<T>(
	r: () => number,
	items: readonly T[],
	weight: (t: T) => number,
): T {
	const total = items.reduce((a, x) => a + weight(x), 0);
	let x = r() * total;
	for (const it of items) {
		x -= weight(it);
		if (x <= 0) return it;
	}
	return items[items.length - 1] as T;
}

/** 本地某天零点（d 天前）。 */
function dayStart(daysAgo: number): number {
	const d = new Date(NOW);
	return new Date(
		d.getFullYear(),
		d.getMonth(),
		d.getDate() - daysAgo,
	).getTime();
}

interface Facts {
	usage: Fact[];
	tools: ToolFact[];
	turns: TurnFact[];
}

let cache: { key: string; facts: Facts } | null = null;

function facts(deps: StatsDeps): Facts {
	const work = deps.threads().filter((t) => (t.kind ?? "work") === "work");
	const key = work
		.map((t) => `${t.id}:${t.projectId ?? ""}:${t.spaceId ?? ""}`)
		.join(",");
	if (cache?.key === key) return cache.facts;
	const r = rng(20261115);
	const usage: Fact[] = [];
	const tools: ToolFact[] = [];
	const turns: TurnFact[] = [];
	for (let d = HISTORY_DAYS - 1; d >= 0; d--) {
		const start = dayStart(d);
		const end = d === 0 ? NOW : start + DAY;
		const weekday = new Date(start).getDay();
		const busy = weekday === 0 || weekday === 6 ? 0.4 : 1;
		const n = Math.round(
			(12 + r() * 28) * busy * (0.6 + (HISTORY_DAYS - d) / HISTORY_DAYS),
		);
		for (let i = 0; i < n; i++) {
			const th =
				work.length === 0
					? undefined
					: pick(r, work, (t) => (t.id === MAIN_THREAD ? 6 : 1));
			const m = pick(r, MODELS, (x) => x.w);
			const ts = Math.floor(start + r() * Math.max(1, end - start));
			const purpose =
				r() < 0.86
					? ""
					: (PURPOSES[Math.floor(r() * PURPOSES.length)] ?? "title");
			const small = purpose !== "";
			const input = Math.round(
				(small ? 400 : 2_000) + r() * (small ? 2_000 : 38_000),
			);
			const output = Math.round(
				(small ? 20 : 200) + r() * (small ? 200 : 3_800),
			);
			const cacheRead = small ? 0 : Math.round(r() * 180_000);
			const cacheWrite = small || r() < 0.6 ? 0 : Math.round(r() * 20_000);
			const [pi, po, pr, pw] = m.price;
			const cost =
				(input * pi + output * po + cacheRead * pr + cacheWrite * pw) /
				1_000_000;
			const base = {
				ts,
				thread: th?.id ?? "",
				project: th?.projectId ?? "",
				space: th?.spaceId ?? "",
			};
			usage.push({
				...base,
				model: m.key,
				purpose,
				input,
				output,
				cacheRead,
				cacheWrite,
				cost,
				ttft: 400 + r() * 2_600,
				duration: 3_000 + r() * 37_000,
				error: r() < 0.02,
				subagent: r() < 0.12,
			});
			if (!small) {
				const calls = Math.floor(r() * 4);
				for (let k = 0; k < calls; k++)
					tools.push({
						...base,
						tool: pick(r, TOOLS, (x) => (x === "bash" || x === "read" ? 4 : 1)),
						error: r() < 0.06,
					});
				if (r() < 0.3) turns.push(base);
			}
		}
	}
	cache = { key, facts: { usage, tools, turns } };
	return cache.facts;
}

// ── 时间桶 ──

const pad = (n: number) => String(n).padStart(2, "0");

function isoWeek(d: Date): string {
	const t = new Date(Date.UTC(d.getFullYear(), d.getMonth(), d.getDate()));
	const day = t.getUTCDay() || 7;
	t.setUTCDate(t.getUTCDate() + 4 - day);
	const y0 = new Date(Date.UTC(t.getUTCFullYear(), 0, 1));
	const w = Math.ceil(((t.getTime() - y0.getTime()) / DAY + 1) / 7);
	return `${t.getUTCFullYear()}-W${pad(w)}`;
}

/** 桶起点（本地）与键。 */
function bucketOf(ts: number, grain: Grain): { key: string; start: number } {
	const d = new Date(ts);
	const y = d.getFullYear();
	const mo = d.getMonth();
	const day = d.getDate();
	if (grain === "hour") {
		const start = new Date(y, mo, day, d.getHours()).getTime();
		return {
			key: `${y}-${pad(mo + 1)}-${pad(day)}T${pad(d.getHours())}:00`,
			start,
		};
	}
	if (grain === "day")
		return {
			key: `${y}-${pad(mo + 1)}-${pad(day)}`,
			start: new Date(y, mo, day).getTime(),
		};
	if (grain === "week") {
		const back = (d.getDay() + 6) % 7;
		const start = new Date(y, mo, day - back).getTime();
		return { key: isoWeek(new Date(start)), start };
	}
	return { key: `${y}-${pad(mo + 1)}`, start: new Date(y, mo, 1).getTime() };
}

/** 窗口内的稠密网格（起点裁到窗口）。 */
function grid(
	from: number,
	to: number,
	grain: Grain,
): { key: string; start: number }[] {
	const out: { key: string; start: number }[] = [];
	let cur = bucketOf(from, grain).start;
	for (let guard = 0; cur < to && guard < 1000; guard++) {
		const b = bucketOf(cur, grain);
		out.push({ key: b.key, start: Math.max(from, b.start) });
		const d = new Date(b.start);
		cur =
			grain === "hour"
				? new Date(
						d.getFullYear(),
						d.getMonth(),
						d.getDate(),
						d.getHours() + 1,
					).getTime()
				: grain === "day"
					? new Date(d.getFullYear(), d.getMonth(), d.getDate() + 1).getTime()
					: grain === "week"
						? new Date(d.getFullYear(), d.getMonth(), d.getDate() + 7).getTime()
						: new Date(d.getFullYear(), d.getMonth() + 1, 1).getTime();
	}
	return out;
}

const GRAINS: readonly string[] = ["hour", "day", "week", "month"];

// ── 用量 ──

function emptyStat(key: string): UsageStat {
	return {
		key,
		requests: 0,
		requestErrors: 0,
		inputTokens: 0,
		outputTokens: 0,
		cacheReadTokens: 0,
		cacheWriteTokens: 0,
		costUsd: 0,
		subagentCostUsd: 0,
		toolCalls: 0,
		toolErrors: 0,
		turns: 0,
	};
}

interface Acc {
	s: UsageStat;
	ttft: number;
	ttftN: number;
	out: number;
	dur: number;
}

function acc(key: string): Acc {
	return { s: emptyStat(key), ttft: 0, ttftN: 0, out: 0, dur: 0 };
}

function addFact(a: Acc, f: Fact) {
	const s = a.s;
	s.requests++;
	if (f.error) s.requestErrors++;
	s.inputTokens += f.input;
	s.outputTokens += f.output;
	s.cacheReadTokens += f.cacheRead;
	s.cacheWriteTokens += f.cacheWrite;
	s.costUsd += f.cost;
	if (f.subagent) s.subagentCostUsd = (s.subagentCostUsd ?? 0) + f.cost;
	a.ttft += f.ttft;
	a.ttftN++;
	a.out += f.output;
	a.dur += f.duration;
}

function finish(a: Acc): UsageStat {
	const s = { ...a.s };
	if (a.ttftN > 0) s.avgTtftMs = a.ttft / a.ttftN;
	if (a.dur > 0) s.tokensPerSecond = a.out / (a.dur / 1000);
	return s;
}

function merge(into: UsageStat, s: UsageStat) {
	into.requests += s.requests;
	into.requestErrors += s.requestErrors;
	into.inputTokens += s.inputTokens;
	into.outputTokens += s.outputTokens;
	into.cacheReadTokens += s.cacheReadTokens;
	into.cacheWriteTokens += s.cacheWriteTokens;
	into.costUsd += s.costUsd;
	into.subagentCostUsd = (into.subagentCostUsd ?? 0) + (s.subagentCostUsd ?? 0);
	into.toolCalls += s.toolCalls;
	into.toolErrors += s.toolErrors;
	into.turns += s.turns;
}

interface Scope {
	from: number;
	to: number;
	project?: string;
	space?: string;
	thread?: string;
	model?: string;
}

function scopeOf(url: URL): Scope {
	const q = url.searchParams;
	const num = (k: string) => (q.get(k) === null ? undefined : Number(q.get(k)));
	const to = num("to") ?? Date.now();
	const from = num("from") ?? to - 7 * DAY;
	const v = (k: string) => {
		const x = q.get(k);
		if (x === null) return undefined;
		return (k === "project" || k === "space") && x === "none" ? "" : x;
	};
	return {
		from,
		to,
		project: v("project"),
		space: v("space"),
		thread: v("thread"),
		model: v("model"),
	};
}

function inScope(
	f: {
		ts: number;
		thread: string;
		project: string;
		space: string;
		model?: string;
	},
	s: Scope,
): boolean {
	return (
		f.ts >= s.from &&
		f.ts < s.to &&
		(s.thread === undefined || f.thread === s.thread) &&
		(s.project === undefined || f.project === s.project) &&
		(s.space === undefined || f.space === s.space) &&
		(s.model === undefined || f.model === undefined || f.model === s.model)
	);
}

type Dim = "total" | "model" | "thread" | "project" | "space" | "purpose";

function dimKey(
	dim: Dim,
	f: {
		thread: string;
		project: string;
		space: string;
		model?: string;
		purpose?: string;
	},
): string {
	if (dim === "total") return "total";
	if (dim === "model") return f.model ?? "";
	if (dim === "purpose") return f.purpose ?? "";
	return f[dim];
}

function labeler(
	deps: StatsDeps,
	dim: Dim,
): (key: string) => string | undefined {
	if (dim === "thread") {
		const m = new Map(deps.threads().map((t) => [t.id, t.title]));
		return (k) => m.get(k);
	}
	if (dim === "project") {
		const m = new Map(deps.projects().map((p) => [p.id, p.name]));
		return (k) => m.get(k);
	}
	if (dim === "space") {
		const m = new Map(deps.spaces().map((s) => [s.id, s.name]));
		return (k) => m.get(k);
	}
	return () => undefined;
}

/** 按维度聚合用量（含工具与回合，模型 / 用途维度或按模型筛选时这两项为 0）。 */
function usageGroups(
	deps: StatsDeps,
	dim: Dim,
	s: Scope,
): Map<string, UsageStat> {
	const F = facts(deps);
	const accs = new Map<string, Acc>();
	const get = (k: string) => {
		const a = accs.get(k) ?? acc(k);
		accs.set(k, a);
		return a;
	};
	for (const f of F.usage) if (inScope(f, s)) addFact(get(dimKey(dim, f)), f);
	const out = new Map<string, UsageStat>();
	for (const [k, a] of accs) out.set(k, finish(a));
	if (s.model === undefined && dim !== "model" && dim !== "purpose") {
		const at = (k: string) => {
			const x = out.get(k) ?? emptyStat(k);
			out.set(k, x);
			return x;
		};
		for (const t of F.tools)
			if (inScope(t, s)) {
				const x = at(dimKey(dim, t));
				x.toolCalls++;
				if (t.error) x.toolErrors++;
			}
		for (const t of F.turns) if (inScope(t, s)) at(dimKey(dim, t)).turns++;
	}
	return out;
}

function totalOf(groups: Iterable<UsageStat>): UsageStat {
	const t = emptyStat("total");
	let ttft = 0;
	let n = 0;
	let tps = 0;
	let m = 0;
	for (const g of groups) {
		merge(t, g);
		if (g.avgTtftMs !== undefined) {
			ttft += g.avgTtftMs * g.requests;
			n += g.requests;
		}
		if (g.tokensPerSecond !== undefined) {
			tps += g.tokensPerSecond * g.requests;
			m += g.requests;
		}
	}
	if (n > 0) t.avgTtftMs = ttft / n;
	if (m > 0) t.tokensPerSecond = tps / m;
	return t;
}

/** 成本降序取前 limit，其余并成 __other__。 */
function fold(groups: UsageStat[], limit: number): UsageStat[] {
	const sorted = [...groups].sort(
		(a, b) => b.costUsd - a.costUsd || a.key.localeCompare(b.key),
	);
	if (sorted.length <= limit) return sorted;
	const rest = sorted.slice(limit);
	const other = emptyStat(OTHER);
	for (const g of rest) merge(other, g);
	return [...sorted.slice(0, limit), other];
}

function usageStats(deps: StatsDeps, url: URL): Json | Response {
	const q = url.searchParams;
	const groupBy = q.get("groupBy") ?? "total";
	const series = q.get("series") ?? undefined;
	const limit = Math.min(500, Math.max(1, Number(q.get("limit") ?? 50)));
	const s = scopeOf(url);
	if (s.from >= s.to)
		return json({ code: "invalid_request", message: "from ≥ to" }, 400);
	if (GRAINS.includes(groupBy) && series)
		return json(
			{ code: "invalid_request", message: "series with time groupBy" },
			400,
		);
	const base = {
		groupBy,
		from: s.from,
		to: s.to,
		tz: q.get("tz") ?? "UTC",
	};
	if (GRAINS.includes(groupBy)) {
		const cells = grid(s.from, s.to, groupBy as Grain);
		const groups = cells.map((b) => {
			const g =
				usageGroups(deps, "total", {
					...s,
					from: b.start,
					to: Math.min(s.to, bucketEnd(b.start, groupBy as Grain)),
				}).get("total") ?? emptyStat("total");
			return { ...g, key: b.key, start: b.start };
		});
		return { ...base, total: totalOf(groups), groups };
	}
	const dim = groupBy as Dim;
	const all = [...usageGroups(deps, dim, s).values()];
	const label = labeler(deps, dim);
	const withLabel = (g: UsageStat) => {
		const l = label(g.key);
		return l ? { ...g, label: l } : g;
	};
	const groups = fold(all, limit).map(withLabel);
	const keys = new Set(groups.map((g) => g.key));
	const out: Json = {
		...base,
		total: { ...totalOf(all), key: "total" },
		groups,
	};
	if (series && GRAINS.includes(series)) {
		out.series = grid(s.from, s.to, series as Grain).map((b) => {
			const sub = {
				...s,
				from: b.start,
				to: Math.min(s.to, bucketEnd(b.start, series as Grain)),
			};
			const inner = [...usageGroups(deps, dim, sub).values()];
			const byKey = new Map<string, UsageStat>();
			for (const g of inner) {
				const k = keys.has(g.key) ? g.key : OTHER;
				const cur = byKey.get(k) ?? emptyStat(k);
				merge(cur, g);
				byKey.set(k, cur);
			}
			return {
				key: b.key,
				start: b.start,
				total: { ...totalOf(inner), key: "total" },
				groups: [...byKey.values()].filter(
					(g) => g.requests > 0 || g.toolCalls > 0,
				),
			};
		});
	}
	return out;
}

function bucketEnd(start: number, grain: Grain): number {
	const d = new Date(start);
	if (grain === "hour") return start + HOUR;
	if (grain === "day")
		return new Date(d.getFullYear(), d.getMonth(), d.getDate() + 1).getTime();
	if (grain === "week") {
		const back = (d.getDay() + 6) % 7;
		return new Date(
			d.getFullYear(),
			d.getMonth(),
			d.getDate() - back + 7,
		).getTime();
	}
	return new Date(d.getFullYear(), d.getMonth() + 1, 1).getTime();
}

function toolStats(deps: StatsDeps, url: URL): Json {
	const s = scopeOf(url);
	const limit = Math.min(
		500,
		Math.max(1, Number(url.searchParams.get("limit") ?? 50)),
	);
	const by = new Map<string, { tool: string; calls: number; errors: number }>();
	const total = { tool: "", calls: 0, errors: 0 };
	for (const t of facts(deps).tools)
		if (inScope(t, { ...s, model: undefined })) {
			const x = by.get(t.tool) ?? { tool: t.tool, calls: 0, errors: 0 };
			x.calls++;
			total.calls++;
			if (t.error) {
				x.errors++;
				total.errors++;
			}
			by.set(t.tool, x);
		}
	const sorted = [...by.values()].sort((a, b) => b.calls - a.calls);
	const tools = sorted.slice(0, limit);
	if (sorted.length > limit) {
		const rest = sorted.slice(limit);
		tools.push({
			tool: OTHER,
			calls: rest.reduce((a, x) => a + x.calls, 0),
			errors: rest.reduce((a, x) => a + x.errors, 0),
		});
	}
	return {
		from: s.from,
		to: s.to,
		tz: url.searchParams.get("tz") ?? "UTC",
		total,
		tools,
	};
}

// ── orb ──

/** 每台 orb 的确定性日程：一天里运行若干小时、冻结若干小时，其余停机。 */
function orbDay(
	orb: Orb,
	idx: number,
	dayStartTs: number,
): { running: number; frozen: number } {
	const r = rng(idx * 7919 + Math.floor(dayStartTs / DAY));
	// 规格越大的 orb 用得越省：运行时长按核数打个折
	const running = (1 + r() * 7) * HOUR * (orb.cpus >= 8 ? 0.6 : 1);
	const frozen = (2 + r() * 10) * HOUR;
	return { running, frozen };
}

const ORB_SPECS = [
	{ type: "medium", cpus: 4, memoryMB: 8192 },
	{ type: "small", cpus: 2, memoryMB: 4096 },
	{ type: "large", cpus: 8, memoryMB: 16384 },
] as const;

function orbStatFor(orb: Orb, idx: number, from: number, to: number): OrbStat {
	let running = 0;
	let frozen = 0;
	let wakes = 0;
	for (let k = 0; k < HISTORY_DAYS; k++) {
		const d = dayStart(k);
		if (d + DAY <= from) break;
		if (d >= to) continue;
		const overlap =
			Math.max(0, Math.min(to, d + DAY, NOW) - Math.max(from, d)) / DAY;
		if (overlap <= 0) continue;
		const x = orbDay(orb, idx, d);
		running += x.running * overlap;
		frozen += x.frozen * overlap;
		if (x.running > 0) wakes++;
	}
	// mock 里的 orb 都当作长期存在（夹具的 createdAt 是近期时刻，按它截会让历史图表几乎全空）
	const span = Math.max(0, Math.min(to, NOW) - from);
	const stopped = Math.max(0, span - running - frozen);
	const created = orb.createdAt >= from && orb.createdAt < to;
	return {
		key: orb.id,
		// 夹具里的 orb 都是 medium：统计里按序号轮换三种规格，按类型表才有内容可看
		...ORB_SPECS[idx % ORB_SPECS.length],
		orbs: running + frozen > 0 ? 1 : 0,
		runningMs: Math.round(running),
		frozenMs: Math.round(frozen),
		stoppedMs: Math.round(stopped),
		setupMs: Math.round(running * 0.03),
		spawns: created ? 1 : 0,
		snapshotSpawns: created && orb.snapshotOf ? 1 : 0,
		freezes: wakes,
		stops: Math.floor(wakes / 3),
		wakes,
		failures: orb.state === "failed" ? 1 : 0,
	};
}

function sumOrbs(key: string, xs: OrbStat[]): OrbStat {
	const t: OrbStat = {
		key,
		orbs: 0,
		runningMs: 0,
		frozenMs: 0,
		stoppedMs: 0,
		setupMs: 0,
		spawns: 0,
		snapshotSpawns: 0,
		freezes: 0,
		stops: 0,
		wakes: 0,
		failures: 0,
	};
	for (const x of xs) {
		t.orbs += x.orbs;
		t.runningMs += x.runningMs;
		t.frozenMs += x.frozenMs;
		t.stoppedMs += x.stoppedMs;
		t.setupMs += x.setupMs;
		t.spawns += x.spawns;
		t.snapshotSpawns += x.snapshotSpawns;
		t.freezes += x.freezes;
		t.stops += x.stops;
		t.wakes += x.wakes;
		t.failures += x.failures;
	}
	return t;
}

function orbStats(deps: StatsDeps, url: URL): Json {
	const q = url.searchParams;
	const s = scopeOf(url);
	const groupBy = q.get("groupBy") ?? "type";
	const project = q.get("project");
	const only = q.get("orb");
	const orbs = deps
		.orbs()
		.filter((o) => !o.coordinator)
		.map((o, i) => ({ o, i }))
		.filter(
			({ o }) =>
				(project === null ||
					(o.projectId ?? "") === (project === "none" ? "" : project)) &&
				(only === null || o.id === only),
		);
	const all = orbs.map(({ o, i }) => orbStatFor(o, i, s.from, s.to));
	let groups: OrbStat[];
	if (GRAINS.includes(groupBy)) {
		groups = grid(s.from, s.to, groupBy as Grain).map((b) => ({
			...sumOrbs(
				b.key,
				orbs.map(({ o, i }) =>
					orbStatFor(
						o,
						i,
						b.start,
						Math.min(s.to, bucketEnd(b.start, groupBy as Grain)),
					),
				),
			),
			start: b.start,
		}));
	} else if (groupBy === "orb") {
		const titles = new Map(deps.threads().map((t) => [t.id, t.title]));
		groups = all
			.filter((x) => x.runningMs + x.frozenMs > 0)
			.map((x, k) => {
				const orb = orbs[k]?.o;
				const label = orb?.threadIds.map((id) => titles.get(id)).find(Boolean);
				return label ? { ...x, label } : x;
			})
			.sort((a, b) => b.runningMs - a.runningMs);
	} else {
		const keyOf = (x: OrbStat, o: Orb) =>
			groupBy === "project" ? (o.projectId ?? "") : (x.type ?? "");
		const by = new Map<string, OrbStat[]>();
		all.forEach((x, k) => {
			const o = orbs[k]?.o;
			if (!o) return;
			const key = keyOf(x, o);
			by.set(key, [...(by.get(key) ?? []), x]);
		});
		const names = new Map(deps.projects().map((p) => [p.id, p.name]));
		groups = [...by.entries()].map(([key, xs]) => {
			const t = sumOrbs(key, xs);
			if (groupBy === "type")
				return {
					...t,
					type: key,
					cpus: xs[0]?.cpus,
					memoryMB: xs[0]?.memoryMB,
				};
			const label = names.get(key);
			return label ? { ...t, label } : t;
		});
		groups.sort((a, b) => b.runningMs - a.runningMs);
	}
	return {
		groupBy,
		from: s.from,
		to: s.to,
		tz: q.get("tz") ?? "UTC",
		total: sumOrbs("total", all),
		groups,
		setup: {
			runs: 14,
			failures: 1,
			slowRuns: 2,
			resumeRuns: 38,
			resumeFailures: 0,
			p50Ms: 48_200,
			p95Ms: 151_000,
			maxMs: 187_400,
		},
	};
}

function timeline(deps: StatsDeps, id: string, url: URL): Response {
	const orbs = deps.orbs();
	const idx = orbs.findIndex((o) => o.id === id);
	const orb = orbs[idx];
	if (!orb) return json({ code: "not_found", message: "orb" }, 404);
	const s = scopeOf(url);
	const to = Math.min(s.to, NOW);
	const from = s.from;
	type Seg = {
		from: number;
		to: number;
		state: Schemas["OrbState"];
		reason?: string;
	};
	const segs: Seg[] = [];
	const push = (a: number, b: number, state: Seg["state"]) => {
		const x = Math.max(a, from);
		const y = Math.min(b, to);
		if (y <= x) return;
		const last = segs[segs.length - 1];
		if (last && last.state === state && last.to === x) last.to = y;
		else segs.push({ from: x, to: y, state });
	};
	for (
		let d = Math.max(dayStart(HISTORY_DAYS), bucketOf(from, "day").start);
		d < to;
		d += DAY
	) {
		const x = orbDay(orb, idx, d);
		let t = d + 8 * HOUR;
		push(d, t, "stopped");
		push(t, t + 4 * 60_000, "resuming");
		t += 4 * 60_000;
		push(t, t + x.running, "ready");
		t += x.running;
		push(t, t + x.frozen, "frozen");
		t += x.frozen;
		push(t, d + DAY, "stopped");
	}
	if (orb.state === "failed" && segs.length) {
		const last = segs[segs.length - 1] as Seg;
		last.state = "failed";
		last.reason = orb.failureReason ?? "spawn: exit status 125";
	}
	return json({ orbId: id, from, to, segments: segs, truncated: false });
}

// ── 产出 ──

function outputStats(deps: StatsDeps, url: URL): Json {
	const q = url.searchParams;
	const s = scopeOf(url);
	const groupBy = q.get("groupBy") ?? "day";
	const day = (d: number) => {
		const r = rng(31 * Math.floor(d / DAY) + 7);
		const created = Math.round(1 + r() * 6);
		const shipped = Math.round(r() * created * 0.7);
		const archived = Math.round(r() * (created - shipped));
		return {
			created,
			shipped,
			archived,
			cost: shipped * (0.4 + r() * 3),
			commits: shipped * Math.round(1 + r() * 5),
			turns: created * Math.round(3 + r() * 9),
		};
	};
	const range = (from: number, to: number, share = 1): OutputStat => {
		const o: OutputStat = {
			key: "",
			threadsCreated: 0,
			threadsShipped: 0,
			threadsArchived: 0,
			shippedCostUsd: 0,
			commits: 0,
			turns: 0,
		};
		for (let d = bucketOf(from, "day").start; d < to && d <= NOW; d += DAY) {
			if (d < dayStart(HISTORY_DAYS)) continue;
			const x = day(d);
			o.threadsCreated += Math.round(x.created * share);
			o.threadsShipped += Math.round(x.shipped * share);
			o.threadsArchived += Math.round(x.archived * share);
			o.shippedCostUsd += x.cost * share;
			o.commits += Math.round(x.commits * share);
			o.turns += Math.round(x.turns * share);
		}
		return o;
	};
	const total = { ...range(s.from, s.to), key: "total" };
	let groups: OutputStat[];
	if (GRAINS.includes(groupBy))
		groups = grid(s.from, s.to, groupBy as Grain).map((b) => ({
			...range(b.start, Math.min(s.to, bucketEnd(b.start, groupBy as Grain))),
			key: b.key,
			start: b.start,
		}));
	else if (groupBy === "project") {
		const ps = deps.projects().filter((p) => !p.archivedAt);
		const shares = ps.map((_, i) => 1 / (i + 2));
		const sum = shares.reduce((a, x) => a + x, 0) + 0.2;
		groups = [
			...ps.map((p, i) => ({
				...range(s.from, s.to, (shares[i] ?? 0) / sum),
				key: p.id,
				label: p.name,
			})),
			{ ...range(s.from, s.to, 0.2 / sum), key: "" },
		];
	} else {
		const sp = deps.spaces().filter((x) => !x.archivedAt);
		groups = [
			...sp.map((x, i) => ({
				...range(s.from, s.to, 0.3 / (i + 1)),
				key: x.id,
				label: x.name,
			})),
			{ ...range(s.from, s.to, 0.5), key: "" },
		];
	}
	return {
		groupBy,
		from: s.from,
		to: s.to,
		tz: q.get("tz") ?? "UTC",
		total,
		groups,
	};
}

// ── 预算 ──

let budget: Schemas["Budget"] = {
	dailyUsd: 10,
	monthlyUsd: 200,
	warnPct: 80,
	timezone: "Asia/Shanghai",
	enabled: true,
};
/** 预算卡展示用的当期花费（与用量事实无关，固定为一警一超）。 */
const SPENT = { day: 8.16, month: 214.52 };

export function resetStatsMock() {
	budget = {
		dailyUsd: 10,
		monthlyUsd: 200,
		warnPct: 80,
		timezone: "Asia/Shanghai",
		enabled: true,
	};
	cache = null;
}

function level(
	spent: number,
	limit: number | undefined,
): Schemas["BudgetLevel"] {
	if (!limit) return "none";
	if (spent >= limit) return "over";
	if (budget.warnPct > 0 && spent >= (limit * budget.warnPct) / 100)
		return "warn";
	return "none";
}

function budgetStatus(): Schemas["BudgetStatus"] {
	const d = new Date(NOW);
	const dayFrom = dayStart(0);
	const monthFrom = new Date(d.getFullYear(), d.getMonth(), 1).getTime();
	return {
		budget,
		day: {
			key: `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`,
			from: dayFrom,
			to: dayFrom + DAY,
			spentUsd: SPENT.day,
			...(budget.dailyUsd ? { limitUsd: budget.dailyUsd } : {}),
			level: level(SPENT.day, budget.dailyUsd),
		},
		month: {
			key: `${d.getFullYear()}-${pad(d.getMonth() + 1)}`,
			from: monthFrom,
			to: new Date(d.getFullYear(), d.getMonth() + 1, 1).getTime(),
			spentUsd: SPENT.month,
			...(budget.monthlyUsd ? { limitUsd: budget.monthlyUsd } : {}),
			level: level(SPENT.month, budget.monthlyUsd),
		},
	};
}

// ── CSV ──

function csv(
	name: string,
	header: string[],
	rows: (string | number | undefined)[][],
): Response {
	const esc = (v: string | number | undefined) => {
		const s = v === undefined ? "" : String(v);
		return /[",\n]/.test(s) ? `"${s.replaceAll('"', '""')}"` : s;
	};
	const text = [header, ...rows].map((r) => r.map(esc).join(",")).join("\n");
	return new Response(`${text}\n`, {
		headers: {
			"Content-Type": "text/csv; charset=utf-8",
			"Content-Disposition": `attachment; filename="${name}.csv"`,
		},
	});
}

function usageCsv(body: Json): Response {
	const header = [
		"bucket",
		"key",
		"label",
		"requests",
		"request_errors",
		"input_tokens",
		"output_tokens",
		"cache_read_tokens",
		"cache_write_tokens",
		"cost_usd",
		"subagent_cost_usd",
		"avg_ttft_ms",
		"tokens_per_second",
		"tool_calls",
		"tool_errors",
		"turns",
	];
	const row = (bucket: string, g: UsageStat) => [
		bucket,
		g.key,
		g.label,
		g.requests,
		g.requestErrors,
		g.inputTokens,
		g.outputTokens,
		g.cacheReadTokens,
		g.cacheWriteTokens,
		g.costUsd,
		g.subagentCostUsd,
		g.avgTtftMs,
		g.tokensPerSecond,
		g.toolCalls,
		g.toolErrors,
		g.turns,
	];
	const series = body.series as Schemas["UsageSeriesPoint"][] | undefined;
	const rows = series
		? series.flatMap((p) => p.groups.map((g) => row(p.key, g)))
		: (body.groups as UsageStat[]).map((g) => row("", g));
	return csv(`oryn-usage-${body.groupBy}`, header, rows);
}

function recordsCsv(deps: StatsDeps, url: URL): Response {
	const s = scopeOf(url);
	const rows = facts(deps)
		.usage.filter((f) => inScope(f, s))
		.map((f) => [
			f.ts,
			new Date(f.ts).toISOString(),
			f.thread,
			f.project,
			f.space,
			f.model.split("/")[0],
			f.model.split("/").slice(1).join("/"),
			f.purpose,
			f.subagent ? 1 : 0,
			f.error ? "error" : "stop",
			f.input,
			f.output,
			f.cacheRead,
			f.cacheWrite,
			f.cost,
			Math.round(f.duration),
			Math.round(f.ttft),
		]);
	return csv(
		"oryn-usage-records",
		[
			"ts",
			"time",
			"thread_id",
			"project_id",
			"space_id",
			"provider",
			"model",
			"purpose",
			"subagent",
			"stop_reason",
			"input_tokens",
			"output_tokens",
			"cache_read_tokens",
			"cache_write_tokens",
			"cost_usd",
			"duration_ms",
			"ttft_ms",
		],
		rows,
	);
}

/** 按 `format` / `download` 包装应答：csv 走各自的列；`download=true` 的 JSON 带附件头。 */
function respond(kind: string, body: Json | Response, url: URL): Response {
	if (body instanceof Response) return body;
	const q = url.searchParams;
	if (q.get("format") === "csv") {
		if (kind === "usage") return usageCsv(body);
		const groups = (body.groups ?? body.tools) as Json[];
		const keys = groups[0] ? Object.keys(groups[0]) : ["key"];
		return csv(
			`oryn-${kind}`,
			keys,
			groups.map((g) => keys.map((k) => g[k] as string)),
		);
	}
	if (q.get("download") === "true")
		return json(body, 200, {
			"Content-Disposition": `attachment; filename="oryn-${kind}-${String(body.groupBy ?? "")}.json"`,
		});
	return json(body);
}

/** `p` 是去掉 `/api/v1/` 的路径；不归这里的请求返回 null。 */
export async function handleStatsFetch(
	req: Request,
	p: string,
	m: string,
	url: URL,
	deps: StatsDeps,
): Promise<Response | null> {
	const tl = /^orbs\/([^/]+)\/timeline$/.exec(p);
	if (tl && m === "GET") return timeline(deps, tl[1] ?? "", url);
	if (!p.startsWith("stats/")) return null;
	const sub = p.slice("stats/".length);
	if (sub === "meta")
		return json({
			now: Date.now(),
			factsVersion: 2,
			earliestTs: facts(deps).usage[0]?.ts,
			retention: { samplesMs: 720 * HOUR, eventsMs: 8760 * HOUR, usageMs: 0 },
		});
	if (sub === "usage") return respond("usage", usageStats(deps, url), url);
	if (sub === "usage/records") return recordsCsv(deps, url);
	if (sub === "tools") return respond("tools", toolStats(deps, url), url);
	if (sub === "orbs") return respond("orbs", orbStats(deps, url), url);
	if (sub === "output") return respond("output", outputStats(deps, url), url);
	if (sub === "budget") {
		if (m === "PUT") {
			let b: Schemas["Budget"];
			try {
				b = (await req.json()) as Schemas["Budget"];
			} catch {
				return json({ code: "invalid_budget", message: "body" }, 400);
			}
			const bad = (v: number | undefined) => v !== undefined && !(v >= 0.01);
			if (
				bad(b.dailyUsd) ||
				bad(b.monthlyUsd) ||
				!(b.warnPct >= 0 && b.warnPct <= 99)
			)
				return json({ code: "invalid_budget", message: "invalid budget" }, 400);
			budget = { ...b };
		}
		return json(budgetStatus());
	}
	return json({ code: "not_found", message: p }, 404);
}
