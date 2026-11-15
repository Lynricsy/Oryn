/**
 * Markdown 文档开头的 YAML frontmatter。
 *
 * 只认文档**首行**是 `---` 的 frontmatter，到下一行独立的 `---`（或 `...`）为止；正文里的 `---` 分隔线不受影响。
 *
 * 两套入口：
 * - 展示用（`splitFrontmatter` / `parseFrontmatterBlock`）：自带的小解析器，不求完整 YAML。
 *   支持的子集：顶层 `key: value`；值为纯标量（缩进续行按 YAML 折成一行）、双引号串（YAML 转义）、单引号串（`''`）、
 *   行内数组 `[a, "b", 'c']`、块式数组（`- item`）、块标量（`|` / `>` 及 `-` / `+` 尾随修饰）。
 *   嵌套映射、行内映射、锚点 / 别名 / 标签、多行引号串等一律不支持：整段解析失败，调用方回落到原来的渲染。
 *   标量一律保留原文字符串（`1.10` 不会变成 `1.1`，`true` 原样显示）。
 * - 编辑用（`parseFrontmatter` / `stringifyFrontmatter`）：完整 YAML（`yaml` 包），供设置页的 frontmatter 表单读写文件。
 */

import { isMap, parseDocument, stringify } from "yaml";

export type FrontmatterValue = string | string[];

export interface FrontmatterField {
	key: string;
	value: FrontmatterValue;
}

export interface FrontmatterDoc {
	fields: FrontmatterField[];
	/** frontmatter 之后的正文（去掉开头空行）。 */
	body: string;
}

const OPEN_RE = /^---[ \t]*$/;
const CLOSE_RE = /^(?:---|\.\.\.)[ \t]*$/;
const KEY_RE = /^([A-Za-z_][\w.-]*)[ \t]*:(?:[ \t]+(.*))?$/;
const BLOCK_SCALAR_RE = /^([|>])([-+]?)[ \t]*(?:#.*)?$/;
const ITEM_RE = /^([ \t]*)-(?:[ \t]+(.*))?$/;
/** 纯标量里的 ` #` 起注释。 */
const COMMENT_RE = /[ \t]+#.*$/;
/** 不支持的值开头：行内映射、锚点、别名、标签、保留字符。 */
const UNSUPPORTED_START_RE = /^[{&*!%@`]/;

/** 解析失败（结构不在支持的子集里）。 */
class Unsupported extends Error {}

function fail(): never {
	throw new Unsupported();
}

function indentOf(line: string): number {
	return /^[ \t]*/.exec(line)?.[0].length ?? 0;
}

function isBlank(line: string): boolean {
	return line.trim() === "";
}

const ESCAPES: Record<string, string> = {
	"0": "\0",
	a: "\x07",
	b: "\b",
	t: "\t",
	"\t": "\t",
	n: "\n",
	v: "\v",
	f: "\f",
	r: "\r",
	e: "\x1b",
	" ": " ",
	'"': '"',
	"/": "/",
	"\\": "\\",
	N: "\u0085",
	_: "\u00a0",
	L: "\u2028",
	P: "\u2029",
};
const HEX_LEN: Record<string, number> = { x: 2, u: 4, U: 8 };

/** 从 `s[start]`（开引号）读一个双引号串，返回值与闭引号之后的位置。 */
function readDouble(s: string, start: number): [string, number] {
	let out = "";
	let i = start + 1;
	while (i < s.length) {
		const c = s[i];
		if (c === '"') return [out, i + 1];
		if (c !== "\\") {
			out += c;
			i++;
			continue;
		}
		const e = s[i + 1];
		if (e === undefined) fail();
		const hex = HEX_LEN[e];
		if (hex) {
			const digits = s.slice(i + 2, i + 2 + hex);
			if (!new RegExp(`^[0-9a-fA-F]{${hex}}$`).test(digits)) fail();
			// 超出 Unicode 范围（> U+10FFFF）或落在代理区的码点不是合法字符：按不支持回落，不让 fromCodePoint 抛 RangeError
			const cp = Number.parseInt(digits, 16);
			if (cp > 0x10ffff || (cp >= 0xd800 && cp <= 0xdfff)) fail();
			out += String.fromCodePoint(cp);
			i += 2 + hex;
			continue;
		}
		const mapped = ESCAPES[e];
		if (mapped === undefined) fail();
		out += mapped;
		i += 2;
	}
	return fail();
}

/** 从 `s[start]`（开引号）读一个单引号串（`''` 表示一个 `'`）。 */
function readSingle(s: string, start: number): [string, number] {
	let out = "";
	let i = start + 1;
	while (i < s.length) {
		if (s[i] === "'") {
			if (s[i + 1] === "'") {
				out += "'";
				i += 2;
				continue;
			}
			return [out, i + 1];
		}
		out += s[i];
		i++;
	}
	return fail();
}

/** 引号串之后只允许空白和注释。 */
function expectEnd(s: string, at: number): void {
	const rest = s.slice(at);
	if (rest.trim() !== "" && !/^[ \t]+#/.test(rest)) fail();
}

function readQuoted(s: string): string {
	const [value, end] = s[0] === '"' ? readDouble(s, 0) : readSingle(s, 0);
	expectEnd(s, end);
	return value;
}

function plain(s: string): string {
	const v = s.replace(COMMENT_RE, "").trim();
	if (UNSUPPORTED_START_RE.test(v)) fail();
	return v;
}

/** 行内数组 `[a, "b", 'c']`；不支持嵌套。 */
function readFlowSeq(s: string): string[] {
	const items: string[] = [];
	let i = 1;
	const skipWs = () => {
		while (s[i] === " " || s[i] === "\t") i++;
	};
	skipWs();
	if (s[i] === "]") {
		expectEnd(s, i + 1);
		return items;
	}
	for (;;) {
		skipWs();
		const c = s[i];
		if (c === undefined) fail();
		if (c === '"' || c === "'") {
			const [value, end] = c === '"' ? readDouble(s, i) : readSingle(s, i);
			items.push(value);
			i = end;
		} else {
			const m = /^[^,\]]*/.exec(s.slice(i))?.[0] ?? "";
			const v = m.trim();
			if (v === "" || /^[[{&*!#]/.test(v)) fail();
			items.push(v);
			i += m.length;
		}
		skipWs();
		if (s[i] === ",") {
			i++;
			continue;
		}
		if (s[i] === "]") {
			expectEnd(s, i + 1);
			return items;
		}
		return fail();
	}
}

/** 一行内的标量值（块式数组的一项也用它）。 */
function readInlineScalar(s: string): string {
	if (s[0] === '"' || s[0] === "'") return readQuoted(s);
	if (s[0] === "[") fail();
	return plain(s);
}

/** 块标量：缩进比 key 深的后续行；`|` 保留换行，`>` 折叠。 */
function readBlockScalar(
	lines: string[],
	from: number,
	style: string,
	chomp: string,
): [string, number] {
	let i = from;
	const body: string[] = [];
	let indent = -1;
	while (i < lines.length) {
		const line = lines[i] ?? "";
		if (isBlank(line)) {
			body.push("");
			i++;
			continue;
		}
		const ind = indentOf(line);
		if (ind === 0) break;
		if (indent < 0) indent = ind;
		if (ind < indent) fail();
		body.push(line.slice(indent));
		i++;
	}
	// 末尾空行属于尾随换行，由修饰符决定
	let trailing = 0;
	while (body.length > 0 && body[body.length - 1] === "") {
		body.pop();
		trailing++;
	}
	let text: string;
	if (style === "|") text = body.join("\n");
	else {
		// 折叠：相邻非空行并成一行，空行保留为换行
		text = "";
		for (const [j, line] of body.entries()) {
			if (j === 0) text = line;
			else if (line === "") text += "\n";
			else if (body[j - 1] === "") text += line;
			else text += ` ${line}`;
		}
	}
	if (chomp === "+") text += "\n".repeat(trailing + 1);
	else if (chomp !== "-" && text !== "") text += "\n";
	return [text, i];
}

/** 解析 frontmatter 块（两条 `---` 之间的行）；不在支持子集里时返回 null。 */
export function parseFrontmatterBlock(
	block: string,
): FrontmatterField[] | null {
	const lines = block.split("\n");
	const fields: FrontmatterField[] = [];
	try {
		let i = 0;
		while (i < lines.length) {
			const line = lines[i] ?? "";
			if (isBlank(line) || /^#/.test(line)) {
				i++;
				continue;
			}
			const m = KEY_RE.exec(line);
			if (!m) return null;
			const key = m[1] ?? "";
			const raw = (m[2] ?? "").trim();
			i++;
			if (raw === "" || raw.startsWith("#")) {
				// 值写在后续行：块式数组，或空值；缩进的映射不支持
				const items: string[] = [];
				while (i < lines.length) {
					const next = lines[i] ?? "";
					if (isBlank(next)) {
						i++;
						continue;
					}
					const item = ITEM_RE.exec(next);
					if (!item) {
						if (indentOf(next) > 0) fail();
						break;
					}
					const content = (item[2] ?? "").trim();
					// 数组项里的映射 / 嵌套数组不支持
					if (KEY_RE.test(content) || /^-(?:\s|$)/.test(content)) fail();
					items.push(readInlineScalar(content));
					i++;
				}
				fields.push({ key, value: items.length > 0 ? items : "" });
				continue;
			}
			const block = BLOCK_SCALAR_RE.exec(raw);
			if (block) {
				const [text, next] = readBlockScalar(
					lines,
					i,
					block[1] ?? "|",
					block[2] ?? "",
				);
				fields.push({ key, value: text });
				i = next;
				continue;
			}
			if (raw[0] === "[") {
				fields.push({ key, value: readFlowSeq(raw) });
				continue;
			}
			if (raw[0] === '"' || raw[0] === "'") {
				fields.push({ key, value: readQuoted(raw) });
				continue;
			}
			// 纯标量：缩进的续行按 YAML 折成一个空格
			let value = plain(raw);
			while (i < lines.length) {
				const next = lines[i] ?? "";
				if (isBlank(next) || indentOf(next) === 0) break;
				const cont = next.trim();
				if (cont.startsWith("- ") || KEY_RE.test(cont)) fail();
				value += ` ${plain(cont)}`;
				i++;
			}
			fields.push({ key, value });
		}
	} catch (e) {
		if (e instanceof Unsupported) return null;
		throw e;
	}
	return fields.length > 0 ? fields : null;
}

type SplitResult =
	| { kind: "none"; src: string }
	| { kind: "unclosed"; src: string }
	| { kind: "ok"; src: string; block: string; body: string };

/** 按首行 `---` 与闭合行拆出 frontmatter 块与正文（正文去掉开头空行）；先去 BOM、统一换行为 `\n`。 */
function splitBlock(text: string): SplitResult {
	const src = text.replace(/^\uFEFF/, "").replace(/\r\n?/g, "\n");
	const nl = src.indexOf("\n");
	if (nl < 0 || !OPEN_RE.test(src.slice(0, nl))) return { kind: "none", src };
	const lines = src.slice(nl + 1).split("\n");
	const close = lines.findIndex((l) => CLOSE_RE.test(l));
	if (close < 0) return { kind: "unclosed", src };
	const body = lines
		.slice(close + 1)
		.join("\n")
		.replace(/^(?:[ \t]*\n)+/, "");
	return { kind: "ok", src, block: lines.slice(0, close).join("\n"), body };
}

/**
 * 拆出文档开头的 frontmatter；文档首行不是 `---`、没有闭合行、或块里有不支持的结构时返回 null。
 */
export function splitFrontmatter(text: string): FrontmatterDoc | null {
	const split = splitBlock(text);
	if (split.kind !== "ok") return null;
	const fields = parseFrontmatterBlock(split.block);
	return fields ? { fields, body: split.body } : null;
}

export interface ParsedFrontmatter {
	/** frontmatter 映射（完整 YAML 语义：数字、布尔、嵌套结构都按 YAML 取值）；没有 frontmatter 时为空对象。 */
	data: Record<string, unknown>;
	/** frontmatter 之后的正文（去掉开头空行）；出错时为整份原文，调用方可回落到原文编辑。 */
	body: string;
	/** frontmatter 存在但无法解析时的原因。 */
	error?: string;
}

/** 解析编辑用的 frontmatter；首行不是 `---` 时整份文本都是正文。 */
export function parseFrontmatter(text: string): ParsedFrontmatter {
	const split = splitBlock(text);
	if (split.kind === "none") return { data: {}, body: split.src };
	if (split.kind === "unclosed") {
		return { data: {}, body: split.src, error: "frontmatter 缺少闭合的 ---" };
	}
	const doc = parseDocument(split.block);
	const err = doc.errors[0];
	if (err) return { data: {}, body: split.src, error: err.message };
	if (doc.contents === null) return { data: {}, body: split.body };
	if (!isMap(doc.contents)) {
		return { data: {}, body: split.src, error: "frontmatter 不是键值映射" };
	}
	return { data: doc.toJS() as Record<string, unknown>, body: split.body };
}

/**
 * 生成 `---\n<yaml>---\n\n<正文>`；值为 undefined 的键略去，没有字段时只输出正文。
 * 与 `parseFrontmatter` 往返一致（正文不以空行开头时）。长字符串不折行（`lineWidth: 0`）。
 */
export function stringifyFrontmatter(
	data: Record<string, unknown>,
	body: string,
): string {
	const fields = Object.fromEntries(
		Object.entries(data).filter(([, v]) => v !== undefined),
	);
	if (Object.keys(fields).length === 0) return body;
	const head = `---\n${stringify(fields, { lineWidth: 0 })}---\n`;
	return body === "" ? head : `${head}\n${body}`;
}

/** 按 key 取字段值。 */
export function fieldValue(
	doc: FrontmatterDoc,
	key: string,
): FrontmatterValue | undefined {
	return doc.fields.find((f) => f.key === key)?.value;
}

/** 字段值统一成列表（单个字符串 → 一项；空串 → 空列表）。 */
export function valueList(value: FrontmatterValue | undefined): string[] {
	if (value === undefined) return [];
	if (Array.isArray(value)) return value;
	return value === "" ? [] : [value];
}
