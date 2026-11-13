/**
 * Markdown 渲染：react-markdown + remark-gfm + remark-breaks（单个换行即换行，与 omp 自带 Web 端的 `breaks: true` 一致）+ rehype-sanitize；代码块按需高亮（highlight.js 懒加载），
 * ```mermaid 图与 $…$ 数学公式在首次出现时才加载 mermaid / KaTeX。
 * `blob:sha256:<hex>` 图片引用改写为 `AssetUrlContext` 给出的地址（站内 `GET /api/v1/blobs/<hex>`，分享页为公开接口）。
 * mermaid 用 `base` 主题，配色取自当前令牌，明暗 / 主题色切换（THEME_EVENT）后重渲染。
 */

import type { MermaidConfig } from "mermaid";
import {
	type ComponentProps,
	memo,
	type ReactNode,
	useEffect,
	useId,
	useRef,
	useState,
} from "react";
import ReactMarkdown, { type Components, type Options } from "react-markdown";
import rehypeSanitize, { defaultSchema } from "rehype-sanitize";
import remarkBreaks from "remark-breaks";
import remarkGfm from "remark-gfm";
import { useAssetUrls } from "../../lib/asset-urls";
import { cssColor } from "../../lib/css-color";
import { useHighlighted } from "../../lib/highlight";
import { THEME_EVENT } from "../../state/theme";
import { CopyButton } from "../ui/CopyButton";
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

export function CodeBlock({
	code,
	lang,
}: {
	code: string;
	lang?: string | null;
}): ReactNode {
	const html = useHighlighted(code, lang);
	return (
		<div className="md-code group">
			<div className="md-code-bar">
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
		a: ({ href, children }) => (
			<a href={href} target="_blank" rel="noopener noreferrer">
				{children}
			</a>
		),
		img: ({ src, alt }) => (
			<img
				src={typeof src === "string" ? src : undefined}
				alt={alt ?? ""}
				loading="lazy"
			/>
		),
		// 表格外包一层圆角描边容器，窄屏横向滚动
		table: ({ children }) => (
			<div className="md-table">
				<table>{children}</table>
			</div>
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

export interface MarkdownProps {
	text: string;
	/** 流式中：不渲染 mermaid（代码未完整）。 */
	streaming?: boolean;
}

export const Markdown = memo(function Markdown({
	text,
	streaming = false,
}: MarkdownProps): ReactNode {
	const assets = useAssetUrls();
	const source = text.replace(BLOB_RE, (_, sha: string) => assets.blob(sha));
	const math = useMath(MATH_RE.test(source));
	const remarkPlugins: PluggableList = math
		? [remarkGfm, remarkBreaks, ...math.remark]
		: [remarkGfm, remarkBreaks];
	// 先清洗再交给 KaTeX，避免其输出被剥掉样式类
	const rehypePlugins: PluggableList = math
		? [[rehypeSanitize, schema], ...math.rehype]
		: [[rehypeSanitize, schema]];
	return (
		<div className="md">
			<ReactMarkdown
				remarkPlugins={remarkPlugins}
				rehypePlugins={rehypePlugins}
				components={streaming ? STREAMING_COMPONENTS : STATIC_COMPONENTS}
			>
				{source}
			</ReactMarkdown>
		</div>
	);
});
