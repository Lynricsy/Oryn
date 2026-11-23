/**
 * Markdown 渲染：react-markdown + remark-gfm + remark-breaks（单个换行即换行，与 omp 自带 Web 端的 `breaks: true` 一致）+ rehype-sanitize；代码块按需高亮（highlight.js 懒加载），
 * ```mermaid 图与 $…$ 数学公式在首次出现时才加载 mermaid / KaTeX。
 * `blob:sha256:<hex>` 图片引用改写为 `AssetUrlContext` 给出的地址（站内 `GET /api/v1/blobs/<hex>`，分享页为公开接口）。
 * mermaid 用 `base` 主题，配色取自当前令牌，明暗 / 主题色切换（THEME_EVENT）后重渲染。
 * 文档首行起的 YAML frontmatter（SKILL.md、rules/*.md 等）渲染成字段网格，不再被当成 setext 标题；解析不了时原样交给 Markdown。
 * `relativeTo`（M24，仓库 README 等）：相对地址的链接改成站内路由（react-router Link，不新开窗口），相对图片改成可加载的地址。
 * `document`（仓库文件、README 等文档）：按 GitHub 渲染文件的方式——单个换行不断行（不用 remark-breaks）、内嵌 HTML
 * 经 rehype-raw 解析后同样过 rehype-sanitize（GitHub 的白名单：`<div align>`、`<img width>`、`<details>` 等），样式见 `.md-doc`。
 * 外链图片不带 Referer。
 */

import type { MermaidConfig } from "mermaid";
import {
	type ComponentProps,
	createContext,
	memo,
	type ReactNode,
	useContext,
	useEffect,
	useId,
	useRef,
	useState,
} from "react";
import ReactMarkdown, { type Components, type Options } from "react-markdown";
import { Link } from "react-router";
import rehypeRaw from "rehype-raw";
import rehypeSanitize, { defaultSchema } from "rehype-sanitize";
import remarkBreaks from "remark-breaks";
import remarkGfm from "remark-gfm";
import i18n from "../../i18n";
import { useAssetUrls } from "../../lib/asset-urls";
import { imageGroup } from "../../lib/context-menu/image-actions";
import { useContextTarget } from "../../lib/context-menu/registry";
import { copyWithToast } from "../../lib/context-menu/selection-groups";
import type {
	ContextRequest,
	MenuGroup,
	SelectionSource,
} from "../../lib/context-menu/types";
import { cssColor } from "../../lib/css-color";
import { splitFrontmatter } from "../../lib/frontmatter";
import { useHighlighted } from "../../lib/highlight";
import { locateEntry, runSideAsk, sideAskBusy } from "../../state/side-ask";
import { THEME_EVENT } from "../../state/theme";
import { CopyButton } from "../ui/CopyButton";
import { FrontmatterFields } from "./Frontmatter";
import { MarkdownSourceContext, MdTable } from "./Table";
import "./markdown.css";

type PluggableList = NonNullable<Options["remarkPlugins"]>;

const MATH_RE =
	/\$\$[\s\S]+?\$\$|\\\(|\\\[|(?:^|[^\\$\w])\$[^\s$][^$\n]*?[^\s\\$]\$(?!\w)/;
const BLOB_RE = /blob:sha256:([0-9a-f]{64})/g;

const schema = {
	...defaultSchema,
	attributes: {
		...defaultSchema.attributes,
		code: [["className", /^language-./, "math-inline", "math-display"]],
		span: [
			...(defaultSchema.attributes?.span ?? []),
			["className", "math-inline", "math-display"],
		],
		div: [
			...(defaultSchema.attributes?.div ?? []),
			["className", "math-display"],
		],
	},
};

interface MathPlugins {
	remark: PluggableList;
	rehype: PluggableList;
}

let mathPlugins: MathPlugins | null = null;
let mathPromise: Promise<MathPlugins> | null = null;

function loadMath(): Promise<MathPlugins> {
	mathPromise ??= Promise.all([
		import("remark-math"),
		import("rehype-katex"),
		import("katex/dist/katex.min.css"),
	]).then(([rm, rk]) => {
		mathPlugins = { remark: [rm.default], rehype: [rk.default] };
		return mathPlugins;
	});
	return mathPromise;
}

function useMath(needed: boolean): MathPlugins | null {
	const [plugins, setPlugins] = useState<MathPlugins | null>(mathPlugins);
	useEffect(() => {
		if (!needed || plugins) return;
		let alive = true;
		void loadMath().then((p) => {
			if (alive) setPlugins(p);
		});
		return () => {
			alive = false;
		};
	}, [needed, plugins]);
	return needed ? plugins : null;
}

/** 挂载 mermaid 产出的 SVG（securityLevel "strict" 下 mermaid 已自行清洗）。 */
function SvgHost({ svg }: { svg: string }): ReactNode {
	const ref = useRef<HTMLDivElement>(null);
	useEffect(() => {
		if (ref.current) ref.current.innerHTML = svg;
	}, [svg]);
	return <div ref={ref} className="md-mermaid" />;
}

/** 主题切换计数：每次 THEME_EVENT 加一，作为 mermaid 重渲染的依赖。 */
function useThemeVersion(): number {
	const [version, setVersion] = useState(0);
	useEffect(() => {
		const onTheme = () => setVersion((v) => v + 1);
		window.addEventListener(THEME_EVENT, onTheme);
		return () => window.removeEventListener(THEME_EVENT, onTheme);
	}, []);
	return version;
}

/** 图内字号：不超过界面正文 13px，免得图比散文（14.5px）还抢眼。 */
const MERMAID_FONT_PX = 13;
const MERMAID_SMALL_FONT_PX = 12;

/** 从当前令牌读出 mermaid `base` 主题变量（mermaid 只认 hex）。节点白底细描边，不用灰块。 */
function mermaidThemeVariables(sans: string): Record<string, string | boolean> {
	const panel = cssColor("--panel", "#fcfcfd");
	const inset = cssColor("--inset", "#f1f2f5");
	const raised = cssColor("--raised", "#f5f6f8");
	const line = cssColor("--line", "#e2e4e8");
	const lineStrong = cssColor("--line-strong", "#cfd2d8");
	const text = cssColor("--text", "#202329");
	const muted = cssColor("--text-muted", "#5a5f6b");
	return {
		darkMode: document.documentElement.dataset.theme === "dark",
		fontFamily: sans,
		fontSize: `${MERMAID_FONT_PX}px`,
		background: panel,
		primaryColor: panel,
		primaryBorderColor: lineStrong,
		primaryTextColor: text,
		secondaryColor: inset,
		secondaryBorderColor: line,
		secondaryTextColor: text,
		tertiaryColor: raised,
		tertiaryBorderColor: line,
		tertiaryTextColor: text,
		mainBkg: panel,
		nodeBorder: lineStrong,
		clusterBkg: inset,
		clusterBorder: line,
		edgeLabelBackground: panel,
		lineColor: muted,
		textColor: text,
		actorBkg: panel,
		actorBorder: lineStrong,
		actorTextColor: text,
		actorLineColor: lineStrong,
		signalColor: muted,
		signalTextColor: text,
		labelBoxBkgColor: inset,
		labelBoxBorderColor: line,
		labelTextColor: text,
		loopTextColor: muted,
		noteBkgColor: inset,
		noteBorderColor: line,
		noteTextColor: muted,
		activationBkgColor: inset,
		activationBorderColor: lineStrong,
		sequenceNumberColor: panel,
	};
}

/**
 * 完整 mermaid 配置。`useMaxWidth: false` 让 SVG 保持自身尺寸（只随容器缩小，不放大），图内字号才是真实的 13px；
 * `look: "classic"`：mermaid 12 默认的 neo 外观给时序图参与者预留 44px 图形带，盒子会高出一倍。
 */
function mermaidConfig(): MermaidConfig {
	const sans =
		getComputedStyle(document.documentElement)
			.getPropertyValue("--sans")
			.trim() || "sans-serif";
	const fit = { useMaxWidth: false };
	return {
		startOnLoad: false,
		securityLevel: "strict",
		theme: "base",
		look: "classic",
		fontFamily: sans,
		fontSize: MERMAID_FONT_PX,
		themeVariables: mermaidThemeVariables(sans),
		flowchart: {
			...fit,
			padding: 8,
			nodeSpacing: 32,
			rankSpacing: 36,
			diagramPadding: 8,
		},
		sequence: {
			...fit,
			diagramMarginX: 8,
			diagramMarginY: 8,
			width: 96,
			height: 34,
			actorMargin: 48,
			boxMargin: 8,
			boxTextMargin: 4,
			noteMargin: 8,
			messageMargin: 28,
			actorFontSize: MERMAID_FONT_PX,
			actorFontFamily: sans,
			messageFontSize: MERMAID_SMALL_FONT_PX,
			messageFontFamily: sans,
			noteFontSize: MERMAID_SMALL_FONT_PX,
			noteFontFamily: sans,
		},
		class: fit,
		state: fit,
		er: fit,
		gantt: { ...fit, fontSize: MERMAID_SMALL_FONT_PX },
		journey: fit,
		timeline: fit,
		pie: fit,
		mindmap: fit,
		gitGraph: fit,
		requirement: fit,
		quadrantChart: fit,
		xyChart: fit,
		sankey: fit,
		block: fit,
		architecture: fit,
	};
}

function MermaidBlock({ code }: { code: string }): ReactNode {
	const id = useId().replace(/[^a-zA-Z0-9]/g, "");
	const version = useThemeVersion();
	const [svg, setSvg] = useState<string | null>(null);
	const [failed, setFailed] = useState(false);
	useEffect(() => {
		let alive = true;
		void import("mermaid")
			.then(async ({ default: mermaid }) => {
				mermaid.initialize(mermaidConfig());
				const out = await mermaid.render(`m${id}v${version}`, code);
				if (alive) setSvg(out.svg);
			})
			.catch(() => {
				if (alive) setFailed(true);
			});
		return () => {
			alive = false;
		};
	}, [code, id, version]);
	if (failed || svg === null) return <CodeBlock code={code} lang="mermaid" />;
	return <SvgHost svg={svg} />;
}

/** 代码块的来源：在转录行里时带上消息与回合信息。 */
function codeSource(el: Element, threadId: string): SelectionSource {
	const row = el.closest("[data-index]");
	const role = row?.getAttribute("data-role");
	if (!row || (role !== "user" && role !== "assistant"))
		return { kind: "other" };
	return {
		kind: "message",
		role,
		part: "text",
		...locateEntry(threadId, row.getAttribute("data-entry-id") ?? undefined),
		live: row.hasAttribute("data-live"),
		spans: false,
	};
}

export function CodeBlock({
	code,
	lang,
}: {
	code: string;
	lang?: string | null;
}): ReactNode {
	const html = useHighlighted(code, lang);
	const self = useRef<HTMLDivElement | null>(null);
	const ctxRef = useContextTarget<HTMLDivElement>({
		items: (req) => {
			const items: MenuGroup["items"] = [
				{
					key: "copy",
					icon: "copy",
					label: i18n.t("contextMenu.copyCode"),
					onSelect: () => void copyWithToast(code),
				},
			];
			const threadId = req.askThreadId;
			const el = self.current;
			if (threadId && el) {
				const busy = sideAskBusy(threadId);
				const r = el.getBoundingClientRect();
				items.push({
					key: "explain",
					icon: "sparkle",
					label: i18n.t("contextMenu.explainCode"),
					disabled: busy,
					title: busy ? i18n.t("sideAsk.busy") : undefined,
					onSelect: () =>
						void runSideAsk(
							{ kind: "explain" },
							{
								threadId,
								text: code,
								before: "",
								after: "",
								source: codeSource(el, threadId),
								anchor: {
									left: r.left,
									top: r.top,
									right: r.right,
									bottom: r.bottom,
								},
							},
						),
				});
			}
			return [{ key: "code", items }];
		},
	});
	return (
		<div
			ref={(el) => {
				self.current = el;
				ctxRef(el);
			}}
			className="md-code focus-inset group"
		>
			<div className="md-code-bar" data-ctx-touch="">
				<span>{lang ?? ""}</span>
				<CopyButton text={code} />
			</div>
			<pre>
				{html !== null ? (
					// biome-ignore lint/security/noDangerouslySetInnerHtml: highlight.js 输出已转义
					<code className="hljs" dangerouslySetInnerHTML={{ __html: html }} />
				) : (
					<code>{code}</code>
				)}
			</pre>
		</div>
	);
}

function makeComponents(streaming: boolean): Components {
	return {
		pre: ({ children }) => <>{children}</>,
		code: ({
			className,
			children,
			...rest
		}: ComponentProps<"code"> & { node?: unknown }) => {
			const text = String(children ?? "").replace(/\n$/, "");
			const m = /language-([\w+#-]+)/.exec(className ?? "");
			const lang = m?.[1];
			const isBlock = Boolean(lang) || text.includes("\n");
			if (!isBlock) {
				const { node: _node, ...props } = rest;
				return (
					<code className="md-inline-code" {...props}>
						{children}
					</code>
				);
			}
			if (lang === "mermaid" && !streaming) return <MermaidBlock code={text} />;
			return <CodeBlock code={text} lang={lang} />;
		},
		a: MdLink,
		img: MdImage,
		// 表格外包圆角描边容器（窄屏横向滚动，宽屏可伸出正文列），悬停时出复制按钮
		table: ({ children, node }) => (
			<MdTable position={node?.position}>{children}</MdTable>
		),
		// 有序列表编号用 CSS 计数器画成小方块；`start` 要落到计数器起点上
		ol: ({ start, children }) => (
			<ol
				start={start}
				style={
					typeof start === "number" && start !== 1
						? { counterReset: `md-ol ${start - 1}` }
						: undefined
				}
			>
				{children}
			</ol>
		),
	};
}

const STATIC_COMPONENTS = makeComponents(false);
const STREAMING_COMPONENTS = makeComponents(true);

/** 相对地址的解析（README 等仓库文档）：不带 scheme、不以 `#` 或 `//` 开头的 href / src 交给它；返回 null 时原样输出 */
export interface RelativeUrls {
	/** 返回站内路由（以 `/` 开头），用 react-router 的 Link 打开，不新开窗口 */
	link(href: string): string | null;
	/** 返回可直接加载的图片地址 */
	image(src: string): string | null;
}

const RelativeUrlContext = createContext<RelativeUrls | null>(null);

function isRelativeUrl(u: string): boolean {
	return (
		!/^[a-z][a-z0-9+.-]*:/i.test(u) && !u.startsWith("#") && !u.startsWith("//")
	);
}

function MdLink({ href, children }: ComponentProps<"a">): ReactNode {
	const rel = useContext(RelativeUrlContext);
	const to = rel && href && isRelativeUrl(href) ? rel.link(href) : null;
	if (to !== null) return <Link to={to}>{children}</Link>;
	return (
		<a href={href} target="_blank" rel="noopener noreferrer">
			{children}
		</a>
	);
}

function MdImage({
	src,
	alt,
	node: _node,
	...rest
}: ComponentProps<"img"> & { node?: unknown }): ReactNode {
	const rel = useContext(RelativeUrlContext);
	const raw = typeof src === "string" ? src : undefined;
	const resolved = rel && raw && isRelativeUrl(raw) ? rel.image(raw) : null;
	// rest 只剩清洗白名单里的属性（内嵌 HTML 的 width / height / align / title）
	return (
		<img
			{...rest}
			src={resolved ?? raw}
			alt={alt ?? ""}
			loading="lazy"
			referrerPolicy="no-referrer"
		/>
	);
}

export interface MarkdownProps {
	text: string;
	/** 流式中：不渲染 mermaid（代码未完整）。 */
	streaming?: boolean;
	/** 相对链接与相对图片的解析；不给时相对地址原样输出 */
	relativeTo?: RelativeUrls;
	/** 按文档渲染（仓库文件、README）：允许清洗后的内嵌 HTML，单个换行不断行 */
	document?: boolean;
}

export const Markdown = memo(function Markdown({
	text,
	streaming = false,
	relativeTo,
	document = false,
}: MarkdownProps): ReactNode {
	const assets = useAssetUrls();
	const root = useRef<HTMLDivElement | null>(null);
	// 正文里的链接、图片、行内代码各有自己的菜单组
	const ctxRef = useContextTarget<HTMLDivElement>({
		items: (req) => markdownGroups(req, root.current),
	});
	const doc = splitFrontmatter(text);
	const source = (doc ? doc.body : text).replace(BLOB_RE, (_, sha: string) =>
		assets.blob(sha),
	);
	const math = useMath(MATH_RE.test(source));
	const remarkPlugins: PluggableList = [
		remarkGfm,
		...(document ? [] : [remarkBreaks]),
		...(math?.remark ?? []),
	];
	// 内嵌 HTML 先解析成元素再统一清洗；先清洗再交给 KaTeX，避免其输出被剥掉样式类
	const rehypePlugins: PluggableList = [
		...(document ? [rehypeRaw] : []),
		[rehypeSanitize, schema],
		...(math?.rehype ?? []),
	];
	return (
		<div
			ref={(el) => {
				root.current = el;
				ctxRef(el);
			}}
			className={document ? "md md-doc" : "md"}
		>
			{doc && <FrontmatterFields fields={doc.fields} />}
			<RelativeUrlContext.Provider value={relativeTo ?? null}>
				<MarkdownSourceContext.Provider value={source}>
					<ReactMarkdown
						remarkPlugins={remarkPlugins}
						rehypePlugins={rehypePlugins}
						components={streaming ? STREAMING_COMPONENTS : STATIC_COMPONENTS}
					>
						{source}
					</ReactMarkdown>
				</MarkdownSourceContext.Provider>
			</RelativeUrlContext.Provider>
		</div>
	);
});

function markdownGroups(
	req: ContextRequest,
	root: HTMLElement | null,
): MenuGroup[] | null {
	if (!root) return null;
	const link = req.target.closest("a[href]");
	if (link && root.contains(link)) {
		const href = link.getAttribute("href") ?? "";
		return [
			{
				key: "link",
				items: [
					{
						key: "open",
						icon: "external",
						label: i18n.t("contextMenu.openLink"),
						onSelect: () => window.open(href, "_blank", "noopener"),
					},
					{
						key: "copyLink",
						icon: "link",
						label: i18n.t("contextMenu.copyLink"),
						onSelect: () => void copyWithToast(href),
					},
				],
			},
		];
	}
	const img = req.target.closest("img");
	if (img && root.contains(img))
		return [imageGroup(img.currentSrc || img.src, req.askThreadId)];
	const inline = req.target.closest("code.md-inline-code");
	if (inline && root.contains(inline))
		return [
			{
				key: "code",
				items: [
					{
						key: "copy",
						icon: "copy",
						label: i18n.t("common.copy"),
						onSelect: () => void copyWithToast(inline.textContent ?? ""),
					},
				],
			},
		];
	return null;
}
