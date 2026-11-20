/**
 * Shared UI primitives for tool renderers. Every renderer composes these
 * instead of inventing new CSS — see tool-render.css for the `tv-` classes.
 */
import {
	Fragment,
	isValidElement,
	type ReactNode,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { ImageViewer } from "../components/ui/ImageViewer";
import { Icon } from "../components/ui/icons";
import { useAssetUrls } from "../lib/asset-urls";
import {
	highlightLinesSync,
	highlightSync,
	useHighlighted,
	useHighlightedLines,
	useLanguagesReady,
} from "../lib/highlight";
import { imageSrc } from "../lib/image-src";
import type { ToolRenderHost, ToolResultLike } from "./types";
import {
	languageFromPath,
	replaceTabs,
	resultImagesOf,
	resultTextOf,
	shortenPath,
	stripAnsi,
} from "./util";

export type Tone = "accent" | "ok" | "err" | "warn";

/** Inline chip. Renders nothing for empty content. */
export function Badge({
	children,
	tone,
}: {
	children: ReactNode;
	tone?: Tone;
}): ReactNode {
	if (children == null || children === "" || children === false) return null;
	return (
		<span className={`tv-badge${tone ? ` tv-badge--${tone}` : ""}`}>
			{children}
		</span>
	);
}

/** Chip row; falsy items are skipped. Element items are already chips and render as-is. */
export function Badges({
	items,
}: {
	items: ReadonlyArray<ReactNode>;
}): ReactNode {
	const visible = items.filter(
		(item) => item != null && item !== "" && item !== false,
	);
	if (visible.length === 0) return null;
	return (
		<span className="tv-badges">
			{visible.map((item, i) =>
				isValidElement(item) ? (
					<span key={i}>{item}</span>
				) : (
					<Badge key={i}>{item}</Badge>
				),
			)}
		</span>
	);
}

/** 头部右侧读数里的一个计数（`212 行`、`3 处`……）；`count` 为 null 时不渲染。 */
export function MetaCount({
	unit,
	count,
	tone,
}: {
	unit: "lines" | "matches" | "files" | "replacements";
	count: number | null;
	tone?: "ok" | "warn" | "err";
}): ReactNode {
	const { t } = useTranslation();
	if (count === null) return null;
	return (
		<span className={tone ? `tv-meta-${tone}` : undefined}>
			{t(`toolRender.meta.${unit}`, { count })}
		</span>
	);
}

/** 头部右侧读数里的增删行数（绿 `+N`、红 `−N`）；都为 0 时不渲染。 */
export function MetaDiff({
	added,
	removed,
}: {
	added: number;
	removed: number;
}): ReactNode {
	if (added === 0 && removed === 0) return null;
	return (
		<>
			{added > 0 && <span className="tv-meta-add">+{added}</span>}
			{removed > 0 && <span className="tv-meta-del">−{removed}</span>}
		</>
	);
}

/** 统一 diff 文本的增删行数（跳过 `+++` / `---` 文件头）。 */
export function diffStats(diff: string): { added: number; removed: number } {
	let added = 0;
	let removed = 0;
	for (const line of diff.split("\n")) {
		if (line.startsWith("+++ ") || line.startsWith("--- ")) continue;
		if (line.startsWith("+")) added++;
		else if (line.startsWith("-")) removed++;
	}
	return { added, removed };
}

/** File path with optional `:start-end` line range or raw selector suffix. */
export function PathText({
	path,
	from,
	to,
	sel,
}: {
	path: string;
	from?: number | null;
	to?: number | null;
	sel?: string | null;
}): ReactNode {
	let range = "";
	if (from != null || to != null) {
		const start = from ?? 1;
		range = to != null ? `:${start}-${to}` : `:${start}`;
	}
	return (
		<span className="tv-path">
			{shortenPath(path)}
			{range && <span className="tv-lines">{range}</span>}
			{sel && <span className="tv-lines">:{sel}</span>}
		</span>
	);
}

/** Key/value grid. */
export function KvGrid({ children }: { children: ReactNode }): ReactNode {
	return <div className="tv-kv">{children}</div>;
}

export function Kv({
	k,
	children,
}: {
	k: ReactNode;
	children: ReactNode;
}): ReactNode {
	if (children == null || children === "" || children === false) return null;
	return (
		<>
			<span className="tv-kv-key">{k}</span>
			<span className="tv-kv-val">{children}</span>
		</>
	);
}

/**
 * 横向可滚动的块：内容溢出时在 `data-overflow` 写上被裁掉的一侧（`end` / `start` / `both`），
 * 配合 `.tv-scroll-x` 的遮罩在那一侧渐隐，提示还能横向滚动。`content` 变化（展开、高亮完成）时重算。
 */
function useOverflowX<T extends HTMLElement>(content: unknown) {
	const ref = useRef<T>(null);
	// biome-ignore lint/correctness/useExhaustiveDependencies: content 变化即需要重新测量
	useLayoutEffect(() => {
		const el = ref.current;
		if (!el) return;
		const update = () => {
			const max = el.scrollWidth - el.clientWidth;
			const x = el.scrollLeft;
			const side =
				max <= 1 ? "" : x <= 1 ? "end" : x >= max - 1 ? "start" : "both";
			if (side) el.dataset.overflow = side;
			else delete el.dataset.overflow;
		};
		update();
		el.addEventListener("scroll", update, { passive: true });
		const ro =
			typeof ResizeObserver === "undefined" ? null : new ResizeObserver(update);
		ro?.observe(el);
		return () => {
			el.removeEventListener("scroll", update);
			ro?.disconnect();
		};
	}, [content]);
	return ref;
}

export interface OutputProps {
	text: string;
	/** Lines shown before collapsing behind a "more" affordance. */
	maxLines?: number;
	/** highlight.js language (only applied when the host exposes hljs). */
	lang?: string | null;
	/** Render in error color. */
	error?: boolean;
	/** "code": horizontal scroll, inset bg. "plain": soft-wrapped. */
	variant?: "code" | "plain";
	/** Uppercase mini-title above the block. */
	title?: string;
	/** Drop the inset background (inline in flow). */
	bare?: boolean;
}

/** 展开 / 收起按钮（界面文案走 i18n；工具输出本身不翻译）。 */
export function ExpandToggle({
	expanded,
	hidden,
	onToggle,
}: {
	expanded: boolean;
	hidden: number;
	onToggle: () => void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<button type="button" className="tv-expand" onClick={onToggle}>
			{expanded
				? t("toolRender.collapse")
				: t("toolRender.moreLines", { count: hidden })}
		</button>
	);
}

/**
 * Expandable text block — the workhorse for command output, file previews,
 * search results. Tabs are widened, ANSI escapes stripped.
 */
export function Output({
	text,
	maxLines = 10,
	lang,
	error,
	variant = "plain",
	title,
	bare,
}: OutputProps): ReactNode {
	const [expanded, setExpanded] = useState(false);
	const clean = useMemo(
		() => replaceTabs(stripAnsi(text)).replace(/\n+$/, ""),
		[text],
	);
	const lines = useMemo(() => clean.split("\n"), [clean]);
	const collapsible = lines.length > maxLines + 1;
	const count = collapsible && !expanded ? maxLines : lines.length;
	const shown =
		count === lines.length ? clean : lines.slice(0, count).join("\n");
	// 整段高亮再截取前几行：折叠预览里跨行的注释、字符串也保持正确的着色
	const highlighted = useHighlightedLines(clean, error ? null : lang);
	const html = useMemo(
		() => (highlighted ? highlighted.slice(0, count).join("\n") : null),
		[highlighted, count],
	);
	const scrollRef = useOverflowX<HTMLPreElement>(html ?? shown);
	const classes = ["tv-pre", "tv-scroll-x"];
	if (variant === "plain") classes.push("tv-pre--wrap");
	if (error) classes.push("tv-pre--error");
	if (bare) classes.push("tv-pre--bare");
	return (
		<div className="tv-out">
			{title && <div className="tv-out-title">{title}</div>}
			{html !== null ? (
				<pre
					ref={scrollRef}
					className={classes.join(" ")}
					dangerouslySetInnerHTML={{ __html: html }}
				/>
			) : (
				<pre ref={scrollRef} className={classes.join(" ")}>
					{shown}
				</pre>
			)}
			{collapsible && (
				<ExpandToggle
					expanded={expanded}
					hidden={lines.length - maxLines}
					onToggle={() => setExpanded((v) => !v)}
				/>
			)}
		</div>
	);
}

/** Source-code block: inset background, no soft wrap, optional title chip. */
export function CodeBlock({
	code,
	lang,
	title,
	maxLines = 14,
}: {
	code: string;
	lang?: string | null;
	title?: string;
	maxLines?: number;
}): ReactNode {
	if (!code) return null;
	return (
		<Output
			text={code}
			lang={lang}
			maxLines={maxLines}
			variant="code"
			title={title}
		/>
	);
}

/**
 * 一条 shell 命令：`$` 提示符 + 可选的环境变量前缀 + 按 bash 高亮的命令（软换行，完整显示）。
 * bash / terminal / job / ssh 等工具的命令行共用这一个形状。
 */
export function CommandLine({
	command,
	prefix,
	prompt = "$",
}: {
	command: string;
	/** 命令前弱化显示的部分，如 `FOO=1 ` 环境变量前缀。 */
	prefix?: string;
	prompt?: string;
}): ReactNode {
	const html = useHighlighted(command, "bash");
	return (
		<div className="tv-cmd">
			<span className="tv-cmd-prompt">{prompt}</span>
			<span className="tv-cmd-text">
				{prefix && <span className="tv-cmd-env">{`${prefix} `}</span>}
				{html !== null ? (
					<span dangerouslySetInnerHTML={{ __html: html }} />
				) : (
					command
				)}
			</span>
		</div>
	);
}

/**
 * Result text of a tool result, styled for success or error automatically.
 * Renders nothing when the result is absent or has no text.
 */
export function ResultText({
	result,
	maxLines = 10,
	lang,
	variant,
	title,
}: {
	result: ToolResultLike | undefined;
	maxLines?: number;
	lang?: string | null;
	variant?: "code" | "plain";
	title?: string;
}): ReactNode {
	const text = resultTextOf(result).trim();
	if (!text) return null;
	return (
		<Output
			text={text}
			maxLines={maxLines}
			lang={result?.isError ? null : lang}
			error={result?.isError === true}
			variant={variant ?? (lang ? "code" : "plain")}
			title={title}
		/>
	);
}

/** 文本是 JSON 对象 / 数组时返回格式化后的 JSON，否则 null。 */
export function jsonText(text: string): string | null {
	const trimmed = text.trim();
	if (!trimmed.startsWith("{") && !trimmed.startsWith("[")) return null;
	try {
		return JSON.stringify(JSON.parse(trimmed), null, 2);
	} catch {
		return null;
	}
}

/** 结果文本：JSON 时格式化并按 JSON 高亮，否则原样（出错时按错误样式）。宿主工具与 artifact 工具共用。 */
export function JsonResultText({
	result,
	maxLines = 10,
}: {
	result: ToolResultLike | undefined;
	maxLines?: number;
}): ReactNode {
	const json = result?.isError ? null : jsonText(resultTextOf(result));
	if (json === null) return <ResultText result={result} maxLines={maxLines} />;
	return <Output text={json} lang="json" variant="code" maxLines={maxLines} />;
}

/** 搜索结果的命中 / 上下文行：`*12:`、` 12|`、`*12│` 行号栏 + 代码。 */
const MATCH_ROW_RE = /^(\s*\*?\d+[:|│])(.*)$/;
/** 文件头：`### name.ts#AB12`、`## dir/`、`# root/`、单文件时的 `[path#AB12]`。 */
const MATCH_HEAD_RE = /^(?:#{1,6} (.+)|\[(.+)\])$/;
/** omp 给文件头加的 hashline 快照标签。 */
const SNAPSHOT_TAG_RE = /#[0-9A-Za-z]{4}$/;

interface MatchLine {
	kind: "head" | "row" | "text";
	text: string;
	gutter: string;
	lang: string | null;
}

function parseMatchLines(text: string, lang: string | null): MatchLine[] {
	let current = lang;
	return text.split("\n").map((line) => {
		const head = MATCH_HEAD_RE.exec(line);
		if (head) {
			const name = (head[1] ?? head[2] ?? "").replace(SNAPSHOT_TAG_RE, "");
			// 目录头（`## dir/`）不换语言；文件头按文件名换，认不出的文件不高亮
			if (!name.endsWith("/")) current = languageFromPath(name);
			return { kind: "head", text: line, gutter: "", lang: null };
		}
		const row = MATCH_ROW_RE.exec(line);
		if (row)
			return {
				kind: "row",
				text: row[2] ?? "",
				gutter: row[1] ?? "",
				lang: current,
			};
		return { kind: "text", text: line, gutter: "", lang: null };
	});
}

/**
 * grep / ast_grep 的结果：按文件头逐文件取语言，命中行的行号栏弱化、代码部分逐行高亮
 * （命中行是不连续的片段，逐行高亮避免一行里未闭合的注释、字符串染到后面）。
 * `lang` 用于没有文件头（单文件、旧格式）时。错误结果按错误样式显示原文。
 */
export function SearchOutput({
	result,
	maxLines = 14,
	lang = null,
}: {
	result: ToolResultLike | undefined;
	maxLines?: number;
	lang?: string | null;
}): ReactNode {
	const [expanded, setExpanded] = useState(false);
	const text = useMemo(
		() => replaceTabs(stripAnsi(resultTextOf(result))).replace(/\n+$/, ""),
		[result],
	);
	const lines = useMemo(() => parseMatchLines(text, lang), [text, lang]);
	const langs = useMemo(() => [...new Set(lines.map((l) => l.lang))], [lines]);
	const ready = useLanguagesReady(langs);
	const collapsible = lines.length > maxLines + 1;
	const shown = collapsible && !expanded ? lines.slice(0, maxLines) : lines;
	// biome-ignore lint/correctness/useExhaustiveDependencies: ready 用于在语言加载后重算
	const html = useMemo(
		() =>
			shown.map((l) =>
				l.kind === "row" ? highlightSync(l.text, l.lang) : null,
			),
		[shown, ready],
	);
	const scrollRef = useOverflowX<HTMLPreElement>(shown);
	if (!text.trim()) return null;
	if (result?.isError)
		return <ResultText result={result} maxLines={maxLines} />;
	return (
		<div className="tv-out">
			<pre ref={scrollRef} className="tv-pre tv-scroll-x">
				{shown.map((l, i) => (
					<Fragment key={i}>
						{i > 0 && "\n"}
						{l.kind === "head" ? (
							<span className="tv-match-head">{l.text}</span>
						) : l.kind === "row" ? (
							<>
								<span className="tv-diff-gutter">{l.gutter}</span>
								{html[i] != null ? (
									<span dangerouslySetInnerHTML={{ __html: html[i] }} />
								) : (
									l.text
								)}
							</>
						) : l.text === "..." || l.text === "…" ? (
							<span className="tv-faint">{l.text}</span>
						) : (
							l.text
						)}
					</Fragment>
				))}
			</pre>
			{collapsible && (
				<ExpandToggle
					expanded={expanded}
					hidden={lines.length - maxLines}
					onToggle={() => setExpanded((v) => !v)}
				/>
			)}
		</div>
	);
}

/** Thumbnails for every image block in a result; click opens the in-app viewer. */
export function ResultImages({
	result,
}: {
	result: ToolResultLike | undefined;
}): ReactNode {
	const { t } = useTranslation();
	const [viewing, setViewing] = useState<number | null>(null);
	const assets = useAssetUrls();
	const images = resultImagesOf(result);
	if (images.length === 0) return null;
	const viewSrc =
		viewing !== null && images[viewing]
			? imageSrc(images[viewing].data, images[viewing].mimeType, assets.blob)
			: null;
	return (
		<div className="tv-imgs">
			{images.map((img, i) => {
				const src = imageSrc(img.data, img.mimeType, assets.blob);
				const n = i + 1;
				if (!src)
					return (
						<Badge key={i} tone="warn">
							{t("image.unsupported", { type: img.mimeType })}
						</Badge>
					);
				return (
					<button
						key={i}
						type="button"
						style={{ all: "unset", display: "inline-flex" }}
						onClick={() => setViewing(i)}
						aria-label={t("image.open", { n })}
					>
						<img className="tv-img" src={src} alt={t("image.alt", { n })} />
					</button>
				);
			})}
			{viewSrc && viewing !== null && (
				<ImageViewer
					src={viewSrc}
					alt={t("image.alt", { n: viewing + 1 })}
					onClose={() => setViewing(null)}
				/>
			)}
		</div>
	);
}

/** Callout block. */
export function Note({
	tone,
	children,
}: {
	tone?: "err" | "warn" | "ok";
	children: ReactNode;
}): ReactNode {
	if (children == null || children === "" || children === false) return null;
	return (
		<div className={`tv-note${tone ? ` tv-note--${tone}` : ""}`}>
			{children}
		</div>
	);
}

/** Labeled row inside a `.tv-list`. */
export function Row({
	k,
	children,
}: {
	k?: ReactNode;
	children: ReactNode;
}): ReactNode {
	return (
		<div className="tv-row">
			{k != null && k !== "" && <span className="tv-row-key">{k}</span>}
			<span className="tv-row-val">{children}</span>
		</div>
	);
}

/** Marker for arguments that arrived with the wrong JSON type. */
export function InvalidArg({ what }: { what?: string }): ReactNode {
	return <span className="tv-err-text">[invalid {what ?? "arg"}]</span>;
}

/** `git diff` 文件头（`diff --git` 与首个 `@@` 之间）里的元信息行。 */
const GIT_META_RE =
	/^(diff --git |index |--- |\+\+\+ |new file mode |deleted file mode |old mode |new mode |similarity index |dissimilarity index |rename from |rename to |copy from |copy to |Binary files )/;

/** Per-row CSS modifier. Unified mode only treats header lines as meta while inside a file header. */
function diffRowClasses(lines: string[], unified: boolean): string[] {
	let inHeader = false;
	let seenHunk = false;
	return lines.map((line, i) => {
		if (!unified) {
			if (line.startsWith("@@")) {
				seenHunk = true;
				return "--hunk";
			}
			// 首个 `@@` 之前成对出现的 `--- ` / `+++ ` 是文件头，按元信息弱化，不当作增删行
			if (
				!seenHunk &&
				((line.startsWith("--- ") && lines[i + 1]?.startsWith("+++ ")) ||
					(line.startsWith("+++ ") && lines[i - 1]?.startsWith("--- ")))
			)
				return "--hunk";
			if (line.trim().length === 0) return "--gap";
			if (line.startsWith("+")) return "--add";
			if (line.startsWith("-")) return "--del";
			return "";
		}
		if (line.startsWith("diff --git ")) inHeader = true;
		if (line.startsWith("@@")) {
			inHeader = false;
			return "--hunk";
		}
		if ((inHeader && GIT_META_RE.test(line)) || line.startsWith("\\ "))
			return "--hunk";
		if (line.startsWith("+")) return "--add";
		if (line.startsWith("-")) return "--del";
		return "";
	});
}

/** Split a diff into display rows plus each row's CSS modifier (`""` | `--add` | `--del` | `--hunk` | `--gap`). */
export function diffRows(
	diff: string,
	unified: boolean,
): { lines: string[]; classes: string[] } {
	const lines = replaceTabs(stripAnsi(diff)).replace(/\n+$/, "").split("\n");
	return { lines, classes: diffRowClasses(lines, unified) };
}

/** 行首的增删标记与 omp 的行号栏（`+12|` / `+12│`）；其后才是代码。 */
const DIFF_PREFIX_RE = /^[ +-](?:\s*\d+[|│])?/;
/** `git diff` 里决定文件（从而决定语言）的头部行。 */
const DIFF_FILE_RE = /^(?:\+\+\+ (?:b\/)?|diff --git a\/\S+ b\/)(\S+)/;

interface DiffSyntaxPlan {
	/** 每个文件段的语言（非 unified 只有一段）。 */
	langs: (string | null)[];
	/** 每行：所在段、行首前缀、代码；hunk 头、文件头、间隔为 null。 */
	rows: ({ seg: number; prefix: string; code: string; side: string } | null)[];
}

function planDiffSyntax(
	lines: string[],
	classes: string[],
	unified: boolean,
	lang: string | null | undefined,
): DiffSyntaxPlan {
	const langs: (string | null)[] = [lang ?? null];
	const rows = lines.map((line, i) => {
		const cls = classes[i] ?? "";
		if (cls === "--hunk" || cls === "--gap") {
			const file = unified ? DIFF_FILE_RE.exec(line)?.[1] : undefined;
			if (file) {
				const fileLang = languageFromPath(file) ?? lang ?? null;
				// `diff --git` 与随后的 `+++` 指向同一文件：语言不变就不开新段
				if (fileLang !== langs[langs.length - 1]) langs.push(fileLang);
			}
			return null;
		}
		const prefix = DIFF_PREFIX_RE.exec(line)?.[0] ?? "";
		const side = cls === "--add" ? "+" : cls === "--del" ? "-" : " ";
		return {
			seg: langs.length - 1,
			prefix,
			code: line.slice(prefix.length),
			side,
		};
	});
	return { langs, rows };
}

/**
 * diff 的语法高亮：每个文件段的旧侧（上下文 + 删除行）与新侧（上下文 + 新增行）各拼成一段整体高亮，
 * 再按行映射回 diff 行，跨行的注释、字符串保持正确着色。返回每行的代码 HTML；不能高亮的行为 null。
 */
function highlightDiff(plan: DiffSyntaxPlan): (string | null)[] {
	const out: (string | null)[] = plan.rows.map(() => null);
	plan.langs.forEach((segLang, seg) => {
		if (!segLang) return;
		const oldIdx: number[] = [];
		const newIdx: number[] = [];
		plan.rows.forEach((row, i) => {
			if (!row || row.seg !== seg) return;
			if (row.side !== "+") oldIdx.push(i);
			if (row.side !== "-") newIdx.push(i);
		});
		for (const idx of [oldIdx, newIdx]) {
			if (idx.length === 0) continue;
			const code = idx.map((i) => plan.rows[i]?.code ?? "").join("\n");
			const html = highlightLinesSync(code, segLang);
			if (!html) continue;
			idx.forEach((rowIdx, k) => {
				out[rowIdx] = html[k] ?? null;
			});
		}
	});
	return out;
}

/**
 * Unified-diff-ish block: `+` rows added, `-` rows removed, `@@` hunk headers
 * faint, blank rows render as `…` gaps (non-contiguous regions).
 * `unified`: input is a real `git diff` patch — file headers render faint and
 * blank rows are context lines, not gaps; each file's language comes from its
 * `+++ b/<path>` header. `lang` highlights the code part of each row (after the
 * `+` / `-` marker and omp's `12|` line-number gutter).
 * `maxLines={Infinity}` disables collapsing.
 */
export function DiffBlock({
	diff,
	maxLines = 80,
	unified = false,
	lang,
}: {
	diff: string;
	maxLines?: number;
	unified?: boolean;
	lang?: string | null;
}): ReactNode {
	const [expanded, setExpanded] = useState(false);
	const { lines, classes } = useMemo(
		() => diffRows(diff, unified),
		[diff, unified],
	);
	const plan = useMemo(
		() => planDiffSyntax(lines, classes, unified, lang),
		[lines, classes, unified, lang],
	);
	const ready = useLanguagesReady(plan.langs);
	// biome-ignore lint/correctness/useExhaustiveDependencies: ready 用于在语言加载后重算
	const syntax = useMemo(() => highlightDiff(plan), [plan, ready]);
	const collapsible = lines.length > maxLines + 1;
	const shown = collapsible && !expanded ? lines.slice(0, maxLines) : lines;
	const diffRef = useOverflowX<HTMLDivElement>(shown);
	return (
		<div className="tv-out">
			<div ref={diffRef} className="tv-diff tv-scroll-x">
				{shown.map((line, i) => {
					const cls = classes[i] ?? "";
					const html = syntax[i];
					const row = plan.rows[i];
					const className = `tv-diff-row${cls ? ` tv-diff-row${cls}` : ""}`;
					if (html != null && row) {
						return (
							<div key={i} className={className}>
								{row.prefix && (
									<span className="tv-diff-gutter">{row.prefix}</span>
								)}
								<span dangerouslySetInnerHTML={{ __html: html || " " }} />
							</div>
						);
					}
					return (
						<div key={i} className={className}>
							{cls === "--gap" ? "…" : line || " "}
						</div>
					);
				})}
			</div>
			{collapsible && (
				<ExpandToggle
					expanded={expanded}
					hidden={lines.length - maxLines}
					onToggle={() => setExpanded((v) => !v)}
				/>
			)}
		</div>
	);
}

/**
 * Agent id chip. Becomes a drill-down button when the host can open that
 * agent's sub-session; otherwise renders as a plain accent badge.
 */
export function AgentLink({
	id,
	host,
	children,
}: {
	id: string;
	host?: ToolRenderHost;
	children?: ReactNode;
}): ReactNode {
	const clickable =
		host?.openAgent !== undefined &&
		(host.hasAgent === undefined || host.hasAgent(id));
	if (!clickable) return <Badge tone="accent">{children ?? id}</Badge>;
	return (
		<button
			type="button"
			className="tv-badge tv-badge--accent tv-agent-link"
			onClick={() => host.openAgent?.(id)}
		>
			{children ?? id}
			<span className="tv-agent-link-arrow" aria-hidden="true">
				<Icon name="external" size={10} />
			</span>
		</button>
	);
}
