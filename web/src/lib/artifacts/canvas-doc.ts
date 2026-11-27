// 画布 artifact 的文档模型：canvas.json 的严格解析、操作与撤销。
//
// 与 internal/artifacts/canvas 的 Go 实现逐条相同，两边共用 schema/testdata/canvas-ops.json 的测试向量；
// 改规则时两边一起改。文档里 canvases 的元素在界面上叫「页面」，pages 的元素叫「画框」，notes 是标注。
// 带顶层 design 的是设计画布：画框可以带 source（TSX 源码，file 是它的构建产物），design 与 source
// 只由服务端封存维护，这里的操作不能添加或修改 source。groups 是方案组：同一屏的几种方案并排比较、选定其一，
// 落选画框由 archivedPages 推导；设计画布的方案组由 src/variants.json 经封存维护，这里只能改 picked。

export const CANVAS_FILE = "canvas.json";
/**
 * 只有 HTML 画框的画布写出的格式；带 design、source 或 device 任一字段时写出 CANVAS_FORMAT_DESIGN，
 * 带 groups 时写出 CANVAS_FORMAT_GROUPS。
 */
export const CANVAS_FORMAT = 1;
export const CANVAS_FORMAT_DESIGN = 2;
export const CANVAS_FORMAT_GROUPS = 3;
/** design.engine 目前唯一的取值。 */
export const DESIGN_ENGINE = 1;

export const CANVAS_LIMITS = {
	canvases: 50,
	pages: 500,
	notes: 5000,
	/** 单笔的数字个数（每点 x、y、压力三个）。 */
	penNumbers: 30_000,
	/** 整个 canvas.json 的 UTF-8 字节数。 */
	jsonBytes: 5 << 20,
	/** 单次操作条数。 */
	ops: 2000,
	/** 方案组个数。 */
	groups: 20,
} as const;

/** 方案组：add 与 set variants 时要求 2–4 个方案（级联删除后可以只剩一个），每个方案 1–24 个画框。 */
export const GROUP_LIMITS = {
	variantsMin: 2,
	variantsMax: 4,
	variantPages: 24,
	/** 方案组与方案的名字字数。 */
	name: 60,
	note: 200,
} as const;

/** 「备选方案」画布页：组选定后落选画框由 expandPicks 搬到这里。 */
export const ALTERNATES_ID = "alternates";
export const ALTERNATES_NAME = "备选方案";

export const NAME_CHARS = 120;
export const NOTE_TEXT_CHARS = 4000;
export const MAX_COORD = 1_000_000;
export const PAGE_MIN = 64;
export const PAGE_MAX = 8000;
export const DEFAULT_PAGE = { width: 1280, height: 800 } as const;
/** 自动排位时画框之间的间距。 */
export const PAGE_GAP = 48;
export const STROKE = { min: 0.5, max: 64, default: 3 } as const;
export const TEXT_SIZE = { min: 8, max: 400, default: 24 } as const;
export const STICKY_WIDTH = { min: 80, max: 2000, default: 240 } as const;
/** 文档没写 canvases 时补上的唯一画布页。 */
export const DEFAULT_CANVAS = { id: "canvas-1", name: "页面 1" } as const;

const MAX_AUTHOR_CHARS = 200;
const MAX_FILE_BYTES = 512;
const MAX_INDEX = Number.MAX_SAFE_INTEGER;

export const NOTE_KINDS = [
	"text",
	"sticky",
	"pen",
	"rect",
	"ellipse",
	"line",
	"arrow",
] as const;
export type NoteKind = (typeof NOTE_KINDS)[number];
export type ShapeKind = "rect" | "ellipse" | "line" | "arrow";

/** 标注可用的颜色名（界面按主题映射成具体颜色）。 */
export const NOTE_COLORS = [
	"ink",
	"red",
	"orange",
	"yellow",
	"green",
	"blue",
	"violet",
] as const;
export type NoteColor = (typeof NOTE_COLORS)[number];

/** 画框的设备外壳。 */
export const DEVICES = ["none", "phone", "tablet", "browser"] as const;
export type Device = (typeof DEVICES)[number];

export interface CanvasSheet {
	id: string;
	name: string;
}

export interface CanvasPage {
	id: string;
	canvas: string;
	name: string;
	/** 画框的 HTML 文件（artifact 里的相对路径）；带 source 时固定是 dist/<名>.html。 */
	file: string;
	x: number;
	y: number;
	width: number;
	height: number;
	locked: boolean;
	/** 设计画框的源码 src/frames/<名>.tsx，只由封存维护。 */
	source?: string;
	/** 设备外壳；省略即 none。 */
	device?: Exclude<Device, "none">;
}

/** 设计画布的顶层信息：引擎版本与原型的起始画框。 */
export interface CanvasDesign {
	engine: typeof DESIGN_ENGINE;
	start: string | null;
}

interface NoteBase {
	id: string;
	canvas: string;
	color: NoteColor;
	locked: boolean;
	/** 画它的线程 id；用户画的为 null。 */
	by: string | null;
}

export interface TextNote extends NoteBase {
	kind: "text";
	x: number;
	y: number;
	text: string;
	size: number;
}

export interface StickyNote extends NoteBase {
	kind: "sticky";
	x: number;
	y: number;
	width: number;
	text: string;
	done: boolean;
}

export interface PenNote extends NoteBase {
	kind: "pen";
	/** x、y、压力（0–1）三个一组。 */
	points: number[];
	width: number;
	marker: boolean;
}

export interface ShapeNote extends NoteBase {
	kind: ShapeKind;
	x1: number;
	y1: number;
	x2: number;
	y2: number;
	width: number;
	/** 吸附的画框 id。 */
	from: string | null;
	to: string | null;
}

export type CanvasNote = TextNote | StickyNote | PenNote | ShapeNote;

/** 方案组里的一个方案：一组画框，可以带一套方案主题（theme 为空串表示没有独立主题）。 */
export interface CanvasVariant {
	id: string;
	name: string;
	note: string;
	theme: string;
	pages: string[];
}

/** 方案组：canvas 是它所在的画布页（落选画框改选回来时回到这里）。 */
export interface CanvasGroup {
	id: string;
	canvas: string;
	name: string;
	variants: CanvasVariant[];
	/** 选定的方案 id；null 为未选定。 */
	picked: string | null;
	/** 选定者的线程 id；用户选的为 null（由服务端盖章）。 */
	pickedBy: string | null;
}

export interface CanvasDoc {
	format:
		| typeof CANVAS_FORMAT
		| typeof CANVAS_FORMAT_DESIGN
		| typeof CANVAS_FORMAT_GROUPS;
	/** 有它即设计画布。 */
	design?: CanvasDesign;
	canvases: CanvasSheet[];
	/** 绘制顺序，先画的在下面。 */
	pages: CanvasPage[];
	/** 绘制顺序，画在画框之上。 */
	notes: CanvasNote[];
	/** 方案组；没有时省略。 */
	groups?: CanvasGroup[];
}

export type CanvasObjectType = "canvas" | "page" | "note" | "group";

/**
 * 对文档的一次修改。add 一个画框时 file 可省略（取 page-<id>.html）；
 * copyOf 表示新画框的 HTML 从那个画框复制（落版本时由服务端复制文件）。
 * 方案组的 picked 与 pickedBy 用 set 改；pickedBy 由服务端按提交方盖章，提交方给的值会被覆盖。
 */
export type CanvasOp =
	| {
			op: "add";
			object: { type: CanvasObjectType } & Record<string, unknown>;
			copyOf?: string;
	  }
	| { op: "set"; id: string; key: string; value: unknown }
	| { op: "remove"; id: string }
	| { op: "order"; id: string; index: number };

/** 文档或操作不合法；where 是出错的位置，如 pages[2].canvas。 */
export class CanvasError extends Error {
	readonly where: string;
	constructor(where: string, message: string) {
		super(where === "" ? message : `${where}：${message}`);
		this.name = "CanvasError";
		this.where = where;
	}
}

type Obj = Record<string, unknown>;

function fail(where: string, message: string): never {
	throw new CanvasError(where, message);
}

function at(where: string, key: string): string {
	return where === "" ? key : `${where}.${key}`;
}

function isObj(v: unknown): v is Obj {
	return typeof v === "object" && v !== null && !Array.isArray(v);
}

function has(m: Obj, key: string): boolean {
	return Object.hasOwn(m, key);
}

function utf8Length(s: string): number {
	return new TextEncoder().encode(s).length;
}

/** 与 JS Math.round 相同的取整，结果不出现 -0。 */
function jsRound(v: number): number {
	return Math.round(v) + 0;
}

/** 保留两位小数（两边实现取整规则一致）。 */
function round2(v: number): number {
	return jsRound(v * 100) / 100;
}

function object(v: unknown, where: string): Obj {
	if (!isObj(v)) fail(where, "必须是对象");
	return v;
}

/** 拒绝 keys 之外的字段（按字段名排序报第一个，两边实现报同一个）。 */
function only(m: Obj, keys: readonly string[], where: string): void {
	const [bad] = Object.keys(m)
		.filter((k) => !keys.includes(k))
		.sort();
	if (bad !== undefined)
		fail(at(where, bad), `未知字段（只允许 ${keys.join("、")}）`);
}

function need(m: Obj, key: string, where: string): unknown {
	if (!has(m, key)) fail(at(where, key), "缺少这个字段");
	return m[key];
}

function opt<T>(
	m: Obj,
	key: string,
	where: string,
	def: T,
	read: (v: unknown, where: string) => T,
): T {
	return has(m, key) ? read(m[key], at(where, key)) : def;
}

const ID = /^[A-Za-z0-9_-]{1,64}$/;

function id(v: unknown, where: string): string {
	if (typeof v !== "string" || !ID.test(v))
		fail(where, "id 必须是 1–64 个字母、数字、- 或 _");
	return v;
}

function text(
	v: unknown,
	where: string,
	max: number,
	blankOK: boolean,
): string {
	if (typeof v !== "string") fail(where, "必须是字符串");
	if (!blankOK && v.trim() === "") fail(where, "不能为空");
	const n = [...v].length;
	if (n > max) fail(where, `不能超过 ${max} 个字符（现在 ${n} 个）`);
	return v;
}

function num(v: unknown, where: string, lo: number, hi: number): number {
	if (typeof v !== "number") fail(where, "必须是数字");
	if (v < lo || v > hi)
		fail(where, `必须在 ${lo} 到 ${hi} 之间（现在是 ${v}）`);
	return v;
}

function coord(v: unknown, where: string): number {
	return round2(num(v, where, -MAX_COORD, MAX_COORD));
}

function boolean(v: unknown, where: string): boolean {
	if (typeof v !== "boolean") fail(where, "必须是 true 或 false");
	return v;
}

function ref(v: unknown, where: string): string | null {
	return v === null ? null : id(v, where);
}

function author(v: unknown, where: string): string | null {
	if (v === null) return null;
	if (typeof v !== "string" || v === "" || [...v].length > MAX_AUTHOR_CHARS) {
		fail(where, "必须是线程 id 或 null");
	}
	return v;
}

function pageFile(v: unknown, where: string): string {
	if (typeof v !== "string") fail(where, "必须是字符串");
	const low = v.toLowerCase();
	const bad =
		v === "" ||
		utf8Length(v) > MAX_FILE_BYTES ||
		v.startsWith("/") ||
		v.includes("\\") ||
		v.startsWith("_oryn") ||
		!(low.endsWith(".html") || low.endsWith(".htm")) ||
		v.split("/").some((seg) => seg === "" || seg === "." || seg === "..");
	if (bad) {
		fail(
			where,
			`必须是这个 artifact 里一个 .html / .htm 文件的相对路径，且不以 _oryn 开头（现在是 ${JSON.stringify(v)}）`,
		);
	}
	return v;
}

/** 设计画框的源码路径：src/frames/<名>.tsx。 */
const SOURCE = /^src\/frames\/[a-z0-9][a-z0-9-]{0,63}\.tsx$/;

function source(v: unknown, where: string): string {
	if (typeof v !== "string" || !SOURCE.test(v)) {
		fail(
			where,
			"必须是 src/frames/<名>.tsx，名字是 1–64 个小写字母、数字或 -，以字母或数字开头",
		);
	}
	return v;
}

/** source 对应的产物文件：src/frames/<名>.tsx → dist/<名>.html。 */
export function frameFile(src: string): string {
	return `dist/${src.replace(/^src\/frames\//, "").replace(/\.tsx$/, "")}.html`;
}

function device(v: unknown, where: string): Device {
	if (typeof v !== "string" || !(DEVICES as readonly string[]).includes(v)) {
		fail(where, `只能是 ${DEVICES.join("、")} 之一`);
	}
	return v as Device;
}

function designOf(v: unknown, where: string): CanvasDesign {
	const m = object(v, where);
	only(m, DESIGN_KEYS, where);
	if (need(m, "engine", where) !== DESIGN_ENGINE)
		fail(at(where, "engine"), `必须是 ${DESIGN_ENGINE}`);
	return {
		engine: DESIGN_ENGINE,
		start: opt<string | null>(m, "start", where, null, ref),
	};
}

function color(v: unknown, where: string): NoteColor {
	if (
		typeof v !== "string" ||
		!(NOTE_COLORS as readonly string[]).includes(v)
	) {
		fail(where, `颜色只能是 ${NOTE_COLORS.join("、")} 之一`);
	}
	return v as NoteColor;
}

function size(v: unknown, where: string): number {
	return jsRound(num(v, where, PAGE_MIN, PAGE_MAX));
}

function stroke(v: unknown, where: string): number {
	return round2(num(v, where, STROKE.min, STROKE.max));
}

function list(m: Obj, key: string, max: number): unknown[] {
	if (!has(m, key)) return [];
	const v = m[key];
	if (!Array.isArray(v)) fail(key, "必须是数组");
	if (v.length > max) fail(key, `最多 ${max} 个（现在 ${v.length} 个）`);
	return v;
}

const DOC_KEYS = ["format", "design", "canvases", "pages", "notes", "groups"];
const GROUP_KEYS = ["id", "canvas", "name", "variants", "picked", "pickedBy"];
const VARIANT_KEYS = ["id", "name", "note", "theme", "pages"];
const DESIGN_KEYS = ["engine", "start"];
const CANVAS_KEYS = ["id", "name"];
const PAGE_KEYS = [
	"id",
	"canvas",
	"name",
	"file",
	"x",
	"y",
	"width",
	"height",
	"locked",
	"source",
	"device",
];
const NOTE_COMMON = ["id", "canvas", "kind", "color", "locked", "by"];
const SHAPE_KEYS = [
	...NOTE_COMMON,
	"x1",
	"y1",
	"x2",
	"y2",
	"width",
	"from",
	"to",
];
const NOTE_KEYS: Record<NoteKind, readonly string[]> = {
	text: [...NOTE_COMMON, "x", "y", "text", "size"],
	sticky: [...NOTE_COMMON, "x", "y", "width", "text", "done"],
	pen: [...NOTE_COMMON, "points", "width", "marker"],
	rect: SHAPE_KEYS,
	ellipse: SHAPE_KEYS,
	line: SHAPE_KEYS,
	arrow: SHAPE_KEYS,
};

export function isShape(note: CanvasNote): note is ShapeNote {
	return (
		note.kind === "rect" ||
		note.kind === "ellipse" ||
		note.kind === "line" ||
		note.kind === "arrow"
	);
}

function sheetOf(v: unknown, where: string): CanvasSheet {
	const m = object(v, where);
	only(m, CANVAS_KEYS, where);
	return {
		id: id(need(m, "id", where), at(where, "id")),
		name: text(need(m, "name", where), at(where, "name"), NAME_CHARS, false),
	};
}

/** 方案组、方案与方案主题的 id。 */
const GROUP_ID = /^[a-z0-9][a-z0-9-]{0,31}$/;

function groupId(v: unknown, where: string): string {
	if (typeof v !== "string" || !GROUP_ID.test(v))
		fail(where, "必须是 1–32 个小写字母、数字或 -，以字母或数字开头");
	return v;
}

/** 方案主题名：空串表示没有独立主题。 */
function theme(v: unknown, where: string): string {
	return v === "" ? "" : groupId(v, where);
}

function variantOf(v: unknown, where: string): CanvasVariant {
	const m = object(v, where);
	only(m, VARIANT_KEYS, where);
	const out: CanvasVariant = {
		id: groupId(need(m, "id", where), at(where, "id")),
		name: text(
			need(m, "name", where),
			at(where, "name"),
			GROUP_LIMITS.name,
			false,
		),
		note: opt(m, "note", where, "", (x, w) =>
			text(x, w, GROUP_LIMITS.note, true),
		),
		theme: opt(m, "theme", where, "", theme),
		pages: [],
	};
	const pw = at(where, "pages");
	const pages = need(m, "pages", where);
	if (!Array.isArray(pages)) fail(pw, "必须是画框 id 的数组");
	if (pages.length < 1 || pages.length > GROUP_LIMITS.variantPages) {
		fail(
			pw,
			`每个方案 1–${GROUP_LIMITS.variantPages} 个画框（现在 ${pages.length} 个）`,
		);
	}
	out.pages = pages.map((x, i) => id(x, `${pw}[${i}]`));
	return out;
}

/**
 * 读一个方案组。strict 为真时（add 与 set variants）要求 2–4 个方案，否则 1–4 个（级联删除后可以只剩一个）。
 * 省略 canvas 时取第一个方案第一个画框所在的画布页。画框是否存在、是否只属于一个方案由 checkLinks 检查。
 */
function groupOf(
	v: unknown,
	where: string,
	pages: readonly CanvasPage[],
	strict: boolean,
): CanvasGroup {
	const m = object(v, where);
	only(m, GROUP_KEYS, where);
	const gid = groupId(need(m, "id", where), at(where, "id"));
	const name = text(
		need(m, "name", where),
		at(where, "name"),
		GROUP_LIMITS.name,
		false,
	);
	const picked = opt<string | null>(m, "picked", where, null, ref);
	const pickedBy = opt<string | null>(m, "pickedBy", where, null, author);
	const vw = at(where, "variants");
	const raw = need(m, "variants", where);
	if (!Array.isArray(raw)) fail(vw, "必须是数组");
	const lo = strict ? GROUP_LIMITS.variantsMin : 1;
	if (raw.length < lo || raw.length > GROUP_LIMITS.variantsMax) {
		fail(
			vw,
			`每组 ${lo}–${GROUP_LIMITS.variantsMax} 个方案（现在 ${raw.length} 个）`,
		);
	}
	const variants: CanvasVariant[] = [];
	raw.forEach((x, i) => {
		const w = `${vw}[${i}]`;
		const vr = variantOf(x, w);
		if (variants.some((o) => o.id === vr.id))
			fail(at(w, "id"), `方案 id ${JSON.stringify(vr.id)} 在本组里重复`);
		variants.push(vr);
	});
	if (picked !== null && !variants.some((o) => o.id === picked))
		fail(
			at(where, "picked"),
			`本组没有 id 为 ${JSON.stringify(picked)} 的方案`,
		);
	let canvas: string;
	if (has(m, "canvas")) canvas = id(m.canvas, at(where, "canvas"));
	else {
		const first = variants[0]?.pages[0];
		const page = pages.find((p) => p.id === first);
		if (!page) fail(at(where, "canvas"), "缺少这个字段");
		canvas = page.canvas;
	}
	return { id: gid, canvas, name, variants, picked, pickedBy };
}

/** 文档换上新的方案组列表：列表为空时不写 groups。 */
function withGroups(doc: CanvasDoc, groups: CanvasGroup[]): CanvasDoc {
	const { groups: _old, ...rest } = doc;
	return groups.length > 0 ? { ...rest, groups } : rest;
}

/** 落选画框：已选定的组里其它方案的画框。只由 groups 推导，与画框当前在哪个画布页无关。 */
export function archivedPages(doc: CanvasDoc): Set<string> {
	const out = new Set<string>();
	for (const g of doc.groups ?? []) {
		if (g.picked === null) continue;
		for (const v of g.variants)
			if (v.id !== g.picked) for (const p of v.pages) out.add(p);
	}
	return out;
}

/** 可能还没有位置的画框。 */
interface LoosePage {
	page: CanvasPage;
	placed: boolean;
}

function pageOf(v: unknown, where: string): LoosePage {
	const m = object(v, where);
	only(m, PAGE_KEYS, where);
	const page: CanvasPage = {
		id: id(need(m, "id", where), at(where, "id")),
		canvas: id(need(m, "canvas", where), at(where, "canvas")),
		name: text(need(m, "name", where), at(where, "name"), NAME_CHARS, false),
		file: pageFile(need(m, "file", where), at(where, "file")),
		x: 0,
		y: 0,
		width: opt(m, "width", where, DEFAULT_PAGE.width, size),
		height: opt(m, "height", where, DEFAULT_PAGE.height, size),
		locked: opt(m, "locked", where, false, boolean),
	};
	if (has(m, "source")) page.source = source(m.source, at(where, "source"));
	const dev = opt(m, "device", where, "none", device);
	if (dev !== "none") page.device = dev;
	const hasX = has(m, "x");
	const hasY = has(m, "y");
	if (hasX && hasY) {
		page.x = coord(m.x, at(where, "x"));
		page.y = coord(m.y, at(where, "y"));
	} else if (hasX) {
		fail(at(where, "y"), "x 与 y 要么都给，要么都省略（省略时自动排位）");
	} else if (hasY) {
		fail(at(where, "x"), "x 与 y 要么都给，要么都省略（省略时自动排位）");
	}
	return { page, placed: hasX && hasY };
}

function points(v: unknown, where: string): number[] {
	if (!Array.isArray(v) || v.length < 3 || v.length % 3 !== 0) {
		fail(where, "必须是数字数组，每个点 x、y、压力三个一组，至少一个点");
	}
	if (v.length > CANVAS_LIMITS.penNumbers) {
		fail(
			where,
			`一笔最多 ${CANVAS_LIMITS.penNumbers} 个数（现在 ${v.length} 个）`,
		);
	}
	return v.map((x, i) =>
		i % 3 === 2
			? round2(num(x, `${where}[${i}]`, 0, 1))
			: coord(x, `${where}[${i}]`),
	);
}

function noteOf(v: unknown, where: string): CanvasNote {
	const m = object(v, where);
	const kind = need(m, "kind", where);
	if (typeof kind !== "string" || !Object.hasOwn(NOTE_KEYS, kind)) {
		fail(at(where, "kind"), `只能是 ${NOTE_KINDS.join("、")} 之一`);
	}
	const k = kind as NoteKind;
	only(m, NOTE_KEYS[k], where);
	const base = {
		id: id(need(m, "id", where), at(where, "id")),
		canvas: id(need(m, "canvas", where), at(where, "canvas")),
		color: opt<NoteColor>(
			m,
			"color",
			where,
			k === "sticky" ? "yellow" : "ink",
			color,
		),
		locked: opt(m, "locked", where, false, boolean),
		by: opt<string | null>(m, "by", where, null, author),
	};
	switch (k) {
		case "text":
			return {
				id: base.id,
				canvas: base.canvas,
				kind: k,
				color: base.color,
				locked: base.locked,
				by: base.by,
				x: coord(need(m, "x", where), at(where, "x")),
				y: coord(need(m, "y", where), at(where, "y")),
				text: text(
					need(m, "text", where),
					at(where, "text"),
					NOTE_TEXT_CHARS,
					false,
				),
				size: opt(m, "size", where, TEXT_SIZE.default, (x, w) =>
					round2(num(x, w, TEXT_SIZE.min, TEXT_SIZE.max)),
				),
			};
		case "sticky":
			return {
				id: base.id,
				canvas: base.canvas,
				kind: k,
				color: base.color,
				locked: base.locked,
				by: base.by,
				x: coord(need(m, "x", where), at(where, "x")),
				y: coord(need(m, "y", where), at(where, "y")),
				width: opt(m, "width", where, STICKY_WIDTH.default, (x, w) =>
					round2(num(x, w, STICKY_WIDTH.min, STICKY_WIDTH.max)),
				),
				text: opt(m, "text", where, "", (x, w) =>
					text(x, w, NOTE_TEXT_CHARS, true),
				),
				done: opt(m, "done", where, false, boolean),
			};
		case "pen":
			return {
				id: base.id,
				canvas: base.canvas,
				kind: k,
				color: base.color,
				locked: base.locked,
				by: base.by,
				points: points(need(m, "points", where), at(where, "points")),
				width: opt(m, "width", where, STROKE.default, stroke),
				marker: opt(m, "marker", where, false, boolean),
			};
		default:
			return {
				id: base.id,
				canvas: base.canvas,
				kind: k,
				color: base.color,
				locked: base.locked,
				by: base.by,
				x1: coord(need(m, "x1", where), at(where, "x1")),
				y1: coord(need(m, "y1", where), at(where, "y1")),
				x2: coord(need(m, "x2", where), at(where, "x2")),
				y2: coord(need(m, "y2", where), at(where, "y2")),
				width: opt(m, "width", where, STROKE.default, stroke),
				from: opt<string | null>(m, "from", where, null, ref),
				to: opt<string | null>(m, "to", where, null, ref),
			};
	}
}

/** 只有一个画布页时给省略了 canvas 的对象补上它。 */
function withCanvas(v: unknown, sole: string | null): unknown {
	return isObj(v) && sole !== null && !has(v, "canvas")
		? { ...v, canvas: sole }
		: v;
}

/**
 * id 不重复（画布页、画框、标注、方案组共用一套 id）、画框与标注都在存在的画布页上、
 * 形状吸附的是同一画布页上存在的画框、方案组在存在的画布页上且方案里的画框存在并只属于一个方案；
 * 设计画布另查 source 只出现在设计画布里、file 与 source 配套、design.start 指向存在的画框。
 */
function checkLinks(doc: CanvasDoc): void {
	const kinds = new Map<string, CanvasObjectType>();
	const claim = (key: string, kind: CanvasObjectType, where: string): void => {
		if (kinds.has(key))
			fail(where, `id ${JSON.stringify(key)} 已被另一个对象使用`);
		kinds.set(key, kind);
	};
	doc.canvases.forEach((c, i) => {
		claim(c.id, "canvas", `canvases[${i}].id`);
	});
	const pageCanvas = new Map<string, string>();
	doc.pages.forEach((p, i) => {
		claim(p.id, "page", `pages[${i}].id`);
		pageCanvas.set(p.id, p.canvas);
	});
	doc.notes.forEach((n, i) => {
		claim(n.id, "note", `notes[${i}].id`);
	});
	const groups = doc.groups ?? [];
	groups.forEach((g, i) => {
		claim(g.id, "group", `groups[${i}].id`);
	});
	doc.pages.forEach((p, i) => {
		if (kinds.get(p.canvas) !== "canvas") {
			fail(
				`pages[${i}].canvas`,
				`没有 id 为 ${JSON.stringify(p.canvas)} 的画布页`,
			);
		}
		if (p.source === undefined) return;
		if (!doc.design) {
			fail(`pages[${i}].source`, "只有设计画布（带 design）的画框能有 source");
		}
		const want = frameFile(p.source);
		if (p.file !== want) {
			fail(
				`pages[${i}].file`,
				`带 source 的画框的 file 必须是 ${JSON.stringify(want)}（现在是 ${JSON.stringify(p.file)}）`,
			);
		}
	});
	doc.notes.forEach((n, i) => {
		if (kinds.get(n.canvas) !== "canvas") {
			fail(
				`notes[${i}].canvas`,
				`没有 id 为 ${JSON.stringify(n.canvas)} 的画布页`,
			);
		}
		if (!isShape(n)) return;
		for (const key of ["from", "to"] as const) {
			const end = n[key];
			if (end !== null && pageCanvas.get(end) !== n.canvas) {
				fail(
					`notes[${i}].${key}`,
					`同一画布页上没有 id 为 ${JSON.stringify(end)} 的画框`,
				);
			}
		}
	});
	const start = doc.design?.start ?? null;
	if (start !== null && kinds.get(start) !== "page")
		fail("design.start", `没有 id 为 ${JSON.stringify(start)} 的画框`);
	const owner = new Map<string, string>();
	groups.forEach((g, i) => {
		const where = `groups[${i}]`;
		if (kinds.get(g.canvas) !== "canvas") {
			fail(
				`${where}.canvas`,
				`没有 id 为 ${JSON.stringify(g.canvas)} 的画布页`,
			);
		}
		g.variants.forEach((v, j) => {
			v.pages.forEach((p, k) => {
				const w = `${where}.variants[${j}].pages[${k}]`;
				if (kinds.get(p) !== "page")
					fail(w, `没有 id 为 ${JSON.stringify(p)} 的画框`);
				const o = owner.get(p);
				if (o !== undefined)
					fail(
						w,
						`画框 ${JSON.stringify(p)} 已属于方案 ${o}，一个画框最多属于一个方案`,
					);
				owner.set(p, `${g.id}/${v.id}`);
			});
		});
	});
}

/** 文档该写出的格式：带 groups 时是 CANVAS_FORMAT_GROUPS；带 design、source 或 device 任一字段时是 CANVAS_FORMAT_DESIGN。 */
function formatOf(doc: CanvasDoc): CanvasDoc["format"] {
	if ((doc.groups ?? []).length > 0) return CANVAS_FORMAT_GROUPS;
	return doc.design ||
		doc.pages.some((p) => p.source !== undefined || p.device !== undefined)
		? CANVAS_FORMAT_DESIGN
		: CANVAS_FORMAT;
}

/** canvas 上 pages 之后的下一个位置：接在最右边（间距 PAGE_GAP、顶边对齐）；没有画框时在原点。 */
function nextSlot(
	pages: readonly CanvasPage[],
	canvas: string,
): { x: number; y: number } {
	let found = false;
	let right = 0;
	let top = 0;
	for (const p of pages) {
		if (p.canvas !== canvas) continue;
		if (!found || p.x + p.width > right) right = p.x + p.width;
		if (!found || p.y < top) top = p.y;
		found = true;
	}
	return found ? { x: round2(right + PAGE_GAP), y: top } : { x: 0, y: 0 };
}

function place(pages: readonly LoosePage[]): CanvasPage[] {
	const placed = pages.filter((p) => p.placed).map((p) => p.page);
	return pages.map((p) => {
		if (p.placed) return p.page;
		const page = { ...p.page, ...nextSlot(placed, p.page.canvas) };
		placed.push(page);
		return page;
	});
}

/**
 * 严格解析 canvas.json：未知字段、类型不对、越界、悬空引用、重复 id 都抛 CanvasError。
 * 只有一个画布页时画框与标注可以省略 canvas；没写 canvases 时补一个缺省画布页；
 * 没写位置的画框在所属画布页里接着已有画框排成一行。
 */
export function parseCanvasDoc(json: string): CanvasDoc {
	if (utf8Length(json) > CANVAS_LIMITS.jsonBytes) {
		fail("", `canvas.json 超过 ${CANVAS_LIMITS.jsonBytes >> 20} MiB`);
	}
	let raw: unknown;
	try {
		raw = JSON.parse(json);
	} catch (e) {
		fail("", `canvas.json 不是合法的 JSON：${(e as Error).message}`);
	}
	if (!isObj(raw)) fail("", "canvas.json 必须是一个 JSON 对象");
	only(raw, DOC_KEYS, "");
	if (
		has(raw, "format") &&
		raw.format !== CANVAS_FORMAT &&
		raw.format !== CANVAS_FORMAT_DESIGN &&
		raw.format !== CANVAS_FORMAT_GROUPS
	)
		fail(
			"format",
			`必须是 ${CANVAS_FORMAT}、${CANVAS_FORMAT_DESIGN} 或 ${CANVAS_FORMAT_GROUPS}`,
		);
	const design = has(raw, "design")
		? designOf(raw.design, "design")
		: undefined;
	let canvases = list(raw, "canvases", CANVAS_LIMITS.canvases).map((v, i) =>
		sheetOf(v, `canvases[${i}]`),
	);
	if (canvases.length === 0) canvases = [{ ...DEFAULT_CANVAS }];
	const sole = canvases.length === 1 ? (canvases[0]?.id ?? null) : null;
	const pages = list(raw, "pages", CANVAS_LIMITS.pages).map((v, i) =>
		pageOf(withCanvas(v, sole), `pages[${i}]`),
	);
	const notes = list(raw, "notes", CANVAS_LIMITS.notes).map((v, i) =>
		noteOf(withCanvas(v, sole), `notes[${i}]`),
	);
	const placed = place(pages);
	const groups = list(raw, "groups", CANVAS_LIMITS.groups).map((v, i) =>
		groupOf(withCanvas(v, sole), `groups[${i}]`, placed, false),
	);
	const doc: CanvasDoc = {
		format: CANVAS_FORMAT,
		...(design ? { design } : {}),
		canvases,
		pages: placed,
		notes,
		...(groups.length > 0 ? { groups } : {}),
	};
	doc.format = formatOf(doc);
	checkLinks(doc);
	return doc;
}

/** 规范化的 canvas.json：两空格缩进、末尾换行。 */
export function stringifyCanvasDoc(doc: CanvasDoc): string {
	return `${JSON.stringify(doc, null, 2)}\n`;
}

/** 画框引用了、但版本里没有的文件（去重，按首次出现的顺序）。 */
export function missingPageFiles(
	doc: CanvasDoc,
	hasFile: (path: string) => boolean,
): string[] {
	return [...new Set(doc.pages.map((p) => p.file))].filter((f) => !hasFile(f));
}

/** 解析一组操作的形状（对象本身的字段在应用时对照文档校验）。 */
export function parseCanvasOps(value: unknown): CanvasOp[] {
	if (!Array.isArray(value)) fail("ops", "必须是数组");
	if (value.length > CANVAS_LIMITS.ops) {
		fail(
			"ops",
			`一次最多 ${CANVAS_LIMITS.ops} 条操作（现在 ${value.length} 条）`,
		);
	}
	return value.map((v, i): CanvasOp => {
		const where = `ops[${i}]`;
		const m = object(v, where);
		switch (m.op) {
			case "add": {
				only(m, ["op", "object", "copyOf"], where);
				const obj = object(need(m, "object", where), at(where, "object"));
				const type = obj.type;
				if (
					type !== "canvas" &&
					type !== "page" &&
					type !== "note" &&
					type !== "group"
				) {
					fail(
						at(where, "object.type"),
						"只能是 canvas、page、note、group 之一",
					);
				}
				const op: Extract<CanvasOp, { op: "add" }> = {
					op: "add",
					object: { ...obj, type },
				};
				if (has(m, "copyOf")) {
					if (type !== "page")
						fail(
							at(where, "copyOf"),
							"只有 add 画框（type: page）可以带 copyOf",
						);
					op.copyOf = id(m.copyOf, at(where, "copyOf"));
				}
				return op;
			}
			case "set": {
				only(m, ["op", "id", "key", "value"], where);
				const target = id(need(m, "id", where), at(where, "id"));
				const key = need(m, "key", where);
				if (typeof key !== "string" || key === "")
					fail(at(where, "key"), "必须是字段名");
				return { op: "set", id: target, key, value: need(m, "value", where) };
			}
			case "remove":
				only(m, ["op", "id"], where);
				return { op: "remove", id: id(need(m, "id", where), at(where, "id")) };
			case "order": {
				only(m, ["op", "id", "index"], where);
				const target = id(need(m, "id", where), at(where, "id"));
				const index = need(m, "index", where);
				if (
					typeof index !== "number" ||
					!Number.isInteger(index) ||
					index < 0 ||
					index > MAX_INDEX
				) {
					fail(at(where, "index"), "必须是不小于 0 的整数");
				}
				return { op: "order", id: target, index };
			}
			default:
				return fail(at(where, "op"), "只能是 add、set、remove、order 之一");
		}
	});
}

type Found =
	| { type: "canvas"; index: number; value: CanvasSheet }
	| { type: "page"; index: number; value: CanvasPage }
	| { type: "note"; index: number; value: CanvasNote }
	| { type: "group"; index: number; value: CanvasGroup };

function find(doc: CanvasDoc, key: string): Found | null {
	let index = doc.canvases.findIndex((c) => c.id === key);
	const canvas = doc.canvases[index];
	if (canvas) return { type: "canvas", index, value: canvas };
	index = doc.pages.findIndex((p) => p.id === key);
	const page = doc.pages[index];
	if (page) return { type: "page", index, value: page };
	index = doc.notes.findIndex((n) => n.id === key);
	const note = doc.notes[index];
	if (note) return { type: "note", index, value: note };
	const groups = doc.groups ?? [];
	index = groups.findIndex((g) => g.id === key);
	const group = groups[index];
	if (group) return { type: "group", index, value: group };
	return null;
}

const CANVAS_SETTABLE = ["name"];
const PAGE_SETTABLE = [
	"name",
	"canvas",
	"file",
	"x",
	"y",
	"width",
	"height",
	"locked",
	"device",
];
/** 带 source 的画框能改的字段：file 跟着 source 走，只由封存改。 */
const FRAME_PAGE_SETTABLE = PAGE_SETTABLE.filter((k) => k !== "file");
const NOTE_FIXED = ["id", "canvas", "kind", "by"];
const GROUP_SETTABLE = ["name", "variants", "picked", "pickedBy"];
/** 设计画布上方案组能改的字段：其余由 src/variants.json 经封存维护。 */
const PICK_SETTABLE = ["picked", "pickedBy"];
const TYPE_LABEL: Record<CanvasObjectType, string> = {
	canvas: "画布页",
	page: "画框",
	note: "标注",
	group: "方案组",
};

function settable(doc: CanvasDoc, found: Found): readonly string[] {
	switch (found.type) {
		case "canvas":
			return CANVAS_SETTABLE;
		case "page":
			return found.value.source === undefined
				? PAGE_SETTABLE
				: FRAME_PAGE_SETTABLE;
		case "group":
			return doc.design ? PICK_SETTABLE : GROUP_SETTABLE;
		case "note":
			return NOTE_KEYS[found.value.kind].filter((k) => !NOTE_FIXED.includes(k));
	}
}

function addTo(
	doc: CanvasDoc,
	op: Extract<CanvasOp, { op: "add" }>,
	where: string,
): CanvasDoc {
	const { type, ...rest } = op.object;
	const obj: Obj = { ...rest };
	const [sole] = doc.canvases;
	if (
		type !== "canvas" &&
		!has(obj, "canvas") &&
		sole &&
		doc.canvases.length === 1
	)
		obj.canvas = sole.id;
	const ow = at(where, "object");
	if (type === "page" && has(obj, "source"))
		fail(at(ow, "source"), "画框的 source 只由封存维护，不能用操作添加");
	if (type === "page" && !has(obj, "file") && typeof obj.id === "string")
		obj.file = `page-${obj.id}.html`;
	const unused = (key: string): void => {
		if (find(doc, key))
			fail(at(ow, "id"), `id ${JSON.stringify(key)} 已被使用`);
	};
	switch (type) {
		case "canvas": {
			const c = sheetOf(obj, ow);
			unused(c.id);
			return { ...doc, canvases: [...doc.canvases, c] };
		}
		case "page": {
			const loose = pageOf(obj, ow);
			unused(loose.page.id);
			if (
				op.copyOf !== undefined &&
				!doc.pages.some((p) => p.id === op.copyOf)
			) {
				fail(
					at(where, "copyOf"),
					`没有 id 为 ${JSON.stringify(op.copyOf)} 的画框`,
				);
			}
			const page = loose.placed
				? loose.page
				: { ...loose.page, ...nextSlot(doc.pages, loose.page.canvas) };
			return { ...doc, pages: [...doc.pages, page] };
		}
		case "note": {
			const n = noteOf(obj, ow);
			unused(n.id);
			return { ...doc, notes: [...doc.notes, n] };
		}
		case "group": {
			if (doc.design)
				fail(
					at(ow, "type"),
					"方案组由 src/variants.json 维护，设计画布上不能用操作添加",
				);
			const g = groupOf(obj, ow, doc.pages, true);
			unused(g.id);
			return withGroups(doc, [...(doc.groups ?? []), g]);
		}
		default:
			return fail(at(ow, "type"), "只能是 canvas、page、note、group 之一");
	}
}

function setIn(
	doc: CanvasDoc,
	op: Extract<CanvasOp, { op: "set" }>,
	where: string,
): CanvasDoc {
	const found = find(doc, op.id);
	if (!found) return doc;
	const keys = settable(doc, found);
	if (!keys.includes(op.key)) {
		if (found.type === "group" && doc.design)
			fail(
				at(where, "key"),
				`设计画布的方案组由 src/variants.json 维护，画布上只能改 ${keys.join("、")}（不能改 ${JSON.stringify(op.key)}）`,
			);
		fail(
			at(where, "key"),
			`${TYPE_LABEL[found.type]} ${JSON.stringify(op.id)} 不能改 ${JSON.stringify(op.key)}（能改的字段：${keys.join("、")}）`,
		);
	}
	const replace = <T>(items: readonly T[], next: T): T[] =>
		items.map((x, j) => (j === found.index ? next : x));
	switch (found.type) {
		case "canvas":
			return {
				...doc,
				canvases: replace(
					doc.canvases,
					sheetOf({ ...found.value, [op.key]: op.value }, where),
				),
			};
		case "page": {
			const page = pageOf({ ...found.value, [op.key]: op.value }, where).page;
			const next = { ...doc, pages: replace(doc.pages, page) };
			return op.key === "canvas" ? detach(next, op.id, page.canvas) : next;
		}
		case "note":
			return {
				...doc,
				notes: replace(
					doc.notes,
					noteOf({ ...found.value, [op.key]: op.value }, where),
				),
			};
		case "group":
			return withGroups(
				doc,
				replace(doc.groups ?? [], setGroup(doc, found.value, op, where)),
			);
	}
}

/**
 * 改方案组的一个字段：改 picked 时 pickedBy 置 null；改 variants 时要求 2–4 个方案，
 * picked 指向的方案还在就保留 picked 与 pickedBy，否则两者都置 null。
 */
function setGroup(
	doc: CanvasDoc,
	old: CanvasGroup,
	op: Extract<CanvasOp, { op: "set" }>,
	where: string,
): CanvasGroup {
	const m: Obj = { ...old, [op.key]: op.value };
	if (op.key === "picked") m.pickedBy = null;
	if (op.key === "variants") {
		m.picked = null;
		m.pickedBy = null;
	}
	const g = groupOf(m, where, doc.pages, op.key === "variants");
	return op.key === "variants" &&
		old.picked !== null &&
		g.variants.some((v) => v.id === old.picked)
		? { ...g, picked: old.picked, pickedBy: old.pickedBy }
		: g;
}

/** 把其它画布页上吸附画框 key 的形状端点置空（画框换了画布页之后）。 */
function detach(doc: CanvasDoc, key: string, canvas: string): CanvasDoc {
	return {
		...doc,
		notes: doc.notes.map((n) =>
			isShape(n) && n.canvas !== canvas && (n.from === key || n.to === key)
				? {
						...n,
						from: n.from === key ? null : n.from,
						to: n.to === key ? null : n.to,
					}
				: n,
		),
	};
}

/**
 * 把删掉的画框从方案里移除：方案变空就删掉方案，组里一个方案都不剩就删掉组；
 * picked 指向的方案被删掉时 picked 与 pickedBy 置 null。没受影响的组保持原对象。
 */
function prune(
	groups: readonly CanvasGroup[],
	gone: ReadonlySet<string>,
): CanvasGroup[] {
	const out: CanvasGroup[] = [];
	for (const g of groups) {
		if (!g.variants.some((v) => v.pages.some((p) => gone.has(p)))) {
			out.push(g);
			continue;
		}
		const variants = g.variants
			.map((v) => ({ ...v, pages: v.pages.filter((p) => !gone.has(p)) }))
			.filter((v) => v.pages.length > 0);
		if (variants.length === 0) continue;
		const keep = g.picked !== null && variants.some((v) => v.id === g.picked);
		out.push({
			...g,
			variants,
			picked: keep ? g.picked : null,
			pickedBy: keep ? g.pickedBy : null,
		});
	}
	return out;
}

/**
 * 删对象：删画布页连带删它的画框、标注与以它为 canvas 的方案组；删画框把吸附它的 from / to
 * 与指向它的 design.start 置空，并把它从方案里移除（见 prune）。删方案组不动它的画框。
 */
function removeFrom(doc: CanvasDoc, key: string): CanvasDoc {
	const found = find(doc, key);
	if (!found) return doc;
	let next: CanvasDoc;
	const gone = new Set<string>();
	switch (found.type) {
		case "canvas":
			for (const p of doc.pages) if (p.canvas === key) gone.add(p.id);
			next = withGroups(
				{
					...doc,
					canvases: doc.canvases.filter((c) => c.id !== key),
					pages: doc.pages.filter((p) => p.canvas !== key),
					notes: doc.notes.filter((n) => n.canvas !== key),
				},
				(doc.groups ?? []).filter((g) => g.canvas !== key),
			);
			break;
		case "page":
			gone.add(key);
			next = { ...doc, pages: doc.pages.filter((p) => p.id !== key) };
			break;
		case "note":
			return { ...doc, notes: doc.notes.filter((n) => n.id !== key) };
		case "group":
			return withGroups(
				doc,
				(doc.groups ?? []).filter((g) => g.id !== key),
			);
	}
	if (gone.size === 0) return next;
	if (next.design && next.design.start !== null && gone.has(next.design.start))
		next = { ...next, design: { ...next.design, start: null } };
	return withGroups(
		{
			...next,
			notes: next.notes.map((n) =>
				isShape(n) &&
				((n.from !== null && gone.has(n.from)) ||
					(n.to !== null && gone.has(n.to)))
					? {
							...n,
							from: n.from !== null && gone.has(n.from) ? null : n.from,
							to: n.to !== null && gone.has(n.to) ? null : n.to,
						}
					: n,
			),
		},
		prune(next.groups ?? [], gone),
	);
}

function move<T>(items: readonly T[], from: number, to: number): T[] {
	const out = [...items];
	const [item] = out.splice(from, 1);
	if (item !== undefined) out.splice(Math.min(to, out.length), 0, item);
	return out;
}

function orderIn(doc: CanvasDoc, key: string, index: number): CanvasDoc {
	const found = find(doc, key);
	if (!found) return doc;
	switch (found.type) {
		case "canvas":
			return { ...doc, canvases: move(doc.canvases, found.index, index) };
		case "page":
			return { ...doc, pages: move(doc.pages, found.index, index) };
		case "note":
			return { ...doc, notes: move(doc.notes, found.index, index) };
		case "group":
			return withGroups(doc, move(doc.groups ?? [], found.index, index));
	}
}

/** 应用一条操作，不做整体检查（数量、引用、大小）。 */
function applyOne(doc: CanvasDoc, op: CanvasOp, where: string): CanvasDoc {
	switch (op.op) {
		case "add":
			return addTo(doc, op, where);
		case "set":
			return setIn(doc, op, where);
		case "remove":
		case "order":
			if (doc.design && find(doc, op.id)?.type === "group")
				fail(
					at(where, "id"),
					"设计画布的方案组由 src/variants.json 维护，不能用操作删除或排序",
				);
			return op.op === "remove"
				? removeFrom(doc, op.id)
				: orderIn(doc, op.id, op.index);
	}
}

/**
 * 按顺序应用操作，返回新文档：同一属性后写的赢；set / remove / order 指向已不存在的对象时跳过
 * （别的客户端可能先删了它）。任何一条让文档不合法就抛 CanvasError，原文档不变。
 */
export function applyCanvasOps(
	start: CanvasDoc,
	ops: readonly CanvasOp[],
): CanvasDoc {
	let doc = start;
	ops.forEach((op, i) => {
		doc = applyOne(doc, op, `ops[${i}]`);
	});
	const format = formatOf(doc);
	if (doc.format !== format) doc = { ...doc, format };
	if (doc.canvases.length === 0) fail("canvases", "至少要保留一个画布页");
	if (doc.canvases.length > CANVAS_LIMITS.canvases)
		fail("canvases", `最多 ${CANVAS_LIMITS.canvases} 个画布页`);
	if (doc.pages.length > CANVAS_LIMITS.pages)
		fail("pages", `最多 ${CANVAS_LIMITS.pages} 个画框`);
	if (doc.notes.length > CANVAS_LIMITS.notes)
		fail("notes", `最多 ${CANVAS_LIMITS.notes} 个标注`);
	if ((doc.groups ?? []).length > CANVAS_LIMITS.groups)
		fail("groups", `最多 ${CANVAS_LIMITS.groups} 个方案组`);
	checkLinks(doc);
	if (utf8Length(stringifyCanvasDoc(doc)) > CANVAS_LIMITS.jsonBytes) {
		fail("", `canvas.json 会超过 ${CANVAS_LIMITS.jsonBytes >> 20} MiB`);
	}
	return doc;
}

/**
 * 撤销删除时被级联改掉的方案组：删掉的组重新 add 回原位，缩小的组改回原来的 variants 与 picked。
 * 设计画布的方案组由封存维护，不经操作恢复。
 */
function restoreGroups(before: CanvasDoc, after: CanvasDoc): CanvasOp[] {
	if (before.design) return [];
	const now = new Map((after.groups ?? []).map((g) => [g.id, g]));
	const out: CanvasOp[] = [];
	(before.groups ?? []).forEach((g, index) => {
		const cur = now.get(g.id);
		if (cur === g) return;
		if (!cur) {
			out.push(
				{ op: "add", object: { type: "group", ...g } },
				{ op: "order", id: g.id, index },
			);
			return;
		}
		out.push({ op: "set", id: g.id, key: "variants", value: g.variants });
		if (g.picked !== null && cur.picked === null)
			out.push({ op: "set", id: g.id, key: "picked", value: g.picked });
		if (g.pickedBy !== null && cur.picked === null)
			out.push({ op: "set", id: g.id, key: "pickedBy", value: g.pickedBy });
	});
	return out;
}

/** 撤销一条操作所需的操作（相对于操作前的文档）。 */
function inverseOne(doc: CanvasDoc, op: CanvasOp): CanvasOp[] {
	switch (op.op) {
		case "add": {
			const key = op.object.id;
			return typeof key === "string" ? [{ op: "remove", id: key }] : [];
		}
		case "set": {
			const found = find(doc, op.id);
			if (!found) return [];
			const current = found.value as unknown as Obj;
			// 画框省略 device 即 none
			const value =
				found.type === "page" && op.key === "device"
					? (current.device ?? "none")
					: current[op.key];
			const out: CanvasOp[] = [{ op: "set", id: op.id, key: op.key, value }];
			if (found.type === "page" && op.key === "canvas") {
				// 换画布页会把吸附它的形状端点置空：撤销时接回来
				for (const n of doc.notes) {
					if (!isShape(n)) continue;
					if (n.from === op.id)
						out.push({ op: "set", id: n.id, key: "from", value: op.id });
					if (n.to === op.id)
						out.push({ op: "set", id: n.id, key: "to", value: op.id });
				}
			}
			if (found.type === "group") {
				// 改 variants 可能清掉 picked，改 picked 会清掉 pickedBy：撤销时一并改回
				const g = found.value;
				if (op.key === "variants" && g.picked !== null)
					out.push({ op: "set", id: op.id, key: "picked", value: g.picked });
				if (
					(op.key === "variants" || op.key === "picked") &&
					g.pickedBy !== null
				)
					out.push({
						op: "set",
						id: op.id,
						key: "pickedBy",
						value: g.pickedBy,
					});
			}
			return out;
		}
		case "order": {
			const found = find(doc, op.id);
			return found ? [{ op: "order", id: op.id, index: found.index }] : [];
		}
		case "remove": {
			const found = find(doc, op.id);
			if (!found) return [];
			const out: CanvasOp[] = [];
			const restore = (
				type: CanvasObjectType,
				value: Found["value"],
				index: number,
			) => {
				out.push(
					{ op: "add", object: { type, ...value } },
					{ op: "order", id: value.id, index },
				);
			};
			switch (found.type) {
				case "canvas":
					// 吸附只能指向同一画布页上的画框，所以这里恢复的标注已带着各自的 from / to
					restore("canvas", found.value, found.index);
					doc.pages.forEach((p, i) => {
						if (p.canvas === op.id) restore("page", p, i);
					});
					doc.notes.forEach((n, i) => {
						if (n.canvas === op.id) restore("note", n, i);
					});
					out.push(...restoreGroups(doc, removeFrom(doc, op.id)));
					break;
				case "page":
					restore("page", found.value, found.index);
					for (const n of doc.notes) {
						if (!isShape(n)) continue;
						if (n.from === op.id)
							out.push({ op: "set", id: n.id, key: "from", value: op.id });
						if (n.to === op.id)
							out.push({ op: "set", id: n.id, key: "to", value: op.id });
					}
					out.push(...restoreGroups(doc, removeFrom(doc, op.id)));
					break;
				case "note":
				case "group":
					restore(found.type, found.value, found.index);
					break;
			}
			return out;
		}
	}
}

/**
 * 撤销 ops 的操作：对 doc 应用 ops 之后再应用返回的操作，文档回到 doc。只针对 ops 本身生成，
 * 供撤销 / 重做只撤自己的操作。
 */
export function inverseOps(
	doc: CanvasDoc,
	ops: readonly CanvasOp[],
): CanvasOp[] {
	const steps: CanvasOp[][] = [];
	let cur = doc;
	ops.forEach((op, i) => {
		steps.push(inverseOne(cur, op));
		cur = applyOne(cur, op, `ops[${i}]`);
	});
	return steps.reverse().flat();
}

/**
 * 在 ops 里每条改方案组 picked 的 set 之后紧跟着插入搬移画框的操作（纯函数）：
 * - 选定后落选、但不在「备选方案」画布页（ALTERNATES_ID）上的画框，整体平移到该页；该页不存在就先 add；
 * - 选中方案里位于备选方案页上的画框，整体平移回组的 canvas 页；选中方案里本来就不在备选方案页上的画框不动；
 * - 落点：左边缘 = 目标页上留下的画框的最右边缘 + PAGE_GAP*4，上边缘 = 它们的最小 y；目标页上没有画框时为 (0, 0)。
 *
 * 插入的操作写的是目标值，所以对已展开的 ops 再展开结果不变（已有备选方案页时，ops 里再 add 它的操作被去掉）。
 * 某条操作模拟应用时出错，后面的操作原样保留、不再展开，由 applyCanvasOps 报错。
 * 与服务端的 canvas.ExpandPicks 逐条相同：提交前调用，乐观状态与服务端回显一致。
 */
export function expandPicks(
	start: CanvasDoc,
	ops: readonly CanvasOp[],
): CanvasOp[] {
	let doc = start;
	let ok = true;
	const out: CanvasOp[] = [];
	ops.forEach((op, i) => {
		if (!ok) {
			out.push(op);
			return;
		}
		if (
			op.op === "add" &&
			op.object.type === "canvas" &&
			op.object.id === ALTERNATES_ID &&
			doc.canvases.some((c) => c.id === ALTERNATES_ID)
		)
			return;
		out.push(op);
		const where = `ops[${i}]`;
		try {
			doc = applyOne(doc, op, where);
			if (op.op !== "set" || op.key !== "picked") return;
			const found = find(doc, op.id);
			if (found?.type !== "group") return;
			const moves = pickMoves(doc, found.value);
			out.push(...moves);
			for (const m of moves) doc = applyOne(doc, m, where);
		} catch (e) {
			if (!(e instanceof CanvasError)) throw e;
			ok = false;
		}
	});
	return out;
}

/** 组 g 按当前 picked 该有的搬移操作（见 expandPicks）。 */
function pickMoves(doc: CanvasDoc, g: CanvasGroup): CanvasOp[] {
	if (g.picked === null) return [];
	const away: CanvasPage[] = [];
	const back: CanvasPage[] = [];
	for (const v of g.variants) {
		for (const key of v.pages) {
			const p = doc.pages.find((x) => x.id === key);
			if (!p) continue;
			if (v.id !== g.picked && p.canvas !== ALTERNATES_ID) away.push(p);
			else if (
				v.id === g.picked &&
				p.canvas === ALTERNATES_ID &&
				g.canvas !== ALTERNATES_ID
			)
				back.push(p);
		}
	}
	const moving = new Set([...away, ...back].map((p) => p.id));
	const ops: CanvasOp[] = [];
	if (away.length > 0) {
		if (find(doc, ALTERNATES_ID)?.type !== "canvas")
			ops.push({
				op: "add",
				object: { type: "canvas", id: ALTERNATES_ID, name: ALTERNATES_NAME },
			});
		ops.push(...shift(doc.pages, away, ALTERNATES_ID, moving));
	}
	ops.push(...shift(doc.pages, back, g.canvas, moving));
	return ops;
}

/** 把 block 整体平移到 canvas 页（落点见 expandPicks）；moving 里的画框不算目标页上留下的画框。 */
function shift(
	pages: readonly CanvasPage[],
	block: readonly CanvasPage[],
	canvas: string,
	moving: ReadonlySet<string>,
): CanvasOp[] {
	const [first] = block;
	if (!first) return [];
	let found = false;
	let right = 0;
	let top = 0;
	for (const p of pages) {
		if (p.canvas !== canvas || moving.has(p.id)) continue;
		if (!found || p.x + p.width > right) right = p.x + p.width;
		if (!found || p.y < top) top = p.y;
		found = true;
	}
	const left = found ? right + PAGE_GAP * 4 : 0;
	let minX = first.x;
	let minY = first.y;
	for (const p of block) {
		minX = Math.min(minX, p.x);
		minY = Math.min(minY, p.y);
	}
	return block.flatMap((p): CanvasOp[] => [
		{ op: "set", id: p.id, key: "canvas", value: canvas },
		{ op: "set", id: p.id, key: "x", value: round2(p.x + left - minX) },
		{ op: "set", id: p.id, key: "y", value: round2(p.y + top - minY) },
	]);
}

export interface Box {
	x: number;
	y: number;
	width: number;
	height: number;
}

/** 标注在画布上的包围盒（选择、吸附、命中测试用）；文字按字号估算。 */
export function noteBox(note: CanvasNote): Box {
	switch (note.kind) {
		case "text": {
			const longest = Math.max(
				1,
				...note.text.split("\n").map((l) => [...l].length),
			);
			const lines = note.text.split("\n").length;
			return {
				x: note.x,
				y: note.y,
				width: Math.max(40, longest * note.size * 0.6),
				height: lines * note.size * 1.3,
			};
		}
		case "sticky":
			return {
				x: note.x,
				y: note.y,
				width: note.width,
				height: Math.max(120, note.width * 0.75),
			};
		case "pen": {
			let left = Number.POSITIVE_INFINITY;
			let top = Number.POSITIVE_INFINITY;
			let right = Number.NEGATIVE_INFINITY;
			let bottom = Number.NEGATIVE_INFINITY;
			for (let i = 0; i + 1 < note.points.length; i += 3) {
				const x = note.points[i] ?? 0;
				const y = note.points[i + 1] ?? 0;
				left = Math.min(left, x);
				right = Math.max(right, x);
				top = Math.min(top, y);
				bottom = Math.max(bottom, y);
			}
			const pad = note.width / 2;
			return {
				x: left - pad,
				y: top - pad,
				width: right - left + 2 * pad,
				height: bottom - top + 2 * pad,
			};
		}
		default:
			return {
				x: Math.min(note.x1, note.x2),
				y: Math.min(note.y1, note.y2),
				width: Math.abs(note.x2 - note.x1),
				height: Math.abs(note.y2 - note.y1),
			};
	}
}

function centre(p: CanvasPage): { x: number; y: number } {
	return { x: p.x + p.width / 2, y: p.y + p.height / 2 };
}

/** 从画框中心朝 toward 方向射出、与画框边框的交点；toward 就在中心时取中心。 */
function edgeToward(
	p: CanvasPage,
	toward: { x: number; y: number },
): { x: number; y: number } {
	const c = centre(p);
	const dx = toward.x - c.x;
	const dy = toward.y - c.y;
	if (dx === 0 && dy === 0) return c;
	const sx = dx === 0 ? Number.POSITIVE_INFINITY : p.width / 2 / Math.abs(dx);
	const sy = dy === 0 ? Number.POSITIVE_INFINITY : p.height / 2 / Math.abs(dy);
	const s = Math.min(sx, sy);
	return { x: c.x + dx * s, y: c.y + dy * s };
}

/** 吸附了画框的形状实际的两个端点：吸附端落在画框边框上，随画框移动；没吸附的端点保持原值。 */
export function joinedEnds(
	note: ShapeNote,
	pages: readonly CanvasPage[],
): { x1: number; y1: number; x2: number; y2: number } {
	const from =
		note.from === null ? undefined : pages.find((p) => p.id === note.from);
	const to = note.to === null ? undefined : pages.find((p) => p.id === note.to);
	let { x1, y1, x2, y2 } = note;
	if (from)
		({ x: x1, y: y1 } = edgeToward(from, to ? centre(to) : { x: x2, y: y2 }));
	if (to)
		({ x: x2, y: y2 } = edgeToward(to, from ? centre(from) : { x: x1, y: y1 }));
	return { x1, y1, x2, y2 };
}

/** 客户端新建对象用的 id：前缀加 12 位随机小写字母与数字。 */
export function canvasId(prefix: string): string {
	const bytes = crypto.getRandomValues(new Uint8Array(12));
	return prefix + Array.from(bytes, (b) => (b % 36).toString(36)).join("");
}
