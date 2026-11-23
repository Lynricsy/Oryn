/**
 * `resolve` / `reject` / `propose` — 了结一个暂存的预览（应用 / 放弃），或提交计划等待批准。
 *
 * 在协作视图里它们是 `write xd://<device>` 调用，由 `ToolView` 拆开：卡片的数据在内层 details
 * （`result.details`，即 `ResolveDetails`）与设备名（`props.name`）上。旧的顶层 `resolve` 调用把
 * `action` / `reason` 放在 `args` 里，所以先读 details 再退回 args；两者都还没有（运行中）时按设备名定结局。
 *
 * 输入段是理由（引文样式）；输出段是结局横幅：应用（绿色对勾）/ 放弃（灰色）/ 提交计划，写明来源工具与
 * 预览的摘要，应用时列出改动的文件。头部读数是结局一词。结果形状见 oryn-core `tui/src/tools/resolve.ts`。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon, type IconName } from "../../components/ui/icons";
import {
	EmptyNote,
	ErrorBlock,
	FootNotes,
	Kv,
	KvGrid,
	PathText,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	isRecord,
	normalizeWs,
	num,
	resultTextOf,
	str,
	truncate,
} from "../util";

type ResolveKind = "apply" | "discard" | "propose";

const KIND_ICON: Record<ResolveKind, IconName> = {
	apply: "checkCircle",
	discard: "minusCircle",
	propose: "clipboard",
};

interface ResolveCard {
	kind: ResolveKind;
	reason: string | null;
	/** 暂存预览的来源工具（`ast_edit`）。 */
	sourceTool: string | null;
	/** 预览标签拆成来源说明与摘要：`AST Edit: 3 replacements in 2 files`。 */
	sourceLabel: string | null;
	summary: string | null;
	/** 应用后来源工具的 details 里的逐文件改动。 */
	files: { path: string; count: number | null }[];
	title: string | null;
	planFilePath: string | null;
	extra: Record<string, unknown> | null;
}

function cardModel({ name, args, result }: ToolRenderProps): ResolveCard {
	const details = detailsRecord(result);
	const explicit = str(details?.action) ?? str(args.action);
	const kind: ResolveKind =
		explicit === "apply" || explicit === "discard"
			? explicit
			: name === "propose"
				? "propose"
				: name === "reject"
					? "discard"
					: "apply";
	const label = str(details?.label) ?? str(args.label);
	const split = label ? /^(.+?)(?:: | -> )(.+)$/.exec(label) : null;
	const source =
		details && isRecord(details.sourceResultDetails)
			? details.sourceResultDetails
			: null;
	const files: ResolveCard["files"] = [];
	if (source && Array.isArray(source.fileReplacements))
		for (const f of source.fileReplacements) {
			const path = isRecord(f) ? str(f.path) : null;
			if (path) files.push({ path, count: isRecord(f) ? num(f.count) : null });
		}
	return {
		kind,
		reason: str(details?.reason) ?? str(args.reason),
		sourceTool: str(details?.sourceToolName) ?? str(args.sourceToolName),
		sourceLabel: split?.[1] ?? null,
		summary: split?.[2] ?? label,
		files,
		title: str(details?.title) ?? str(args.title),
		planFilePath: str(details?.planFilePath) ?? str(args.planFilePath),
		extra: isRecord(args.extra)
			? args.extra
			: details && isRecord(details.extra)
				? details.extra
				: null,
	};
}

function Summary(props: ToolRenderProps): ReactNode {
	const card = cardModel(props);
	const text =
		card.kind === "propose" ? card.title : (card.reason ?? card.summary);
	return text ? (
		<span className="tv-sum-intent">{truncate(normalizeWs(text), 100)}</span>
	) : null;
}

/** 头部读数：结局一词（应用为绿色）。 */
function Meta(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const { result } = props;
	if (!result || result.isError) return null;
	const card = cardModel(props);
	if (/^Nothing to reject/.test(resultTextOf(result).trim()))
		return <span>{t("toolRender.resolve.nothing")}</span>;
	return (
		<span className={card.kind === "apply" ? "tv-meta-ok" : undefined}>
			{t(`toolRender.resolve.meta.${card.kind}`)}
		</span>
	);
}

/** 输入段：理由 / 计划标题（引文样式），附加字段逐项列出。 */
function Input(props: ToolRenderProps): ReactNode {
	const card = cardModel(props);
	const text = card.kind === "propose" ? card.title : card.reason;
	const extra = card.extra ? Object.entries(card.extra) : [];
	return (
		<>
			{text && <blockquote className="tv-quote">{text}</blockquote>}
			{extra.length > 0 && (
				<KvGrid>
					{extra.map(([k, v]) => (
						<Kv key={k} k={k}>
							{truncate(
								normalizeWs(
									typeof v === "string" ? v : (JSON.stringify(v) ?? ""),
								),
								200,
							)}
						</Kv>
					))}
				</KvGrid>
			)}
		</>
	);
}

function Result(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const { result } = props;
	if (!result) return null;
	const text = resultTextOf(result).trim();
	if (result.isError) return <ErrorBlock text={text} />;
	if (/^Nothing to reject/.test(text) || /^No plan is awaiting/.test(text))
		return <EmptyNote>{text}</EmptyNote>;
	const card = cardModel(props);
	const tool = card.sourceTool ?? card.sourceLabel;
	const headline =
		card.kind === "propose"
			? t("toolRender.resolve.proposed")
			: tool
				? t(`toolRender.resolve.${card.kind}From`, { tool })
				: t(`toolRender.resolve.${card.kind}`);
	// 结果文本只是「已应用 / 已放弃」的复述时不再重复
	const echo =
		/^(?:Applied|Discarded)\b/.test(text) &&
		(card.files.length > 0 || card.summary !== null);
	const lines = echo
		? text
				.split("\n")
				.slice(1)
				.filter((l) => l.trim() && !/^\[[^\s\]]+\]$/.test(l.trim()))
		: [text];
	return (
		<>
			<div className={`tv-outcome tv-outcome--${card.kind}`}>
				<span className="tv-outcome-icon" aria-hidden="true">
					<Icon name={KIND_ICON[card.kind]} size={16} />
				</span>
				<div className="tv-outcome-body">
					<div className="tv-outcome-title">{headline}</div>
					{(card.summary || card.title) && (
						<div className="tv-outcome-sub">
							{card.kind === "propose" ? card.title : card.summary}
						</div>
					)}
				</div>
			</div>
			{card.files.length > 0 && (
				<div className="tv-lsp-files">
					{card.files.map((f) => (
						<div key={f.path} className="tv-lsp-file">
							<Icon name="file" size={13} />
							<PathText path={f.path} />
							{f.count !== null && (
								<span className="tv-lsp-file-aside">
									{t("toolRender.meta.replacements", { count: f.count })}
								</span>
							)}
						</div>
					))}
				</div>
			)}
			{card.kind === "propose" && card.planFilePath && (
				<KvGrid>
					<Kv k={t("toolRender.resolve.plan")}>
						<PathText path={card.planFilePath} />
					</Kv>
				</KvGrid>
			)}
			<FootNotes notes={lines.filter(Boolean)} />
		</>
	);
}

export const resolveRenderer: ToolRenderer = { Summary, Meta, Input, Result };
