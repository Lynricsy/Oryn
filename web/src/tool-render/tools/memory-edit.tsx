/**
 * `memory_edit` — 改写 Mnemopi 记忆：update（换内容 / 重要度）、forget（删除）、invalidate（作废并指向替代记忆）。
 * 结果形状见 oryn-core `tools/memory-edit.ts`：details 为 `{ status, bank?, store? }`，
 * status ∈ updated / deleted / invalidated / not_found / not_editable。
 * 输入段画成一张变更卡：update 是带写入标记的新内容 + 重要度条，forget 是划掉的记忆 id，invalidate 是「旧 → 替代」。
 */
import type { CSSProperties, ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/ui/icons";
import { ErrorBlock, InvalidArg, Note } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	normalizeWs,
	num,
	resultTextOf,
	str,
	truncate,
} from "../util";
import { MemoryCard } from "./memory-recall";

const OPS = ["update", "forget", "invalidate"] as const;
type Op = (typeof OPS)[number];

const STATUSES = [
	"updated",
	"deleted",
	"invalidated",
	"not_found",
	"not_editable",
] as const;
type Status = (typeof STATUSES)[number];

function opOf(args: Record<string, unknown>): Op | null {
	const op = str(args.op);
	return OPS.find((o) => o === op) ?? null;
}

function statusOf(props: ToolRenderProps): Status | null {
	const status = str(detailsRecord(props.result)?.status);
	return STATUSES.find((s) => s === status) ?? null;
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const op = opOf(args);
	const id = str(args.id);
	if (!op) return <InvalidArg what="op" />;
	const content = op === "update" ? str(args.content) : null;
	const replacement = op === "invalidate" ? str(args.replacement_id) : null;
	return (
		<>
			<span className="tv-memedit-op">
				{t(`toolRender.memoryEdit.op.${op}`)}
			</span>
			{id ? <span className="tv-path">{id}</span> : <InvalidArg what="id" />}
			{replacement && <span className="tv-muted">→ {replacement}</span>}
			{content && (
				<span className="tv-trunc tv-muted">
					{truncate(normalizeWs(content), 90)}
				</span>
			)}
		</>
	);
}

/** 右侧读数：编辑结果；找不到 / 只读事实用警告色。 */
function Meta(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const status = statusOf(props);
	if (!status) return null;
	const tone =
		status === "not_found" || status === "not_editable" ? "warn" : "ok";
	return (
		<span className={`tv-meta-${tone}`}>
			{t(`toolRender.memoryEdit.status.${status}`)}
		</span>
	);
}

/** 输入：变更卡。 */
function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const op = opOf(args);
	const content = str(args.content);
	const id = str(args.id);
	const replacement = str(args.replacement_id);
	const importanceRaw = num(args.importance);
	const importance =
		importanceRaw === null ? null : Math.max(0, Math.min(1, importanceRaw));
	if (!content && !id && !replacement && importance === null) return null;
	const idChip = id && <span className="tv-memedit-id">{id}</span>;
	if (op === "forget")
		return (
			<div className="tv-memedit tv-memedit--del">
				<Icon name="trash" size={12} />
				{idChip}
			</div>
		);
	if (op === "invalidate")
		return (
			<div className="tv-memedit">
				<span className="tv-memedit-id tv-memedit-id--old">{id ?? "?"}</span>
				<Icon name="arrowUpRight" size={12} />
				{replacement ? (
					<span className="tv-memedit-id">{replacement}</span>
				) : (
					<span className="tv-faint">
						{t("toolRender.memoryEdit.noReplacement")}
					</span>
				)}
			</div>
		);
	return (
		<>
			{content ? (
				<ul className="tv-mems">
					<MemoryCard added content={content} id={id} />
				</ul>
			) : (
				idChip && <div className="tv-memedit">{idChip}</div>
			)}
			{importance !== null && (
				<div className="tv-memedit-imp">
					<span className="tv-memedit-label">
						{t("toolRender.memoryEdit.importance")}
					</span>
					<span
						className="tv-memedit-imp-bar"
						style={{ "--tv-frac": importance } as CSSProperties}
						aria-hidden="true"
					/>
					<span className="tv-memedit-imp-val">{importance.toFixed(2)}</span>
				</div>
			)}
		</>
	);
}

/** 输出：落在哪个记忆库；找不到、只读与出错时给出工具原文（含下一步提示）。 */
function Result(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const { result } = props;
	if (!result) return null;
	const text = resultTextOf(result).trim();
	if (result.isError) return <ErrorBlock text={text} />;
	const details = detailsRecord(result);
	const bank = str(details?.bank);
	const store = str(details?.store);
	const status = statusOf(props);
	const success =
		status === "updated" || status === "deleted" || status === "invalidated";
	return (
		<>
			{bank && (
				<div className="tv-memedit-bank">
					<Icon name="database" size={12} />
					<span>{t("toolRender.memoryEdit.bank")}</span>
					<span className="tv-path">{store ? `${bank} · ${store}` : bank}</span>
				</div>
			)}
			{!success && text && <Note tone="warn">{text}</Note>}
		</>
	);
}

export const memoryEditRenderer: ToolRenderer = {
	Summary,
	Meta,
	Input,
	Result,
};
