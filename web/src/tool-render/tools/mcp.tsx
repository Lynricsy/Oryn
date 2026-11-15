/**
 * MCP 工具（`mcp__<server>_<tool>`，顶层调用或 `write xd://mcp__…` 派发）：
 * 摘要给出 server / tool 与主要参数，读数显示运行中的进度，展开体给参数、图片与文本结果。
 * 结果形状见 oryn-core `mcp/tool-bridge.ts`：details 带 `serverName` / `mcpToolName`，
 * 部分结果带 `progress: { progress, total?, message? }`；文本结果是 JSON 时按 JSON 显示，否则按 Markdown（同 TUI）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Output, ResultImages, ResultText } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	argsDigest,
	detailsRecord,
	isRecord,
	normalizeWs,
	num,
	resultTextOf,
	str,
	truncate,
} from "../util";
import { CardMarkdown } from "./xdev-cards";

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

function progressText(partial: ToolRenderProps["partial"]): string | null {
	const details = detailsRecord(partial);
	const p = details && isRecord(details.progress) ? details.progress : null;
	const progress = num(p?.progress);
	if (progress === null) return null;
	const total = num(p?.total);
	const amount = total === null ? `${progress}` : `${progress}/${total}`;
	const message = str(p?.message)?.trim();
	if (!message) return amount;
	return total === null ? message : `${message} (${amount})`;
}

/** 参数的一行预览：标量写成 `key=value`，嵌套值写成紧凑 JSON（同 TUI 的 formatArgsInline）。 */
function inlineArgs(args: Record<string, unknown>): string {
	const parts: string[] = [];
	for (const key in args) {
		const value = args[key];
		if (value === undefined || value === null) continue;
		parts.push(
			`${key}=${typeof value === "object" ? argsDigest(value, 40) : String(value)}`,
		);
	}
	return parts.join(" ");
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

/** 右侧读数：运行中的 MCP 进度。 */
function Meta({ result, partial }: ToolRenderProps): ReactNode {
	const text = result ? null : progressText(partial);
	return text ? <span>{truncate(normalizeWs(text), 60)}</span> : null;
}

function jsonText(text: string): string | null {
	const trimmed = text.trim();
	if (!trimmed.startsWith("{") && !trimmed.startsWith("[")) return null;
	try {
		return JSON.stringify(JSON.parse(trimmed), null, 2);
	} catch {
		return null;
	}
}

function ResultBody({ result }: Pick<ToolRenderProps, "result">): ReactNode {
	const text = resultTextOf(result).trim();
	if (!text) return null;
	if (result?.isError) return <ResultText result={result} maxLines={12} />;
	const json = jsonText(text);
	if (json !== null)
		return <Output text={json} lang="json" variant="code" maxLines={16} />;
	return <CardMarkdown text={text} />;
}

function Body({ args, result, partial }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	let argText = "";
	if (Object.keys(args).length > 0) {
		try {
			argText = JSON.stringify(args, null, 2) ?? "";
		} catch {
			argText = String(args);
		}
	}
	const progress = result ? null : progressText(partial);
	return (
		<>
			{argText && (
				<Output
					text={argText}
					lang="json"
					variant="code"
					maxLines={12}
					title={t("toolRender.args")}
				/>
			)}
			{progress && <div className="tv-prose">{progress}</div>}
			<ResultImages result={result} />
			<ResultBody result={result} />
		</>
	);
}

export const mcpRenderer: ToolRenderer = {
	Summary,
	Meta,
	Body,
	livePartial: true,
};
