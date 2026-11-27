/**
 * 视图（M33）的规格：`render_ui` 参数的类型与宽容归一化。
 *
 * 契约见 docs/plan-m33.md 的 C2。归一化同时服务于三种来源（C10）：结果的 `details.view.spec`、
 * `tool_execution_start` 的已修复参数、流式中逐 delta 补全括号解析出的残缺参数——所以对每个块只做最低要求
 * 检查，不满足的跳过（不报错），其余字段按类型取用、取不到就省略。每个块带上它在原始参数里的块路径
 * （跳过的块不让后面的块路径错位），作答与锁定都按块路径记。
 */

export type Tone = "neutral" | "info" | "success" | "warning" | "danger";
export type Trend = "up" | "down" | "flat";
export type ViewWidth = "normal" | "wide";
export type ChartType =
	| "bar"
	| "line"
	| "pie"
	| "doughnut"
	| "radar"
	| "polarArea"
	| "scatter"
	| "bubble";
export type FieldKind =
	| "text"
	| "textarea"
	| "number"
	| "email"
	| "url"
	| "date"
	| "time"
	| "select"
	| "multiselect"
	| "radio"
	| "checkbox"
	| "switch"
	| "slider";

/** 表单字段的取值：多选是字符串数组，复选 / 开关是布尔，数字 / 滑块是数，空值为 null。 */
export type FieldValue = string | number | boolean | string[] | null;
export type TableCell = string | number | boolean | null;

export interface FieldOption {
	label: string;
	value: string;
}

export interface FormField {
	name: string;
	label: string;
	kind: FieldKind;
	placeholder?: string;
	description?: string;
	required: boolean;
	defaultValue?: string | number | boolean | string[];
	span?: "full";
	/** select / multiselect / radio 一定有（至少一项）；其余种类没有。 */
	options?: FieldOption[];
	min?: number;
	max?: number;
	step?: number;
}

interface BlockBase {
	/** 块路径（C2）：`2`、`0.1`、`3.c0.2`、`1.t1.0`。 */
	path: string;
}

export interface HeadingBlock extends BlockBase {
	type: "heading";
	text: string;
	level: 1 | 2 | 3;
}
export interface TextBlock extends BlockBase {
	type: "text";
	/** Markdown。 */
	text: string;
	muted: boolean;
}
export interface BadgesBlock extends BlockBase {
	type: "badges";
	items: { label: string; tone: Tone }[];
}
export interface AlertBlock extends BlockBase {
	type: "alert";
	tone: Tone;
	title?: string;
	text: string;
}
export interface DividerBlock extends BlockBase {
	type: "divider";
}
export interface StatItem {
	label: string;
	value: string | number;
	delta?: string;
	trend?: Trend;
	hint?: string;
}
export interface StatsBlock extends BlockBase {
	type: "stats";
	items: StatItem[];
}
export interface KeyValueBlock extends BlockBase {
	type: "keyValue";
	items: { label: string; value: string | number }[];
}
export interface TableColumn {
	key: string;
	label: string;
	align: "left" | "right" | "center";
}
export interface TableBlock extends BlockBase {
	type: "table";
	caption?: string;
	columns: TableColumn[];
	rows: Record<string, TableCell>[];
}
export interface ListBlock extends BlockBase {
	type: "list";
	ordered: boolean;
	items: { title: string; description?: string; meta?: string }[];
}
export interface ProgressBlock extends BlockBase {
	type: "progress";
	label?: string;
	value: number;
	max: number;
	tone: Tone;
}
export interface ImageBlock extends BlockBase {
	type: "image";
	src: string;
	alt: string;
	caption?: string;
}
export interface ChartBlock extends BlockBase {
	type: "chart";
	chartType: ChartType;
	title?: string;
	/** 标准 Chart.js `data`，已去掉原型污染键（`stripPrototypeKeys`）。 */
	data: { labels?: (string | number)[]; datasets: Record<string, unknown>[] };
	/** 标准 Chart.js `options`，已去掉原型污染键。 */
	options?: Record<string, unknown>;
	height: number;
}
export interface SvgBlock extends BlockBase {
	type: "svg";
	/** 原始标记；渲染前必须经 `sanitizeSvgMarkup`。 */
	svg: string;
	caption?: string;
}
export interface FormBlock extends BlockBase {
	type: "form";
	title?: string;
	description?: string;
	columns: 1 | 2 | 3;
	fields: FormField[];
	submitLabel?: string;
}
export interface ActionButton {
	label: string;
	prompt: string;
	variant: "primary" | "secondary";
}
export interface ActionsBlock extends BlockBase {
	type: "actions";
	buttons: ActionButton[];
}
export interface CardBlock extends BlockBase {
	type: "card";
	title?: string;
	description?: string;
	blocks: ViewBlock[];
}
export interface ColumnsBlock extends BlockBase {
	type: "columns";
	/** 与 `columns` 等长；没给或给错时为 undefined（等宽）。 */
	weights?: number[];
	columns: { blocks: ViewBlock[] }[];
}
export interface TabsBlock extends BlockBase {
	type: "tabs";
	tabs: { label: string; blocks: ViewBlock[] }[];
}

export type LeafBlock =
	| HeadingBlock
	| TextBlock
	| BadgesBlock
	| AlertBlock
	| DividerBlock
	| StatsBlock
	| KeyValueBlock
	| TableBlock
	| ListBlock
	| ProgressBlock
	| ImageBlock
	| ChartBlock
	| SvgBlock
	| FormBlock
	| ActionsBlock;
export type ContainerBlock = CardBlock | ColumnsBlock | TabsBlock;
export type ViewBlock = LeafBlock | ContainerBlock;
export type InteractiveBlock = FormBlock | ActionsBlock;

/** 归一化后的 `render_ui` 文档。 */
export interface UiViewSpec {
	title?: string;
	description?: string;
	width: ViewWidth;
	blocks: ViewBlock[];
}

/** 归一化后的 `render_html` 规格（HTML 正文不在这里：帧内容由服务端从转录取）。 */
export interface HtmlViewSpec {
	title?: string;
	libs: ("tailwind" | "chartjs")[];
	/** 固定高度；省略 = 跟随内容。 */
	height?: number;
}

export const VIEW_TOOL_UI = "render_ui";
export const VIEW_TOOL_HTML = "render_html";
/** 容器最多嵌套的层数（C2）。 */
export const MAX_CONTAINER_DEPTH = 3;

const TONES: readonly Tone[] = [
	"neutral",
	"info",
	"success",
	"warning",
	"danger",
];
const CHART_TYPES: readonly ChartType[] = [
	"bar",
	"line",
	"pie",
	"doughnut",
	"radar",
	"polarArea",
	"scatter",
	"bubble",
];
const FIELD_KINDS: readonly FieldKind[] = [
	"text",
	"textarea",
	"number",
	"email",
	"url",
	"date",
	"time",
	"select",
	"multiselect",
	"radio",
	"checkbox",
	"switch",
	"slider",
];
/** 需要 `options` 的字段种类。 */
export const CHOICE_KINDS: Partial<Record<FieldKind, true>> = {
	select: true,
	multiselect: true,
	radio: true,
};
const FIELD_NAME = /^[A-Za-z][\w-]{0,63}$/;
const IMAGE_SRC = /^(?:https:\/\/|data:image\/)/i;

type Json = Record<string, unknown>;

function isObj(v: unknown): v is Json {
	return typeof v === "object" && v !== null && !Array.isArray(v);
}
function str(v: unknown, max = 4000): string | undefined {
	return typeof v === "string" && v.length > 0 ? v.slice(0, max) : undefined;
}
function num(v: unknown): number | undefined {
	if (typeof v === "number") return Number.isFinite(v) ? v : undefined;
	if (typeof v === "string" && v.trim() !== "") {
		const n = Number(v);
		return Number.isFinite(n) ? n : undefined;
	}
	return undefined;
}
function strOrNum(v: unknown): string | number | undefined {
	if (typeof v === "number") return Number.isFinite(v) ? v : undefined;
	return typeof v === "string" ? v : undefined;
}
function oneOf<T extends string | number>(
	v: unknown,
	allowed: readonly T[],
): T | undefined {
	return allowed.includes(v as T) ? (v as T) : undefined;
}
function arr(v: unknown): unknown[] {
	return Array.isArray(v) ? v : [];
}
function clamp(n: number, lo: number, hi: number): number {
	return Math.min(hi, Math.max(lo, n));
}

const POLLUTING_KEYS = new Set(["__proto__", "constructor", "prototype"]);

/** 深拷贝并去掉 `__proto__` / `constructor` / `prototype` 键（交给 Chart.js 前必须过一遍，C10）。 */
export function stripPrototypeKeys<T>(value: T): T {
	if (Array.isArray(value)) return value.map(stripPrototypeKeys) as T;
	if (!isObj(value)) return value;
	const out: Json = {};
	for (const [k, v] of Object.entries(value)) {
		if (!POLLUTING_KEYS.has(k)) out[k] = stripPrototypeKeys(v);
	}
	return out as T;
}

function normalizeField(raw: unknown): FormField | null {
	if (!isObj(raw)) return null;
	const name = typeof raw.name === "string" ? raw.name : "";
	const label = str(raw.label, 200);
	const kind = oneOf(raw.kind, FIELD_KINDS);
	if (!FIELD_NAME.test(name) || !label || !kind) return null;
	const field: FormField = {
		name,
		label,
		kind,
		required: raw.required === true,
	};
	const placeholder = str(raw.placeholder, 200);
	if (placeholder) field.placeholder = placeholder;
	const description = str(raw.description, 400);
	if (description) field.description = description;
	if (raw.span === "full") field.span = "full";
	const dv = raw.defaultValue;
	if (
		typeof dv === "string" ||
		typeof dv === "boolean" ||
		(typeof dv === "number" && Number.isFinite(dv))
	) {
		field.defaultValue = dv;
	} else if (Array.isArray(dv)) {
		field.defaultValue = dv.filter((x): x is string => typeof x === "string");
	}
	if (CHOICE_KINDS[kind]) {
		const options: FieldOption[] = [];
		const seen = new Set<string>();
		for (const o of arr(raw.options).slice(0, 50)) {
			if (!isObj(o)) continue;
			const ol = str(o.label, 200);
			const ov =
				typeof o.value === "number" ? String(o.value) : str(o.value, 200);
			if (!ol || !ov || seen.has(ov)) continue;
			seen.add(ov);
			options.push({ label: ol, value: ov });
		}
		if (options.length > 0) {
			field.options = options;
		} else {
			// 选择题没给选项（schema 只在描述里要求）：退成文本框，表单照样能作答——await 的调用不会挂着一个答不了的视图
			field.kind = "text";
			if (Array.isArray(field.defaultValue))
				field.defaultValue = field.defaultValue.join(", ");
		}
	}
	const min = num(raw.min);
	if (min !== undefined) field.min = min;
	const max = num(raw.max);
	if (max !== undefined) field.max = max;
	const step = num(raw.step);
	if (step !== undefined && step > 0) field.step = step;
	return field;
}

function normalizeLeaf(raw: Json, path: string): LeafBlock | null {
	switch (raw.type) {
		case "heading": {
			const text = str(raw.text, 200);
			if (!text) return null;
			return {
				type: "heading",
				path,
				text,
				level: oneOf(raw.level, [1, 2, 3] as const) ?? 2,
			};
		}
		case "text": {
			const text = str(raw.text);
			if (!text) return null;
			return { type: "text", path, text, muted: raw.muted === true };
		}
		case "badges": {
			const items = arr(raw.items)
				.slice(0, 20)
				.flatMap((it) => {
					const label = isObj(it) ? str(it.label, 200) : undefined;
					return label && isObj(it)
						? [{ label, tone: oneOf(it.tone, TONES) ?? "neutral" }]
						: [];
				});
			return items.length ? { type: "badges", path, items } : null;
		}
		case "alert": {
			const text = str(raw.text, 2000);
			if (!text) return null;
			const block: AlertBlock = {
				type: "alert",
				path,
				tone: oneOf(raw.tone, TONES) ?? "neutral",
				text,
			};
			const title = str(raw.title, 200);
			if (title) block.title = title;
			return block;
		}
		case "divider":
			return { type: "divider", path };
		case "stats": {
			const items = arr(raw.items)
				.slice(0, 8)
				.flatMap((it): StatItem[] => {
					if (!isObj(it)) return [];
					const label = str(it.label, 200);
					const value = strOrNum(it.value);
					if (!label || value === undefined) return [];
					const item: StatItem = { label, value };
					const delta = str(it.delta, 40);
					if (delta) item.delta = delta;
					const trend = oneOf(it.trend, ["up", "down", "flat"] as const);
					if (trend) item.trend = trend;
					const hint = str(it.hint, 120);
					if (hint) item.hint = hint;
					return [item];
				});
			return items.length ? { type: "stats", path, items } : null;
		}
		case "keyValue": {
			const items = arr(raw.items)
				.slice(0, 40)
				.flatMap((it) => {
					if (!isObj(it)) return [];
					const label = str(it.label, 200);
					const value = strOrNum(it.value);
					return label && value !== undefined ? [{ label, value }] : [];
				});
			return items.length ? { type: "keyValue", path, items } : null;
		}
		case "table": {
			const columns = arr(raw.columns)
				.slice(0, 12)
				.flatMap((c): TableColumn[] => {
					if (!isObj(c)) return [];
					const key = str(c.key, 64);
					const label = str(c.label, 200);
					return key && label
						? [
								{
									key,
									label,
									align:
										oneOf(c.align, ["left", "right", "center"] as const) ??
										"left",
								},
							]
						: [];
				});
			if (!columns.length) return null;
			const rows = arr(raw.rows)
				.slice(0, 200)
				.flatMap((r) => {
					if (!isObj(r)) return [];
					const row: Record<string, TableCell> = {};
					for (const c of columns) {
						const v = r[c.key];
						row[c.key] =
							typeof v === "string" ||
							typeof v === "boolean" ||
							(typeof v === "number" && Number.isFinite(v))
								? v
								: null;
					}
					return [row];
				});
			const block: TableBlock = { type: "table", path, columns, rows };
			const caption = str(raw.caption, 200);
			if (caption) block.caption = caption;
			return block;
		}
		case "list": {
			const items = arr(raw.items)
				.slice(0, 50)
				.flatMap((it) => {
					if (!isObj(it)) return [];
					const title = str(it.title, 200);
					if (!title) return [];
					const item: ListBlock["items"][number] = { title };
					const description = str(it.description, 400);
					if (description) item.description = description;
					const meta = str(it.meta, 80);
					if (meta) item.meta = meta;
					return [item];
				});
			return items.length
				? { type: "list", path, ordered: raw.ordered === true, items }
				: null;
		}
		case "progress": {
			const value = num(raw.value);
			if (value === undefined) return null;
			const max = num(raw.max);
			const block: ProgressBlock = {
				type: "progress",
				path,
				value,
				max: max !== undefined && max > 0 ? max : 100,
				tone: oneOf(raw.tone, TONES) ?? "neutral",
			};
			const label = str(raw.label, 200);
			if (label) block.label = label;
			return block;
		}
		case "image": {
			const src = typeof raw.src === "string" ? raw.src : "";
			if (!IMAGE_SRC.test(src)) return null;
			const block: ImageBlock = {
				type: "image",
				path,
				src,
				alt: str(raw.alt, 200) ?? "",
			};
			const caption = str(raw.caption, 200);
			if (caption) block.caption = caption;
			return block;
		}
		case "chart": {
			const chartType = oneOf(raw.chartType, CHART_TYPES);
			if (!chartType || !isObj(raw.data)) return null;
			const data = stripPrototypeKeys(raw.data);
			const datasets = arr(data.datasets)
				.slice(0, 12)
				.filter((d): d is Json => isObj(d) && Array.isArray(d.data));
			if (!datasets.length) return null;
			const labels = Array.isArray(data.labels)
				? data.labels.filter(
						(l): l is string | number =>
							typeof l === "string" || typeof l === "number",
					)
				: undefined;
			const block: ChartBlock = {
				type: "chart",
				path,
				chartType,
				data: { ...data, labels, datasets },
				height: clamp(num(raw.height) ?? 280, 160, 560),
			};
			if (labels === undefined) delete block.data.labels;
			if (isObj(raw.options)) block.options = stripPrototypeKeys(raw.options);
			const title = str(raw.title, 200);
			if (title) block.title = title;
			return block;
		}
		case "svg": {
			const svg = str(raw.svg, 50_000);
			if (!svg) return null;
			const block: SvgBlock = { type: "svg", path, svg };
			const caption = str(raw.caption, 200);
			if (caption) block.caption = caption;
			return block;
		}
		case "form": {
			const fields: FormField[] = [];
			const names = new Set<string>();
			for (const f of arr(raw.fields).slice(0, 30)) {
				const field = normalizeField(f);
				if (!field || names.has(field.name)) continue;
				names.add(field.name);
				fields.push(field);
			}
			if (!fields.length) return null;
			const block: FormBlock = {
				type: "form",
				path,
				columns: oneOf(raw.columns, [1, 2, 3] as const) ?? 1,
				fields,
			};
			const title = str(raw.title, 200);
			if (title) block.title = title;
			const description = str(raw.description, 600);
			if (description) block.description = description;
			const submitLabel = str(raw.submitLabel, 40);
			if (submitLabel) block.submitLabel = submitLabel;
			return block;
		}
		case "actions": {
			const buttons = arr(raw.buttons)
				.slice(0, 6)
				.flatMap((b): ActionButton[] => {
					if (!isObj(b)) return [];
					const label = str(b.label, 200);
					const prompt = str(b.prompt, 2000);
					return label && prompt
						? [
								{
									label,
									prompt,
									variant:
										oneOf(b.variant, ["primary", "secondary"] as const) ??
										"secondary",
								},
							]
						: [];
				});
			return buttons.length ? { type: "actions", path, buttons } : null;
		}
		default:
			return null;
	}
}

function normalizeBlocks(
	raw: unknown,
	parent: string,
	depth: number,
	limit: number,
): ViewBlock[] {
	const out: ViewBlock[] = [];
	arr(raw)
		.slice(0, limit)
		.forEach((b, i) => {
			const block = normalizeBlock(
				b,
				parent ? `${parent}.${i}` : String(i),
				depth,
			);
			if (block) out.push(block);
		});
	return out;
}

/** 容器的一列：`{ blocks }`，模型偶尔直接给一个块，按只有它一个块处理。 */
function columnBlocks(raw: unknown): unknown {
	if (isObj(raw) && !Array.isArray(raw.blocks) && typeof raw.type === "string")
		return [raw];
	return isObj(raw) ? raw.blocks : undefined;
}

function normalizeBlock(
	raw: unknown,
	path: string,
	depth: number,
): ViewBlock | null {
	if (!isObj(raw) || typeof raw.type !== "string") return null;
	const t = raw.type;
	if (t !== "card" && t !== "columns" && t !== "tabs")
		return normalizeLeaf(raw, path);
	// depth 是当前块所在的容器层数；容器本身再加一层，超过上限的容器整个跳过
	if (depth >= MAX_CONTAINER_DEPTH) return null;
	if (t === "card") {
		const block: CardBlock = {
			type: "card",
			path,
			blocks: normalizeBlocks(raw.blocks, path, depth + 1, 20),
		};
		const title = str(raw.title, 200);
		if (title) block.title = title;
		const description = str(raw.description, 600);
		if (description) block.description = description;
		return block;
	}
	if (t === "columns") {
		const columns = arr(raw.columns)
			.slice(0, 4)
			.map((c, i) => ({
				blocks: normalizeBlocks(
					columnBlocks(c),
					`${path}.c${i}`,
					depth + 1,
					20,
				),
			}));
		if (!columns.length) return null;
		const block: ColumnsBlock = { type: "columns", path, columns };
		const weights = arr(raw.weights).map(num);
		if (
			weights.length === columns.length &&
			weights.every((w) => w !== undefined && w >= 1 && w <= 6)
		) {
			block.weights = weights as number[];
		}
		return block;
	}
	const tabs = arr(raw.tabs)
		.slice(0, 6)
		.flatMap((tab, i) => {
			const label = isObj(tab) ? str(tab.label, 200) : undefined;
			if (!label || !isObj(tab)) return [];
			return [
				{
					label,
					blocks: normalizeBlocks(tab.blocks, `${path}.t${i}`, depth + 1, 20),
				},
			];
		});
	return tabs.length ? { type: "tabs", path, tabs } : null;
}

/**
 * 归一化 `render_ui` 的参数（规格来源见 C10）。取不到任何块时返回 null（还在流式开头，或参数完全不对）。
 * 只读取，不修改入参。
 */
export function normalizeUiSpec(raw: unknown): UiViewSpec | null {
	if (!isObj(raw)) return null;
	const blocks = normalizeBlocks(raw.blocks, "", 0, 40);
	if (!blocks.length) return null;
	const spec: UiViewSpec = {
		width: raw.width === "wide" ? "wide" : "normal",
		blocks,
	};
	const title = str(raw.title, 200);
	if (title) spec.title = title;
	const description = str(raw.description, 600);
	if (description) spec.description = description;
	return spec;
}

/** 归一化 `render_html` 的规格部分（`libs` 兼容单个字符串与数组）。 */
export function normalizeHtmlSpec(raw: unknown): HtmlViewSpec {
	const o = isObj(raw) ? raw : {};
	const libsRaw = typeof o.libs === "string" ? [o.libs] : arr(o.libs);
	const libs = [
		...new Set(
			libsRaw.filter(
				(l): l is "tailwind" | "chartjs" => l === "tailwind" || l === "chartjs",
			),
		),
	];
	const spec: HtmlViewSpec = { libs };
	const title = str(o.title, 200);
	if (title) spec.title = title;
	const height = num(o.height);
	if (height !== undefined) spec.height = clamp(height, 120, 1600);
	return spec;
}

/** 深度优先列出全部交互块（表单与按钮组）。 */
export function interactiveBlocks(
	blocks: readonly ViewBlock[],
): InteractiveBlock[] {
	const out: InteractiveBlock[] = [];
	const walk = (list: readonly ViewBlock[]) => {
		for (const b of list) {
			if (b.type === "form" || b.type === "actions") out.push(b);
			else if (b.type === "card") walk(b.blocks);
			else if (b.type === "columns") for (const c of b.columns) walk(c.blocks);
			else if (b.type === "tabs") for (const t of b.tabs) walk(t.blocks);
		}
	};
	walk(blocks);
	return out;
}

/** 按块路径找块（作答回执反查表单的字段标签用）。 */
export function findBlock(
	blocks: readonly ViewBlock[],
	path: string,
): ViewBlock | undefined {
	for (const b of blocks) {
		if (b.path === path) return b;
		if (!path.startsWith(`${b.path}.`)) continue;
		if (b.type === "card") return findBlock(b.blocks, path);
		if (b.type === "columns")
			for (const c of b.columns) {
				const hit = findBlock(c.blocks, path);
				if (hit) return hit;
			}
		if (b.type === "tabs")
			for (const t of b.tabs) {
				const hit = findBlock(t.blocks, path);
				if (hit) return hit;
			}
	}
	return undefined;
}
