/**
 * 线程公开分享对话框（M5 计划 5.3）：已有链接（复制 / 打开 / 两步撤销）+ 新建（快照 / 实时）。
 * 创建前服务端扫描公开范围内的疑似密钥：有命中时列出（规则、所在消息、脱敏预览、次数），
 * 每项可「定位」：关闭对话框并把转录滚到那条消息（工具输出则标出对应工具卡），预览保持脱敏；
 * 标题与附件名里的命中写成「标题」/「附件 <名>」（不对应转录条目，没有「定位」）；
 * 勾选「我已检查」后带 `acknowledgeFindings` 重发。新链接出现在列表顶部并尝试自动复制：
 * 只有剪贴板写入成功才说「已复制」，失败时保留完整链接供手动复制。
 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type ShareFinding,
	type ShareMode,
	type ShareScan,
	shareQk,
	type ThreadShare,
	useCreateShare,
	useRevokeShare,
	useThreadShares,
} from "../../api/share-queries";
import { listThreadAttachments } from "../../lib/attachments";
import { copyToClipboard } from "../../lib/clipboard";
import { revealEntry } from "../transcript/reveal";
import { CopyButton } from "../ui/CopyButton";
import { Badge, Callout } from "../ui/data";
import { Icon } from "../ui/icons";
import { ManualCopy } from "../ui/ManualCopy";
import {
	Button,
	Checkbox,
	Dialog,
	ErrorText,
	Radio,
	Spinner,
} from "../ui/primitives";
import { ConfirmButton } from "./ConfirmButton";
import { ShareModeBadge, shortShareUrl } from "./share-url";

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
	attachmentName,
	onLocate,
}: {
	f: ShareFinding;
	/** 附件名命中：附件 id → 名称（还没取到时退回 id） */
	attachmentName: (id: string) => string;
	/** 定位到所在消息；找不到返回 false */
	onLocate: (f: ShareFinding) => boolean;
}): ReactNode {
	const { t } = useTranslation();
	const [missing, setMissing] = useState(false);
	const inTitle = f.entryId === TITLE_FINDING;
	const attachmentId = f.entryId.startsWith(ATTACHMENT_FINDING)
		? f.entryId.slice(ATTACHMENT_FINDING.length)
		: null;
	const inEntry = !inTitle && attachmentId === null;
	return (
		<li className="flex items-start gap-2.5 px-3 py-2 text-xs">
			<span className="mt-px shrink-0">
				<Badge tone="warn">{t(`share.rule.${f.rule}`)}</Badge>
			</span>
			<span className="min-w-0 flex-1">
				<code className="block truncate font-mono text-fg">{f.preview}</code>
				<span className="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-faint">
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
						onClick={() => setMissing(!onLocate(f))}
					>
						{t("share.findings.locate")}
					</Button>
				)}
			</span>
		</li>
	);
}

function Findings({
	threadId,
	scan,
	ack,
	onAck,
	onLocate,
}: {
	threadId: string;
	scan: ShareScan;
	ack: boolean;
	onAck: (v: boolean) => void;
	onLocate: (f: ShareFinding) => boolean;
}): ReactNode {
	const { t } = useTranslation();
	// 附件名命中只带附件 id：有这类命中时才取线程的附件列表换成名称（与附件对话框同一个查询）
	const attachments = useQuery({
		queryKey: ["attachments", threadId],
		queryFn: () => listThreadAttachments(threadId),
		enabled: scan.findings.some((f) =>
			f.entryId.startsWith(ATTACHMENT_FINDING),
		),
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
				{t("share.findings.hint")}
			</Callout>
			<ul className="max-h-56 divide-y divide-border overflow-y-auto rounded-md border border-border">
				{scan.findings.map((f) => (
					<FindingRow
						key={`${f.rule}:${f.entryId}:${f.seq}:${f.secretName ?? ""}`}
						f={f}
						attachmentName={attachmentName}
						onLocate={onLocate}
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

/** 刚创建的链接及其自动复制的结果（pending = 剪贴板写入还没落定）。 */
interface Fresh {
	id: string;
	copy: "pending" | "copied" | "failed";
}

function ShareRow({
	share,
	fresh,
}: {
	share: ThreadShare;
	fresh: Fresh["copy"] | null;
}): ReactNode {
	const { t } = useTranslation();
	const revoke = useRevokeShare();
	return (
		<li className="flex flex-col gap-1 py-2">
			<div className="flex min-w-0 items-center gap-2">
				{/* 窄屏放不下时 URL 折到徽标下一行，不把两者都挤成省略号 */}
				<span className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-0.5">
					<ShareModeBadge mode={share.mode} createdAt={share.createdAt} />
					<span
						className={`min-w-0 max-w-full truncate font-mono text-xs ${fresh !== null ? "text-fg" : "text-muted"}`}
						title={share.url}
					>
						{shortShareUrl(share.url)}
					</span>
				</span>
				<CopyButton text={share.url} ariaLabel={t("share.page.copyLink")} />
				<a
					href={share.url}
					target="_blank"
					rel="noopener"
					aria-label={t("share.dialog.open")}
					title={t("share.dialog.open")}
					className="inline-grid h-6 w-6 shrink-0 place-items-center rounded-sm text-faint transition-colors duration-150 hover:bg-hover hover:text-fg"
				>
					<Icon name="external" size={13} aria-hidden />
				</a>
				<ConfirmButton
					label={t("share.dialog.revoke")}
					confirmLabel={t("share.dialog.revokeConfirm")}
					disabled={revoke.isPending}
					onConfirm={() => revoke.mutate(share.id)}
				/>
			</div>
			{fresh !== null && (
				<p
					role="status"
					className="flex items-center gap-1 text-ok-ink text-xs"
				>
					<Icon name="check" size={12} aria-hidden />
					{fresh === "copied"
						? t("share.dialog.createdCopied")
						: t("share.dialog.created")}
				</p>
			)}
			{fresh === "failed" && (
				<ManualCopy
					text={share.url}
					label={t("publish.urlLabel")}
					message={t("publish.copyFailed")}
				/>
			)}
			<ErrorText error={revoke.error} />
		</li>
	);
}

export function ShareDialog({
	threadId,
	onClose,
}: {
	threadId: string;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const shares = useThreadShares(threadId);
	const create = useCreateShare(threadId);
	const [mode, setMode] = useState<ShareMode>("snapshot");
	const [scan, setScan] = useState<ShareScan | null>(null);
	const [ack, setAck] = useState(false);
	const [fresh, setFresh] = useState<Fresh | null>(null);
	const list = shares.data?.shares ?? [];

	const submit = async () => {
		const r = await create.mutateAsync({
			mode,
			acknowledgeFindings: scan !== null && ack,
		});
		if (r.kind === "findings") {
			setScan(r.scan);
			setAck(false);
			return;
		}
		setScan(null);
		setAck(false);
		const id = r.share.id;
		setFresh({ id, copy: "pending" });
		// 新链接立即放到列表顶部（随后的重新拉取按创建时间倒序，结果一致）
		qc.setQueryData(
			shareQk.thread(threadId),
			(old: { shares: ThreadShare[] } | undefined) => ({
				shares: [
					r.share,
					...(old?.shares ?? []).filter((s) => s.id !== r.share.id),
				],
			}),
		);
		const copied = await copyToClipboard(r.share.url);
		setFresh((f) =>
			f?.id === id ? { id, copy: copied ? "copied" : "failed" } : f,
		);
	};

	const locate = (f: ShareFinding) => {
		const found = revealEntry(threadId, { entryId: f.entryId, seq: f.seq });
		if (found) onClose();
		return found;
	};

	return (
		<Dialog
			open
			onClose={onClose}
			title={t("share.dialog.title")}
			footer={
				<>
					<Button onClick={onClose}>{t("common.close")}</Button>
					<Button
						variant="primary"
						icon="link"
						disabled={create.isPending || (scan !== null && !ack)}
						onClick={() => void submit().catch(() => {})}
					>
						{scan ? t("share.findings.createAnyway") : t("share.dialog.create")}
					</Button>
				</>
			}
		>
			<div className="flex flex-col gap-4">
				<section aria-label={t("share.dialog.existing")}>
					<h3 className="mb-1 font-medium text-muted text-xs">
						{t("share.dialog.existing")}
					</h3>
					{shares.isPending && <Spinner />}
					<ErrorText error={shares.error} />
					{shares.isSuccess && list.length === 0 && (
						<p className="py-1 text-faint text-sm">{t("share.dialog.none")}</p>
					)}
					{list.length > 0 && (
						<ul className="divide-y divide-border">
							{list.map((s) => (
								<ShareRow
									key={s.id}
									share={s}
									fresh={s.id === fresh?.id ? fresh.copy : null}
								/>
							))}
						</ul>
					)}
				</section>
				<section className="flex flex-col gap-2">
					<h3 className="font-medium text-muted text-xs">
						{t("share.dialog.newTitle")}
					</h3>
					<div className="grid gap-2 sm:grid-cols-2">
						{(["snapshot", "live"] as const).map((v) => (
							// biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Radio 组件（内含原生 input）
							<label
								key={v}
								className={`flex cursor-pointer gap-3 rounded-lg border p-3 transition-colors duration-150 ${mode === v ? "border-accent bg-accent-soft" : "border-border bg-panel/60 hover:bg-hover"}`}
							>
								<Radio
									name="share-mode"
									checked={mode === v}
									onChange={() => setMode(v)}
									className="mt-0.5"
								/>
								<span className="min-w-0">
									<span className="flex items-center gap-1.5 font-medium text-sm">
										<Icon
											name={v === "snapshot" ? "camera" : "broadcast"}
											size={14}
											aria-hidden
											className="text-faint"
										/>
										{t(`share.mode.${v}`)}
									</span>
									<span className="block text-muted text-xs">
										{t(`share.dialog.${v}Hint`)}
									</span>
								</span>
							</label>
						))}
					</div>
					<p className="text-faint text-xs">{t("share.dialog.scope")}</p>
				</section>
				{scan && (
					<Findings
						threadId={threadId}
						scan={scan}
						ack={ack}
						onAck={setAck}
						onLocate={locate}
					/>
				)}
				<ErrorText error={create.error} />
			</div>
		</Dialog>
	);
}
