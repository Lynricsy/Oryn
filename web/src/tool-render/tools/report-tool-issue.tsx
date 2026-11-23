/**
 * `report_tool_issue` / `xd://report_issue` — automated QA grievance: which tool misbehaved + how.
 * 新版经设备写入，参数只有一段 `report` 文本（`<tool>: <描述>`、首行工具名 + 正文，或 JSON），
 * 拆法同 oryn-core `tools/report-tool-issue.ts` 的 `parseReportIssueBody`；旧版直接给 `tool` + `report`。
 * 卡片画成一张问题单：被报告的工具（等宽）+ 描述全文。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/ui/icons";
import { ErrorBlock, InvalidArg, ResultText } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import { isRecord, normalizeWs, resultTextOf, str, truncate } from "../util";

const JSON_TOOL_KEYS = ["tool", "toolName", "tool_name", "name"] as const;
const JSON_REPORT_KEYS = [
	"report",
	"description",
	"issue",
	"error",
	"message",
	"problem",
	"details",
	"body",
] as const;

function firstString(
	record: Record<string, unknown>,
	keys: readonly string[],
): string | null {
	for (const key of keys) {
		const value = str(record[key])?.trim();
		if (value) return value;
	}
	return null;
}

/** 拆出被报告的工具与描述；拆不出工具时 tool 为 null、描述为原文。 */
function parseIssue(args: Record<string, unknown>): {
	tool: string | null;
	report: string;
} {
	const tool = str(args.tool)?.trim() || null;
	const body = (str(args.report) ?? "").trim();
	if (tool) return { tool, report: body };
	if (body.startsWith("{")) {
		try {
			const parsed: unknown = JSON.parse(body);
			if (isRecord(parsed)) {
				const t = firstString(parsed, JSON_TOOL_KEYS);
				const r = firstString(parsed, JSON_REPORT_KEYS);
				if (t && r) return { tool: t, report: r };
			}
		} catch {
			// 不是 JSON：按文本拆
		}
	}
	// 与 Core 一致：多行时首行是工具名、其余是描述（Core 就是这样记录的）
	const nl = body.indexOf("\n");
	if (nl > 0) {
		const head = body.slice(0, nl).trim();
		const rest = body.slice(nl + 1).trim();
		if (head && rest) return { tool: head, report: rest };
	}
	// 只认冒号后跟空白：URI（`xd://tui`）留在工具名里
	const colon = body.search(/:\s/);
	if (colon > 0) {
		const t = body.slice(0, colon).trim();
		const r = body.slice(colon + 1).trim();
		if (t && r) return { tool: t, report: r };
	}
	return { tool: null, report: body };
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const { tool, report } = parseIssue(args);
	if (!tool && !report) return <InvalidArg what="report" />;
	return (
		<>
			{tool && <span className="tv-issue-tool">{tool}</span>}
			{report && (
				<span className="tv-muted">{truncate(normalizeWs(report), 80)}</span>
			)}
		</>
	);
}

/** 输入：问题单（被报告的工具 + 描述全文）。 */
function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const { tool, report } = parseIssue(args);
	if (!tool && !report) return null;
	return (
		<div className="tv-issue">
			<div className="tv-issue-head">
				<Icon name="bug" size={12} />
				<span className="tv-issue-label">
					{t("toolRender.reportIssue.title")}
				</span>
				{tool && <span className="tv-issue-tool">{tool}</span>}
			</div>
			{report && <div className="tv-issue-body">{report}</div>}
		</div>
	);
}

/** 输出：出错时给错误；成功时只有一句致谢，状态勾已说明，不再占一段。 */
function Result({ result }: ToolRenderProps): ReactNode {
	if (!result) return null;
	if (result.isError) return <ErrorBlock text={resultTextOf(result)} />;
	const text = resultTextOf(result).trim();
	return text && !/^(?:noted, thanks!?|reported\.?)$/i.test(text) ? (
		<ResultText result={result} maxLines={4} />
	) : null;
}

export const reportToolIssueRenderer: ToolRenderer = {
	Summary,
	Input,
	Result,
};
