/**
 * 幻灯片 artifact 的模型（计划 M14 5.3）：`slides.json` 的类型、解析与就地改字。
 * 纯函数与类型，沙箱里的幻灯片查看器与应用里的导出（5.4）共用。
 * 结构与上限和服务端 `internal/artifacts/types_slides.go` 逐条一致；服务端已挡住不合格的版本，这里的解析是兜底，
 * 问题用结构化的 `DeckProblem` 报告，文案由使用方按界面语言给出。
 */

export type DeckTheme = "light" | "dark";
export type DeckAspect = "16:9" | "4:3";

/** 要点：字符串，或带一层子要点（子要点只能是字符串）。 */
export type Bullet = string | { text: string; bullets?: string[] };

/** 两栏版式的一栏，至少有一项。 */
export interface Column {
	heading?: string;
	text?: string;
	bullets?: string[];
}

export interface Stat {
	value: string;
	label?: string;
}

interface SlideBase {
	notes?: string;
}

export type Slide = SlideBase &
	(
		| { layout: "title" | "section"; title: string; subtitle?: string }
		| {
				layout: "bullets";
				title: string;
				bullets: Bullet[];
				image?: string;
				alt?: string;
		  }
		| { layout: "two-column"; left: Column; right: Column; title?: string }
		| {
				layout: "image";
				image: string;
				alt: string;
				title?: string;
				caption?: string;
		  }
		| { layout: "quote"; quote: string; attribution?: string }
		| { layout: "stats"; stats: Stat[]; title?: string }
		| { layout: "table"; columns: string[]; rows: string[][]; title?: string }
	);

export type SlideLayout = Slide["layout"];

export interface Deck {
	title?: string;
	theme?: DeckTheme;
	aspect?: DeckAspect;
	slides: Slide[];
}

/** 全部版式，顺序同服务端。 */
export const SLIDE_LAYOUTS: readonly SlideLayout[] = [
	"title",
	"section",
	"bullets",
	"two-column",
	"image",
	"quote",
	"stats",
	"table",
];

/** 上限（与服务端同值）。 */
export const DECK_LIMITS = {
	slides: 200,
	titleRunes: 200,
	textRunes: 1500,
	bulletRunes: 500,
	bullets: 12,
	subBullets: 8,
	stats: 4,
	tableColumns: 8,
	tableRows: 20,
	cellRunes: 300,
	notesRunes: 5000,
	pathBytes: 512,
} as const;

/** 画板尺寸（CSS 像素）。 */
export function slideSize(aspect: DeckAspect | undefined): {
	width: number;
	height: number;
} {
	return aspect === "4:3"
		? { width: 960, height: 720 }
		: { width: 1280, height: 720 };
}

export interface DeckPalette {
	background: string;
	text: string;
	muted: string;
	accent: string;
	surface: string;
	line: string;
}

/** 两套主题的颜色；幻灯片只用这些颜色。 */
export const DECK_PALETTES: Readonly<Record<DeckTheme, DeckPalette>> = {
	light: {
		background: "#ffffff",
		text: "#18181b",
		muted: "#71717a",
		accent: "#2563eb",
		surface: "#f4f4f5",
		line: "#e4e4e7",
	},
	dark: {
		background: "#0f0f11",
		text: "#fafafa",
		muted: "#a1a1aa",
		accent: "#60a5fa",
		surface: "#1c1c1f",
		line: "#2e2e33",
	},
};

// ---------------------------------------------------------------------------
// 解析

export type DeckIssue =
	| "json"
	| "object"
	| "string"
	| "array"
	| "empty"
	| "too_long"
	| "too_few"
	| "too_many"
	| "unknown"
	| "required"
	| "layout"
	| "enum"
	| "cells"
	| "column_empty";

/** 一处问题：位置写成 `slides[2].bullets[0].text`；`where` 为空表示整个 slides.json。 */
export interface DeckProblem {
	where: string;
	issue: DeckIssue;
	/** `too_long` 是字符上限，`too_few` / `too_many` 是项数界限，`cells` 是应有的格数。 */
	limit?: number;
}

export type DeckParse =
	| { ok: true; deck: Deck }
	| { ok: false; problems: DeckProblem[] };

const MAX_PROBLEMS = 20;

type Fields = Record<string, unknown>;

function at(where: string, key: string): string {
	return where === "" ? key : `${where}.${key}`;
}

const LAYOUT_FIELDS: Record<
	SlideLayout,
	{ required: string[]; optional: string[] }
> = {
	title: { required: ["title"], optional: ["subtitle"] },
	section: { required: ["title"], optional: ["subtitle"] },
	bullets: { required: ["title", "bullets"], optional: ["image", "alt"] },
	"two-column": { required: ["left", "right"], optional: ["title"] },
	image: { required: ["image", "alt"], optional: ["title", "caption"] },
	quote: { required: ["quote"], optional: ["attribution"] },
	stats: { required: ["stats"], optional: ["title"] },
	table: { required: ["columns", "rows"], optional: ["title"] },
};

class Checker {
	readonly problems: DeckProblem[] = [];

	add(where: string, issue: DeckIssue, limit?: number): void {
		if (this.problems.length >= MAX_PROBLEMS) return;
		this.problems.push(
			limit === undefined ? { where, issue } : { where, issue, limit },
		);
	}

	object(where: string, v: unknown, allowed: readonly string[]): Fields | null {
		if (typeof v !== "object" || v === null || Array.isArray(v)) {
			this.add(where, "object");
			return null;
		}
		const m = v as Fields;
		for (const k of Object.keys(m).sort()) {
			if (!allowed.includes(k)) this.add(at(where, k), "unknown");
		}
		return m;
	}

	str(where: string, v: unknown, limit: number, required: boolean): boolean {
		if (typeof v !== "string") {
			this.add(where, "string");
			return false;
		}
		if (required && v.trim() === "") {
			this.add(where, "empty");
			return false;
		}
		// 按码点计，与服务端的 utf8.RuneCountInString 一致
		if ([...v].length > limit) {
			this.add(where, "too_long", limit);
			return false;
		}
		return true;
	}

	array(where: string, v: unknown, min: number, max: number): unknown[] | null {
		if (!Array.isArray(v)) {
			this.add(where, "array");
			return null;
		}
		if (v.length < min) this.add(where, "too_few", min);
		else if (v.length > max) this.add(where, "too_many", max);
		return v;
	}

	deck(v: unknown): void {
		const m = this.object("", v, ["title", "theme", "aspect", "slides"]);
		if (!m) return;
		if ("title" in m) this.str("title", m.title, DECK_LIMITS.titleRunes, false);
		if ("theme" in m && m.theme !== "light" && m.theme !== "dark")
			this.add("theme", "enum");
		if ("aspect" in m && m.aspect !== "16:9" && m.aspect !== "4:3")
			this.add("aspect", "enum");
		if (!("slides" in m)) {
			this.add("slides", "required");
			return;
		}
		const slides = this.array("slides", m.slides, 1, DECK_LIMITS.slides);
		slides?.forEach((s, i) => {
			if (i < DECK_LIMITS.slides) this.slide(`slides[${i}]`, s);
		});
	}

	slide(where: string, v: unknown): void {
		if (typeof v !== "object" || v === null || Array.isArray(v)) {
			this.add(where, "object");
			return;
		}
		const m = v as Fields;
		if (!("layout" in m)) {
			this.add(at(where, "layout"), "required");
			return;
		}
		const name = m.layout as SlideLayout;
		const spec = typeof m.layout === "string" ? LAYOUT_FIELDS[name] : undefined;
		if (!spec || !Object.hasOwn(LAYOUT_FIELDS, name)) {
			this.add(at(where, "layout"), "layout");
			return;
		}
		const allowed = ["layout", ...spec.required, ...spec.optional, "notes"];
		for (const k of Object.keys(m).sort()) {
			if (!allowed.includes(k)) this.add(at(where, k), "unknown");
		}
		for (const k of spec.required) {
			if (!(k in m)) this.add(at(where, k), "required");
		}
		for (const k of [...spec.required, ...spec.optional, "notes"]) {
			if (k in m) this.field(at(where, k), k, m[k], spec.required.includes(k));
		}
		if (name !== "table") return;
		const cols = Array.isArray(m.columns) ? m.columns : [];
		const rows = Array.isArray(m.rows) ? m.rows : [];
		rows.forEach((r, i) => {
			if (Array.isArray(r) && cols.length > 0 && r.length !== cols.length)
				this.add(`${at(where, "rows")}[${i}]`, "cells", cols.length);
		});
	}

	field(where: string, key: string, v: unknown, required: boolean): void {
		switch (key) {
			case "title":
				this.str(where, v, DECK_LIMITS.titleRunes, required);
				return;
			case "subtitle":
			case "alt":
			case "caption":
			case "quote":
			case "attribution":
				this.str(where, v, DECK_LIMITS.textRunes, required);
				return;
			case "notes":
				this.str(where, v, DECK_LIMITS.notesRunes, false);
				return;
			case "image":
				// 服务端另查图片在版本里；这里只查形状
				if (typeof v !== "string") this.add(where, "string");
				else if (required && v.trim() === "") this.add(where, "empty");
				else if (new TextEncoder().encode(v).length > DECK_LIMITS.pathBytes)
					this.add(where, "too_long", DECK_LIMITS.pathBytes);
				return;
			case "bullets":
				this.bullets(where, v, DECK_LIMITS.bullets, true);
				return;
			case "left":
			case "right":
				this.column(where, v);
				return;
			case "stats": {
				const items = this.array(where, v, 1, DECK_LIMITS.stats);
				items?.forEach((it, i) => {
					const w = `${where}[${i}]`;
					const s = this.object(w, it, ["value", "label"]);
					if (!s) return;
					if ("value" in s)
						this.str(at(w, "value"), s.value, DECK_LIMITS.titleRunes, true);
					else this.add(at(w, "value"), "required");
					if ("label" in s)
						this.str(at(w, "label"), s.label, DECK_LIMITS.bulletRunes, false);
				});
				return;
			}
			case "columns": {
				const cols = this.array(where, v, 1, DECK_LIMITS.tableColumns);
				cols?.forEach((c, i) => {
					this.str(`${where}[${i}]`, c, DECK_LIMITS.cellRunes, false);
				});
				return;
			}
			case "rows": {
				const rows = this.array(where, v, 1, DECK_LIMITS.tableRows);
				rows?.forEach((r, i) => {
					const w = `${where}[${i}]`;
					const cells = this.array(w, r, 1, DECK_LIMITS.tableColumns);
					cells?.forEach((cell, j) => {
						this.str(`${w}[${j}]`, cell, DECK_LIMITS.cellRunes, false);
					});
				});
				return;
			}
		}
	}

	bullets(where: string, v: unknown, max: number, nested: boolean): void {
		const items = this.array(where, v, 1, max);
		items?.forEach((it, i) => {
			const w = `${where}[${i}]`;
			if (typeof it === "string" || !nested) {
				this.str(w, it, DECK_LIMITS.bulletRunes, true);
				return;
			}
			const m = this.object(w, it, ["text", "bullets"]);
			if (!m) return;
			if ("text" in m)
				this.str(at(w, "text"), m.text, DECK_LIMITS.bulletRunes, true);
			else this.add(at(w, "text"), "required");
			if ("bullets" in m)
				this.bullets(
					at(w, "bullets"),
					m.bullets,
					DECK_LIMITS.subBullets,
					false,
				);
		});
	}

	column(where: string, v: unknown): void {
		const m = this.object(where, v, ["heading", "text", "bullets"]);
		if (!m) return;
		if (Object.keys(m).length === 0) this.add(where, "column_empty");
		if ("heading" in m)
			this.str(at(where, "heading"), m.heading, DECK_LIMITS.titleRunes, false);
		if ("text" in m)
			this.str(at(where, "text"), m.text, DECK_LIMITS.textRunes, false);
		if ("bullets" in m)
			this.bullets(at(where, "bullets"), m.bullets, DECK_LIMITS.bullets, false);
	}
}

/** 严格解析 `slides.json`：结构、版式字段与上限都与服务端一致，不允许未知字段。 */
export function parseDeck(text: string): DeckParse {
	let raw: unknown;
	try {
		raw = JSON.parse(text);
	} catch {
		return { ok: false, problems: [{ where: "", issue: "json" }] };
	}
	const c = new Checker();
	c.deck(raw);
	return c.problems.length > 0
		? { ok: false, problems: c.problems }
		: { ok: true, deck: raw as Deck };
}

// ---------------------------------------------------------------------------
// 就地改字

/**
 * 一张幻灯片上能就地编辑的文字位置（相对这张幻灯片，如 `bullets[1].text`），按显示顺序。
 * 不含 `alt` 与 `notes`（画面上看不到）和图片路径。
 */
export function textPaths(slide: Slide): string[] {
	const out: string[] = [];
	const opt = (key: string, v: string | undefined) => {
		if (v !== undefined) out.push(key);
	};
	const list = (key: string, items: readonly Bullet[]) => {
		items.forEach((b, i) => {
			if (typeof b === "string") {
				out.push(`${key}[${i}]`);
				return;
			}
			out.push(`${key}[${i}].text`);
			b.bullets?.forEach((_, j) => {
				out.push(`${key}[${i}].bullets[${j}]`);
			});
		});
	};
	switch (slide.layout) {
		case "title":
		case "section":
			out.push("title");
			opt("subtitle", slide.subtitle);
			break;
		case "bullets":
			out.push("title");
			list("bullets", slide.bullets);
			break;
		case "two-column":
			opt("title", slide.title);
			for (const side of ["left", "right"] as const) {
				const col = slide[side];
				opt(`${side}.heading`, col.heading);
				opt(`${side}.text`, col.text);
				if (col.bullets) list(`${side}.bullets`, col.bullets);
			}
			break;
		case "image":
			opt("title", slide.title);
			opt("caption", slide.caption);
			break;
		case "quote":
			out.push("quote");
			opt("attribution", slide.attribution);
			break;
		case "stats":
			opt("title", slide.title);
			slide.stats.forEach((s, i) => {
				out.push(`stats[${i}].value`);
				if (s.label !== undefined) out.push(`stats[${i}].label`);
			});
			break;
		case "table":
			opt("title", slide.title);
			slide.columns.forEach((_, i) => {
				out.push(`columns[${i}]`);
			});
			slide.rows.forEach((r, i) => {
				r.forEach((_, j) => {
					out.push(`rows[${i}][${j}]`);
				});
			});
			break;
	}
	return out;
}

type Segment = string | number;

const SEGMENT_RE = /([A-Za-z][A-Za-z0-9-]*)|\[(\d+)\]/y;

/** `slides[1].bullets[0]` → `["slides", 1, "bullets", 0]`；不合法时返回 `null`。 */
export function parseTextPath(path: string): Segment[] | null {
	const out: Segment[] = [];
	let i = 0;
	while (i < path.length) {
		if (out.length > 0 && path[i] === ".") i++;
		SEGMENT_RE.lastIndex = i;
		const m = SEGMENT_RE.exec(path);
		if (!m) return null;
		out.push(m[1] ?? Number(m[2]));
		i = SEGMENT_RE.lastIndex;
	}
	return out.length > 0 ? out : null;
}

/**
 * 把整份幻灯片里 `path`（如 `slides[1].bullets[0]`）处的文字换成 `text`，返回新对象；沿途只复制被改到的那一条链，
 * 其余字段原样共用。位置不存在、指向的不是字符串或文字没变时原样返回 `deck`。
 */
export function withText(deck: Deck, path: string, text: string): Deck {
	const segs = parseTextPath(path);
	if (!segs) return deck;
	const set = (node: unknown, k: number): unknown => {
		const seg = segs[k] as Segment;
		if (Array.isArray(node)) {
			if (typeof seg !== "number" || seg >= node.length) return node;
			const child = node[seg];
			const next = k === segs.length - 1 ? leaf(child) : set(child, k + 1);
			if (next === child) return node;
			const copy = node.slice();
			copy[seg] = next;
			return copy;
		}
		if (typeof node !== "object" || node === null || typeof seg !== "string")
			return node;
		const obj = node as Fields;
		if (!Object.hasOwn(obj, seg)) return node;
		const child = obj[seg];
		const next = k === segs.length - 1 ? leaf(child) : set(child, k + 1);
		return next === child ? node : { ...obj, [seg]: next };
	};
	const leaf = (child: unknown) => (typeof child === "string" ? text : child);
	return set(deck, 0) as Deck;
}

/** 读整份幻灯片里 `path` 处的文字；不存在或不是字符串时为 `undefined`。 */
export function textAt(deck: Deck, path: string): string | undefined {
	const segs = parseTextPath(path);
	if (!segs) return undefined;
	let node: unknown = deck;
	for (const seg of segs) {
		if (typeof node !== "object" || node === null) return undefined;
		node = (node as Record<string | number, unknown>)[seg];
	}
	return typeof node === "string" ? node : undefined;
}
