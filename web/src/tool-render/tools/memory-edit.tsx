/**
 * `memory_edit` — 改写 Mnemopi 记忆：update（换内容 / 重要度）、forget（删除）、invalidate（作废并指向替代记忆）。
 * 结果形状见 oryn-core `tools/memory-edit.ts`：details 为 `{ status, bank?, store? }`，
 * status ∈ updated / deleted / invalidated / not_found / not_editable。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Badge, InvalidArg, Kv, KvGrid, ResultText } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import { detailsRecord, normalizeWs, num, str, truncate } from "../util";

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
			<Badge>{t(`toolRender.memoryEdit.op.${op}`)}</Badge>{" "}
			{id ? <span className="tv-path">{id}</span> : <InvalidArg what="id" />}
			{replacement && <span className="tv-muted"> → {replacement}</span>}
			{content && (
				<span className="tv-trunc"> {truncate(normalizeWs(content), 90)}</span>
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

/** 输入：头部会被省略号截断，id、替代记忆、重要度与内容在这里完整给出。 */
function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const content = str(args.content);
	const id = str(args.id);
	const replacement = str(args.replacement_id);
	const importance = num(args.importance);
	if (!content && !id && !replacement && importance === null) return null;
	return (
		<>
			{content && <div className="tv-prose">{content}</div>}
			{(id || replacement || importance !== null) && (
				<KvGrid>
					<Kv k={t("toolRender.memoryEdit.id")}>
						{id && <span className="tv-path">{id}</span>}
					</Kv>
					<Kv k={t("toolRender.memoryEdit.replacement")}>
						{replacement && <span className="tv-path">{replacement}</span>}
					</Kv>
					<Kv k={t("toolRender.memoryEdit.importance")}>
						{importance === null ? null : importance.toFixed(2)}
					</Kv>
				</KvGrid>
			)}
		</>
	);
}

/** 输出：落在哪个记忆库；成功时读数已说明结果，找不到、只读与出错时给出工具原文（含下一步提示）。 */
function Result(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const details = detailsRecord(props.result);
	const bank = str(details?.bank);
	const store = str(details?.store);
	const where = bank && (store ? `${bank} · ${store}` : bank);
	const status = statusOf(props);
	const success =
		status === "updated" || status === "deleted" || status === "invalidated";
	return (
		<>
			{where && (
				<KvGrid>
					<Kv k={t("toolRender.memoryEdit.bank")}>{where}</Kv>
				</KvGrid>
			)}
			{!success && <ResultText result={props.result} maxLines={6} />}
		</>
	);
}

export const memoryEditRenderer: ToolRenderer = {
	Summary,
	Meta,
	Input,
	Result,
};
