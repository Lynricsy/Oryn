/**
 * Shared UI primitives for tool renderers. Every renderer composes these
 * instead of inventing new CSS — see tool-render.css for the `tv-` classes.
 */
import {
	type CSSProperties,
	Fragment,
	isValidElement,
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { Markdown } from "../components/markdown/Markdown";
import { FileTypeIcon } from "../components/ui/FileTypeIcon";
import { ImageViewer, type LightboxImage } from "../components/ui/ImageViewer";
import { Icon, type IconName } from "../components/ui/icons";
import { useAssetUrls } from "../lib/asset-urls";
import {
	highlightLinesSync,
	useHighlighted,
	useHighlightedLines,
	useLanguagesReady,
} from "../lib/highlight";
import { imageSrc } from "../lib/image-src";
import type { ToolRenderHost, ToolResultLike } from "./types";
import {
	formatElapsed,
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
	const gallery: LightboxImage[] = [];
	const galleryIndexByRaw = new Map<number, number>();
	const srcs = images.map((img, i) => {
		const src = imageSrc(img.data, img.mimeType, assets.blob);
		if (src) {
			galleryIndexByRaw.set(i, gallery.length);
			gallery.push({
				src,
				alt: t("image.alt", { n: i + 1 }),
			});
		}
		return src;
	});
	return (
		<div className="tv-imgs">
			{images.map((img, i) => {
				const src = srcs[i];
				const n = i + 1;
				if (!src)
					return (
						<Badge key={i} tone="warn">
							{t("image.unsupported", { type: img.mimeType })}
						</Badge>
					);
				const gIdx = galleryIndexByRaw.get(i) ?? 0;
				return (
					<button
						key={i}
						type="button"
						style={{ all: "unset", display: "inline-flex", cursor: "zoom-in" }}
						onClick={() => setViewing(gIdx)}
						aria-label={t("image.open", { n })}
					>
						<img className="tv-img" src={src} alt={t("image.alt", { n })} />
					</button>
				);
			})}
			{viewing !== null && gallery.length > 0 && (
				<ImageViewer
					images={gallery}
					initialIndex={viewing}
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

/** `active` 时每 `ms` 毫秒给出一次新的当前时刻（运行中的计时随之走动）；不活动时停表。 */
export function useLiveNow(active: boolean, ms = 1000): number {
	const [now, setNow] = useState(() => Date.now());
	useEffect(() => {
		if (!active) return;
		setNow(Date.now());
		const timer = setInterval(() => setNow(Date.now()), ms);
		return () => clearInterval(timer);
	}, [active, ms]);
	return now;
}

/** 自 `since` 起的实时已用时长（`8s` → `1:05`），每秒走动。 */
export function Elapsed({
	since,
	className,
}: {
	since: number;
	className?: string;
}): ReactNode {
	const now = useLiveNow(true);
	return <span className={className}>{formatElapsed(now - since)}</span>;
}

/** 进度格的一格：完成 / 进行中 / 待办 / 警告 / 失败。 */
export type CellTone = "ok" | "accent" | "idle" | "warn" | "err";

/** 格数超过这个值改画连续的分段条，免得格子细成一条线。 */
const MAX_CELLS = 16;

/**
 * 小进度条：每一项一格（按调用方给的顺序），项太多时按色调聚合成连续的分段条。
 * 纯装饰：`label` 给读屏器，数字读数由调用方另行写出。
 */
export function ProgressCells({
	tones,
	label,
}: {
	tones: readonly CellTone[];
	label: string;
}): ReactNode {
	if (tones.length === 0) return null;
	if (tones.length <= MAX_CELLS) {
		return (
			<span className="tv-cells-bar" role="img" aria-label={label}>
				{tones.map((tone, i) => (
					<i key={i} className={`tv-cell-dot tv-cell-dot--${tone}`} />
				))}
			</span>
		);
	}
	const order: CellTone[] = ["ok", "accent", "warn", "err", "idle"];
	return (
		<span
			className="tv-cells-bar tv-cells-bar--stack"
			role="img"
			aria-label={label}
		>
			{order.map((tone) => {
				const n = tones.filter((t) => t === tone).length;
				return n > 0 ? (
					<i
						key={tone}
						className={`tv-cell-dot tv-cell-dot--${tone}`}
						style={{ flexGrow: n }}
					/>
				) : null;
			})}
		</span>
	);
}

/**
 * 站点标记：主机名首字母的小方块（不向第三方请求 favicon：分享页与离线导出也一样显示）。
 * 同一主机总是同一个字母，列表里一眼能认出重复的来源。
 */
export function SiteMark({ host }: { host: string }): ReactNode {
	const letter = (/[\p{L}\p{N}]/u.exec(host)?.[0] ?? "?").toUpperCase();
	return (
		<span className="tv-site" aria-hidden="true">
			{letter}
		</span>
	);
}

/**
 * 卡片里的 Markdown（文档、搜索答案、网页正文、MCP 文本结果）：卡片默认等宽 12px，
 * 正文退回无衬线并用卡片正文字号，避免整段文档读成代码。
 * `images={false}`：外部来源的正文（搜索答案、抓取的网页）不加载远程图片（可能是追踪像素），
 * 图片留作链接，mermaid 也不渲染。
 */
export function CardMarkdown({
	text,
	images,
}: {
	text: string;
	images?: boolean;
}): ReactNode {
	return (
		<div className="font-sans [&>.md]:text-sm">
			<Markdown text={text} images={images} />
		</div>
	);
}

/**
 * 可折叠的 Markdown：超过 `maxHeight` 时先显示开头一截，底部渐隐，并给「展开全文」。
 * 高度在渲染后实测（图片、代码块加载后由 ResizeObserver 重算），不按行数猜。
 */
export function MarkdownBlock({
	text,
	maxHeight = 280,
	images,
}: {
	text: string;
	maxHeight?: number;
	/** 见 `CardMarkdown`：外部内容传 false。 */
	images?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const innerRef = useRef<HTMLDivElement>(null);
	const [tall, setTall] = useState(false);
	const [expanded, setExpanded] = useState(false);
	useLayoutEffect(() => {
		const el = innerRef.current;
		if (!el) return;
		// 多出不到一行半就直接放全，免得为一两行给一个按钮
		const update = () => setTall(el.scrollHeight > maxHeight + 36);
		update();
		if (typeof ResizeObserver === "undefined") return;
		const ro = new ResizeObserver(update);
		ro.observe(el);
		return () => ro.disconnect();
	}, [maxHeight]);
	const clamped = tall && !expanded;
	return (
		<div className="tv-mdblock">
			<div
				className="tv-mdblock-clip"
				data-clamped={clamped ? "" : undefined}
				style={{ "--tv-md-max": `${maxHeight}px` } as CSSProperties}
			>
				<div ref={innerRef}>
					<CardMarkdown text={text} images={images} />
				</div>
			</div>
			{tall && (
				<button
					type="button"
					className="tv-expand"
					aria-expanded={expanded}
					onClick={() => setExpanded((v) => !v)}
				>
					{expanded ? t("toolRender.collapse") : t("toolRender.showAll")}
				</button>
			)}
		</div>
	);
}

/** JSON 树一层先列这么多项，其余点「还有 N 项」展开。 */
const JSON_MAX_ITEMS = 60;
/** 短于这个长度、且不含多行字符串的对象 / 数组写成一行。 */
const JSON_INLINE_MAX = 64;
/** 长字符串先显示这么多字符。 */
const JSON_STRING_MAX = 360;

function isJsonContainer(value: unknown): value is object {
	return typeof value === "object" && value !== null;
}

function jsonChildren(value: object): [string | null, unknown][] {
	return Array.isArray(value)
		? value.map((v) => [null, v])
		: Object.entries(value);
}

/** 值写成一行是否放得下（预算内逐项估算，不整棵序列化）；含多行字符串时不写成一行。 */
function jsonFitsInline(value: unknown): boolean {
	let left = JSON_INLINE_MAX;
	const walk = (v: unknown): boolean => {
		if (typeof v === "string") {
			if (v.includes("\n")) return false;
			left -= v.length + 2;
		} else if (isJsonContainer(v)) {
			left -= 2;
			for (const [k, child] of jsonChildren(v)) {
				left -= k === null ? 2 : k.length + 6;
				if (left < 0 || !walk(child)) return false;
			}
		} else left -= String(v).length;
		return left >= 0;
	};
	return walk(value);
}

function JsonPunct({ children }: { children: ReactNode }): ReactNode {
	return <span className="tv-json-p">{children}</span>;
}

/** 字符串值：保留真实换行（不转义成 `\n`），超长时先显示开头一截。 */
function JsonString({ text }: { text: string }): ReactNode {
	const { t } = useTranslation();
	const [full, setFull] = useState(false);
	const long = text.length > JSON_STRING_MAX;
	return (
		<>
			<span className="hljs-string tv-json-str">
				"{long && !full ? `${text.slice(0, JSON_STRING_MAX)}…` : text}"
			</span>
			{long && (
				<button
					type="button"
					className="tv-json-more"
					onClick={() => setFull((v) => !v)}
				>
					{full ? t("toolRender.collapse") : t("toolRender.showAll")}
				</button>
			)}
		</>
	);
}

function JsonScalar({ value }: { value: unknown }): ReactNode {
	if (typeof value === "string") return <JsonString text={value} />;
	if (typeof value === "number")
		return <span className="hljs-number">{String(value)}</span>;
	if (typeof value === "boolean" || value === null)
		return <span className="hljs-literal">{String(value)}</span>;
	return <span className="tv-faint">{String(value)}</span>;
}

/** 放得下一行的值：`{"a": 1, "b": [2]}`。 */
function JsonInline({ value }: { value: unknown }): ReactNode {
	if (!isJsonContainer(value)) return <JsonScalar value={value} />;
	const array = Array.isArray(value);
	return (
		<>
			<JsonPunct>{array ? "[" : "{"}</JsonPunct>
			{jsonChildren(value).map(([k, v], i) => (
				<Fragment key={k ?? i}>
					{i > 0 && <JsonPunct>, </JsonPunct>}
					{k !== null && (
						<>
							<span className="hljs-attr">"{k}"</span>
							<JsonPunct>: </JsonPunct>
						</>
					)}
					<JsonInline value={v} />
				</Fragment>
			))}
			<JsonPunct>{array ? "]" : "}"}</JsonPunct>
		</>
	);
}

/** 树里的一行：缩进按深度（`--tv-json-d`），可折叠的行在缩进里放展开箭头。 */
function JsonLine({
	depth,
	caret,
	children,
}: {
	depth: number;
	caret?: ReactNode;
	children: ReactNode;
}): ReactNode {
	return (
		<div
			className="tv-json-line"
			style={{ "--tv-json-d": depth } as CSSProperties}
		>
			{caret}
			{children}
		</div>
	);
}

function JsonEntry({
	name,
	value,
	depth,
	comma,
	openDepth,
}: {
	name: string | null;
	value: unknown;
	depth: number;
	comma: boolean;
	openDepth: number;
}): ReactNode {
	const { t } = useTranslation();
	const nested =
		isJsonContainer(value) &&
		jsonChildren(value).length > 0 &&
		!jsonFitsInline(value);
	const [open, setOpen] = useState(depth < openDepth);
	const label = name !== null && (
		<>
			<span className="hljs-attr">"{name}"</span>
			<JsonPunct>: </JsonPunct>
		</>
	);
	const tail = comma && <JsonPunct>,</JsonPunct>;
	if (!nested)
		return (
			<JsonLine depth={depth}>
				{label}
				<JsonInline value={value} />
				{tail}
			</JsonLine>
		);
	const array = Array.isArray(value);
	const entries = jsonChildren(value);
	const caret = (
		<button
			type="button"
			className="tv-json-caret"
			aria-expanded={open}
			aria-label={open ? t("toolRender.collapse") : t("toolRender.json.expand")}
			onClick={() => setOpen((v) => !v)}
		>
			<Icon name="chevronRight" size={10} />
		</button>
	);
	const [l, r] = array ? ["[", "]"] : ["{", "}"];
	if (!open)
		return (
			<JsonLine depth={depth} caret={caret}>
				{label}
				<JsonPunct>{l}</JsonPunct>
				<button
					type="button"
					className="tv-json-fold"
					onClick={() => setOpen(true)}
				>
					{t(array ? "toolRender.json.items" : "toolRender.json.keys", {
						count: entries.length,
					})}
				</button>
				<JsonPunct>{r}</JsonPunct>
				{tail}
			</JsonLine>
		);
	return (
		<>
			<JsonLine depth={depth} caret={caret}>
				{label}
				<JsonPunct>{l}</JsonPunct>
			</JsonLine>
			<JsonChildren entries={entries} depth={depth + 1} openDepth={openDepth} />
			<JsonLine depth={depth}>
				<JsonPunct>{r}</JsonPunct>
				{tail}
			</JsonLine>
		</>
	);
}

function JsonChildren({
	entries,
	depth,
	openDepth,
	bare = false,
}: {
	entries: [string | null, unknown][];
	depth: number;
	openDepth: number;
	/** 根层逐行列出：不写逗号。 */
	bare?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const [all, setAll] = useState(false);
	const shown =
		all || entries.length <= JSON_MAX_ITEMS + 5
			? entries
			: entries.slice(0, JSON_MAX_ITEMS);
	return (
		<>
			{shown.map(([k, v], i) => (
				<JsonEntry
					key={k ?? i}
					name={k}
					value={v}
					depth={depth}
					comma={!bare && i < entries.length - 1}
					openDepth={openDepth}
				/>
			))}
			{shown.length < entries.length && (
				<JsonLine depth={depth}>
					<button
						type="button"
						className="tv-json-fold"
						onClick={() => setAll(true)}
					>
						{t("toolRender.json.more", {
							count: entries.length - shown.length,
						})}
					</button>
				</JsonLine>
			)}
		</>
	);
}

/**
 * JSON 值的可折叠树（调用参数、JSON 结果）：键值按 JSON 原样写出（`"key": value`），短的对象 / 数组写成一行，
 * 深于 `openDepth` 层的先收起（显示项数，点开再看）；字符串保留真实换行，超长的先显示开头。
 * 颜色沿用卡片里 highlight.js 的令牌类，与高亮过的 JSON 代码块一致。
 */
export function JsonTree({
	value,
	openDepth = 2,
	bare = false,
}: {
	value: unknown;
	openDepth?: number;
	/** 根是对象 / 数组时不写外层括号与逗号，每一项各占一行（调用参数用：键值逐行列出）。 */
	bare?: boolean;
}): ReactNode {
	return (
		<div className="tv-json">
			{bare && isJsonContainer(value) && jsonChildren(value).length > 0 ? (
				<JsonChildren
					entries={jsonChildren(value)}
					depth={0}
					openDepth={openDepth}
					bare
				/>
			) : (
				<JsonEntry
					name={null}
					value={value}
					depth={0}
					comma={false}
					openDepth={openDepth}
				/>
			)}
		</div>
	);
}

/**
 * 出错结果：首行（通常就是原因）醒目地写在前面，其余（堆栈、上下文、建议）弱化并可折叠。
 */
export function ErrorBlock({
	text,
	maxLines = 8,
}: {
	text: string;
	maxLines?: number;
}): ReactNode {
	const clean = stripAnsi(text).trim();
	if (!clean) return null;
	const nl = clean.indexOf("\n");
	const head = nl < 0 ? clean : clean.slice(0, nl);
	const rest = nl < 0 ? "" : clean.slice(nl + 1).replace(/^\s*\n/, "");
	return (
		<div className="tv-errblock">
			<div className="tv-errblock-head">
				<span className="tv-errblock-icon" aria-hidden="true">
					<Icon name="xCircle" size={13} />
				</span>
				<span>{head}</span>
			</div>
			{rest.trim() && (
				<div className="tv-errblock-rest">
					<Output text={rest} maxLines={maxLines} variant="plain" bare />
				</div>
			)}
		</div>
	);
}

/** 成功但没有内容的结果：一行弱化的说明，代替整段空白。 */
export function EmptyNote({ children }: { children: ReactNode }): ReactNode {
	return (
		<div className="tv-empty">
			<Icon name="minusCircle" size={13} />
			<span>{children}</span>
		</div>
	);
}

// ---- 文件与代码视图（read / write / edit / ast_edit / grep / glob / ast_grep / lsp 共用） ----

const IMAGE_EXTS: Record<string, true> = {
	png: true,
	jpg: true,
	jpeg: true,
	gif: true,
	webp: true,
	svg: true,
	avif: true,
	bmp: true,
	ico: true,
};

/** 文件图标：目录、图片、Markdown、表格、认得出语言的代码，其余按文本。 */
export function fileIconName(path: string): IconName {
	if (path.endsWith("/")) return "folder";
	const base = (path.split("/").pop() ?? "").toLowerCase();
	const dot = base.lastIndexOf(".");
	const ext = dot > 0 ? base.slice(dot + 1) : "";
	if (Object.hasOwn(IMAGE_EXTS, ext)) return "fileImage";
	if (ext === "md" || ext === "mdx") return "markdown";
	if (ext === "csv" || ext === "tsv") return "fileCsv";
	return languageFromPath(path) ? "fileCode" : "fileText";
}

/** 路径拆成所在目录（带末尾 `/`）与名字；目录路径末尾的 `/` 留在名字上。 */
export function splitDir(path: string): { dir: string; base: string } {
	const cut = path.slice(0, -1).lastIndexOf("/");
	return cut < 0
		? { dir: "", base: path }
		: { dir: path.slice(0, cut + 1), base: path.slice(cut + 1) };
}

/**
 * 文件头条：文件图标 + 弱化的目录 + 文件名 + 行范围，右侧放这个文件自己的读数（命中数、+N −M、语言……）。
 * 展开体里铺满段宽成一条带子；逐文件分组的结果（搜索命中、diff、诊断）都以它开头。
 */
export function FileHead({
	path,
	from,
	to,
	children,
}: {
	path: string;
	from?: number | null;
	to?: number | null;
	children?: ReactNode;
}): ReactNode {
	const { dir, base } = splitDir(shortenPath(path));
	const range =
		from == null
			? ""
			: to != null && to !== from
				? `:${from}-${to}`
				: `:${from}`;
	return (
		<div className="tv-fh" title={path}>
			<span className="tv-fh-icon" aria-hidden="true">
				<FileTypeIcon name={path} size={14} />
			</span>
			<span className="tv-fh-path">
				{dir && <span className="tv-fh-dir">{dir}</span>}
				<span className="tv-fh-base">{base}</span>
				{range && <span className="tv-lines">{range}</span>}
			</span>
			{children != null && children !== false && (
				<span className="tv-fh-aside">{children}</span>
			)}
		</div>
	);
}

/**
 * 代码视图的一行：上下文 / 命中 / 新增 / 删除是代码行；gap 是省略（`count` 为省略的行数，已知时），
 * hunk 是分段头（`text` 是所在函数等上下文），note 是夹在代码间的附注（`\ No newline`、ast_grep 的捕获）。
 */
export type CodeRowKind =
	| "ctx"
	| "hit"
	| "add"
	| "del"
	| "gap"
	| "hunk"
	| "note";

export interface CodeRow {
	kind: CodeRowKind;
	/** 行号：新增与上下文行是新文件侧，删除行是旧文件侧；没有时留空。 */
	no?: number | null;
	text: string;
	count?: number;
	/** 弱化显示（read 在所选范围外补的上下文行）。 */
	dim?: boolean;
}

/**
 * 逐段高亮：gap / hunk / note 把行切成连续的段，每段的旧侧（上下文 + 删除）与新侧（上下文 + 新增）
 * 各拼成一段整体高亮再按行映射回来，跨行的注释、字符串保持正确着色。没有删除行时只高亮一侧。
 */
function highlightRows(
	rows: readonly CodeRow[],
	lang: string | null | undefined,
): (string | null)[] {
	const out: (string | null)[] = rows.map(() => null);
	if (!lang) return out;
	let start = 0;
	const flush = (end: number) => {
		const oldSide: number[] = [];
		const newSide: number[] = [];
		let dels = false;
		for (let i = start; i < end; i++) {
			const kind = rows[i]?.kind;
			if (kind === "del") {
				oldSide.push(i);
				dels = true;
			} else if (kind === "add") newSide.push(i);
			else if (kind === "ctx" || kind === "hit") {
				oldSide.push(i);
				newSide.push(i);
			}
		}
		for (const idx of dels ? [oldSide, newSide] : [newSide]) {
			if (idx.length === 0) continue;
			const code = idx.map((i) => rows[i]?.text ?? "").join("\n");
			const html = highlightLinesSync(code, lang);
			if (!html) continue;
			idx.forEach((rowIdx, k) => {
				out[rowIdx] = html[k] ?? null;
			});
		}
	};
	rows.forEach((row, i) => {
		if (row.kind === "gap" || row.kind === "hunk" || row.kind === "note") {
			flush(i);
			start = i + 1;
		}
	});
	flush(rows.length);
	return out;
}

/** 全站共用一个 CSS 自定义高亮（`::highlight(tv-hit)`）；不支持的环境（jsdom、旧浏览器）为 null。 */
function hitHighlight(): Highlight | null {
	if (
		typeof Highlight === "undefined" ||
		typeof CSS === "undefined" ||
		!("highlights" in CSS)
	)
		return null;
	let hl = CSS.highlights.get("tv-hit");
	if (!hl) {
		hl = new Highlight();
		CSS.highlights.set("tv-hit", hl);
	}
	return hl;
}

/** 文本节点拼接串里的偏移 → (节点, 节点内偏移)。 */
function locateText(
	nodes: Text[],
	starts: number[],
	pos: number,
): [Text, number] | null {
	for (let i = nodes.length - 1; i >= 0; i--) {
		const start = starts[i] ?? 0;
		const node = nodes[i];
		if (node && start <= pos) return [node, pos - start];
	}
	return null;
}

/**
 * 命中行里与搜索模式匹配的片段用 CSS 自定义高亮标出：不改 DOM，不打断语法高亮的 span。
 * `key` 变化（展开、高亮完成）时重算。
 */
function useHitMarks(
	ref: { current: HTMLElement | null },
	re: RegExp | null | undefined,
	key: string,
): void {
	const source = re?.source ?? "";
	const flags = re?.flags ?? "";
	// biome-ignore lint/correctness/useExhaustiveDependencies: key 变化即需要重新标注
	useEffect(() => {
		const root = ref.current;
		const hl = source ? hitHighlight() : null;
		if (!root || !hl) return;
		const global = new RegExp(
			source,
			flags.includes("g") ? flags : `${flags}g`,
		);
		const ranges: Range[] = [];
		for (const el of root.querySelectorAll(".tv-cl-row--hit .tv-cl-code")) {
			const nodes: Text[] = [];
			const starts: number[] = [];
			let text = "";
			const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
			for (let n = walker.nextNode(); n; n = walker.nextNode()) {
				nodes.push(n as Text);
				starts.push(text.length);
				text += n.nodeValue ?? "";
			}
			global.lastIndex = 0;
			for (let m = global.exec(text); m; m = global.exec(text)) {
				if (m[0].length === 0) {
					global.lastIndex++;
					continue;
				}
				const a = locateText(nodes, starts, m.index);
				const b = locateText(nodes, starts, m.index + m[0].length);
				if (!a || !b) continue;
				const range = document.createRange();
				range.setStart(a[0], a[1]);
				range.setEnd(b[0], b[1]);
				ranges.push(range);
				hl.add(range);
			}
		}
		return () => {
			for (const r of ranges) hl.delete(r);
		};
	}, [ref, source, flags, key]);
}

/**
 * 带行号栏的代码：读文件、写入内容、搜索命中、diff 共用。行号栏不可选中（复制只得代码），
 * 有增删行时多一列 `+` / `−` 标记；`hitRe` 在命中行里标出匹配片段。
 */
export function CodeLines({
	rows,
	lang,
	maxLines = 14,
	signs,
	hitRe,
}: {
	rows: readonly CodeRow[];
	lang?: string | null;
	maxLines?: number;
	/** 是否画增删标记列；缺省时有增删行才画。 */
	signs?: boolean;
	hitRe?: RegExp | null;
}): ReactNode {
	const { t } = useTranslation();
	const [expanded, setExpanded] = useState(false);
	const ready = useLanguagesReady([lang]);
	// biome-ignore lint/correctness/useExhaustiveDependencies: ready 用于在语言加载后重算
	const html = useMemo(() => highlightRows(rows, lang), [rows, lang, ready]);
	const collapsible = rows.length > maxLines + 1;
	const shown = collapsible && !expanded ? rows.slice(0, maxLines) : rows;
	const ref = useOverflowX<HTMLDivElement>(shown);
	useHitMarks(ref, hitRe, `${shown.length}:${ready}`);
	const withSigns =
		signs ?? rows.some((r) => r.kind === "add" || r.kind === "del");
	// 没有一行带行号时不画行号栏（模式、补丁正文这类片段）
	let width = 0;
	for (const r of rows)
		if (r.no != null) width = Math.max(width, String(r.no).length);
	if (rows.length === 0) return null;
	return (
		<div className="tv-out">
			<div
				ref={ref}
				className={`tv-cl tv-scroll-x${withSigns ? " tv-cl--signs" : ""}${width === 0 ? " tv-cl--nonum" : ""}`}
				style={{ "--tv-cl-w": `${width}ch` } as CSSProperties}
			>
				{shown.map((row, i) => {
					const code = html[i];
					return (
						<div
							key={i}
							className={`tv-cl-row tv-cl-row--${row.kind}${row.dim ? " tv-cl-row--dim" : ""}`}
						>
							{width > 0 && (
								<span className="tv-cl-no">
									{row.kind === "gap" ? "⋯" : (row.no ?? "")}
								</span>
							)}
							{withSigns && (
								<span className="tv-cl-sign">
									{row.kind === "add" ? "+" : row.kind === "del" ? "−" : ""}
								</span>
							)}
							{code != null ? (
								<span
									className="tv-cl-code"
									dangerouslySetInnerHTML={{ __html: code || " " }}
								/>
							) : (
								<span className="tv-cl-code">
									{row.kind === "gap"
										? row.count
											? t("toolRender.files.gap", { count: row.count })
											: width === 0
												? "⋯"
												: ""
										: row.text || " "}
								</span>
							)}
						</div>
					);
				})}
			</div>
			{collapsible && (
				<ExpandToggle
					expanded={expanded}
					hidden={rows.length - maxLines}
					onToggle={() => setExpanded((v) => !v)}
				/>
			)}
		</div>
	);
}

/** omp 的带号 diff 行：`+12|code`、`-12|old`、` 12|ctx`（也认 `│`）。 */
const NUMBERED_DIFF_RE = /^([ +-])\s*(\d+)[|│](.*)$/;
/** 统一 diff 的分段头：`@@ -42,9 +42,14 @@ export class X {`。 */
const HUNK_HEAD_RE = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@ ?(.*)$/;
/** 统一 diff 的文件头等元信息行。 */
const DIFF_META_RE =
	/^(?:diff --git |index |--- |\+\+\+ |new file mode |deleted file mode )/;

/**
 * diff 文本转成代码行：omp 的带号 diff（整文件改动，行号跳跃处补上省略行及其行数）、
 * 统一 diff（按 `@@` 头推算行号，分段头显示所在上下文）、或不带行号的 `+` / `-` 行。
 */
export function parseDiff(diff: string): CodeRow[] {
	const rows: CodeRow[] = [];
	// 带号 diff：新旧行号之差，与下一个期望的新 / 旧行号（据此发现跳过的未改动行）
	let offset = 0;
	let nextOld: number | null = null;
	let nextNew: number | null = null;
	// 统一 diff：当前分段里的旧 / 新行号
	let hunkOld: number | null = null;
	let hunkNew: number | null = null;
	const gap = (count: number) => {
		if (count > 0) rows.push({ kind: "gap", text: "", count });
	};
	const lines = replaceTabs(stripAnsi(diff)).replace(/\n+$/, "").split("\n");
	for (const line of lines) {
		const hunk = HUNK_HEAD_RE.exec(line);
		if (hunk) {
			hunkOld = Number(hunk[1]);
			hunkNew = Number(hunk[2]);
			rows.push({ kind: "hunk", text: hunk[3]?.trim() || line });
			continue;
		}
		if (line.startsWith("\\ ")) {
			rows.push({ kind: "note", text: line.slice(2) });
			continue;
		}
		if (hunkOld !== null && hunkNew !== null) {
			const sign = line[0];
			const text = line.slice(1);
			if (sign === "-") rows.push({ kind: "del", no: hunkOld++, text });
			else if (sign === "+") rows.push({ kind: "add", no: hunkNew++, text });
			else {
				rows.push({ kind: "ctx", no: hunkNew, text });
				hunkOld++;
				hunkNew++;
			}
			continue;
		}
		if (DIFF_META_RE.test(line)) {
			rows.push({ kind: "note", text: line });
			continue;
		}
		const numbered = NUMBERED_DIFF_RE.exec(line);
		if (numbered) {
			const no = Number(numbered[2]);
			const text = numbered[3] ?? "";
			if (numbered[1] === "-") {
				if (nextOld !== null) gap(no - nextOld);
				nextOld = no + 1;
				nextNew ??= no + offset;
				offset--;
				rows.push({ kind: "del", no, text });
			} else if (numbered[1] === "+") {
				if (nextNew !== null) gap(no - nextNew);
				nextNew = no + 1;
				nextOld ??= no - offset;
				offset++;
				rows.push({ kind: "add", no, text });
			} else {
				if (nextNew !== null) gap(no - nextNew);
				nextNew = no + 1;
				nextOld = no - offset + 1;
				rows.push({ kind: "ctx", no, text });
			}
			continue;
		}
		if (line.trim() === "") rows.push({ kind: "gap", text: "" });
		else if (line[0] === "+") rows.push({ kind: "add", text: line.slice(1) });
		else if (line[0] === "-") rows.push({ kind: "del", text: line.slice(1) });
		else
			rows.push({
				kind: "ctx",
				text: line.startsWith(" ") ? line.slice(1) : line,
			});
	}
	return rows;
}

/** 带行号栏、增删标记列的 diff（edit / write 覆盖 / ast_edit 共用）。`maxLines={Infinity}` 不折叠。 */
export function DiffBlock({
	diff,
	lang,
	maxLines = 80,
}: {
	diff: string;
	lang?: string | null;
	maxLines?: number;
}): ReactNode {
	const rows = useMemo(() => parseDiff(diff), [diff]);
	return <CodeLines rows={rows} lang={lang} maxLines={maxLines} signs />;
}

/** 逐文件分组的结果里的一个文件：路径、头部附注（ast_edit 的 `2 replacements`）与它的行。 */
export interface FileGroup {
	path: string;
	label: string | null;
	rows: CodeRow[];
}

/** 目录 / 文件头：`# src/`、`## a.ts#7F3A`、`## a.ts (2 replacements)`。 */
const GROUP_HASH_RE = /^(#{1,6}) (.+)$/;
/** 单文件时的 hashline 头：`[src/a.ts#7F3A]`（不含空白，提示语 `[200 results …]` 不算）。 */
const GROUP_BRACKET_RE = /^\[([^\s\]]+)\]$/;
const SNAPSHOT_TAG_RE = /#[0-9A-Za-z]{4}$/;
const GROUP_LABEL_RE = /^(\S+) \((.+)\)$/;
/** 命中 / 上下文 / 增删行：`*12:`、` 12|`、`  *12│`、`  -12│`。 */
const GROUP_ROW_RE = /^\s*([*+-]?)(\d+)[:|│](.*)$/;
const GROUP_GAP_RE = /^\s*│?\s*(?:\.\.\.|…)$/;
/** ast_grep 每处命中后的捕获：`  meta: A=x, B=y`。 */
const GROUP_META_RE = /^\s+meta: (.*)$/;
/** 旧格式（ripgrep 风格）：`path:12:code`。 */
const LEGACY_HIT_RE = /^([^\s:#[][^:]*):(\d+):(.*)$/;

function groupHeader(raw: string): { name: string; label: string | null } {
	const m = GROUP_LABEL_RE.exec(raw);
	return {
		name: (m?.[1] ?? raw).replace(SNAPSHOT_TAG_RE, ""),
		label: m?.[2] ?? null,
	};
}

/**
 * grep / ast_grep / ast_edit 的分组输出（`formatGroupedFiles`：`#` 一层目录、文件头、`*12:` 命中行、
 * `...` 间隔；`displayContent` 是同样的版式换成 `│` 行号栏），以及旧的 `path:12:code` 格式，
 * 解析成逐文件的行。认不出的行（翻页提示、跳过的路径、上限说明……）收进 `notes`。
 * 整个输出没有 `*` / `+` / `-` 标记时（旧格式只列命中行）每行都算命中。
 */
export function parseGrouped(text: string): {
	groups: FileGroup[];
	notes: string[];
} {
	const lines = replaceTabs(stripAnsi(text)).replace(/\n+$/, "").split("\n");
	const groups: FileGroup[] = [];
	const notes: string[] = [];
	const dirs: string[] = [];
	let current: FileGroup | null = null;
	let marked = false;
	const open = (path: string, label: string | null = null): FileGroup => {
		const group: FileGroup = { path, label, rows: [] };
		groups.push(group);
		return group;
	};
	for (let i = 0; i < lines.length; i++) {
		const line = lines[i] ?? "";
		if (!line.trim()) continue;
		const hash = GROUP_HASH_RE.exec(line);
		if (hash) {
			const depth = hash[1]?.length ?? 1;
			const { name, label } = groupHeader((hash[2] ?? "").trim());
			dirs.length = Math.min(dirs.length, depth - 1);
			if (name.endsWith("/")) {
				dirs[depth - 1] = name;
				current = null;
			} else current = open(dirs.join("") + name, label);
			continue;
		}
		const bracket = GROUP_BRACKET_RE.exec(line.trim());
		if (bracket) {
			const { name, label } = groupHeader(bracket[1] ?? "");
			current = open(name, label);
			continue;
		}
		const row = GROUP_ROW_RE.exec(line);
		if (row) {
			const mark = row[1];
			if (mark) marked = true;
			current ??= open("");
			current.rows.push({
				kind:
					mark === "*"
						? "hit"
						: mark === "+"
							? "add"
							: mark === "-"
								? "del"
								: "ctx",
				no: Number(row[2]),
				text: row[3] ?? "",
			});
			continue;
		}
		if (current && GROUP_GAP_RE.test(line)) {
			current.rows.push({ kind: "gap", text: "" });
			continue;
		}
		const meta = GROUP_META_RE.exec(line);
		if (current && meta) {
			current.rows.push({ kind: "note", text: meta[1] ?? "" });
			continue;
		}
		const legacy = LEGACY_HIT_RE.exec(line);
		if (legacy && /[./]/.test(legacy[1] ?? "")) {
			const path = legacy[1] ?? "";
			if (current?.path !== path) current = open(path);
			current.rows.push({
				kind: "ctx",
				no: Number(legacy[2]),
				text: legacy[3] ?? "",
			});
			continue;
		}
		// 旧格式：单独一行的路径，紧跟着它的命中行
		if (
			!/\s/.test(line) &&
			/[./]/.test(line) &&
			GROUP_ROW_RE.test(lines[i + 1] ?? "")
		) {
			current = open(line);
			continue;
		}
		notes.push(line.trim().replace(/^\[(.*)\]$/, "$1"));
	}
	if (!marked)
		for (const g of groups)
			for (const r of g.rows) if (r.kind === "ctx") r.kind = "hit";
	return { groups: groups.filter((g) => g.rows.length > 0), notes };
}

/**
 * 逐文件分组的代码：每个文件一条文件头（右侧 `aside` 放该文件的读数）+ 带行号的行；
 * 语言按文件名取，认不出时用 `lang`。行数超过 `maxRows` 时整体折叠，给一个展开按钮。
 */
export function GroupedCode({
	groups,
	lang,
	hitRe,
	maxRows = 16,
	aside,
}: {
	groups: readonly FileGroup[];
	lang?: string | null;
	hitRe?: RegExp | null;
	maxRows?: number;
	aside?: (group: FileGroup) => ReactNode;
}): ReactNode {
	const [expanded, setExpanded] = useState(false);
	const total = groups.reduce((n, g) => n + g.rows.length, 0);
	const collapsible = total > maxRows + 2;
	const shown = useMemo(() => {
		let budget = collapsible && !expanded ? maxRows : Number.POSITIVE_INFINITY;
		const out: { group: FileGroup; rows: CodeRow[] }[] = [];
		for (const group of groups) {
			if (budget <= 0) break;
			const rows =
				group.rows.length > budget ? group.rows.slice(0, budget) : group.rows;
			budget -= rows.length;
			out.push({ group, rows });
		}
		return out;
	}, [groups, collapsible, expanded, maxRows]);
	if (groups.length === 0) return null;
	return (
		<div className="tv-fgroups">
			{shown.map(({ group, rows }, i) => (
				<div key={`${group.path}:${i}`} className="tv-fgroup">
					{group.path && (
						// 只有一个文件时它的读数就是头部读数，不再重复
						<FileHead path={group.path}>
							{groups.length > 1 && aside?.(group)}
						</FileHead>
					)}
					<CodeLines
						rows={rows}
						lang={(group.path && languageFromPath(group.path)) || lang}
						maxLines={Number.POSITIVE_INFINITY}
						hitRe={hitRe}
					/>
				</div>
			))}
			{collapsible && (
				<ExpandToggle
					expanded={expanded}
					hidden={total - maxRows}
					onToggle={() => setExpanded((v) => !v)}
				/>
			)}
		</div>
	);
}

/** 结果末尾的附注（翻页提示、跳过的路径、上限说明）：每条一行，弱化，带 info 图标。 */
export function FootNotes({
	notes,
	tone,
}: {
	notes: readonly string[];
	tone?: "warn";
}): ReactNode {
	if (notes.length === 0) return null;
	return (
		<div className={`tv-fnotes${tone ? ` tv-fnotes--${tone}` : ""}`}>
			{notes.map((note, i) => (
				<div key={i} className="tv-fnote">
					<Icon name={tone === "warn" ? "alert" : "info"} size={12} />
					<span>{note}</span>
				</div>
			))}
		</div>
	);
}

export type Severity = "error" | "warning" | "info" | "hint";

/** 一条语言服务器诊断。 */
export interface Diagnostic {
	file: string;
	line: number | null;
	col: number | null;
	severity: Severity;
	message: string;
	source: string | null;
	code: string | null;
}

/** 某个文件的检查状态行：`✓ src/b.ts: no issues`、`✗ src/c.ts: no language server …`。 */
export interface DiagnosticStatus {
	file: string;
	tone: "ok" | "warn" | "err";
	text: string;
}

/** `path:12:5 [error] [ts] msg (2304)` 或分组里的 `  12:5 [error] msg`。 */
const DIAG_ROW_RE =
	/^\s*(?:(.+?):)?(\d+):(\d+)\s+\[(error|warning|info|information|hint)\]\s*(.*)$/i;
const DIAG_SOURCE_RE = /^\[([^\]]+)\]\s*(.*)$/;
const DIAG_CODE_RE = /^(.*\S) \(([^()\s]{1,40})\)$/;
const DIAG_STATUS_RE = /^([✓✗⚠])\s+(.+?):\s+(.*)$/;
/** 计数行：`2 error(s), 1 warning(s):`。 */
const DIAG_SUMMARY_RE = /^\d+ \w+\(s\)(?:, \d+ \w+\(s\))*:?$/;

/**
 * 诊断文本（lsp diagnostics 的输出、write / edit 的 `diagnostics.messages`）解析成条目与文件状态行；
 * 认得 `path:line:col [severity]` 平铺格式与 `# dir/` `## file` 分组格式。认不出的行收进 `notes`。
 */
export function parseDiagnostics(text: string): {
	diags: Diagnostic[];
	status: DiagnosticStatus[];
	notes: string[];
} {
	const diags: Diagnostic[] = [];
	const status: DiagnosticStatus[] = [];
	const notes: string[] = [];
	const dirs: string[] = [];
	let file = "";
	for (const raw of stripAnsi(text).split("\n")) {
		const line = raw.trimEnd();
		if (
			!line.trim() ||
			line.trim() === "OK" ||
			DIAG_SUMMARY_RE.test(line.trim())
		)
			continue;
		const hash = GROUP_HASH_RE.exec(line);
		if (hash) {
			const depth = hash[1]?.length ?? 1;
			const name = (hash[2] ?? "").trim();
			dirs.length = Math.min(dirs.length, depth - 1);
			if (name.endsWith("/")) dirs[depth - 1] = name;
			else file = dirs.join("") + name;
			continue;
		}
		const row = DIAG_ROW_RE.exec(line);
		if (row) {
			let message = row[5] ?? "";
			const src = DIAG_SOURCE_RE.exec(message);
			if (src) message = src[2] ?? "";
			const code = DIAG_CODE_RE.exec(message);
			if (code) message = code[1] ?? message;
			const sev = (row[4] ?? "").toLowerCase();
			diags.push({
				file: row[1] ?? file,
				line: Number(row[2]),
				col: Number(row[3]),
				severity:
					sev === "error" || sev === "warning" || sev === "hint" ? sev : "info",
				message,
				source: src?.[1] ?? null,
				code: code?.[2] ?? null,
			});
			continue;
		}
		const st = DIAG_STATUS_RE.exec(line.trim());
		if (st) {
			status.push({
				file: st[2] ?? "",
				tone: st[1] === "✓" ? "ok" : st[1] === "✗" ? "err" : "warn",
				text: st[3] ?? "",
			});
			continue;
		}
		notes.push(line.trim());
	}
	return { diags, status, notes };
}

const SEVERITY_ICON: Record<Severity, IconName> = {
	error: "xCircle",
	warning: "alert",
	info: "info",
	hint: "lightbulb",
};

/** 诊断按严重度计数（info 与 hint 合并为「提示」）。 */
export function severityCounts(diags: readonly Diagnostic[]): {
	errors: number;
	warnings: number;
	infos: number;
} {
	let errors = 0;
	let warnings = 0;
	let infos = 0;
	for (const d of diags) {
		if (d.severity === "error") errors++;
		else if (d.severity === "warning") warnings++;
		else infos++;
	}
	return { errors, warnings, infos };
}

/** 严重度计数读数：红「2 错误」、黄「1 警告」、灰「3 提示」；都为 0 时不渲染。 */
export function SeverityCounts({
	diags,
}: {
	diags: readonly Diagnostic[];
}): ReactNode {
	const { t } = useTranslation();
	const { errors, warnings, infos } = severityCounts(diags);
	return (
		<>
			{errors > 0 && (
				<span className="tv-meta-err">
					{t("toolRender.files.errors", { count: errors })}
				</span>
			)}
			{warnings > 0 && (
				<span className="tv-meta-warn">
					{t("toolRender.files.warnings", { count: warnings })}
				</span>
			)}
			{infos > 0 && (
				<span>{t("toolRender.files.infos", { count: infos })}</span>
			)}
		</>
	);
}

const STATUS_ICON: Record<DiagnosticStatus["tone"], IconName> = {
	ok: "checkCircle",
	warn: "alert",
	err: "xCircle",
};

/**
 * 诊断列表：按文件分组，每组一条文件头（右侧是该文件的严重度计数，或检查状态），
 * 每条诊断一行：严重度图标（按色）+ `行:列` + 说明 + 来源与代码。超过 `maxRows` 条时折叠。
 */
export function DiagnosticList({
	diags,
	status = [],
	maxRows = 24,
	heads = true,
}: {
	diags: readonly Diagnostic[];
	status?: readonly DiagnosticStatus[];
	maxRows?: number;
	/** 是否给每个文件一条文件头（嵌在某个文件的小节里时不必重复）。 */
	heads?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const [expanded, setExpanded] = useState(false);
	const files: string[] = [];
	for (const d of diags) if (!files.includes(d.file)) files.push(d.file);
	for (const s of status) if (!files.includes(s.file)) files.push(s.file);
	if (files.length === 0) return null;
	const collapsible = diags.length > maxRows + 2;
	let budget = collapsible && !expanded ? maxRows : Number.POSITIVE_INFINITY;
	return (
		<div className="tv-fgroups">
			{files.map((file) => {
				const items = diags.filter((d) => d.file === file);
				const st = status.find((s) => s.file === file);
				if (budget <= 0 && items.length > 0) return null;
				const shown = items.slice(0, budget);
				budget -= shown.length;
				return (
					<div key={file} className="tv-fgroup">
						{heads && file && (
							<FileHead path={file}>
								{items.length > 0
									? files.length > 1 && <SeverityCounts diags={items} />
									: st && (
											<span className={`tv-dg-status tv-dg-status--${st.tone}`}>
												<Icon name={STATUS_ICON[st.tone]} size={12} />
												{st.text}
											</span>
										)}
							</FileHead>
						)}
						{shown.length > 0 && (
							<div className="tv-dgs">
								{shown.map((d, i) => (
									<div key={i} className={`tv-dg tv-dg--${d.severity}`}>
										<span
											className="tv-dg-icon"
											role="img"
											aria-label={t(`toolRender.files.severity.${d.severity}`)}
										>
											<Icon name={SEVERITY_ICON[d.severity]} size={12} />
										</span>
										{d.line != null && (
											<span className="tv-dg-pos">
												{d.line}:{d.col}
											</span>
										)}
										<span className="tv-dg-msg">
											{d.message}
											{(d.source || d.code) && (
												<span className="tv-dg-src">
													{[d.source, d.code].filter(Boolean).join(" ")}
												</span>
											)}
										</span>
									</div>
								))}
							</div>
						)}
					</div>
				);
			})}
			{collapsible && (
				<ExpandToggle
					expanded={expanded}
					hidden={diags.length - maxRows}
					onToggle={() => setExpanded((v) => !v)}
				/>
			)}
		</div>
	);
}
