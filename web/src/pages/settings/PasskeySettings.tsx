/**
 * 设置 → 账户与设备里的「Passkey」「恢复码」两组（default export，由 AccountSettings 放在头像与设备之间）。
 * 只给网页会话（含 App 会话）显示：设备令牌、恢复会话调不了这些接口。
 *
 * 新增 / 删除 Passkey、重新生成恢复码要求 5 分钟内做过一次 Passkey 验证。缓存里的有效期不够时先弹一次确认，
 * 用户点「用 Passkey 验证」后才发起系统提示——每次系统提示之前都有一次点击、中间只隔一次请求（与登录页相同），
 * Safari 不会因缺少用户手势而拒绝。所以不做「操作失败后自动补验证再重试」。
 * 桌面壳与移动壳的 WebView 里没有可用的 Passkey：隐藏这三项操作，引导到系统浏览器（壳把 `target="_blank"` 交给系统）。
 * 用户在系统提示里取消（`NotAllowedError`）不算错误。
 */
import { browserSupportsWebAuthn } from "@simplewebauthn/browser";
import { type QueryClient, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	addPasskey,
	deletePasskey,
	passkeyKeys,
	reauth,
	regenerateRecoveryCodes,
	renamePasskey,
	usePasskeys,
	useRecoveryCodeStatus,
} from "../../api/auth-queries";
import type { Schemas } from "../../api/client";
import { useMe } from "../../api/queries";
import { CopyButton } from "../../components/ui/CopyButton";
import { Badge } from "../../components/ui/data";
import {
	Button,
	Dialog,
	ErrorText,
	Field,
	IconButton,
	Spinner,
	TextInput,
} from "../../components/ui/primitives";
import {
	Group,
	GroupList,
	GroupPanel,
	GroupRow,
	Reveal,
} from "../../components/ui/workbench";
import { confirmAction } from "../../lib/confirm";

type Passkey = Schemas["Passkey"];

/** 行说明里的分隔点 */
const SEP = (
	<span aria-hidden className="text-faint">
		·
	</span>
);

/** 缓存里的验证有效期至少还要剩这么久才直接放行，免得请求到达服务端时恰好过期。 */
const REAUTH_MARGIN_MS = 5_000;

/**
 * 确保当前会话在有效期内验证过：缓存里还剩 5 秒以上直接放行；否则先确认再发起系统提示。
 * 用户取消确认返回 false；验证失败照常抛错。
 */
async function ensureVerified(qc: QueryClient, t: TFunction): Promise<boolean> {
	const until =
		qc.getQueryData<Schemas["PasskeyList"]>(passkeyKeys.list)
			?.reauthValidUntil ?? null;
	if (until !== null && until - Date.now() > REAUTH_MARGIN_MS) return true;
	if (
		!(await confirmAction({
			title: t("account.passkeys.verifyTitle"),
			body: t("account.passkeys.verifyBody"),
			action: t("account.passkeys.verifyAction"),
		}))
	)
		return false;
	await reauth(qc);
	return true;
}

/** 设置类异步动作（同 `useAsync`），但用户在系统提示里取消时不记错误；`run` 返回是否成功完成。 */
function useCeremony() {
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const run = async (fn: () => Promise<unknown>): Promise<boolean> => {
		setBusy(true);
		setError(null);
		try {
			await fn();
			return true;
		} catch (e) {
			// 用户在系统的 Passkey 提示里取消或超时：浏览器抛 `NotAllowedError`，不当作错误显示
			if (!(e instanceof Error && e.name === "NotAllowedError")) setError(e);
			return false;
		} finally {
			setBusy(false);
		}
	};
	return { busy, error, run };
}

function useFormatDay(): (ts: number) => string {
	const { i18n } = useTranslation();
	return (ts) =>
		new Intl.DateTimeFormat(i18n.language, { dateStyle: "medium" }).format(ts);
}

/** 改名对话框：不需要重新验证。 */
function RenameDialog({
	passkey,
	onClose,
}: {
	passkey: Passkey;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const [name, setName] = useState(passkey.name);
	const op = useCeremony();
	const trimmed = name.trim();
	const submit = async () => {
		if (!trimmed || trimmed === passkey.name) return;
		if (await op.run(() => renamePasskey(qc, passkey.id, trimmed))) onClose();
	};
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("account.passkeys.renameTitle")}
			footer={
				<>
					<Button onClick={onClose}>{t("common.cancel")}</Button>
					<Button
						variant="primary"
						disabled={op.busy || !trimmed || trimmed === passkey.name}
						onClick={() => void submit()}
					>
						{t("common.save")}
					</Button>
				</>
			}
		>
			<div className="flex flex-col gap-3">
				<Field label={t("account.passkeys.name")}>
					<TextInput
						autoFocus
						value={name}
						maxLength={80}
						onChange={(e) => setName(e.target.value)}
						onKeyDown={(e) => {
							if (e.key === "Enter") void submit();
						}}
					/>
				</Field>
				<ErrorText error={op.error} />
			</div>
		</Dialog>
	);
}

/** 添加对话框：打开之前已验证过；点「添加」触发系统的注册提示。 */
function AddDialog({ onClose }: { onClose: () => void }) {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const [name, setName] = useState(() => navigator.platform || "Passkey");
	const op = useCeremony();
	const submit = async () => {
		if (await op.run(() => addPasskey(qc, name.trim()))) onClose();
	};
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("account.passkeys.addTitle")}
			footer={
				<>
					<Button onClick={onClose}>{t("common.cancel")}</Button>
					<Button
						variant="primary"
						disabled={op.busy}
						onClick={() => void submit()}
					>
						{op.busy ? <Spinner /> : t("account.passkeys.add")}
					</Button>
				</>
			}
		>
			<div className="flex flex-col gap-3">
				<Field label={t("account.passkeys.name")}>
					<TextInput
						value={name}
						maxLength={80}
						onChange={(e) => setName(e.target.value)}
					/>
				</Field>
				<ErrorText error={op.error} />
			</div>
		</Dialog>
	);
}

function PasskeyGroup({ webauthn }: { webauthn: boolean }) {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const day = useFormatDay();
	const list = usePasskeys();
	const op = useCeremony();
	const [adding, setAdding] = useState(false);
	const [renaming, setRenaming] = useState<Passkey | null>(null);
	const items = list.data?.items ?? [];

	const onAdd = () =>
		void op.run(async () => {
			if (await ensureVerified(qc, t)) setAdding(true);
		});

	const onDelete = async (pk: Passkey) => {
		if (
			!(await confirmAction({
				title: t("account.passkeys.deleteConfirm", { name: pk.name }),
				body: t("account.passkeys.deleteBody"),
				action: t("common.delete"),
				danger: true,
			}))
		)
			return;
		await op.run(async () => {
			if (!(await ensureVerified(qc, t))) return;
			await deletePasskey(qc, pk.id);
		});
	};

	return (
		<Group
			title={t("account.passkeys.title")}
			action={
				webauthn ? (
					<Button
						size="sm"
						icon="plus"
						disabled={op.busy || list.isPending}
						onClick={onAdd}
					>
						{t("account.passkeys.add")}
					</Button>
				) : undefined
			}
		>
			{list.isPending ? (
				<GroupPanel>
					<Spinner />
				</GroupPanel>
			) : (
				<GroupList>
					{items.map((pk) => (
						<GroupRow
							key={pk.id}
							icon="key"
							title={
								<>
									<span className="truncate">{pk.name}</span>
									{pk.current && (
										<Badge tone="accent">{t("account.passkeys.current")}</Badge>
									)}
								</>
							}
							meta={
								<>
									<span>
										{t("account.passkeys.created", { date: day(pk.createdAt) })}
									</span>
									{SEP}
									<span>
										{t("account.passkeys.lastUsed", {
											date: pk.lastUsedAt
												? day(pk.lastUsedAt)
												: t("account.passkeys.never"),
										})}
									</span>
									{pk.backedUp && (
										<>
											{SEP}
											<span>{t("account.passkeys.synced")}</span>
										</>
									)}
								</>
							}
							trailing={
								<Reveal>
									<IconButton
										icon="edit"
										box="sm"
										size={14}
										label={t("account.passkeys.rename", { name: pk.name })}
										disabled={op.busy}
										onClick={() => setRenaming(pk)}
									/>
									{webauthn && items.length > 1 && (
										<IconButton
											icon="trash"
											box="sm"
											size={14}
											label={t("account.passkeys.delete", { name: pk.name })}
											disabled={op.busy}
											onClick={() => void onDelete(pk)}
										/>
									)}
								</Reveal>
							}
						/>
					))}
					{!webauthn && (
						<GroupRow
							icon="globe"
							title={t("account.passkeys.browserOnlyTitle")}
							meta={t("account.passkeys.browserOnly")}
							trailing={
								<a
									href={`${window.location.origin}/settings/account`}
									target="_blank"
									rel="noreferrer"
									className="text-[12.5px] text-accent-ink underline underline-offset-3"
								>
									{t("account.passkeys.openInBrowser")}
								</a>
							}
						/>
					)}
				</GroupList>
			)}
			<ErrorText error={list.error ?? op.error} />
			{adding && <AddDialog onClose={() => setAdding(false)} />}
			{renaming && (
				<RenameDialog passkey={renaming} onClose={() => setRenaming(null)} />
			)}
		</Group>
	);
}

/** 新生成的恢复码：只在这里展示一次，关闭后不再能看到。 */
function CodesDialog({
	codes,
	onClose,
}: {
	codes: string[];
	onClose: () => void;
}) {
	const { t } = useTranslation();
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("account.recovery.codesTitle")}
			footer={
				<>
					<CopyButton text={codes.join("\n")} label={t("common.copy")} />
					<Button variant="primary" onClick={onClose}>
						{t("account.recovery.saved")}
					</Button>
				</>
			}
		>
			<p className="mb-3 text-muted text-sm">
				{t("account.recovery.codesHint", { n: codes.length })}
			</p>
			<ul className="grid grid-cols-2 gap-1.5 whitespace-nowrap font-mono text-xs">
				{codes.map((c) => (
					<li
						key={c}
						className="rounded-md bg-inset px-2 py-1 text-center tabular-nums"
					>
						{c}
					</li>
				))}
			</ul>
		</Dialog>
	);
}

function RecoveryGroup({ webauthn }: { webauthn: boolean }) {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const day = useFormatDay();
	const status = useRecoveryCodeStatus();
	const op = useCeremony();
	const [codes, setCodes] = useState<string[] | null>(null);
	const s = status.data;
	const low = s !== undefined && s.remaining <= 3;

	const onRegenerate = async () => {
		if (
			!(await confirmAction({
				title: t("account.recovery.regenerateConfirm"),
				body: t("account.recovery.regenerateBody"),
				action: t("account.recovery.regenerate"),
				danger: true,
			}))
		)
			return;
		await op.run(async () => {
			if (!(await ensureVerified(qc, t))) return;
			setCodes(await regenerateRecoveryCodes(qc));
		});
	};

	return (
		<Group title={t("account.recovery.title")}>
			{status.isPending ? (
				<GroupPanel>
					<Spinner />
				</GroupPanel>
			) : (
				s && (
					<GroupList>
						<GroupRow
							icon="shield"
							title={t("account.recovery.rowTitle")}
							meta={
								<>
									<span className={low ? "text-warn-ink" : undefined}>
										{t("account.recovery.remaining", {
											remaining: s.remaining,
											total: s.total,
										})}
									</span>
									{s.generatedAt ? (
										<>
											{SEP}
											<span>
												{t("account.recovery.generated", {
													date: day(s.generatedAt),
												})}
											</span>
										</>
									) : null}
									{low && (
										<>
											{SEP}
											<span className="text-warn-ink">
												{t("account.recovery.low")}
											</span>
										</>
									)}
								</>
							}
							trailing={
								webauthn ? (
									<Button
										size="sm"
										disabled={op.busy}
										onClick={() => void onRegenerate()}
									>
										{t("account.recovery.regenerate")}
									</Button>
								) : undefined
							}
						/>
					</GroupList>
				)
			)}
			<ErrorText error={status.error ?? op.error} />
			{codes && <CodesDialog codes={codes} onClose={() => setCodes(null)} />}
		</Group>
	);
}

export default function PasskeySettings(): ReactNode {
	const me = useMe();
	const [webauthn] = useState(() => browserSupportsWebAuthn());
	if (me.data?.auth.kind !== "web") return null;
	return (
		<>
			<PasskeyGroup webauthn={webauthn} />
			<RecoveryGroup webauthn={webauthn} />
		</>
	);
}
