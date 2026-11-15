/**
 * 回合头的 role 判定（M9.6 修订稿 §4；其它客户端照此实现）。
 *
 * 唯一依据是会话里 Oryn Core 在「选择发生的那一刻」写下的来源记录（`custom` 条目，`customType = oryn-model-source`）：
 * 点 role 写 `source: "role"`，模型列表手动选写 `manual`，新会话按配置的 default role 启动写 `startup`。
 * `model_change` 只用来让记录失效（`fallback` / `prewalk` 是临时覆盖，只标记「正在回退」），回合自己的模型只用来否决。
 * 这里没有、也不允许有任何「模型 → role」的反推：手动选了某个 role 的同一模型，照样回落。
 *
 * 显示哪张脸（M13 R5）：选中的 role 有自定义头像就用它；否则是内置 role 时用它的默认字形；否则回落到默认 Agent 身份。
 * 助手对话（R4）不走这里，回合头恒为全局助手。
 */
import type { Entry, Schemas } from "../api/client";
import { isRecord } from "../tool-render/util";
import { ROLE_DEFAULT_GLYPHS, roleDefaultGlyph } from "./avatar-defaults";

export const MODEL_SOURCE_ENTRY = "oryn-model-source";

export interface ModelSourceRecord {
	source: "role" | "manual" | "startup";
	/** 写入时的活动模型 `provider/modelId`。 */
	model: string;
	/** source=role：被点的 role；startup：恒为 `default`；manual：没有。分享页上是不透明键。 */
	role?: string;
	/** role 的配置是指向另一个 role 的单一别名时，那个 role（分享页上同样是不透明键）。 */
	alias?: string;
}

/** 沿活动分支走到某处时的来源状态。 */
export interface SourceTrail {
	/** 当前生效的来源记录；被未记录来源的切换清掉后为空。 */
	record?: ModelSourceRecord;
	/** 正处在重试回退模型上（`fallback` 且 `resolvedModelIsFallback`）。 */
	fallback: boolean;
	/** 线程里见过至少一条合法记录（旧 Core 的线程恒为假）。 */
	tracked: boolean;
}

export const EMPTY_TRAIL: SourceTrail = Object.freeze({
	fallback: false,
	tracked: false,
});

/** 身份表里的一项。站内 key = role id；分享页 key = 不透明键。 */
export interface RoleFace {
	key: string;
	role: string;
	name?: string;
	avatarIcon?: string;
	avatarImage?: string;
	/** 内置 role 的默认字形：没设自定义头像时显示它。 */
	glyph?: string;
}

const hasCustom = (f: RoleFace) => !!(f.avatarIcon || f.avatarImage);

/**
 * 站内的身份表（R5）：设了头像的 role 带自定义头像；没设头像的内置 role 带默认字形（列表里缺的内置 role 也补上）；
 * 没设头像的自定义 role 不进表，回合回落到默认 Agent 身份。
 */
export function siteRoleFaces(
	roles: readonly Schemas["RoleIdentity"][] | undefined,
): RoleFace[] {
	const out: RoleFace[] = [];
	const seen = new Set<string>();
	for (const r of roles ?? []) {
		seen.add(r.role);
		const face: RoleFace = { key: r.role, role: r.role, name: r.name };
		if (r.avatarImage) face.avatarImage = r.avatarImage;
		else if (r.avatarIcon) face.avatarIcon = r.avatarIcon;
		const glyph = roleDefaultGlyph(r.role);
		if (glyph) face.glyph = glyph;
		if (hasCustom(face) || glyph) out.push(face);
	}
	for (const [role, glyph] of Object.entries(ROLE_DEFAULT_GLYPHS))
		if (!seen.has(role)) out.push({ key: role, role, glyph });
	return out;
}

const SOURCES: Record<string, ModelSourceRecord["source"]> = {
	role: "role",
	manual: "manual",
	startup: "startup",
};

/** 临时覆盖：重试回退的切入 / 切回、上下文提升、prewalk。不改变「选的是谁」。 */
const TEMPORARY_ROLES: Record<string, true> = { fallback: true, prewalk: true };

const nonEmpty = (v: unknown): string | undefined =>
	typeof v === "string" && v !== "" ? v : undefined;

/** 解析来源记录；形状不合法（版本、来源、模型）一律当作不存在。 */
export function parseModelSource(entry: {
	type: string;
	payload?: unknown;
}): ModelSourceRecord | undefined {
	if (entry.type !== "custom" || !isRecord(entry.payload)) return undefined;
	const p = entry.payload;
	if (p.customType !== MODEL_SOURCE_ENTRY || !isRecord(p.data))
		return undefined;
	const d = p.data;
	const source =
		typeof d.source === "string" && Object.hasOwn(SOURCES, d.source)
			? SOURCES[d.source]
			: undefined;
	const model = nonEmpty(d.model);
	if (d.v !== 1 || !source || !model) return undefined;
	const out: ModelSourceRecord = { source, model };
	const role = nonEmpty(d.role);
	const alias = nonEmpty(d.alias);
	if (role) out.role = role;
	if (alias) out.alias = alias;
	return out;
}

/** 并入一条条目；轨迹没变时返回同一个对象（历史行的 memo 不被打破）。 */
export function advanceTrail(
	trail: SourceTrail,
	entry: { type: string; payload?: unknown },
): SourceTrail {
	const record = parseModelSource(entry);
	if (record) return { record, fallback: false, tracked: true };
	if (entry.type !== "model_change") return trail;
	const p = isRecord(entry.payload) ? entry.payload : {};
	if (typeof p.role === "string" && Object.hasOwn(TEMPORARY_ROLES, p.role)) {
		const fallback = p.resolvedModelIsFallback === true;
		return fallback === trail.fallback ? trail : { ...trail, fallback };
	}
	// 未记录来源的切换（旧路径的 set_model、TUI、重启时的模型校正……）：记录失效
	if (!trail.record && !trail.fallback) return trail;
	return { fallback: false, tracked: trail.tracked };
}

/** 活动分支末端的轨迹（输入 = activeBranch 的结果；多个主会话文件按顺序接着走，换文件不清零）。 */
export function trailOf(branches: readonly (readonly Entry[])[]): SourceTrail {
	let trail = EMPTY_TRAIL;
	for (const branch of branches)
		for (const e of branch) trail = advanceTrail(trail, e);
	return trail;
}

/**
 * §4 的 1–5 步：回合模型 `model`（回合里第一条带模型的助手消息）在轨迹 `trail` 下是否显示某个 role。
 * 按 `key` 匹配记录里的 `role` / `alias`：先找两者中设了自定义头像的，再找带默认字形的（R5）；
 * 两者都没有可显示的脸 → 回落。`faces` 里既无头像又无字形的项不算。
 */
export function resolveTurnRole(
	model: { provider: string; model: string } | undefined,
	trail: SourceTrail | undefined,
	faces: readonly RoleFace[],
): RoleFace | undefined {
	const record = trail?.record;
	if (!record || trail.fallback || record.source === "manual") return;
	// 否决校验：记录选的模型与这一回合实际跑的模型不同（切了分支、临时覆盖、服务端降级）→ 不认
	if (!model?.provider || !model.model) return;
	const m = `${model.provider}/${model.model}`.toLowerCase();
	if (m !== record.model.toLowerCase()) return;
	const keys = [record.role, record.alias].filter((k): k is string => !!k);
	for (const shows of [hasCustom, (f: RoleFace) => !!f.glyph])
		for (const key of keys) {
			const face = faces.find((f) => f.key === key && shows(f));
			if (face) return face;
		}
	return;
}
