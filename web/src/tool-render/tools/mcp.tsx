/**
 * MCP 工具（`mcp__<server>_<tool>`，顶层调用或 `write xd://mcp__…` 派发）：
 * 头部名字由 ToolView 写成 server / tool，摘要给主要参数，读数在运行中是进度条 + 进度文字、结束后是结果的项数。
 * 展开体：参数是可折叠的 JSON 树；运行中输出段是进度面板，结束后是图片与文本结果（JSON 树 / Markdown / 文本）。
 * 结果形状见 oryn-core `mcp/tool-bridge.ts`：details 带 `serverName` / `mcpToolName`，
 * 部分结果带 `progress: { progress, total?, message? }`。
 */
import type { CSSProperties, ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ArgsTree, inlineArgs, parseJsonText, SmartResult } from "../generic";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	isRecord,
	normalizeWs,
	num,
	resultImagesOf,
	resultTextOf,
	str,
	truncate,
} from "../util";

/** 摘要里优先展示的参数：先动作，再对象（同 TUI 的设备活动摘要）。 */
const VERB_KEYS = ["action", "op", "command"] as const;
const OBJECT_KEYS = [
	"query",
	"symbol",
	"path",
	"file",
	"pattern",
	"url",
	"name",
] as const;

function pick(
	args: Record<string, unknown>,
	keys: readonly string[],
): string | null {
	for (const key of keys) {
		const value = args[key];
		if (typeof value === "string" && value.trim())
			return value.split("\n", 1)[0] ?? value;
	}
	return null;
}

interface McpProgress {
	progress: number;
	total: number | null;
	message: string | null;
}

function progressOf(partial: ToolRenderProps["partial"]): McpProgress | null {
	const details = detailsRecord(partial);
	const p = details && isRecord(details.progress) ? details.progress : null;
	const progress = num(p?.progress);
	if (progress === null) return null;
	const total = num(p?.total);
	return {
		progress,
		total: total !== null && total > 0 ? total : null,
		message: str(p?.message)?.trim() || null,
	};
}

/** `3/10`，没有总数时只写已完成数。 */
function amountText(p: McpProgress): string {
	return p.total === null ? `${p.progress}` : `${p.progress}/${p.total}`;
}

/** 读数里的进度文字：`Scanning (3/10)`，没有说明时只写数字（同 TUI）。 */
function progressText(p: McpProgress): string {
	if (!p.message) return amountText(p);
	return p.total === null ? p.message : `${p.message} (${amountText(p)})`;
}

/** 细进度条：有总数时按比例填充，没有总数时一段光标来回走（不确定进度）。 */
function ProgressBar({
	p,
	size,
}: {
	p: McpProgress;
	size: "sm" | "lg";
}): ReactNode {
	const ratio =
		p.total === null ? null : Math.max(0, Math.min(1, p.progress / p.total));
	return (
		<span
			className={`tv-mcp-bar tv-mcp-bar--${size}${ratio === null ? " tv-mcp-bar--indet" : ""}`}
			role="progressbar"
			aria-valuemin={0}
			aria-valuemax={p.total ?? undefined}
			aria-valuenow={p.progress}
			aria-valuetext={progressText(p)}
		>
			<i
				style={
					ratio === null
						? undefined
						: ({ "--tv-mcp-ratio": ratio } as CSSProperties)
				}
			/>
		</span>
	);
}

/** server / tool 在卡片头的名字里（ToolView 的 mcpOrigin），摘要只给主要参数。 */
function Summary({ args }: ToolRenderProps): ReactNode {
	const verb = pick(args, VERB_KEYS);
	const object = pick(args, OBJECT_KEYS);
	const main =
		verb || object
			? [verb, object].filter(Boolean).join(" ")
			: inlineArgs(args);
	return main ? <span>{truncate(normalizeWs(main), 120)}</span> : null;
}

/** 右侧读数：运行中是进度条与进度文字；结束后是 JSON 数组的项数、图片张数。 */
function Meta({ result, partial }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result) {
		const p = progressOf(partial);
		if (!p) return null;
		return (
			<>
				<ProgressBar p={p} size="sm" />
				<span className="tv-mcp-meta-text">
					{truncate(normalizeWs(progressText(p)), 60)}
				</span>
			</>
		);
	}
	if (result.isError) return null;
	const json = parseJsonText(resultTextOf(result));
	const images = resultImagesOf(result).length;
	return (
		<>
			{Array.isArray(json) && (
				<span>{t("toolRender.json.items", { count: json.length })}</span>
			)}
			{images > 0 && (
				<span>{t("toolRender.mcp.images", { count: images })}</span>
			)}
		</>
	);
}

/** 运行中的进度面板：说明文字在上，粗一点的进度条与百分比在下。 */
function ProgressPanel({ p }: { p: McpProgress }): ReactNode {
	const pct =
		p.total === null ? null : Math.round((p.progress / p.total) * 100);
	return (
		<div className="tv-mcp-progress">
			<div className="tv-mcp-progress-line">
				<span className="tv-mcp-progress-msg">{p.message ?? "…"}</span>
				<span className="tv-mcp-progress-num">
					{amountText(p)}
					{pct !== null && <span className="tv-faint"> · {pct}%</span>}
				</span>
			</div>
			<ProgressBar p={p} size="lg" />
		</div>
	);
}

function Result({ result, partial }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result) {
		const p = progressOf(partial);
		return p ? <ProgressPanel p={p} /> : null;
	}
	return (
		<SmartResult result={result} empty={t("toolRender.mcp.empty")} markdown />
	);
}

export const mcpRenderer: ToolRenderer = {
	Summary,
	Meta,
	Input: ArgsTree,
	Result,
	livePartial: true,
};
