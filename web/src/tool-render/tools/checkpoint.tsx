/**
 * `checkpoint` / `rewind` — 调查检查点：checkpoint 记下调查目标，rewind 带着调查报告回到检查点
 * （检查点之后的上下文由报告替换）。结果形状见 oryn-core `tools/checkpoint.ts`：
 * checkpoint 的 details 为 `{ goal, startedAt }`，rewind 为 `{ report, rewound }`。
 * 两张卡是一对书签：checkpoint 画成插下的旗标（目标 + 时刻），rewind 画成带回的报告（Markdown，可折叠）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/ui/icons";
import { formatDateTime } from "../../lib/format";
import { ErrorBlock, InvalidArg, MarkdownBlock, Note } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	normalizeWs,
	resultTextOf,
	str,
	truncate,
} from "../util";

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

/** 开始时刻（details.startedAt，ISO）；不合法时为 null。 */
function startedOf(props: ToolRenderProps): number | null {
	const ms = Date.parse(str(detailsRecord(props.result)?.startedAt) ?? "");
	return Number.isFinite(ms) ? ms : null;
}

function CheckpointSummary({ args }: ToolRenderProps): ReactNode {
	const goal = str(args.goal);
	if (goal === null) return <InvalidArg what="goal" />;
	return <span className="tv-trunc">{truncate(normalizeWs(goal), 120)}</span>;
}

/** 右侧读数：插旗的时刻（只写时分，完整日期在悬停提示里）。 */
function CheckpointMeta(props: ToolRenderProps): ReactNode {
	const started = startedOf(props);
	if (started === null) return null;
	return (
		<span title={formatDateTime(started)}>
			{new Date(started).toLocaleTimeString([], {
				hour: "2-digit",
				minute: "2-digit",
			})}
		</span>
	);
}

/** 输入：调查目标，画成一面旗标。 */
function CheckpointInput({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const goal = str(detailsRecord(result)?.goal) ?? str(args.goal);
	if (!goal?.trim()) return null;
	return (
		<div className="tv-flag">
			<span className="tv-flag-icon" aria-hidden="true">
				<Icon name="snapshot" size={13} />
			</span>
			<div className="tv-flag-body">
				<div className="tv-flag-label">{t("toolRender.checkpoint.goal")}</div>
				<div className="tv-flag-text">{goal.trim()}</div>
			</div>
		</div>
	);
}

/** 输出：检查点已设立（含完整开始时间）；出错时给错误。 */
function CheckpointResult(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (props.result?.isError)
		return <ErrorBlock text={resultTextOf(props.result)} />;
	const started = startedOf(props);
	if (started === null) return null;
	return (
		<div className="tv-flag-note">
			{t("toolRender.checkpoint.set")}
			<span className="tv-flag-time">{formatDateTime(started)}</span>
		</div>
	);
}

export const checkpointRenderer: ToolRenderer = {
	Summary: CheckpointSummary,
	Meta: CheckpointMeta,
	Input: CheckpointInput,
	Result: CheckpointResult,
};

/** 报告：结果里 Core 存下的（已 trim）优先，空串时退回参数。 */
function rewindReport(props: ToolRenderProps): string | null {
	return str(detailsRecord(props.result)?.report) || str(props.args.report);
}

function RewindSummary(props: ToolRenderProps): ReactNode {
	const report = rewindReport(props);
	if (report === null) return <InvalidArg what="report" />;
	return <span className="tv-trunc">{truncate(plainLine(report), 120)}</span>;
}

/** 右侧读数：已回退（绿）。 */
function RewindMeta({ result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	return detailsRecord(result)?.rewound === true ? (
		<span className="tv-meta-ok">{t("toolRender.checkpoint.rewound")}</span>
	) : null;
}

/** 输入：带回的调查报告（Markdown，超高折叠）。 */
function RewindInput(props: ToolRenderProps): ReactNode {
	const text = rewindReport(props)?.trim();
	return text ? <MarkdownBlock text={text} maxHeight={320} /> : null;
}

/** 输出：上下文已回到检查点、由报告替换；出错时给错误。 */
function RewindResult(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (props.result?.isError)
		return <ErrorBlock text={resultTextOf(props.result)} />;
	return detailsRecord(props.result)?.rewound === true ? (
		<Note>{t("toolRender.checkpoint.rewoundNote")}</Note>
	) : null;
}

export const rewindRenderer: ToolRenderer = {
	Summary: RewindSummary,
	Meta: RewindMeta,
	Input: RewindInput,
	Result: RewindResult,
};
