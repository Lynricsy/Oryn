/**
 * `read` — 读文件 / 目录 / 图片 / 网址。整个展开体就是「这个文件」：文件头条（图标、路径、所读范围，
 * 右侧语言与大小）下接带行号栏的代码；所选范围外 read 自动补的上下文行弱化。目录是带大小、时间列的树，
 * 图片是缩略图，网址正文按 Markdown 渲染。头部读数是读到的行数（只读了一段时写 `12 / 340 行`）、
 * 冲突数与截断。结果形状见 oryn-core `tui/src/tools/read.ts`（`ReadToolDetails`）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { FileTypeIcon } from "../../components/ui/FileTypeIcon";
import { formatBytes } from "../../lib/format";
import {
	CodeLines,
	type CodeRow,
	EmptyNote,
	ErrorBlock,
	FileHead,
	FootNotes,
	MarkdownBlock,
	Output,
	PathText,
	ResultImages,
	SiteMark,
} from "../parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "../types";
import {
	detailsRecord,
	hostOf,
	isRecord,
	languageFromPath,
	num,
	replaceTabs,
	resultImagesOf,
	resultTextOf,
	splitPathSel,
	str,
	stripAnsi,
} from "../util";

interface ReadArgs {
	path: string;
	sel: string | null;
	/** 所选的单段行范围（`:40-52`、`:40+5`、`offset/limit`）；多段、`raw` 等为 null。 */
	from: number | null;
	to: number | null;
}

/** 单段行范围选择器：`40`、`40-52`、`40-`、`40+5`。 */
const RANGE_SEL_RE = /^(\d+)(?:-(\d*)|\+(\d+))?$/;

function readArgs(args: Record<string, unknown>): ReadArgs {
	const rawPath = str(args.path) ?? str(args.file_path) ?? "";
	const split = splitPathSel(rawPath);
	const sel = str(args.sel) ?? split.sel;
	let from: number | null = null;
	let to: number | null = null;
	const range = sel ? RANGE_SEL_RE.exec(sel.replace(/:raw$/i, "")) : null;
	if (range) {
		from = Number(range[1]);
		if (range[2]) to = Number(range[2]);
		else if (range[3]) to = from + Number(range[3]) - 1;
		else if (range[2] === undefined) to = from;
	} else {
		const offset = num(args.offset);
		const limit = num(args.limit);
		if (offset !== null || limit !== null) {
			from = offset ?? 1;
			to = limit !== null ? from + limit - 1 : null;
		}
	}
	return { path: split.path || rawPath, sel, from, to };
}

/** `ReadToolDetails` 里卡片用得上的字段（线路上的 JSON，逐项收窄）。 */
interface ReadInfo {
	kind: string | null;
	isDirectory: boolean;
	resolvedPath: string | null;
	suffixFrom: string | null;
	suffixTo: string | null;
	display: {
		text: string;
		startLine: number;
		lineNumbers: (number | null)[] | null;
	} | null;
	rowPrefixes: boolean | null;
	totalLines: number | null;
	fileSize: number | null;
	truncated: boolean;
	conflictCount: number;
	contentType: string | null;
	url: string | null;
}

function readInfo(result: ToolResultLike | undefined): ReadInfo {
	const d = detailsRecord(result);
	const suffix = d && isRecord(d.suffixResolution) ? d.suffixResolution : null;
	const meta = d && isRecord(d.meta) ? d.meta : null;
	const dc = d && isRecord(d.displayContent) ? d.displayContent : null;
	const dcText = dc ? str(dc.text) : null;
	return {
		kind: d ? str(d.kind) : null,
		isDirectory: d?.isDirectory === true,
		resolvedPath: d ? str(d.resolvedPath) : null,
		suffixFrom: suffix ? str(suffix.from) : null,
		suffixTo: suffix ? str(suffix.to) : null,
		display:
			dc && dcText !== null
				? {
						text: dcText,
						startLine: num(dc.startLine) ?? 1,
						lineNumbers: Array.isArray(dc.lineNumbers)
							? dc.lineNumbers.map((n) => num(n))
							: null,
					}
				: null,
		rowPrefixes: typeof d?.rowPrefixes === "boolean" ? d.rowPrefixes : null,
		totalLines: d ? num(d.totalLines) : null,
		fileSize: d ? num(d.fileSize) : null,
		truncated:
			d !== null &&
			(isRecord(d.truncation) ||
				(meta !== null && isRecord(meta.truncation)) ||
				d.truncated === true),
		conflictCount: (d ? num(d.conflictCount) : null) ?? 0,
		contentType: d ? str(d.contentType) : null,
		url: d ? (str(d.finalUrl) ?? str(d.url)) : null,
	};
}

/** 行首前缀：hashline `12:`、行号模式 `12|`；结构摘要里合并的花括号对 `12-40:`。 */
const PREFIX_ROW_RE = /^(\d+)(?:-\d+)?[:|](.*)$/;
/** 单文件 hashline 头 `[src/a.ts#7F3A]`。 */
const HASHLINE_HEAD_RE = /^\[[^\s\]]+#[0-9A-Za-z]{4}\]$/;
/** 头尾的方括号提示：`[Showing lines 1-200 of 900 …]`、`[Path 'x' not found; resolved to …]`。 */
const BRACKET_NOTE_RE = /^\[(.+)\]$/;
const ELLIPSIS_RE = /^(?:…|\.\.\.)$/;

/** 结果文本拆成正文、头尾的提示与冲突警告。 */
function splitReadText(text: string): {
	body: string[];
	notes: string[];
	conflicts: string;
} {
	const lines = replaceTabs(stripAnsi(text)).replace(/\n+$/, "").split("\n");
	const notes: string[] = [];
	// 冲突警告：空行后以 `⚠` 开头的一整段
	let conflicts = "";
	const warn = lines.findIndex(
		(l, i) => l.startsWith("⚠ ") && (i === 0 || lines[i - 1]?.trim() === ""),
	);
	if (warn >= 0) {
		conflicts = lines.slice(warn).join("\n");
		lines.length = warn;
	}
	// 开头的提示（按后缀纠正的路径等）与 hashline 头
	while (lines.length > 0) {
		const first = lines[0]?.trim() ?? "";
		if (HASHLINE_HEAD_RE.test(first)) lines.shift();
		else if (BRACKET_NOTE_RE.test(first) && lines.length > 1) {
			notes.push(first.slice(1, -1));
			lines.shift();
		} else break;
	}
	// 末尾的续读 / 截断提示
	const tail: string[] = [];
	while (lines.length > 0) {
		const last = lines[lines.length - 1]?.trim() ?? "";
		if (last === "") lines.pop();
		else if (BRACKET_NOTE_RE.test(last) && lines.length > 1) {
			tail.unshift(last.slice(1, -1));
			lines.pop();
		} else break;
	}
	return { body: lines, notes: [...notes, ...tail], conflicts };
}

/** 正文行转成代码行：认得行号前缀时用它的行号，否则从 `start` 起顺序编号。 */
function bodyRows(
	body: string[],
	start: number,
	prefixes: boolean | null,
): CodeRow[] {
	const nonBlank = body.filter((l) => l.trim() !== "");
	const prefixed = nonBlank.filter(
		(l) => PREFIX_ROW_RE.test(l) || ELLIPSIS_RE.test(l.trim()),
	).length;
	const usePrefix =
		prefixes === true ||
		(prefixes === null &&
			nonBlank.length > 0 &&
			prefixed / nonBlank.length >= 0.8);
	if (!usePrefix)
		return body.map((text, i) => ({ kind: "ctx", no: start + i, text }));
	return body.map((line) => {
		if (ELLIPSIS_RE.test(line.trim())) return { kind: "gap", text: "" };
		const m = PREFIX_ROW_RE.exec(line);
		return m
			? { kind: "ctx", no: Number(m[1]), text: m[2] ?? "" }
			: { kind: "ctx", text: line };
	});
}

/** `displayContent`（无前缀的原文 + 起始行 + 每行行号，null 是省略）转成代码行。 */
function displayRows(display: NonNullable<ReadInfo["display"]>): CodeRow[] {
	const lines = replaceTabs(display.text).replace(/\n+$/, "").split("\n");
	return lines.map((text, i) => {
		if (display.lineNumbers) {
			const no = display.lineNumbers[i];
			return no == null ? { kind: "gap", text: "" } : { kind: "ctx", no, text };
		}
		return { kind: "ctx", no: display.startLine + i, text };
	});
}

/** 所选范围外的行（read 给范围读补的前后文）弱化。 */
function dimOutside(rows: CodeRow[], from: number | null, to: number | null) {
	if (from === null) return rows;
	return rows.map((r) =>
		r.no != null && (r.no < from || (to !== null && r.no > to))
			? { ...r, dim: true }
			: r,
	);
}

/** 目录列表的一行：`  - src/   4.0KB  2m ago`、`  - … 28 more`。 */
interface DirEntry {
	depth: number;
	name: string;
	size: string | null;
	age: string | null;
	more: boolean;
}

const DIR_ROW_RE = /^(\s*)- (.+)$/;
const SIZE_RE = /^[\d.]+\s?[KMGT]?i?B$/i;

function parseDirListing(body: string[]): {
	root: string | null;
	entries: DirEntry[];
} {
	let root: string | null = null;
	const entries: DirEntry[] = [];
	for (const line of body) {
		if (!line.trim()) continue;
		const m = DIR_ROW_RE.exec(line);
		if (!m) {
			// 归档目录：`name/`、`name (1.2KB)`；普通目录的首行是根名
			const archive = /^(.+?) \(([^()]+)\)$/.exec(line.trim());
			if (entries.length === 0 && root === null && !archive) {
				root = line.trim();
				continue;
			}
			entries.push({
				depth: 0,
				name: archive?.[1] ?? line.trim(),
				size: archive?.[2] ?? null,
				age: null,
				more: false,
			});
			continue;
		}
		const depth = Math.max(0, Math.floor((m[1]?.length ?? 2) / 2) - 1);
		const cols = (m[2] ?? "").split(/\s{2,}/);
		const name = cols.shift() ?? "";
		let size: string | null = null;
		if (cols[0] && SIZE_RE.test(cols[0])) size = cols.shift() ?? null;
		entries.push({
			depth,
			name,
			size,
			age: cols.join(" ") || null,
			more: /^… \d+ more$/.test(name),
		});
	}
	return { root, entries };
}

function DirTree({ body }: { body: string[] }): ReactNode {
	const { t } = useTranslation();
	const { entries } = parseDirListing(body);
	if (entries.length === 0)
		return <EmptyNote>{t("toolRender.read.emptyDir")}</EmptyNote>;
	return (
		<div className="tv-dir">
			{entries.map((e, i) => {
				const dir = e.name.endsWith("/");
				return (
					<div
						key={i}
						className={`tv-dir-row${e.more ? " tv-dir-row--more" : ""}${dir ? " tv-dir-row--dir" : ""}`}
					>
						{e.depth > 0 && (
							<span className="tv-dir-indent" aria-hidden="true">
								{Array.from({ length: e.depth }, (_, k) => (
									<i key={k} />
								))}
							</span>
						)}
						<span className="tv-dir-icon" aria-hidden="true">
							{!e.more && <FileTypeIcon name={e.name} size={14} />}
						</span>
						<span className="tv-dir-name">{e.name}</span>
						{e.size && <span className="tv-dir-size">{e.size}</span>}
						{e.age && <span className="tv-dir-age">{e.age}</span>}
					</div>
				);
			})}
		</div>
	);
}

/** 网址读取：`URL: …` 等头部行，`---` 之后是 Markdown 正文。 */
function UrlBody({ text, info }: { text: string; info: ReadInfo }): ReactNode {
	const sep = text.indexOf("\n---\n");
	const head = sep >= 0 ? text.slice(0, sep) : "";
	const body = sep >= 0 ? text.slice(sep + 5).trim() : text.trim();
	const url = info.url ?? /^URL: (.+)$/m.exec(head)?.[1] ?? "";
	const type = info.contentType ?? /^Content-Type: (.+)$/m.exec(head)?.[1];
	const host = hostOf(url);
	return (
		<>
			{url && (
				<div className="tv-fh">
					{host && <SiteMark host={host} />}
					<span className="tv-fh-path">
						<span className="tv-fh-dir">{url}</span>
					</span>
					{type && <span className="tv-fh-aside">{type}</span>}
				</div>
			)}
			{body && <MarkdownBlock text={body} />}
		</>
	);
}

function Summary(props: ToolRenderProps): ReactNode {
	const { path, sel } = readArgs(props.args);
	return <PathText path={path || "…"} sel={sel} />;
}

/** 头部读数：读到的行数（只读了一段且知道总行数时 `12 / 340 行`）、目录项数、图片、冲突与截断。 */
function Meta({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result || result.isError) return null;
	const info = readInfo(result);
	const text = resultTextOf(result);
	let main: ReactNode = null;
	if (resultImagesOf(result).length > 0) main = t("toolRender.read.image");
	else if (info.isDirectory) {
		const { entries } = parseDirListing(splitReadText(text).body);
		const n = entries.filter((e) => !e.more).length;
		main = t("toolRender.read.entries", { count: n });
	} else if (info.kind !== "url") {
		const rows = readRows(args, result, info);
		const n = rows.filter((r) => r.kind !== "gap" && !r.dim).length;
		if (n > 0)
			main =
				info.totalLines !== null && n < info.totalLines
					? t("toolRender.read.partial", { shown: n, total: info.totalLines })
					: t("toolRender.meta.lines", { count: n });
	}
	return (
		<>
			{main && <span>{main}</span>}
			{info.conflictCount > 0 && (
				<span className="tv-meta-err">
					{t("toolRender.read.conflicts", { count: info.conflictCount })}
				</span>
			)}
			{info.truncated && (
				<span className="tv-meta-warn">{t("toolRender.read.truncated")}</span>
			)}
		</>
	);
}

/** 正文的代码行（displayContent 优先，否则解析结果文本），所选范围外的行弱化。 */
function readRows(
	args: Record<string, unknown>,
	result: ToolResultLike | undefined,
	info: ReadInfo,
): CodeRow[] {
	const { from, to } = readArgs(args);
	const rows = info.display
		? displayRows(info.display)
		: bodyRows(
				splitReadText(resultTextOf(result)).body,
				from ?? 1,
				info.rowPrefixes,
			);
	return dimOutside(rows, from, to);
}

/** 文件头条右侧：语言 · 总行数 · 大小。 */
function HeadAside({ lang, info }: { lang: string | null; info: ReadInfo }) {
	const { t } = useTranslation();
	const parts: string[] = [];
	if (lang) parts.push(lang);
	if (info.totalLines !== null)
		parts.push(t("toolRender.meta.lines", { count: info.totalLines }));
	if (info.fileSize !== null) parts.push(formatBytes(info.fileSize));
	return parts.length > 0 ? <span>{parts.join(" · ")}</span> : null;
}

function Result({ args, result, running }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const { path, sel, from, to } = readArgs(args);
	if (!path && !result) return null;
	const info = readInfo(result);
	const shownPath = info.suffixTo ?? info.resolvedPath ?? path;
	const lang =
		languageFromPath(path) ??
		(shownPath ? languageFromPath(shownPath) : null) ??
		null;
	const rangeFrom = from ?? null;
	const rangeTo = to ?? null;
	const head = path && (
		<FileHead
			path={info.kind === "url" ? path : path || shownPath}
			from={rangeFrom}
			to={rangeTo}
		>
			{sel && rangeFrom === null && <span className="tv-lines">:{sel}</span>}
			{!running && !result?.isError && <HeadAside lang={lang} info={info} />}
		</FileHead>
	);
	if (!result) return head;
	const text = resultTextOf(result);
	if (result.isError)
		return (
			<>
				{head}
				<ErrorBlock text={text} />
			</>
		);
	const split = splitReadText(text);
	const suffixNote =
		info.suffixFrom && info.suffixTo
			? t("toolRender.read.suffix", {
					from: info.suffixFrom,
					to: info.suffixTo,
				})
			: null;
	// 按后缀纠正路径的提示已由上面的说明替代
	const notes = split.notes.filter(
		(n) => !(suffixNote && n.startsWith("Path '")),
	);
	const truncNotes = notes.filter((n) => /^Showing |truncated|elided/i.test(n));
	const infoNotes = notes.filter((n) => !truncNotes.includes(n));

	let body: ReactNode;
	if (resultImagesOf(result).length > 0) {
		// `Read image file [image/png]` 只留类型，其后的尺寸等说明照录
		const caption = text
			.split("\n")
			.map((l) => l.trim().replace(/^Read (?:image|SVG) file \[(.+?)\]/, "$1"))
			.filter(Boolean)
			.join(" · ");
		body = (
			<>
				<ResultImages result={result} />
				{caption && <div className="tv-read-caption">{caption}</div>}
			</>
		);
	} else if (info.kind === "url" || /^URL: /.test(text)) {
		body = <UrlBody text={text} info={info} />;
	} else if (info.isDirectory) {
		body = <DirTree body={split.body} />;
	} else if (text.trim() === "The file is empty.") {
		body = <EmptyNote>{t("toolRender.read.emptyFile")}</EmptyNote>;
	} else if (
		split.body.length === 0 ||
		(split.body.length === 1 &&
			BRACKET_NOTE_RE.test(split.body[0]?.trim() ?? ""))
	) {
		// 只有一条提示（二进制、越界……）
		const only = (split.body[0] ?? text).trim().replace(BRACKET_NOTE_RE, "$1");
		body = only ? <FootNotes notes={[only]} tone="warn" /> : null;
	} else {
		body = (
			<CodeLines
				rows={readRows(args, result, info)}
				lang={lang}
				maxLines={24}
			/>
		);
	}
	return (
		<>
			{head}
			{suffixNote && <FootNotes notes={[suffixNote]} />}
			{body}
			<FootNotes notes={infoNotes} />
			<FootNotes notes={truncNotes} tone="warn" />
			{split.conflicts && (
				<div className="tv-read-conflicts">
					<Output text={split.conflicts} maxLines={8} variant="plain" bare />
				</div>
			)}
		</>
	);
}

export const readRenderer: ToolRenderer = { Summary, Meta, Result };
