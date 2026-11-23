/**
 * Fallback renderer for tools without a dedicated view, plus the shared result
 * body for integration tools (MCP, xdev docs, Oryn host tools, artifacts):
 * arguments as a collapsible JSON tree, results as JSON tree / Markdown / text.
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	EmptyNote,
	ErrorBlock,
	JsonTree,
	MarkdownBlock,
	MetaCount,
	Output,
	ResultImages,
} from "./parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "./types";
import {
	isRecord,
	normalizeWs,
	resultImagesOf,
	resultTextOf,
	truncate,
} from "./util";

/** 文本是 JSON 对象 / 数组时返回解析后的值，否则 undefined。 */
export function parseJsonText(text: string): unknown {
	const trimmed = text.trim();
	if (!trimmed.startsWith("{") && !trimmed.startsWith("[")) return undefined;
	try {
		return JSON.parse(trimmed) as unknown;
	} catch {
		return undefined;
	}
}

/** 结构性的 Markdown 记号：标题、围栏代码、表格行、引用。出现一个就按 Markdown。 */
const MD_STRONG_RE = /^(?:#{1,6} \S|```|\|.+\|\s*$|> \S)/m;
/** 较弱的记号：列表、粗体、链接、行内代码。出现两种以上才按 Markdown。 */
const MD_WEAK_RES = [
	/^\s*(?:[-*+]|\d+\.) \S/m,
	/\*\*[^*\n]+\*\*/,
	/\[[^\]\n]+\]\([^)\s]+\)/,
	/`[^`\n]+`/,
];

/** 文本看起来是 Markdown（MCP、设备文档、GitHub 视图多半是）；日志、命令输出等不按 Markdown 渲染。 */
export function looksLikeMarkdown(text: string): boolean {
	if (MD_STRONG_RE.test(text)) return true;
	return MD_WEAK_RES.filter((re) => re.test(text)).length >= 2;
}

/**
 * 一段文本结果按原生形态展示：JSON → 可折叠树，Markdown → 渲染（超高折叠），其余 → 等宽文本。
 * `markdown`：不是 JSON 的文本一律按 Markdown（MCP 结果约定是 Markdown，同 TUI）。
 */
export function SmartText({
	text,
	maxLines = 14,
	markdown = false,
}: {
	text: string;
	maxLines?: number;
	markdown?: boolean;
}): ReactNode {
	const json = parseJsonText(text);
	if (json !== undefined) return <JsonTree value={json} />;
	if (markdown || looksLikeMarkdown(text)) return <MarkdownBlock text={text} />;
	return <Output text={text} maxLines={maxLines} />;
}

/**
 * 结果段：出错时错误块（首行醒目），否则图片 + 文本（{@link SmartText}）；成功却什么都没返回时给空状态。
 * 没有结果（运行中）时不渲染。
 */
export function SmartResult({
	result,
	empty,
	maxLines,
	markdown,
}: {
	result: ToolResultLike | undefined;
	/** 空状态文案；默认「没有输出」。 */
	empty?: string;
	maxLines?: number;
	markdown?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	if (!result) return null;
	const text = resultTextOf(result).trim();
	if (result.isError)
		return <ErrorBlock text={text || t("toolRender.generic.failed")} />;
	const images = resultImagesOf(result).length > 0;
	if (!text && !images)
		return <EmptyNote>{empty ?? t("toolRender.generic.empty")}</EmptyNote>;
	return (
		<>
			<ResultImages result={result} />
			{text && (
				<SmartText text={text} maxLines={maxLines} markdown={markdown} />
			)}
		</>
	);
}

/** 调用参数的完整视图：可折叠的 JSON 树；没有参数时不渲染。 */
export function ArgsTree({ args }: Pick<ToolRenderProps, "args">): ReactNode {
	if (!isRecord(args) || Object.keys(args).length === 0) return null;
	return <JsonTree value={args} bare />;
}

/** 参数的一行预览：标量写成 `key=value`，嵌套值写成紧凑 JSON。 */
export function inlineArgs(args: Record<string, unknown>): string {
	const parts: string[] = [];
	for (const key in args) {
		const value = args[key];
		if (value === undefined || value === null) continue;
		let shown: string;
		if (typeof value === "object") {
			try {
				shown = truncate(JSON.stringify(value) ?? "", 40);
			} catch {
				shown = "…";
			}
		} else shown = String(value);
		parts.push(`${key}=${normalizeWs(shown)}`);
	}
	return parts.join(" ");
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const text = inlineArgs(args);
	return text ? <span>{truncate(text, 120)}</span> : null;
}

/** 右侧读数：JSON 数组结果的项数，或多行文本的行数。 */
function Meta({ result }: ToolRenderProps): ReactNode {
	if (!result || result.isError) return null;
	const text = resultTextOf(result).trim();
	if (!text) return null;
	const json = parseJsonText(text);
	if (Array.isArray(json)) return <ItemCount count={json.length} />;
	if (json !== undefined) return null;
	const lines = text.split("\n").length;
	return lines > 1 ? <MetaCount unit="lines" count={lines} /> : null;
}

function ItemCount({ count }: { count: number }): ReactNode {
	const { t } = useTranslation();
	return <span>{t("toolRender.json.items", { count })}</span>;
}

function Result({ result }: ToolRenderProps): ReactNode {
	return <SmartResult result={result} />;
}

export const genericRenderer: ToolRenderer = {
	Summary,
	Meta,
	Input: ArgsTree,
	Result,
};
