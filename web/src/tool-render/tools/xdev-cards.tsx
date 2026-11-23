/**
 * `xd://` 设备的伪工具卡片：设备文档（help 模式与 `read xd://…`）与未能派发的设备写入。
 * 参数由 `xdev.ts` 的 `xdevCardOf` 生成：文档卡 `{ device, topic }`，设备卡 `{ device, payload }`。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { parseJsonText, SmartResult } from "../generic";
import { Badge, ErrorBlock, MarkdownBlock, MetaCount, Output } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import { normalizeWs, resultTextOf, str, truncate } from "../util";

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

/** 文档的篇幅（行数），收起时也看得出是一页还是一本。 */
function DocsMeta({ result }: ToolRenderProps): ReactNode {
	if (!result || result.isError) return null;
	const text = resultTextOf(result).trim();
	return text ? (
		<MetaCount unit="lines" count={text.split("\n").length} />
	) : null;
}

function DocsResult({ result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const text = resultTextOf(result).trim();
	if (!result) return null;
	if (result.isError || !text)
		return <SmartResult result={result} empty={t("toolRender.xdev.noDocs")} />;
	return <MarkdownBlock text={text} maxHeight={360} />;
}

/** 设备文档：Markdown 渲染文档正文（超高折叠），失败时给错误块。 */
export const xdevDocsRenderer: ToolRenderer = {
	Summary: DocsSummary,
	Meta: DocsMeta,
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

/** 写入设备的原始内容（模型写的就是这段，原样给出）；是 JSON 时按 JSON 高亮（设备参数多为 JSON 对象）。 */
function DeviceInput({ args }: ToolRenderProps): ReactNode {
	const payload = str(args.payload);
	if (!payload?.trim()) return null;
	return (
		<Output
			text={payload}
			lang={parseJsonText(payload) === undefined ? null : "json"}
			variant="code"
			maxLines={12}
		/>
	);
}

function DeviceResult({ result }: ToolRenderProps): ReactNode {
	if (result?.isError)
		return <ErrorBlock text={resultTextOf(result)} maxLines={12} />;
	return <SmartResult result={result} />;
}

/** 未派发的设备写入：错误（设备不存在、参数无效等，首行醒目）与写入的原始内容。 */
export const xdevDeviceRenderer: ToolRenderer = {
	Summary: DeviceSummary,
	Input: DeviceInput,
	Result: DeviceResult,
};
