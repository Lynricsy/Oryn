// 方案清单 src/variants.json 的解析（M34 C2）。与 internal/artifacts/design/variants.go 逐条相同，
// 两边共用 schema/testdata/design-variants.json 的测试向量；改规则时两边一起改。
//
//   {"groups": [{"id", "name", "variants": [{"id", "name", "note"?, "theme"?, "frames": [帧 id]}]}]}
//
// 规则同 canvas.json 的方案组（C1），只是 variants 列的是帧 id 而不是画框 id；不写 picked（选定只在 canvas.json）。

/** 组、方案与主题名的 id 规则。 */
export const VARIANT_ID = /^[a-z0-9][a-z0-9-]{0,31}$/;
export const MAX_GROUPS = 20;
export const MIN_VARIANTS = 2;
export const MAX_VARIANTS = 4;
export const MAX_VARIANT_FRAMES = 24;
export const MAX_GROUP_NAME_RUNES = 60;
export const MAX_VARIANT_NOTE_RUNES = 200;

/**
 * @typedef {{ id: string, name: string, note: string, theme: string, frames: string[] }} Variant
 * @typedef {{ id: string, name: string, variants: Variant[] }} Group
 * @typedef {{ groups: Group[], frameTheme: Record<string, string> }} Variants
 */

/** 解析失败：path 是出错位置（如 groups[0].variants[1].frames[0]），整份文件不合法时为空串。 */
export class VariantsError extends Error {
	/** @param {string} path @param {string} message */
	constructor(path, message) {
		super(path === "" ? message : `${path}：${message}`);
		this.path = path;
	}
}

function fail(path, message) {
	throw new VariantsError(path, message);
}

function isObject(v) {
	return typeof v === "object" && v !== null && !Array.isArray(v);
}

function only(obj, keys, path) {
	for (const k of Object.keys(obj)) {
		if (!keys.includes(k)) fail(at(path, k), "未知字段");
	}
}

function at(path, key) {
	return path === "" ? key : `${path}.${key}`;
}

function runes(s) {
	return [...s].length;
}

function text(v, path, max, blankOK) {
	if (typeof v !== "string") fail(path, "必须是字符串");
	if (!blankOK && v.trim() === "") fail(path, "不能为空");
	const n = runes(v);
	if (n > max) fail(path, `不能超过 ${max} 个字符（现在 ${n} 个）`);
	return v;
}

function id(v, path) {
	if (typeof v !== "string" || !VARIANT_ID.test(v))
		fail(path, `必须匹配 ${VARIANT_ID.source}`);
	return v;
}

function list(v, path, lo, hi, what) {
	if (!Array.isArray(v)) fail(path, "必须是数组");
	if (v.length < lo || v.length > hi) fail(path, `${what}要有 ${lo}–${hi} 个（现在 ${v.length} 个）`);
	return v;
}

/**
 * 解析 src/variants.json 的文本。frames 是 src/frames 下存在的帧 id，themes 是 src/themes 下存在的主题名
 * （文件名去掉 .css）。返回规范化的清单与「帧 → 主题」（只含带主题的方案的帧）。
 * @param {string} raw
 * @param {string[]} frames
 * @param {string[]} themes
 * @returns {Variants}
 */
export function parseVariants(raw, frames, themes) {
	let doc;
	try {
		doc = JSON.parse(raw);
	} catch (e) {
		fail("", `不是合法的 JSON（${e instanceof Error ? e.message : String(e)}）`);
	}
	if (!isObject(doc)) fail("", "必须是对象");
	only(doc, ["groups"], "");
	if (!("groups" in doc)) fail("groups", "缺少字段");
	const haveFrames = new Set(frames);
	const haveThemes = new Set(themes);
	/** @type {Map<string, string>} 帧 → 首次出现的位置 */
	const used = new Map();
	const groupIds = new Set();
	/** @type {Group[]} */
	const groups = [];
	/** @type {Record<string, string>} */
	const frameTheme = {};
	list(doc.groups, "groups", 0, MAX_GROUPS, "方案组");
	doc.groups.forEach((g, gi) => {
		const gp = `groups[${gi}]`;
		if (!isObject(g)) fail(gp, "必须是对象");
		only(g, ["id", "name", "variants"], gp);
		for (const k of ["id", "name", "variants"]) if (!(k in g)) fail(at(gp, k), "缺少字段");
		const gid = id(g.id, at(gp, "id"));
		if (groupIds.has(gid)) fail(at(gp, "id"), `方案组 id「${gid}」重复`);
		// 组与画框在 canvas.json 里共用 id 命名空间（C1 修订），组 id 不能与帧 id 相同
		if (haveFrames.has(gid)) fail(at(gp, "id"), `方案组 id「${gid}」与帧 src/frames/${gid}.tsx 同名，换一个`);
		groupIds.add(gid);
		const name = text(g.name, at(gp, "name"), MAX_GROUP_NAME_RUNES, false);
		list(g.variants, at(gp, "variants"), MIN_VARIANTS, MAX_VARIANTS, "方案");
		const variantIds = new Set();
		/** @type {Variant[]} */
		const variants = g.variants.map((v, vi) => {
			const vp = `${gp}.variants[${vi}]`;
			if (!isObject(v)) fail(vp, "必须是对象");
			only(v, ["id", "name", "note", "theme", "frames"], vp);
			for (const k of ["id", "name", "frames"]) if (!(k in v)) fail(at(vp, k), "缺少字段");
			const vid = id(v.id, at(vp, "id"));
			if (variantIds.has(vid)) fail(at(vp, "id"), `方案 id「${vid}」在本组重复`);
			variantIds.add(vid);
			const vname = text(v.name, at(vp, "name"), MAX_GROUP_NAME_RUNES, false);
			const note = "note" in v ? text(v.note, at(vp, "note"), MAX_VARIANT_NOTE_RUNES, true) : "";
			let theme = "";
			if ("theme" in v) {
				if (typeof v.theme !== "string") fail(at(vp, "theme"), "必须是字符串");
				if (v.theme !== "") {
					theme = id(v.theme, at(vp, "theme"));
					if (!haveThemes.has(theme)) fail(at(vp, "theme"), `没有主题文件 src/themes/${theme}.css`);
				}
			}
			list(v.frames, at(vp, "frames"), 1, MAX_VARIANT_FRAMES, "帧");
			const out = v.frames.map((f, fi) => {
				const fp = `${vp}.frames[${fi}]`;
				if (typeof f !== "string") fail(fp, "必须是字符串");
				if (!haveFrames.has(f)) fail(fp, `没有帧 src/frames/${f}.tsx`);
				const prev = used.get(f);
				if (prev !== undefined) fail(fp, `帧「${f}」已经属于 ${prev}，一个帧只能属于一个方案`);
				used.set(f, vp);
				if (theme !== "") frameTheme[f] = theme;
				return f;
			});
			return { id: vid, name: vname, note, theme, frames: out };
		});
		groups.push({ id: gid, name, variants });
	});
	return { groups, frameTheme };
}
