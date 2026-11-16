// 画布 artifact 的文档模型：canvas.json 的严格解析、操作与撤销。
//
// 与 internal/artifacts/canvas 的 Go 实现逐条相同，两边共用 schema/testdata/canvas-ops.json 的测试向量；
// 改规则时两边一起改。文档里 canvases 的元素在界面上叫「页面」，pages 的元素叫「画框」，notes 是标注。

export const CANVAS_FILE = "canvas.json";
export const CANVAS_FORMAT = 1;

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
} as const;

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

export interface CanvasSheet {
	id: string;
	name: string;
}

export interface CanvasPage {
	id: string;
	canvas: string;
	name: string;
	/** 画框的 HTML 文件（artifact 里的相对路径）。 */
	file: string;
	x: number;
	y: number;
	width: number;
	height: number;
	locked: boolean;
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

export interface CanvasDoc {
	format: typeof CANVAS_FORMAT;
	canvases: CanvasSheet[];
	/** 绘制顺序，先画的在下面。 */
	pages: CanvasPage[];
	/** 绘制顺序，画在画框之上。 */
	notes: CanvasNote[];
}

export type CanvasObjectType = "canvas" | "page" | "note";

/**
 * 对文档的一次修改。add 一个画框时 file 可省略（取 page-<id>.html）；
 * copyOf 表示新画框的 HTML 从那个画框复制（落版本时由服务端复制文件）。
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

const DOC_KEYS = ["format", "canvases", "pages", "notes"];
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

/** id 不重复、画框与标注都在存在的画布页上、形状吸附的是同一画布页上存在的画框。 */
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
	doc.pages.forEach((p, i) => {
		if (kinds.get(p.canvas) !== "canvas") {
			fail(
				`pages[${i}].canvas`,
				`没有 id 为 ${JSON.stringify(p.canvas)} 的画布页`,
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
	if (has(raw, "format") && raw.format !== CANVAS_FORMAT)
		fail("format", `必须是 ${CANVAS_FORMAT}`);
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
	const doc: CanvasDoc = {
		format: CANVAS_FORMAT,
		canvases,
		pages: place(pages),
		notes,
	};
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
				if (type !== "canvas" && type !== "page" && type !== "note") {
					fail(at(where, "object.type"), "只能是 canvas、page、note 之一");
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
	| { type: "note"; index: number; value: CanvasNote };

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
	return null;
}

const CANVAS_SETTABLE = ["name"];
const PAGE_SETTABLE = ["name", "file", "x", "y", "width", "height", "locked"];
const NOTE_FIXED = ["id", "canvas", "kind", "by"];
const TYPE_LABEL: Record<CanvasObjectType, string> = {
	canvas: "画布页",
	page: "画框",
	note: "标注",
};

function settable(found: Found): readonly string[] {
	if (found.type === "canvas") return CANVAS_SETTABLE;
	if (found.type === "page") return PAGE_SETTABLE;
	return NOTE_KEYS[found.value.kind].filter((k) => !NOTE_FIXED.includes(k));
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
	if (type === "page" && !has(obj, "file") && typeof obj.id === "string")
		obj.file = `page-${obj.id}.html`;
	const ow = at(where, "object");
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
		default:
			return fail(at(ow, "type"), "只能是 canvas、page、note 之一");
	}
}

function setIn(
	doc: CanvasDoc,
	op: Extract<CanvasOp, { op: "set" }>,
	where: string,
): CanvasDoc {
	const found = find(doc, op.id);
	if (!found) return doc;
	const keys = settable(found);
	if (!keys.includes(op.key)) {
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
		case "page":
			return {
				...doc,
				pages: replace(
					doc.pages,
					pageOf({ ...found.value, [op.key]: op.value }, where).page,
				),
			};
		case "note":
			return {
				...doc,
				notes: replace(
					doc.notes,
					noteOf({ ...found.value, [op.key]: op.value }, where),
				),
			};
	}
}

/** 删对象：删画布页连带删它的画框与标注；删画框把吸附它的 from / to 置空。 */
function removeFrom(doc: CanvasDoc, key: string): CanvasDoc {
	const found = find(doc, key);
	if (!found) return doc;
	let next: CanvasDoc;
	const gone = new Set<string>();
	switch (found.type) {
		case "canvas":
			for (const p of doc.pages) if (p.canvas === key) gone.add(p.id);
			next = {
				...doc,
				canvases: doc.canvases.filter((c) => c.id !== key),
				pages: doc.pages.filter((p) => p.canvas !== key),
				notes: doc.notes.filter((n) => n.canvas !== key),
			};
			break;
		case "page":
			gone.add(key);
			next = { ...doc, pages: doc.pages.filter((p) => p.id !== key) };
			break;
		case "note":
			return { ...doc, notes: doc.notes.filter((n) => n.id !== key) };
	}
	if (gone.size === 0) return next;
	return {
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
	};
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
			return removeFrom(doc, op.id);
		case "order":
			return orderIn(doc, op.id, op.index);
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
	if (doc.canvases.length === 0) fail("canvases", "至少要保留一个画布页");
	if (doc.canvases.length > CANVAS_LIMITS.canvases)
		fail("canvases", `最多 ${CANVAS_LIMITS.canvases} 个画布页`);
	if (doc.pages.length > CANVAS_LIMITS.pages)
		fail("pages", `最多 ${CANVAS_LIMITS.pages} 个画框`);
	if (doc.notes.length > CANVAS_LIMITS.notes)
		fail("notes", `最多 ${CANVAS_LIMITS.notes} 个标注`);
	checkLinks(doc);
	if (utf8Length(stringifyCanvasDoc(doc)) > CANVAS_LIMITS.jsonBytes) {
		fail("", `canvas.json 会超过 ${CANVAS_LIMITS.jsonBytes >> 20} MiB`);
	}
	return doc;
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
			return [{ op: "set", id: op.id, key: op.key, value: current[op.key] }];
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
					break;
				case "note":
					restore("note", found.value, found.index);
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
