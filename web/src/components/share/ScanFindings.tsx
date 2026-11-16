/**
 * 密钥扫描的命中清单与「我已检查」确认（409 `secrets_found` 之后显示）。线程分享对话框与 artifact 的发布弹层共用：
 * - 线程（`source.kind = "thread"`）：命中落在转录条目上，可定位到所在消息；标题与附件名的命中单独标出。
 * - 文件（`source.kind = "files"`）：artifact 发布前扫描版本里的文本文件，`entryId` 是文件路径。
 */
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import type { ShareFinding, ShareScan } from "../../api/share-queries";
import { listThreadAttachments } from "../../lib/attachments";
import { Badge, Callout } from "../ui/data";
import { Button, Checkbox } from "../ui/primitives";

/** 命中来自哪里：线程转录（可定位）或一组文件。 */
export type FindingSource =
	| {
			kind: "thread";
			threadId: string;
			/** 定位到所在消息；找不到返回 false */
			onLocate: (f: ShareFinding) => boolean;
	  }
	| { kind: "files" };

/** 命中所在消息的角色文案（未知角色原样显示）。 */
function RoleText({ role }: { role?: string }): ReactNode {
	const { t } = useTranslation();
	if (!role) return null;
	if (role === "user" || role === "assistant" || role === "toolResult")
		return <span>{t(`share.role.${role}`)}</span>;
	return <span>{role}</span>;
}

/** 标题与附件名的命中不对应任何条目（seq 为 0）：entryId 为 `__title__` / `attachment:<附件 id>`。 */
const TITLE_FINDING = "__title__";
const ATTACHMENT_FINDING = "attachment:";

function FindingRow({
	f,
	source,
	attachmentName,
}: {
	f: ShareFinding;
	source: FindingSource;
	/** 附件名命中：附件 id → 名称（还没取到时退回 id） */
	attachmentName: (id: string) => string;
}): ReactNode {
	const { t } = useTranslation();
	const [missing, setMissing] = useState(false);
	const thread = source.kind === "thread" ? source : null;
	const inTitle = thread !== null && f.entryId === TITLE_FINDING;
	const attachmentId =
		thread !== null && f.entryId.startsWith(ATTACHMENT_FINDING)
			? f.entryId.slice(ATTACHMENT_FINDING.length)
			: null;
	const inEntry = thread !== null && !inTitle && attachmentId === null;
	return (
		<li className="flex items-start gap-2.5 px-3 py-2 text-xs">
			<span className="mt-px shrink-0">
				<Badge tone="warn">{t(`share.rule.${f.rule}`)}</Badge>
			</span>
			<span className="min-w-0 flex-1">
				<code className="block truncate font-mono text-fg">{f.preview}</code>
				<span className="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-faint">
					{thread === null && (
						<span className="min-w-0 truncate font-mono" title={f.entryId}>
							{f.entryId}
						</span>
					)}
					{inTitle && <span>{t("share.findings.inTitle")}</span>}
					{attachmentId !== null && (
						<span className="min-w-0 truncate" title={attachmentId}>
							{t("share.findings.inAttachment", {
								name: attachmentName(attachmentId),
							})}
						</span>
					)}
					{/* 所在消息：条目序号 + entryId，长转录里同角色同工具的多条输出靠它区分 */}
					{inEntry && (
						<>
							<span className="tabular-nums" title={f.entryId}>
								{t("share.findings.where", { seq: f.seq })}
							</span>
							<span className="font-mono">{f.entryId.slice(0, 8)}</span>
						</>
					)}
					<RoleText role={f.role} />
					{f.tool && <span className="font-mono">{f.tool}</span>}
					{f.secretName && (
						<span>
							{t("share.findings.secretName", { name: f.secretName })}
						</span>
					)}
				</span>
				{missing && (
					<span className="mt-0.5 block text-muted">
						{t("share.findings.notShown")}
					</span>
				)}
			</span>
			<span className="flex shrink-0 flex-col items-end gap-0.5">
				<span className="text-faint tabular-nums">
					{t("share.findings.count", { n: f.count })}
				</span>
				{inEntry && (
					<Button
						size="sm"
						variant="ghost"
						icon="locate"
						title={t("share.findings.locateTitle")}
						onClick={() => setMissing(!thread.onLocate(f))}
					>
						{t("share.findings.locate")}
					</Button>
				)}
			</span>
		</li>
	);
}

export function ScanFindings({
	source,
	scan,
	ack,
	onAck,
	hint,
}: {
	source: FindingSource;
	scan: ShareScan;
	ack: boolean;
	onAck: (v: boolean) => void;
	/** 换掉提示正文（缺省是线程分享的说法） */
	hint?: string;
}): ReactNode {
	const { t } = useTranslation();
	const threadId = source.kind === "thread" ? source.threadId : "";
	// 附件名命中只带附件 id：有这类命中时才取线程的附件列表换成名称（与附件对话框同一个查询）
	const attachments = useQuery({
		queryKey: ["attachments", threadId],
		queryFn: () => listThreadAttachments(threadId),
		enabled:
			source.kind === "thread" &&
			scan.findings.some((f) => f.entryId.startsWith(ATTACHMENT_FINDING)),
	});
	const attachmentName = (id: string) =>
		attachments.data?.find((a) => a.id === id)?.name ?? id;
	const total = scan.findings.reduce((n, f) => n + f.count, 0);
	return (
		<section className="flex flex-col gap-2" data-testid="share-findings">
			<Callout
				tone="warn"
				role="alert"
				title={t("share.findings.title", { n: total })}
			>
				{hint ?? t("share.findings.hint")}
			</Callout>
			<ul className="max-h-56 divide-y divide-border overflow-y-auto rounded-md border border-border">
				{scan.findings.map((f) => (
					<FindingRow
						key={`${f.rule}:${f.entryId}:${f.seq}:${f.secretName ?? ""}`}
						f={f}
						source={source}
						attachmentName={attachmentName}
					/>
				))}
			</ul>
			{scan.truncated && (
				<p className="text-faint text-xs">{t("share.findings.truncated")}</p>
			)}
			{/* biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Checkbox 组件（内含原生 input） */}
			<label className="flex cursor-pointer items-center gap-2 text-sm">
				<Checkbox checked={ack} onChange={(e) => onAck(e.target.checked)} />
				{t("share.findings.ack")}
			</label>
		</section>
	);
}
