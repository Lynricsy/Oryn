/**
 * 角色模型轮换（Ctrl+P）：按 omp 的 `cycleOrder` 解析每个角色的模型，确定当前角色，按方向取下一个。
 * omp 的 RPC 没有「按角色循环」的命令（`cycle_model` 循环的是 scoped models），所以由网页端照
 * `resolveRoleModelCycle` / `resolveModelRoleValue`（omp `config/model-resolver.ts`）的规则自己算。
 */

/** omp `cycleOrder` 的默认值（`config/model-settings.ts`）。 */
export const DEFAULT_CYCLE_ORDER: readonly string[] = [
	"smol",
	"default",
	"slow",
];

/** `get_model_roles` 里本模块用到的字段（`RpcModelRoleEntry` 的子集）。 */
export interface RoleEntry {
	id: string;
	name: string;
	hidden?: boolean;
	section?: "chat" | "kind";
	/** 原样保留的配置值；未配置时缺省。 */
	model?: string;
	/** 配置值直接点名某个候选时才有；别名、回退链、不在候选里的模型都没有。 */
	selected?: { provider: string; id: string; thinkingLevel?: string };
	candidates?: readonly ModelRef[];
}

export interface ModelRef {
	provider: string;
	id: string;
	name?: string;
}

export type RoleStatus = "ok" | "unconfigured" | "unresolved";

/** 轨道里的一格：cycleOrder 里的每个角色都有一格，解析不出模型的置为禁用。 */
export interface RoleSlot {
	id: string;
	/** omp 元数据里的显示名（自定义角色就是它自己的名字）。 */
	name: string;
	status: RoleStatus;
	/** 原始配置值（`@Cyrene`、`openai/gpt-6.1-sol:high` …）。 */
	raw?: string;
	model?: ModelRef;
	/** 选择器里显式写出的思考级别；没写时沿用当前级别。 */
	thinkingLevel?: string;
}

/** 当前会话的模型与思考级别（`get_state` 的子集）。 */
export interface CurrentModel {
	provider: string;
	id: string;
	name?: string;
	thinkingLevel?: string;
}

/** 客户端记住的上一次选择（按线程存）。 */
export interface RoleMemory {
	role: string;
	provider: string;
	id: string;
	thinkingLevel?: string;
}

// ---------------------------------------------------------------- 选择器解析

const THINKING_LEVELS = [
	"inherit",
	"off",
	"minimal",
	"low",
	"medium",
	"high",
	"xhigh",
	"max",
] as const;

/**
 * 解析 `:level` 后缀（omp `parseThinkingSuffix` + `MAX_THINKING_SUFFIX_OPTIONS`）：
 * 完整名、无歧义的 ≥2 字符缩写（`xhi`、`med`），以及 `max`、`auto`。
 */
export function parseThinkingLevel(value: string): string | undefined {
	if ((THINKING_LEVELS as readonly string[]).includes(value)) return value;
	if (value === "auto") return "auto";
	if (value.length < 2) return undefined;
	const hits = THINKING_LEVELS.filter((l) => l.startsWith(value));
	return hits.length === 1 ? hits[0] : undefined;
}

/** 切掉末尾的 `:<level>`；冒号必须在 `minColon` 之后（别名前缀长度）。 */
export function splitThinkingSuffix(
	pattern: string,
	minColon = -1,
): { base: string; level?: string } {
	const i = pattern.lastIndexOf(":");
	if (i <= minColon) return { base: pattern };
	const level = parseThinkingLevel(pattern.slice(i + 1));
	return level ? { base: pattern.slice(0, i), level } : { base: pattern };
}

const ALIAS_PREFIXES = ["@", "pi/"];

/** 别名前缀长度（`@role`、`pi/role`、`*`），不是别名形状时返回 undefined。 */
function aliasPrefixLength(value: string): number | undefined {
	if (value === "*" || value.startsWith("*:")) return 0;
	return ALIAS_PREFIXES.find((p) => value.startsWith(p))?.length;
}

/** 内置角色在别处没配置时的回退（omp `ROLE_CONFIGURED_FALLBACK`）。 */
const CONFIGURED_FALLBACK: Record<
	string,
	{ role: string; configuredOnly: boolean }
> = {
	advisor: { role: "slow", configuredOnly: true },
	memory: { role: "tiny", configuredOnly: false },
	tiny: { role: "smol", configuredOnly: false },
	widget: { role: "smol", configuredOnly: false },
};

/** 逗号分隔的回退链（omp `normalizeModelPatternList`）。 */
function patternList(value: string): string[] {
	return value
		.split(",")
		.map((p) => p.trim())
		.filter(Boolean);
}

/**
 * 把一个配置值展开成具体的模型选择器列表（omp `resolveConfiguredModelPatterns`）：
 * 回退链按逗号拆开；`@role` / `pi/role` / `*` 递归展开成该角色的配置值，别名上的 `:level` 追加到每一项；
 * 已经走过的角色不再展开（防成环）。
 */
function expandPatterns(
	value: string,
	roles: ReadonlyMap<string, RoleEntry>,
	visited: ReadonlySet<string>,
): string[] {
	const out: string[] = [];
	for (const pattern of patternList(value)) {
		const prefix = aliasPrefixLength(pattern);
		const { base, level } = splitThinkingSuffix(
			pattern,
			prefix ?? "pi/".length,
		);
		const role =
			prefix === undefined
				? undefined
				: base === "*"
					? "default"
					: base.slice(prefix);
		// 只有已知角色才算别名，否则按字面模型选择器处理（同 omp `getModelRoleAlias`）
		if (role === undefined || !roles.has(role)) {
			out.push(pattern);
			continue;
		}
		if (visited.has(role)) continue;
		const next = new Set(visited).add(role);
		const configured = roles.get(role)?.model?.trim();
		let expanded: string[] = [];
		if (configured) expanded = expandPatterns(configured, roles, next);
		else {
			const fb = CONFIGURED_FALLBACK[role];
			const target = fb && roles.get(fb.role)?.model?.trim();
			if (fb && (target || !fb.configuredOnly))
				expanded = expandPatterns(`@${fb.role}`, roles, next);
			else if (role === "smol" || role === "slow") {
				const def = roles.get("default")?.model?.trim();
				if (def) expanded = expandPatterns(def, roles, next);
			}
			// 其余情况 omp 会用目录内置的默认优先级列表，RPC 拿不到，只能解析失败
		}
		out.push(...(level ? expanded.map((p) => `${p}:${level}`) : expanded));
	}
	return out;
}

const lower = (s: string) => s.toLowerCase();

/** 精确匹配：`provider/id`、裸 id、显示名，大小写不敏感；候选在前、可用模型在后。 */
function matchExact(
	pattern: string,
	pool: readonly ModelRef[],
): ModelRef | undefined {
	const p = lower(pattern);
	return (
		pool.find((m) => lower(`${m.provider}/${m.id}`) === p) ??
		pool.find((m) => lower(m.id) === p) ??
		pool.find((m) => m.name !== undefined && lower(m.name) === p)
	);
}

/**
 * 单个选择器 → 模型（omp `parseModelPatternWithContext` 的精确匹配部分）：
 * 先整串精确匹配（字面 id 里带 `:max` 的模型优先），再切合法的 `:level` 递归，最后把非法后缀当噪音切掉。
 */
function matchPattern(
	pattern: string,
	pool: readonly ModelRef[],
): { model?: ModelRef; level?: string } {
	const exact = matchExact(pattern, pool);
	if (exact) return { model: exact };
	const { base, level } = splitThinkingSuffix(pattern);
	if (level) {
		const inner = matchPattern(base, pool);
		return inner.model ? { model: inner.model, level } : {};
	}
	const colon = pattern.lastIndexOf(":");
	if (colon === -1) return {};
	return { model: matchPattern(pattern.slice(0, colon), pool).model };
}

/** 去重合并候选与可用模型（按 `provider/id`），候选优先。 */
function mergePool(
	a: readonly ModelRef[] = [],
	b: readonly ModelRef[] = [],
): ModelRef[] {
	const seen = new Set<string>();
	const out: ModelRef[] = [];
	for (const m of [...a, ...b]) {
		const k = `${m.provider}/${m.id}`;
		if (seen.has(k)) continue;
		seen.add(k);
		out.push(m);
	}
	return out;
}

/**
 * 解析一个角色配置值（omp `resolveModelRoleValue`）：展开别名与回退链，取第一个能匹配的。
 * `pool` 是该角色的候选加上可用模型。
 */
export function resolveRoleValue(
	value: string,
	roles: ReadonlyMap<string, RoleEntry>,
	pool: readonly ModelRef[],
	self?: string,
): { model?: ModelRef; thinkingLevel?: string } {
	const v = value.trim();
	if (!v || v === "default") return {};
	const visited = new Set<string>(self ? [self] : []);
	for (const pattern of expandPatterns(v, roles, visited)) {
		const hit = matchPattern(pattern, pool);
		if (hit.model) return { model: hit.model, thinkingLevel: hit.level };
	}
	return {};
}

/** `get_settings` 的 `cycleOrder` 值 → 角色顺序；不是字符串数组时用默认值。 */
export function parseCycleOrder(value: unknown): string[] {
	if (!Array.isArray(value)) return [...DEFAULT_CYCLE_ORDER];
	const out = value.filter(
		(v): v is string => typeof v === "string" && v.trim() !== "",
	);
	return out.length > 0 ? out : [...DEFAULT_CYCLE_ORDER];
}

/**
 * 按 cycleOrder 解析出轨道里的每一格（omp `resolveRoleModelCycle`）：
 * - `selected` 存在就直接用（级别取它的 `thinkingLevel`）；
 * - 否则照 omp 规则解析配置字符串（别名、回退链、`:level`）；
 * - `default` 没配置时用当前模型；其它没配置的角色禁用（omp 会跳过它们）；
 * - 怎么也解析不出来的禁用但保留位置，轮换时跳过。
 */
export function buildRoleSlots(
	order: readonly string[],
	entries: readonly RoleEntry[],
	available: readonly ModelRef[],
	current: CurrentModel | undefined,
): RoleSlot[] {
	const roles = new Map(entries.map((e) => [e.id, e]));
	const seen = new Set<string>();
	const slots: RoleSlot[] = [];
	for (const id of order) {
		if (seen.has(id)) continue;
		seen.add(id);
		const e = roles.get(id);
		const name = e?.name || id;
		const raw = e?.model?.trim() || undefined;
		if (e?.selected) {
			const pool = mergePool(e.candidates, available);
			const named = pool.find(
				(m) => m.provider === e.selected?.provider && m.id === e.selected.id,
			);
			slots.push({
				id,
				name,
				raw,
				status: "ok",
				model: {
					provider: e.selected.provider,
					id: e.selected.id,
					name: named?.name,
				},
				thinkingLevel: e.selected.thinkingLevel,
			});
			continue;
		}
		if (!raw) {
			if (id === "default" && current)
				slots.push({
					id,
					name,
					status: "ok",
					model: {
						provider: current.provider,
						id: current.id,
						name: current.name,
					},
				});
			else slots.push({ id, name, status: "unconfigured" });
			continue;
		}
		const r = resolveRoleValue(
			raw,
			roles,
			mergePool(e?.candidates, available),
			id,
		);
		slots.push(
			r.model
				? {
						id,
						name,
						raw,
						status: "ok",
						model: r.model,
						thinkingLevel: r.thinkingLevel,
					}
				: { id, name, raw, status: "unresolved" },
		);
	}
	return slots;
}

// ---------------------------------------------------------------- 当前角色与轮换

const sameModel = (a: ModelRef | undefined, b: ModelRef | undefined) =>
	!!a && !!b && a.provider === b.provider && a.id === b.id;

/** 转录里最后一条 `model_change` 的 `role`（TUI 在别处切换时会记录）。 */
export function lastModelChangeRole(
	entries: readonly { type: string; payload?: unknown }[],
): string | undefined {
	for (let i = entries.length - 1; i >= 0; i--) {
		const e = entries[i];
		if (e?.type !== "model_change") continue;
		const p = e.payload as { role?: unknown } | undefined;
		return typeof p?.role === "string" && p.role !== "" ? p.role : undefined;
	}
	return undefined;
}

/**
 * 当前角色在 `slots` 里的下标，认不出时返回 -1。优先级：
 * 1. 客户端记住的选择，只要它记录的模型与级别仍等于当前状态（网页 `set_model` 会让 omp 记成 `default`，所以它排第一）；
 * 2. 最后一条 `model_change` 的 role，且该角色的模型仍是当前模型（认出 TUI 在别处的切换）；
 *    `default` 不可信：网页 `set_model` 一律被 omp 记成它，交给第 3 步按 cycleOrder 顺序匹配；
 * 3. 模型 + 级别都一致的第一格；再退到只看模型的第一格。
 */
export function currentRoleIndex(
	slots: readonly RoleSlot[],
	current: CurrentModel | undefined,
	memory?: RoleMemory,
	lastChangeRole?: string,
): number {
	if (!current) return -1;
	if (
		memory &&
		memory.provider === current.provider &&
		memory.id === current.id &&
		(memory.thinkingLevel === undefined ||
			memory.thinkingLevel === current.thinkingLevel)
	) {
		const i = slots.findIndex((s) => s.id === memory.role);
		if (slots[i]?.status === "ok") return i;
	}
	if (lastChangeRole && lastChangeRole !== "default") {
		const i = slots.findIndex((s) => s.id === lastChangeRole);
		if (slots[i]?.status === "ok" && sameModel(slots[i]?.model, current))
			return i;
	}
	// 没写级别的格沿用当前级别，视为级别一致
	const strict = slots.findIndex(
		(s) =>
			s.status === "ok" &&
			sameModel(s.model, current) &&
			(s.thinkingLevel === undefined ||
				s.thinkingLevel === current.thinkingLevel),
	);
	if (strict !== -1) return strict;
	return slots.findIndex(
		(s) => s.status === "ok" && sameModel(s.model, current),
	);
}

/**
 * 从 `from`（-1 = 认不出，按 omp 视为第一个可用格）按方向走到下一个可用格；可用格少于 2 个时返回 undefined。
 */
export function stepRole(
	slots: readonly RoleSlot[],
	from: number,
	dir: 1 | -1,
): number | undefined {
	const usable = slots.flatMap((s, i) => (s.status === "ok" ? [i] : []));
	if (usable.length < 2) return undefined;
	const at = Math.max(0, usable.indexOf(from));
	return usable[(at + dir + usable.length) % usable.length];
}

/** 选择器的完整写法，用作悬停提示：`provider/id:level`。 */
export function slotSelector(slot: RoleSlot): string | undefined {
	if (!slot.model) return undefined;
	const base = `${slot.model.provider}/${slot.model.id}`;
	return slot.thinkingLevel ? `${base}:${slot.thinkingLevel}` : base;
}

// ---------------------------------------------------------------- 颜色

/**
 * 轨道色板（`index.css` 的 `--role-h-0..7`）：8 个色相、相邻 ≥40°，尽量远离语义色带（红 25、琥珀 78、绿 152）。
 * 8×40° 已占 320°，三条色带最多只能各让出约 17–20°。排列顺序让相邻位置色相相差最大。
 */
export const ROLE_PALETTE_SIZE = 8;

/** 内置角色的固定色相（`index.css` 的 `--role-h-<id>`）。 */
const BUILTIN_HUE_ROLES: Record<string, true> = {
	default: true,
	smol: true,
	slow: true,
	plan: true,
	vision: true,
	task: true,
	advisor: true,
	commit: true,
	tiny: true,
	memory: true,
};

/** 名字哈希成色相（稳定：同名同色）。 */
export function hashHue(name: string): number {
	let h = 0;
	for (const ch of name) h = (h * 31 + (ch.codePointAt(0) ?? 0)) >>> 0;
	return h % 360;
}

/**
 * 角色色相（CSS 值，写进 `--role-h`）。在轨道里传 `position`（cycleOrder 里的位置），从色板取，同一条轨道不撞色；
 * 否则内置角色用固定色相，未知角色按名字哈希。
 */
export function roleHue(id: string, position?: number): string {
	if (position !== undefined && position >= 0)
		return `var(--role-h-${position % ROLE_PALETTE_SIZE})`;
	if (Object.hasOwn(BUILTIN_HUE_ROLES, id)) return `var(--role-h-${id})`;
	return String(hashHue(id));
}

/** omp 内置角色的默认英文名；与之相同时显示本地化名，用户改过名（cfgModelTags）就原样显示。 */
export const BUILTIN_ROLE_NAMES = {
	default: "Default",
	smol: "Fast",
	slow: "Thinking",
	vision: "Vision",
	plan: "Architect",
	commit: "Commit",
	tiny: "Tiny",
	memory: "Memory",
	task: "Subtask",
	advisor: "Advisor",
	widget: "Job widgets",
} as const satisfies Record<string, string>;

export type BuiltinRole = keyof typeof BUILTIN_ROLE_NAMES;
