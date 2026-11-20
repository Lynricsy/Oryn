/**
 * `xd://` 设备的伪工具卡片：设备文档（help 模式与 `read xd://…`）与未能派发的设备写入。
 * 参数由 `xdev.ts` 的 `xdevCardOf` 生成：文档卡 `{ device, topic }`，设备卡 `{ device, payload }`。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Markdown } from "../../components/markdown/Markdown";
import { Badge, Output, ResultText } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import { normalizeWs, resultTextOf, str, truncate } from "../util";

/**
 * 卡片里的 Markdown（设备文档、MCP 文本结果、rewind 报告）：卡片默认等宽 12px，
 * 正文退回无衬线并用卡片正文字号，避免整段文档读成代码。
 */
export function CardMarkdown({ text }: { text: string }): ReactNode {
	return (
		<div className="font-sans [&>.md]:text-sm">
			<Markdown text={text} />
		</div>
	);
}

function DocsSummary({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const device = str(args.device);
	const topic = str(args.topic);
	return (
		<>
			<Badge>
				{device ? t("toolRender.xdev.docs") : t("toolRender.xdev.catalog")}
			</Badge>
			{topic && <span> {topic}</span>}
		</>
	);
}

function DocsResult({ result }: ToolRenderProps): ReactNode {
	const text = resultTextOf(result).trim();
	if (!text) return null;
	if (result?.isError) return <ResultText result={result} maxLines={12} />;
	return <CardMarkdown text={text} />;
}

/** 设备文档：Markdown 渲染文档正文，失败时显示错误文本。 */
export const xdevDocsRenderer: ToolRenderer = {
	Summary: DocsSummary,
	Result: DocsResult,
};

function DeviceSummary({ args, result }: ToolRenderProps): ReactNode {
	const error = result?.isError ? resultTextOf(result).trim() : "";
	if (error)
		return (
			<span className="tv-err-text">
				{truncate(normalizeWs(error.split("\n", 1)[0] ?? error), 120)}
			</span>
		);
	const payload = str(args.payload);
	return payload ? <span>{truncate(normalizeWs(payload), 96)}</span> : null;
}

/** 写入设备的原始内容；能解析成 JSON 时按 JSON 高亮（设备参数多为 JSON 对象）。 */
function DeviceInput({ args }: ToolRenderProps): ReactNode {
	const payload = str(args.payload);
	if (!payload?.trim()) return null;
	return (
		<Output
			text={payload}
			lang={isJson(payload) ? "json" : null}
			variant="code"
			maxLines={12}
		/>
	);
}

function isJson(text: string): boolean {
	try {
		JSON.parse(text);
		return true;
	} catch {
		return false;
	}
}

function DeviceResult({ result }: ToolRenderProps): ReactNode {
	return <ResultText result={result} maxLines={16} />;
}

/** 未派发的设备写入：错误首行（设备不存在、参数无效等）与写入的原始内容。 */
export const xdevDeviceRenderer: ToolRenderer = {
	Summary: DeviceSummary,
	Input: DeviceInput,
	Result: DeviceResult,
};
