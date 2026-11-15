/**
 * `checkpoint` / `rewind` — 调查检查点：checkpoint 记下调查目标，rewind 带着调查报告回到检查点
 * （检查点之后的上下文由报告替换）。结果形状见 oryn-core `tools/checkpoint.ts`：
 * checkpoint 的 details 为 `{ goal, startedAt }`，rewind 为 `{ report, rewound }`。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { formatDateTime } from "../../lib/format";
import { Badge, InvalidArg, Kv, KvGrid, ResultText } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import { detailsRecord, normalizeWs, str, truncate } from "../util";
import { CardMarkdown } from "./xdev-cards";

// 头部摘要会被 CSS 省略号截断（窄屏下几十个字符就放不下），展开体总是给出全文，不按字数猜头部是否装得下
/** Markdown 报告的一行摘要：跳过标题行，去掉强调、行内代码与列表记号。 */
function plainLine(markdown: string): string {
	const lines = markdown
		.split("\n")
		.map((l) => l.trim())
		.filter(Boolean);
	const line = lines.find((l) => !/^#{1,6}\s/.test(l)) ?? lines[0] ?? "";
	return line
		.replace(/^#{1,6}\s+|^(?:[-*+]|\d+\.)\s+/, "")
		.replace(/\*\*|__|`/g, "");
}
function CheckpointSummary({ args }: ToolRenderProps): ReactNode {
	const goal = str(args.goal);
	if (goal === null) return <InvalidArg what="goal" />;
	return <span className="tv-trunc">{truncate(normalizeWs(goal), 120)}</span>;
}

function CheckpointBody({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const details = detailsRecord(result);
	const goal = str(details?.goal) ?? str(args.goal);
	const started = Date.parse(str(details?.startedAt) ?? "");
	return (
		<>
			{goal && <div className="tv-prose">{goal}</div>}
			{Number.isFinite(started) && (
				<KvGrid>
					<Kv k={t("toolRender.checkpoint.started")}>
						{formatDateTime(started)}
					</Kv>
				</KvGrid>
			)}
			{result?.isError && <ResultText result={result} maxLines={6} />}
		</>
	);
}

export const checkpointRenderer: ToolRenderer = {
	Summary: CheckpointSummary,
	Body: CheckpointBody,
};

function RewindSummary({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const report = str(detailsRecord(result)?.report) ?? str(args.report);
	if (report === null) return <InvalidArg what="report" />;
	return (
		<>
			{detailsRecord(result)?.rewound === true && (
				<>
					<Badge tone="ok">{t("toolRender.checkpoint.rewound")}</Badge>{" "}
				</>
			)}
			<span className="tv-trunc">{truncate(plainLine(report), 120)}</span>
		</>
	);
}

function RewindBody({ args, result }: ToolRenderProps): ReactNode {
	const report = str(detailsRecord(result)?.report) ?? str(args.report);
	return (
		<>
			{report?.trim() && <CardMarkdown text={report.trim()} />}
			{result?.isError && <ResultText result={result} maxLines={6} />}
		</>
	);
}

export const rewindRenderer: ToolRenderer = {
	Summary: RewindSummary,
	Body: RewindBody,
};
