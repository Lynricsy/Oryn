/** `report_tool_issue` — automated QA grievance: which tool misbehaved + how. */
import type { ReactNode } from "react";
import { Badge, InvalidArg, Note, ResultText } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import { normalizeWs, str, truncate } from "../util";

function Summary({ args }: ToolRenderProps): ReactNode {
	const tool = str(args.tool);
	const report = str(args.report);
	if (!tool && !report) return <InvalidArg what="report" />;
	return (
		<span>
			{tool && <Badge tone="warn">{tool}</Badge>}
			{report && <span> {truncate(normalizeWs(report), 80)}</span>}
		</span>
	);
}

function Input({ args }: ToolRenderProps): ReactNode {
	const report = str(args.report);
	// 折叠行只是摘要（会被截断），展开体始终给出完整报告
	return report ? <Note tone="warn">{report}</Note> : null;
}

function Result({ result }: ToolRenderProps): ReactNode {
	return <ResultText result={result} maxLines={4} />;
}

export const reportToolIssueRenderer: ToolRenderer = {
	Summary,
	Input,
	Result,
};
