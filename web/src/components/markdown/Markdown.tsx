/**
 * Markdown 渲染：react-markdown + remark-gfm + remark-breaks（单个换行即换行，与 omp 自带 Web 端的 `breaks: true` 一致）+ rehype-sanitize；代码块按需高亮（highlight.js 懒加载），
 * ```mermaid 图与 $…$ 数学公式在首次出现时才加载 mermaid / KaTeX。
 * `blob:sha256:<hex>` 图片引用改写为 `GET /api/v1/blobs/<hex>`。
 */
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
import { useHighlighted } from "../../lib/highlight";
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

function MermaidBlock({ code }: { code: string }): ReactNode {
	const id = useId().replace(/[^a-zA-Z0-9]/g, "");
	const [svg, setSvg] = useState<string | null>(null);
	const [failed, setFailed] = useState(false);
	useEffect(() => {
		let alive = true;
		void import("mermaid")
			.then(async ({ default: mermaid }) => {
				const dark =
					window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false;
				mermaid.initialize({
					startOnLoad: false,
					securityLevel: "strict",
					theme: dark ? "dark" : "neutral",
				});
				const out = await mermaid.render(`m${id}`, code);
				if (alive) setSvg(out.svg);
			})
			.catch(() => {
				if (alive) setFailed(true);
			});
		return () => {
			alive = false;
		};
	}, [code, id]);
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
	const source = text.replace(BLOB_RE, "/api/v1/blobs/$1");
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
