/**
 * Shared UI primitives for tool renderers. Every renderer composes these
 * instead of inventing new CSS — see tool-render.css for the `tv-` classes.
 */
import {
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
import { useHighlighted } from "../lib/highlight";
import { imageSrc } from "../lib/image-src";
import type { ToolRenderHost, ToolResultLike } from "./types";
import {
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

function useHighlight(
	code: string,
	lang: string | null | undefined,
): string | null {
	// Oryn：highlight.js 与各语言按需懒加载，加载完成后重新渲染
	return useHighlighted(code, lang);
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
function ExpandToggle({
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
	const shown =
		collapsible && !expanded ? lines.slice(0, maxLines).join("\n") : clean;
	const html = useHighlight(shown, error ? null : lang);
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
/** Thumbnails for every image block in a result; click opens the in-app viewer. */
export function ResultImages({
	result,
}: {
	result: ToolResultLike | undefined;
}): ReactNode {
	const { t } = useTranslation();
	const [viewing, setViewing] = useState<number | null>(null);
	const images = resultImagesOf(result);
	if (images.length === 0) return null;
	const viewSrc =
		viewing !== null && images[viewing]
			? imageSrc(images[viewing].data, images[viewing].mimeType)
			: null;
	return (
		<div className="tv-imgs">
			{images.map((img, i) => {
				const src = imageSrc(img.data, img.mimeType);
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

/**
 * Unified-diff-ish block: `+` rows added, `-` rows removed, `@@` hunk headers
 * faint, blank rows render as `…` gaps (non-contiguous regions).
 * `unified`: input is a real `git diff` patch — file headers render faint and
 * blank rows are context lines, not gaps. `maxLines={Infinity}` disables collapsing.
 */
export function DiffBlock({
	diff,
	maxLines = 80,
	unified = false,
}: {
	diff: string;
	maxLines?: number;
	unified?: boolean;
}): ReactNode {
	const [expanded, setExpanded] = useState(false);
	const { lines, classes } = useMemo(
		() => diffRows(diff, unified),
		[diff, unified],
	);
	const collapsible = lines.length > maxLines + 1;
	const shown = collapsible && !expanded ? lines.slice(0, maxLines) : lines;
	const diffRef = useOverflowX<HTMLDivElement>(shown);
	return (
		<div className="tv-out">
			<div ref={diffRef} className="tv-diff tv-scroll-x">
				{shown.map((line, i) => {
					const cls = classes[i] ?? "";
					const gap = cls === "--gap";
					return (
						<div
							key={i}
							className={`tv-diff-row${cls ? ` tv-diff-row${cls}` : ""}`}
						>
							{gap ? "…" : line || " "}
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
