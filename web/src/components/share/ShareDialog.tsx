/**
 * 线程公开分享对话框（M5 计划 5.3）：已有链接（复制 / 打开 / 两步撤销）+ 新建（快照 / 实时）。
 * 创建前服务端扫描公开范围内的疑似密钥：有命中时列出（规则、所在消息、脱敏预览、次数），
 * 每项可「定位」：关闭对话框并把转录滚到那条消息（工具输出则标出对应工具卡），预览保持脱敏；
 * 标题与附件名里的命中写成「标题」/「附件 <名>」（不对应转录条目，没有「定位」）；
 * 勾选「我已检查」后带 `acknowledgeFindings` 重发。新链接出现在列表顶部并尝试自动复制：
 * 只有剪贴板写入成功才说「已复制」，失败时保留完整链接供手动复制。
 */
import { useQueryClient } from "@tanstack/react-query";
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
import { copyToClipboard } from "../../lib/clipboard";
import { revealEntry } from "../transcript/reveal";
import { CopyButton } from "../ui/CopyButton";
import { Icon } from "../ui/icons";
import { ManualCopy } from "../ui/ManualCopy";
import { Button, Dialog, ErrorText, Radio, Spinner } from "../ui/primitives";
import { ConfirmButton } from "./ConfirmButton";
import { ScanFindings } from "./ScanFindings";
import { ShareModeBadge, shortShareUrl } from "./share-url";

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
					<p className="text-faint text-xs" data-testid="share-identity-hint">
						{t("share.identityHint")}
					</p>
					<p className="text-faint text-xs">{t("share.dialog.scope")}</p>
				</section>
				{scan && (
					<ScanFindings
						source={{ kind: "thread", threadId, onLocate: locate }}
						scan={scan}
						ack={ack}
						onAck={setAck}
					/>
				)}
				<ErrorText error={create.error} />
			</div>
		</Dialog>
	);
}
