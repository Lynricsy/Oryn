/**
 * mock 后端的 M9 部分：Space、全局助手对话、定时（线程内 / 每次新建线程）及其运行记录、操作记录、协调宿主状态，
 * 格式与 orynd 一致。`MockServer.handleFetch` 开头先调 `handle`，不归这里的请求返回 null。
 * 协调宿主默认可用；`localStorage["oryn.mock.coordinator"] = "down"` 时模拟不可用（版本钉不符），新建 Space 与助手对话回 409。
 * 预览只认 cron 的数字 / `*` / 区间 / 列表 / 步长（够 mock 用），时区按浏览器本地时间算。
 */
import type { Schemas, Thread } from "../api/client";
import {
	automationActions,
	coordinatorDown,
	coordinatorStatus,
	M9_IDS,
	scheduleRuns,
	schedules,
	spaceProgress,
	spaces,
} from "./fixtures";

type Json = Record<string, unknown>;
type Schedule = Schemas["Schedule"];
type ScheduleInput = Schemas["ScheduleInput"];
type ScheduleRun = Schemas["ScheduleRun"];
type Space = Schemas["Space"];

const MIN = 60_000;

export interface AutomationDeps {
	threads: Map<string, Thread>;
	/** 新线程：登记条目 / 会话状态并推送 index.patch */
	addThread(t: Thread): void;
	upsertThread(t: Thread): void;
	removeThread(id: string): void;
}

function json(body: unknown, status = 200): Response {
	return Response.json(body, { status });
}

function error(status: number, code: string, message: string): Response {
	return json({ code, message }, status);
}

let seq = 0;
function ulid(prefix: string): string {
	seq++;
	return `${prefix}_01JDM${String(seq).padStart(21, "0")}`;
}

function coordinatorDownMocked(): boolean {
	try {
		return globalThis.localStorage?.getItem("oryn.mock.coordinator") === "down";
	} catch {
		return false;
	}
}

// ---------------------------------------------------------------- 时间计算

/** Go 时长串（`30m`、`6h0m0s`）→ 毫秒；不合法为 null。 */
function durationMs(s: string | undefined): number | null {
	if (!s) return null;
	const re = /(\d+)(h|m|s)/g;
	let total = 0;
	let consumed = 0;
	for (const m of s.matchAll(re)) {
		total += Number(m[1]) * (m[2] === "h" ? 3600 : m[2] === "m" ? 60 : 1);
		consumed += m[0].length;
	}
	return consumed === s.length && consumed > 0 ? total * 1000 : null;
}

/** 规范化成 orynd 的时长串（`6h0m0s`）。 */
function normalizeEvery(ms: number): string {
	const sec = Math.round(ms / 1000);
	return `${Math.floor(sec / 3600)}h${Math.floor((sec % 3600) / 60)}m${sec % 60}s`;
}

const MACROS: Record<string, string> = {
	"@hourly": "0 * * * *",
	"@daily": "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly": "0 0 * * 0",
	"@monthly": "0 0 1 * *",
	"@yearly": "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
};

/** cron 一段 → 允许的取值集合；不合法为 null。 */
function field(spec: string, lo: number, hi: number): Set<number> | null {
	const out = new Set<number>();
	for (const part of spec.split(",")) {
		const m = /^(\*|(\d+)(?:-(\d+))?)(?:\/(\d+))?$/.exec(part);
		if (!m) return null;
		const start = m[1] === "*" ? lo : Number(m[2]);
		const end = m[1] === "*" ? hi : m[3] !== undefined ? Number(m[3]) : start;
		const step = m[4] !== undefined ? Number(m[4]) : 1;
		if (start < lo || end > hi || start > end || step < 1) return null;
		for (let v = start; v <= end; v += step) out.add(v);
	}
	return out;
}

/** 接下来至多 `n` 次触发（逐分钟扫描 14 天）；表达式不合法时抛错。 */
function cronNext(expr: string, from: number, n: number): number[] {
	const parts = (MACROS[expr.trim()] ?? expr).trim().split(/\s+/);
	if (parts.length !== 5) throw new Error("cron 须恰好 5 段");
	const [mi, ho, dom, mon, dow] = [
		field(parts[0] ?? "", 0, 59),
		field(parts[1] ?? "", 0, 23),
		field(parts[2] ?? "", 1, 31),
		field(parts[3] ?? "", 1, 12),
		field((parts[4] ?? "").replace(/\b7\b/g, "0"), 0, 6),
	];
	if (!mi || !ho || !dom || !mon || !dow)
		throw new Error(`cron 不合法：${expr}`);
	const out: number[] = [];
	const d = new Date(from);
	d.setSeconds(0, 0);
	for (let i = 0; i < 14 * 24 * 60 && out.length < n; i++) {
		d.setMinutes(d.getMinutes() + 1);
		if (
			mi.has(d.getMinutes()) &&
			ho.has(d.getHours()) &&
			dom.has(d.getDate()) &&
			mon.has(d.getMonth() + 1) &&
			dow.has(d.getDay())
		)
			out.push(d.getTime());
	}
	return out;
}

/** 按时间字段算接下来的触发；不合法时返回错误文字。 */
function nextRuns(b: ScheduleInput, n = 5): number[] | string {
	const now = Date.now();
	const until = (list: number[]) =>
		b.endsAt !== undefined ? list.filter((t) => t <= (b.endsAt ?? 0)) : list;
	if (b.kind === "every") {
		const ms = durationMs(b.every);
		if (ms === null || ms < MIN) return "every 须是 ≥1m 的时长串";
		return until(Array.from({ length: n }, (_, i) => now + ms * (i + 1)));
	}
	if (b.kind === "once") {
		if (b.at === undefined || b.at < now - MIN) return "at 不得早于当前时间";
		return [b.at];
	}
	if (b.kind === "cron") {
		try {
			return until(cronNext(b.cron ?? "", now, n));
		} catch (e) {
			return (e as Error).message;
		}
	}
	return "kind 须为 cron / every / once";
}

// ---------------------------------------------------------------- 状态

export class MockAutomation {
	private readonly deps: AutomationDeps;
	readonly spaces: Space[] = spaces.map((s) => ({ ...s }));
	readonly schedules: Schedule[] = schedules.map((s) => ({ ...s }));
	readonly runs = new Map<string, ScheduleRun[]>(
		Object.entries(scheduleRuns).map(([k, v]) => [k, v.map((r) => ({ ...r }))]),
	);
	readonly actions: Schemas["AutomationAction"][] = automationActions.map(
		(a) => ({ ...a }),
	);

	constructor(deps: AutomationDeps) {
		this.deps = deps;
	}

	private coordinator(): Schemas["CoordinatorStatus"] {
		return coordinatorDownMocked() ? coordinatorDown : coordinatorStatus;
	}

	/** 新建一个挂在协调 orb 上的协调对话线程。 */
	private coordinatorThread(
		kind: "space" | "assistant",
		title: string,
		extra: Partial<Thread> = {},
	): Thread {
		const now = Date.now();
		const t: Thread = {
			id: `T-mock-coord-${(++seq).toString(16)}`,
			title,
			titleSource: "auto",
			state: "idle",
			stateChangedAt: now,
			pinned: false,
			labels: [],
			unread: false,
			createdAt: now,
			updatedAt: now,
			kind,
			orbId: M9_IDS.coordOrb,
			...extra,
		};
		this.deps.addThread(t);
		return t;
	}

	private setThreadSummary(threadId: string) {
		const t = this.deps.threads.get(threadId);
		if (!t) return;
		const s = this.schedules.find(
			(x) => x.mode === "thread" && x.threadId === threadId,
		);
		const { schedule: _, ...rest } = t;
		this.deps.upsertThread(
			s
				? {
						...rest,
						schedule: {
							enabled: s.enabled,
							...(s.nextRunAt !== undefined ? { nextRunAt: s.nextRunAt } : {}),
						},
					}
				: rest,
		);
	}

	/** 请求体 → 定义（新建或替换）；不合法时返回错误响应。 */
	private build(
		b: ScheduleInput,
		existing: Schedule | undefined,
		forceThread?: string,
	): Schedule | Response {
		const mode = forceThread ? "thread" : (existing?.mode ?? b.mode);
		if (mode !== "thread" && mode !== "new_thread")
			return error(400, "invalid_schedule", "mode 须为 thread / new_thread");
		const threadId = forceThread ?? existing?.threadId ?? b.threadId;
		if (mode === "thread" && !threadId)
			return error(400, "invalid_schedule", "thread 模式须给 threadId");
		if (existing && b.mode && b.mode !== existing.mode)
			return error(400, "invalid_schedule", "mode 不可改");
		if (
			existing &&
			mode === "new_thread" &&
			b.projectId !== undefined &&
			b.projectId !== existing.projectId
		)
			return error(400, "invalid_schedule", "projectId 不可改");
		const name = (b.name ?? "").trim();
		if (mode === "new_thread" && !name)
			return error(400, "invalid_schedule", "new_thread 模式须给 name");
		if (!b.prompt?.trim())
			return error(400, "invalid_schedule", "prompt 不能为空");
		if (mode === "thread") {
			const t = threadId ? this.deps.threads.get(threadId) : undefined;
			if (!t) return error(404, "not_found", "thread");
			if (t.archivedAt) return error(409, "thread_archived", "thread archived");
		}
		const projectId =
			mode === "new_thread"
				? (existing?.projectId ?? b.projectId ?? undefined)
				: undefined;
		if (
			mode === "new_thread" &&
			this.schedules.some(
				(s) =>
					s.id !== existing?.id &&
					s.mode === "new_thread" &&
					(s.projectId ?? null) === (projectId ?? null) &&
					s.name === name,
			)
		)
			return error(409, "schedule_name_taken", `已有同名定时「${name}」`);
		const next = nextRuns(b);
		if (typeof next === "string") return error(400, "invalid_schedule", next);
		const enabled = b.enabled !== false;
		const now = Date.now();
		const every = b.kind === "every" ? durationMs(b.every) : null;
		const s: Schedule = {
			id: existing?.id ?? ulid("sch"),
			mode,
			...(threadId && mode === "thread" ? { threadId } : {}),
			...(projectId ? { projectId } : {}),
			...((existing?.spaceId ?? b.spaceId)
				? { spaceId: existing?.spaceId ?? b.spaceId }
				: {}),
			name,
			kind: b.kind,
			...(b.kind === "cron" ? { cron: b.cron?.trim() } : {}),
			...(every !== null ? { every: normalizeEvery(every) } : {}),
			...(b.kind === "once" ? { at: b.at } : {}),
			timezone: b.timezone || "UTC",
			...(b.endsAt !== undefined ? { endsAt: b.endsAt } : {}),
			prompt: b.prompt,
			enabled,
			...(mode === "new_thread" && b.model ? { model: b.model } : {}),
			...(mode === "new_thread" && b.thinkingLevel
				? { thinkingLevel: b.thinkingLevel }
				: {}),
			...(mode === "new_thread" && b.provider ? { provider: b.provider } : {}),
			...(mode === "new_thread" && b.orbType ? { orbType: b.orbType } : {}),
			overlap: mode === "new_thread" ? (b.overlap ?? "skip") : "skip",
			keepLast: mode === "new_thread" ? (b.keepLast ?? 5) : 5,
			...(enabled ? {} : { pausedReason: "user" as const }),
			...(enabled && next[0] !== undefined ? { nextRunAt: next[0] } : {}),
			...(existing?.lastRunAt !== undefined
				? { lastRunAt: existing.lastRunAt }
				: {}),
			runCount: existing?.runCount ?? 0,
			failures: 0,
			createdBy: existing?.createdBy ?? "user",
			createdAt: existing?.createdAt ?? now,
			updatedAt: now,
			...(existing?.lastRun ? { lastRun: existing.lastRun } : {}),
			openRuns: existing?.openRuns ?? 0,
			rev: existing ? existing.rev + 1 : 1,
		};
		return s;
	}

	private save(s: Schedule) {
		const i = this.schedules.findIndex((x) => x.id === s.id);
		if (i >= 0) this.schedules[i] = s;
		else this.schedules.push(s);
		if (s.mode === "thread" && s.threadId) this.setThreadSummary(s.threadId);
	}

	private remove(s: Schedule) {
		const i = this.schedules.findIndex((x) => x.id === s.id);
		if (i >= 0) this.schedules.splice(i, 1);
		this.runs.delete(s.id);
		if (s.mode === "thread" && s.threadId) this.setThreadSummary(s.threadId);
	}

	/** 立即运行：`new_thread` 建一个运行线程（带 `scheduleRun`），`thread` 只记一条运行。 */
	private runNow(s: Schedule): Response {
		const busy =
			s.openRuns > 0 && (s.mode === "thread" || s.overlap === "skip");
		if (busy) return error(409, "schedule_busy", "上一次运行还没结束");
		const now = Date.now();
		const n = s.runCount + 1;
		let threadId = s.mode === "thread" ? s.threadId : undefined;
		if (s.mode === "new_thread") {
			const d = new Date(now);
			const pad = (v: number) => String(v).padStart(2, "0");
			const t: Thread = {
				id: `T-mock-run-${(++seq).toString(16)}`,
				title: `${s.name} · ${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`,
				titleSource: "auto",
				state: "working",
				stateChangedAt: now,
				pinned: false,
				labels: [],
				unread: false,
				createdAt: now,
				updatedAt: now,
				lastMessageAt: now,
				...(s.projectId ? { projectId: s.projectId } : {}),
				...(s.spaceId ? { spaceId: s.spaceId } : {}),
				scheduleRun: { scheduleId: s.id, run: n },
			};
			this.deps.addThread(t);
			threadId = t.id;
		}
		const r: ScheduleRun = {
			id: ulid("srn"),
			scheduleId: s.id,
			...(threadId ? { threadId } : {}),
			trigger: "manual",
			scheduledFor: now,
			missed: 0,
			startedAt: now,
			deliveredAt: now,
			status: "running",
		};
		this.runs.set(s.id, [r, ...(this.runs.get(s.id) ?? [])]);
		this.save({
			...s,
			runCount: n,
			lastRunAt: now,
			lastRun: r,
			openRuns: s.openRuns + 1,
		});
		return json(r);
	}

	async handle(
		req: Request,
		p: string,
		m: string,
		url: URL,
	): Promise<Response | null> {
		const body = async (): Promise<Json> => {
			try {
				return (await req.json()) as Json;
			} catch {
				return {};
			}
		};
		if (p === "coordinator" && m === "GET") return json(this.coordinator());
		if (p === "automation/actions" && m === "GET") {
			const limit = Number(url.searchParams.get("limit") ?? 50);
			const thread = url.searchParams.get("thread");
			const list = this.actions.filter(
				(a) =>
					!thread || a.actorThreadId === thread || a.targetThreadId === thread,
			);
			return json({ actions: list.slice(0, limit) });
		}
		if (p === "assistant/conversations" && m === "POST") {
			const c = this.coordinator();
			if (!c.available)
				return error(409, "coordinator_unavailable", c.reason ?? "unavailable");
			return json(
				{ thread: this.coordinatorThread("assistant", "新对话") },
				201,
			);
		}
		if (p === "spaces") {
			if (m === "GET") {
				const archived = url.searchParams.get("archived") === "true";
				return json({
					spaces: this.spaces
						.filter((s) => Boolean(s.archivedAt) === archived)
						.sort((a, b) => a.name.localeCompare(b.name)),
				});
			}
			if (m === "POST") {
				const b = (await body()) as Schemas["SpaceCreate"];
				const c = this.coordinator();
				if (!c.available)
					return error(
						409,
						"coordinator_unavailable",
						c.reason ?? "unavailable",
					);
				const name = (b.name ?? "").trim();
				if (!name) return error(400, "invalid_request", "name 不能为空");
				if (this.spaces.some((s) => s.name === name))
					return error(409, "space_name_taken", `已有同名空间「${name}」`);
				const id = ulid("spc");
				const chat = this.coordinatorThread("space", name, {
					spaceId: id,
					...(b.projectId ? { projectId: b.projectId } : {}),
				});
				const now = Date.now();
				const s: Space = {
					id,
					name,
					goal: b.goal ?? "",
					...(b.projectId ? { projectId: b.projectId } : {}),
					threadId: chat.id,
					createdAt: now,
					updatedAt: now,
				};
				this.spaces.push(s);
				return json(s, 201);
			}
		}
		const spaceMatch = /^spaces\/([^/]+)(\/progress)?$/.exec(p);
		if (spaceMatch) {
			const i = this.spaces.findIndex((s) => s.id === spaceMatch[1]);
			const cur = this.spaces[i];
			if (!cur) return error(404, "space_not_found", "space");
			if (spaceMatch[2]) {
				const days = Number(url.searchParams.get("days") ?? 30);
				return json({ days: spaceProgress(days) });
			}
			if (m === "PATCH") {
				const b = (await body()) as Schemas["SpacePatch"];
				const next: Space = { ...cur, updatedAt: Date.now() };
				if (typeof b.name === "string") next.name = b.name.trim();
				if (typeof b.goal === "string") next.goal = b.goal;
				if (b.projectId !== undefined) {
					if (b.projectId) next.projectId = b.projectId;
					else delete next.projectId;
				}
				if (b.archived !== undefined)
					next.archivedAt = b.archived ? Date.now() : undefined;
				this.spaces[i] = next;
				const chat = this.deps.threads.get(cur.threadId);
				if (chat)
					this.deps.upsertThread({
						...chat,
						title: next.name,
						archivedAt: next.archivedAt,
					});
				return json(next);
			}
			if (m === "DELETE") {
				this.spaces.splice(i, 1);
				this.deps.removeThread(cur.threadId);
				for (const t of [...this.deps.threads.values()])
					if (t.spaceId === cur.id) {
						const { spaceId: _, ...rest } = t;
						this.deps.upsertThread(rest);
					}
				for (const s of this.schedules)
					if (s.spaceId === cur.id) delete s.spaceId;
				return new Response(null, { status: 204 });
			}
			return json(cur);
		}
		if (p === "schedules/preview" && m === "POST") {
			const next = nextRuns((await body()) as ScheduleInput);
			return typeof next === "string"
				? error(400, "invalid_schedule", next)
				: json({ next });
		}
		if (p === "schedules") {
			if (m === "GET") {
				const q = url.searchParams;
				const mode = q.get("mode");
				const project = q.get("project");
				const space = q.get("space");
				const thread = q.get("thread");
				const spaceThreads = new Set(
					[...this.deps.threads.values()]
						.filter((t) => space && t.spaceId === space)
						.map((t) => t.id),
				);
				return json({
					schedules: this.schedules.filter(
						(s) =>
							(!mode || s.mode === mode) &&
							(!project ||
								(project === "none"
									? s.mode === "new_thread" && !s.projectId
									: s.projectId === project)) &&
							(!space ||
								s.spaceId === space ||
								(s.threadId !== undefined && spaceThreads.has(s.threadId))) &&
							(!thread || s.threadId === thread),
					),
				});
			}
			if (m === "POST") {
				const b = (await body()) as ScheduleInput;
				if (!b.mode) return error(400, "invalid_schedule", "必须给 mode");
				if (
					b.mode === "thread" &&
					this.schedules.some((s) => s.threadId === b.threadId)
				)
					return error(409, "conflict", "该线程已有定时");
				const s = this.build(b, undefined);
				if (s instanceof Response) return s;
				this.save(s);
				return json(s, 201);
			}
		}
		const threadSchedule = /^threads\/([^/]+)\/schedule$/.exec(p);
		if (threadSchedule) {
			const threadId = threadSchedule[1] ?? "";
			const cur = this.schedules.find(
				(s) => s.mode === "thread" && s.threadId === threadId,
			);
			if (m === "PUT") {
				const s = this.build((await body()) as ScheduleInput, cur, threadId);
				if (s instanceof Response) return s;
				this.save(s);
				return json(s);
			}
			if (!cur) return error(404, "schedule_not_found", "schedule");
			if (m === "DELETE") {
				this.remove(cur);
				return new Response(null, { status: 204 });
			}
			return json(cur);
		}
		const one = /^schedules\/([^/]+)(?:\/(pause|resume|run|runs))?$/.exec(p);
		if (one) {
			const cur = this.schedules.find((s) => s.id === one[1]);
			if (!cur) return error(404, "schedule_not_found", "schedule");
			const action = one[2];
			if (action === "runs") {
				const limit = Number(url.searchParams.get("limit") ?? 20);
				return json({ runs: (this.runs.get(cur.id) ?? []).slice(0, limit) });
			}
			if (action === "run") return this.runNow(cur);
			if (action === "pause") {
				const { nextRunAt: _, ...rest } = cur;
				const next: Schedule = {
					...rest,
					enabled: false,
					pausedReason: "user",
					updatedAt: Date.now(),
				};
				this.save(next);
				return json(next);
			}
			if (action === "resume") {
				const at = nextRuns({ ...cur, prompt: cur.prompt });
				if (typeof at === "string" || at.length === 0)
					return error(400, "invalid_schedule", "已结束的定时不能恢复");
				const { pausedReason: _, ...rest } = cur;
				const next: Schedule = {
					...rest,
					enabled: true,
					failures: 0,
					nextRunAt: at[0],
					updatedAt: Date.now(),
				};
				this.save(next);
				return json(next);
			}
			if (m === "PUT") {
				const s = this.build((await body()) as ScheduleInput, cur);
				if (s instanceof Response) return s;
				this.save(s);
				return json(s);
			}
			if (m === "DELETE") {
				this.remove(cur);
				return new Response(null, { status: 204 });
			}
			return json(cur);
		}
		return null;
	}
}
