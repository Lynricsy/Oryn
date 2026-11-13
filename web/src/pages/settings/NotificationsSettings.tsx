/**
 * 设置 → 通知（M5 计划 6.3）：三张卡片。
 * 1. 此设备：系统通知权限、「后台推送」开关（Web Push 订阅 / 退订）、其它已订阅的浏览器。
 * 2. 渠道：Telegram / Webhook / ntfy / Bark 的列表、启停、测试、编辑、删除与「添加渠道 ▾」。
 *    机密字段只写不读：编辑时整组配置留空 = 不变（不提交配置对象），填了任一项 = 整体替换。
 *    Telegram 创建后进入绑定向导：显示 `/start <码>`，每 2s 拉一次渠道列表直到已绑定。
 * 3. 最近通知：最近 20 条与各投递的状态。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiError, type Schemas } from "../../api/client";
import {
	type NotificationChannel,
	notifyKeys,
	useCreateChannel,
	useDeleteChannel,
	useDeletePushSubscription,
	useLinkTelegram,
	useNotificationChannels,
	usePushSubscriptions,
	useRecentNotifications,
	useTestChannel,
	useUpdateChannel,
} from "../../api/notify-queries";
import { KIND_ICON } from "../../components/notify/NotificationBell";
import { CopyButton } from "../../components/ui/CopyButton";
import {
	Badge,
	Callout,
	Card,
	ListRow,
	StatusDot,
} from "../../components/ui/data";
import { Icon, type IconName } from "../../components/ui/icons";
import {
	Button,
	Checkbox,
	Dialog,
	describeError,
	ErrorText,
	Field,
	IconButton,
	Menu,
	Select,
	Spinner,
	TextArea,
	TextInput,
	Toggle,
} from "../../components/ui/primitives";
import { formatDateTime, formatRelative } from "../../lib/format";
import {
	currentSubscription,
	needsHomeScreen,
	pushSupported,
	subscribePush,
	unsubscribePush,
} from "../../lib/push";
import {
	notificationsStore,
	useNotifications,
} from "../../state/notifications";

type Kind = Schemas["NotificationChannelKind"];
type EventKind = Schemas["NotificationKind"];
type Locale = Schemas["NotificationLocale"];

const KINDS: readonly Kind[] = ["telegram", "webhook", "ntfy", "bark"];
const EVENTS: readonly EventKind[] = ["done", "input", "error"];
const LOCALES: readonly Locale[] = ["zh-CN", "en"];

export const CHANNEL_ICON: Record<Kind, IconName> = {
	telegram: "telegram",
	webhook: "webhook",
	ntfy: "megaphone",
	bark: "phone",
};

interface FieldDef {
	key: string;
	required?: boolean;
	/** 密码框（token、secret、topic、deviceKey） */
	secret?: boolean;
	multiline?: boolean;
	placeholder?: string;
}

/** 各渠道的配置字段（与 `<Kind>ChannelConfig` 同名）。 */
const FIELDS: Record<Kind, readonly FieldDef[]> = {
	telegram: [
		{ key: "botToken", required: true, secret: true },
		{ key: "apiBase", placeholder: "https://api.telegram.org" },
	],
	webhook: [
		{
			key: "url",
			required: true,
			placeholder: "https://hooks.example.com/oryn",
		},
		{ key: "secret", secret: true },
		{ key: "headers", multiline: true, placeholder: "Authorization: Bearer …" },
	],
	ntfy: [
		{ key: "server", placeholder: "https://ntfy.sh" },
		{ key: "topic", required: true, secret: true },
		{ key: "token", secret: true },
	],
	bark: [
		{ key: "server", placeholder: "https://api.day.app" },
		{ key: "deviceKey", required: true, secret: true },
	],
};

/** 每行 `Name: value` → 头表（空行忽略）。 */
export function parseHeaders(text: string): Record<string, string> {
	const out: Record<string, string> = {};
	for (const line of text.split("\n")) {
		const i = line.indexOf(":");
		if (i <= 0) continue;
		const name = line.slice(0, i).trim();
		if (name) out[name] = line.slice(i + 1).trim();
	}
	return out;
}

/**
 * 表单字段 → 该 kind 的配置对象；一个字段都没填时返回 `undefined`（编辑时 = 保持原配置）。
 * 空的可选字段不提交。
 */
export function buildConfig(
	kind: Kind,
	values: Record<string, string>,
): Record<string, unknown> | undefined {
	const defs = FIELDS[kind];
	if (defs.every((d) => !values[d.key]?.trim())) return undefined;
	const out: Record<string, unknown> = {};
	for (const d of defs) {
		const v = values[d.key]?.trim();
		if (!v) continue;
		out[d.key] = d.key === "headers" ? parseHeaders(v) : v;
	}
	return out;
}

function LeadIcon({ name }: { name: IconName }) {
	return (
		<span className="grid h-8 w-8 place-items-center rounded-md border border-border bg-raised text-muted">
			<Icon name={name} size={15} />
		</span>
	);
}

/** UA 的简短摘要：浏览器 · 系统。 */
export function uaSummary(ua: string): string {
	const browser = /Edg\//.test(ua)
		? "Edge"
		: /Firefox\//.test(ua)
			? "Firefox"
			: /Chrome\//.test(ua)
				? "Chrome"
				: /Safari\//.test(ua)
					? "Safari"
					: "";
	const os = /Android/.test(ua)
		? "Android"
		: /iPhone|iPad|iPod/.test(ua)
			? "iOS"
			: /Mac OS X/.test(ua)
				? "macOS"
				: /Windows/.test(ua)
					? "Windows"
					: /Linux/.test(ua)
						? "Linux"
						: "";
	return [browser, os].filter(Boolean).join(" · ") || ua || "—";
}

// ---------------------------------------------------------------- 此设备

type Permission = NotificationPermission | "unsupported";

function permissionNow(): Permission {
	return typeof Notification === "undefined"
		? "unsupported"
		: Notification.permission;
}

const PERMISSION_TONE = {
	granted: "ok",
	denied: "err",
	default: "neutral",
	unsupported: "neutral",
} as const;

function DeviceCard() {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const [permission, setPermission] = useState<Permission>(permissionNow);
	const [ownEndpoint, setOwnEndpoint] = useState<string | null>(null);
	const pushActive = useNotifications((s) => s.pushActive);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const subs = usePushSubscriptions();
	const remove = useDeletePushSubscription();
	const supported = pushSupported();

	useEffect(() => {
		let live = true;
		void currentSubscription().then(
			(sub) => {
				if (!live) return;
				setOwnEndpoint(sub?.endpoint ?? null);
				notificationsStore.getState().setPushActive(sub !== null);
			},
			() => undefined,
		);
		return () => {
			live = false;
		};
	}, []);

	const request = async (): Promise<Permission> => {
		if (typeof Notification === "undefined") return "unsupported";
		const p = await Notification.requestPermission();
		setPermission(p);
		return p;
	};

	const setPush = async (on: boolean) => {
		setBusy(true);
		setError(null);
		try {
			if (on) {
				if ((await request()) !== "granted") return;
				const sub = await subscribePush();
				setOwnEndpoint(sub.endpoint);
			} else {
				await unsubscribePush();
				setOwnEndpoint(null);
			}
			notificationsStore.getState().setPushActive(on);
			await qc.invalidateQueries({ queryKey: notifyKeys.subscriptions });
		} catch (e) {
			setError(e);
		} finally {
			setBusy(false);
		}
	};

	const others =
		subs.data?.subscriptions.filter((s) => s.endpoint !== ownEndpoint) ?? [];
	const secure = typeof window !== "undefined" && window.isSecureContext;
	return (
		<Card title={t("notifySettings.device.title")} icon="monitor">
			<div className="flex flex-col gap-3">
				{permission === "unsupported" || !secure ? (
					<Callout tone="neutral">
						{secure
							? t("notifySettings.device.unsupported")
							: t("notifySettings.device.insecure")}
					</Callout>
				) : (
					<div className="flex flex-wrap items-center gap-2 text-sm">
						<span className="text-muted">
							{t("notifySettings.device.permission")}
						</span>
						<Badge tone={PERMISSION_TONE[permission]}>
							{t(`notifySettings.device.perm.${permission}`)}
						</Badge>
						{permission === "default" && (
							<Button size="sm" onClick={() => void request()}>
								{t("notifySettings.device.allow")}
							</Button>
						)}
						{permission === "denied" && (
							<span className="text-faint text-xs">
								{t("notifySettings.device.deniedHint")}
							</span>
						)}
					</div>
				)}
				{needsHomeScreen() && (
					<Callout tone="neutral">
						{t("notifySettings.device.homeScreen")}
					</Callout>
				)}
				{supported && (
					<div className="flex flex-col gap-1">
						<Toggle
							checked={pushActive}
							disabled={busy || permission === "denied"}
							onChange={(v) => void setPush(v)}
							label={t("notifySettings.device.push")}
						/>
						<p className="pl-10 text-faint text-xs">
							{t("notifySettings.device.pushHint")}
						</p>
					</div>
				)}
				<ErrorText error={error ?? subs.error ?? remove.error} />
				{others.length > 0 && (
					<div>
						<h4 className="mb-1 font-medium text-muted text-xs">
							{t("notifySettings.device.others")}
						</h4>
						<ul className="-mx-2 flex flex-col">
							{others.map((s) => (
								<li key={s.id}>
									<ListRow
										leading={<LeadIcon name="browser" />}
										title={
											<span className="truncate" title={s.userAgent}>
												{uaSummary(s.userAgent)}
											</span>
										}
										meta={
											<>
												<span>
													{t("notifySettings.device.subscribedAt", {
														when: formatRelative(s.createdAt),
													})}
												</span>
												{s.lastError && (
													<span className="text-err">{s.lastError}</span>
												)}
											</>
										}
										trailing={
											<IconButton
												icon="trash"
												label={t("notifySettings.device.remove")}
												disabled={remove.isPending}
												onClick={() => remove.mutate(s.id)}
											/>
										}
									/>
								</li>
							))}
						</ul>
					</div>
				)}
			</div>
		</Card>
	);
}

// ---------------------------------------------------------------- 渠道

interface FormState {
	name: string;
	events: EventKind[];
	locale: Locale;
	values: Record<string, string>;
}

function ChannelDialog({
	kind,
	channel,
	onClose,
	onCreated,
}: {
	kind: Kind;
	/** 编辑的渠道；缺省 = 新建 */
	channel?: NotificationChannel;
	onClose: () => void;
	onCreated: (ch: NotificationChannel) => void;
}) {
	const { t, i18n } = useTranslation();
	const create = useCreateChannel();
	const update = useUpdateChannel();
	const [form, setForm] = useState<FormState>(() => ({
		name: channel?.name ?? t(`notifySettings.kind.${kind}`),
		events: channel?.events ?? [...EVENTS],
		locale: channel?.locale ?? (i18n.language === "en" ? "en" : "zh-CN"),
		values: {},
	}));
	const editing = channel !== undefined;
	const config = buildConfig(kind, form.values);
	const missing = FIELDS[kind].some(
		(d) => d.required && !form.values[d.key]?.trim(),
	);
	// 新建必须填全必填项；编辑时整组留空 = 不变，填了就要填全
	const configOk = editing ? config === undefined || !missing : !missing;
	const valid = form.name.trim() !== "" && form.events.length > 0 && configOk;
	const busy = create.isPending || update.isPending;

	const submit = async (e: FormEvent) => {
		e.preventDefault();
		if (!valid) return;
		const base = {
			name: form.name.trim(),
			events: form.events,
			locale: form.locale,
		};
		if (channel) {
			await update.mutateAsync({
				id: channel.id,
				patch: { ...base, ...(config ? { [kind]: config } : {}) },
			});
			onClose();
			return;
		}
		const created = await create.mutateAsync({
			kind,
			enabled: true,
			...base,
			[kind]: config,
		});
		onCreated(created);
	};

	const toggleEvent = (ev: EventKind, on: boolean) =>
		setForm((f) => ({
			...f,
			events: on
				? EVENTS.filter((x) => x === ev || f.events.includes(x))
				: f.events.filter((x) => x !== ev),
		}));

	const formId = "notify-channel-form";
	return (
		<Dialog
			open
			onClose={onClose}
			title={t(
				editing ? "notifySettings.editTitle" : "notifySettings.addTitle",
				{
					kind: t(`notifySettings.kind.${kind}`),
				},
			)}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						type="submit"
						form={formId}
						variant="primary"
						disabled={!valid || busy}
					>
						{editing ? t("common.save") : t("common.create")}
					</Button>
				</>
			}
		>
			<form
				id={formId}
				className="flex flex-col gap-3"
				onSubmit={(e) => void submit(e).catch(() => {})}
			>
				<Field label={t("notifySettings.name")}>
					<TextInput
						required
						value={form.name}
						onChange={(e) => setForm({ ...form, name: e.target.value })}
					/>
				</Field>
				<fieldset className="flex flex-col gap-1">
					<legend className="mb-1 font-medium text-muted text-xs">
						{t("notifySettings.events")}
					</legend>
					<div className="flex flex-wrap gap-x-4 gap-y-1.5">
						{EVENTS.map((ev) => (
							// biome-ignore lint/a11y/noLabelWithoutControl: 控件是包在里面的 Checkbox（原生 input）
							<label
								key={ev}
								className="inline-flex items-center gap-2 text-sm"
							>
								<Checkbox
									checked={form.events.includes(ev)}
									onChange={(e) => toggleEvent(ev, e.target.checked)}
								/>
								{t(`notify.kind.${ev}`)}
							</label>
						))}
					</div>
				</fieldset>
				<Field
					label={t("notifySettings.locale")}
					hint={t("notifySettings.localeHint")}
				>
					<Select
						value={form.locale}
						onChange={(e) =>
							setForm({ ...form, locale: e.target.value as Locale })
						}
					>
						{LOCALES.map((l) => (
							<option key={l} value={l}>
								{t(`notifySettings.localeName.${l === "en" ? "en" : "zhCN"}`)}
							</option>
						))}
					</Select>
				</Field>
				{editing && (
					<p className="text-faint text-xs">{t("notifySettings.keepConfig")}</p>
				)}
				{FIELDS[kind].map((d) => {
					const label = t(
						`notifySettings.field.${kind}.${d.key}` as "notifySettings.name",
					);
					const value = form.values[d.key] ?? "";
					const set = (v: string) =>
						setForm((f) => ({ ...f, values: { ...f.values, [d.key]: v } }));
					return (
						<Field
							key={d.key}
							label={d.required && !editing ? `${label} *` : label}
							hint={
								d.key === "headers"
									? t("notifySettings.headersHint")
									: undefined
							}
						>
							{d.multiline ? (
								<TextArea
									rows={3}
									aria-label={label}
									placeholder={d.placeholder}
									value={value}
									onChange={(e) => set(e.target.value)}
								/>
							) : (
								<TextInput
									aria-label={label}
									type={d.secret ? "password" : "text"}
									autoComplete="off"
									spellCheck={false}
									placeholder={
										editing && d.secret
											? t("notifySettings.unchanged")
											: d.placeholder
									}
									value={value}
									onChange={(e) => set(e.target.value)}
								/>
							)}
						</Field>
					);
				})}
				{kind === "telegram" && !editing && (
					<p className="text-faint text-xs">
						{t("notifySettings.telegramHint")}
					</p>
				)}
				<ErrorText error={create.error ?? update.error} />
			</form>
		</Dialog>
	);
}

function TelegramLinkDialog({
	channelId,
	onClose,
}: {
	channelId: string;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const link = useLinkTelegram();
	const channels = useNotificationChannels(2_000);
	const channel = channels.data?.channels.find((c) => c.id === channelId);
	const linked = channel?.telegram?.linked === true;
	const { mutate } = link;
	useEffect(() => {
		mutate(channelId);
	}, [channelId, mutate]);
	const code = link.data?.code;
	const bot = link.data?.botUsername ?? channel?.telegram?.botUsername ?? "";
	const expired = link.data ? link.data.expiresAt <= Date.now() : false;
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("notifySettings.link.title")}
			footer={
				<Button variant={linked ? "primary" : "ghost"} onClick={onClose}>
					{linked ? t("notifySettings.link.done") : t("common.close")}
				</Button>
			}
		>
			{linked ? (
				<Callout tone="ok" title={t("notifySettings.link.linked")}>
					{channel?.telegram?.chatTitle &&
						t("notifySettings.link.linkedChat", {
							chat: channel.telegram.chatTitle,
						})}
				</Callout>
			) : (
				<div className="flex flex-col gap-3 text-sm">
					{link.isPending && <Spinner />}
					<ErrorText error={link.error} />
					{code && (
						<>
							<p>{t("notifySettings.link.step", { bot: `@${bot}` })}</p>
							<div className="flex items-center gap-2 rounded-md bg-inset px-3 py-2">
								<code className="min-w-0 flex-1 truncate font-mono text-fg">
									{`/start ${code}`}
								</code>
								<CopyButton text={`/start ${code}`} />
							</div>
							<p className="flex items-center gap-2 text-faint text-xs">
								<Spinner size={11} />
								{t("notifySettings.link.waiting", {
									when: formatDateTime(link.data?.expiresAt ?? 0),
								})}
							</p>
							{expired && (
								<Button size="sm" onClick={() => mutate(channelId)}>
									{t("notifySettings.link.renew")}
								</Button>
							)}
						</>
					)}
				</div>
			)}
		</Dialog>
	);
}

function channelTone(ch: NotificationChannel) {
	if (!ch.enabled) return "neutral" as const;
	return ch.lastError ? ("err" as const) : ("ok" as const);
}

type TestResult = { ok: boolean; error?: string };

function ChannelRow({
	ch,
	onEdit,
	onLink,
}: {
	ch: NotificationChannel;
	onEdit: () => void;
	onLink: () => void;
}) {
	const { t } = useTranslation();
	const update = useUpdateChannel();
	const del = useDeleteChannel();
	const test = useTestChannel();
	const [result, setResult] = useState<TestResult | null>(null);
	const runTest = async () => {
		setResult(null);
		try {
			setResult(await test.mutateAsync(ch.id));
		} catch (e) {
			setResult({
				ok: false,
				error:
					e instanceof ApiError && e.code === "channel_not_linked"
						? t("notifySettings.notLinked")
						: describeError(e, t),
			});
		}
	};
	const tg = ch.telegram;
	const tone = channelTone(ch);
	return (
		<li>
			<ListRow
				leading={<LeadIcon name={CHANNEL_ICON[ch.kind]} />}
				title={
					<>
						<StatusDot tone={tone} label={t(`notifySettings.status.${tone}`)} />
						<span className="truncate font-medium">{ch.name}</span>
						{/* 名称就是渠道类型时不再重复 */}
						{ch.name !== t(`notifySettings.kind.${ch.kind}`) && (
							<Badge>{t(`notifySettings.kind.${ch.kind}`)}</Badge>
						)}
					</>
				}
				meta={
					<>
						<span className="truncate font-mono">{ch.summary}</span>
						{tg &&
							(tg.linked ? (
								<span>
									{t("notifySettings.linkedTo", { chat: tg.chatTitle ?? "" })}
								</span>
							) : (
								<button
									type="button"
									className="text-accent-ink underline-offset-2 hover:underline"
									onClick={onLink}
								>
									{t("notifySettings.linkNow")}
								</button>
							))}
						{ch.lastError && <span className="text-err">{ch.lastError}</span>}
					</>
				}
				trailing={
					<>
						<Toggle
							checked={ch.enabled}
							disabled={update.isPending}
							onChange={(v) =>
								update.mutate({ id: ch.id, patch: { enabled: v } })
							}
							label={
								<span className="sr-only">{t("notifySettings.enabled")}</span>
							}
						/>
						<Button
							size="sm"
							variant="ghost"
							disabled={test.isPending}
							onClick={() => void runTest()}
						>
							{t("notifySettings.test")}
						</Button>
						<IconButton
							icon="edit"
							box="sm"
							label={t("common.edit")}
							onClick={onEdit}
						/>
						<IconButton
							icon="trash"
							box="sm"
							label={t("common.delete")}
							disabled={del.isPending}
							onClick={() => {
								if (
									window.confirm(
										t("notifySettings.deleteConfirm", { name: ch.name }),
									)
								)
									del.mutate(ch.id);
							}}
						/>
					</>
				}
			/>
			{(result || test.isPending) && (
				<p
					role="status"
					className={`flex items-center gap-1.5 pb-1.5 pl-[3.25rem] text-xs ${result?.ok ? "text-ok-ink" : "text-err"}`}
				>
					{test.isPending ? (
						<Spinner size={11} />
					) : (
						<Icon name={result?.ok ? "checkCircle" : "alert"} size={12} />
					)}
					{test.isPending
						? t("notifySettings.testing")
						: result?.ok
							? t("notifySettings.testOk")
							: t("notifySettings.testFailed", { error: result?.error ?? "" })}
				</p>
			)}
			<ErrorText error={update.error ?? del.error} />
		</li>
	);
}

type Editor =
	| { mode: "create"; kind: Kind }
	| { mode: "edit"; channel: NotificationChannel }
	| { mode: "link"; channelId: string };

function ChannelsCard() {
	const { t } = useTranslation();
	const channels = useNotificationChannels();
	const [editor, setEditor] = useState<Editor | null>(null);
	const list = channels.data?.channels ?? [];
	const hasTelegram = list.some((c) => c.kind === "telegram");
	return (
		<Card
			title={t("notifySettings.channels")}
			icon="bell"
			action={
				<Menu
					trigger={(p) => (
						<Button size="sm" variant="ghost" icon="plus" {...p}>
							{t("notifySettings.add")}
							<Icon name="chevronDown" size={11} className="text-faint" />
						</Button>
					)}
					items={KINDS.map((k) => ({
						key: k,
						icon: CHANNEL_ICON[k],
						label: t(`notifySettings.kind.${k}`),
						disabled: k === "telegram" && hasTelegram,
						hint:
							k === "telegram" && hasTelegram
								? t("notifySettings.onlyOne")
								: undefined,
						onSelect: () => setEditor({ mode: "create", kind: k }),
					}))}
				/>
			}
		>
			{channels.isPending && <Spinner />}
			<ErrorText error={channels.error} />
			{channels.data && list.length === 0 && (
				<p className="text-faint text-sm">{t("notifySettings.noChannels")}</p>
			)}
			<ul className="-mx-2 flex flex-col">
				{list.map((ch) => (
					<ChannelRow
						key={ch.id}
						ch={ch}
						onEdit={() => setEditor({ mode: "edit", channel: ch })}
						onLink={() => setEditor({ mode: "link", channelId: ch.id })}
					/>
				))}
			</ul>
			{editor?.mode === "create" && (
				<ChannelDialog
					kind={editor.kind}
					onClose={() => setEditor(null)}
					onCreated={(ch) =>
						setEditor(
							ch.kind === "telegram"
								? { mode: "link", channelId: ch.id }
								: null,
						)
					}
				/>
			)}
			{editor?.mode === "edit" && (
				<ChannelDialog
					kind={editor.channel.kind}
					channel={editor.channel}
					onClose={() => setEditor(null)}
					onCreated={() => setEditor(null)}
				/>
			)}
			{editor?.mode === "link" && (
				<TelegramLinkDialog
					channelId={editor.channelId}
					onClose={() => setEditor(null)}
				/>
			)}
		</Card>
	);
}

// ---------------------------------------------------------------- 最近通知

const DELIVERY_TONE = {
	sent: "text-faint",
	pending: "text-warn",
	failed: "text-err",
} as const;

function RecentCard() {
	const { t } = useTranslation();
	const recent = useRecentNotifications(20);
	const list = recent.data?.notifications ?? [];
	return (
		<Card title={t("notifySettings.recent")} icon="clock">
			{recent.isPending && <Spinner />}
			<ErrorText error={recent.error} />
			{recent.data && list.length === 0 && (
				<p className="text-faint text-sm">{t("notify.empty")}</p>
			)}
			<ul className="flex flex-col divide-y divide-border">
				{list.map((n) => {
					const kind = KIND_ICON[n.kind];
					return (
						<li
							key={n.id}
							className="flex flex-col gap-1 py-2 first:pt-0 last:pb-0"
						>
							<div className="flex min-w-0 items-center gap-2 text-sm">
								<Icon
									name={kind.icon}
									size={14}
									className={`shrink-0 ${kind.className}`}
									aria-label={t(`notify.kind.${n.kind}`)}
								/>
								<span className="min-w-0 flex-1 truncate">
									{n.title || t("notify.untitled")}
								</span>
								<span className="shrink-0 text-faint text-xs tabular-nums">
									{formatRelative(n.createdAt)}
								</span>
							</div>
							{n.deliveries.length > 0 && (
								<ul className="flex flex-col gap-0.5 pl-6 text-xs">
									{n.deliveries.map((d, i) => (
										<li
											// biome-ignore lint/suspicious/noArrayIndexKey: 投递没有 id（同一订阅可有多条推送），列表只读、不重排
											key={`${d.target}-${d.channelId ?? ""}-${i}`}
											className={`flex min-w-0 flex-wrap items-baseline gap-x-1.5 ${DELIVERY_TONE[d.status]}`}
										>
											<span className="font-medium">
												{d.channelName ??
													t(`notifySettings.target.${d.target}`)}
											</span>
											<span>
												{t(`notifySettings.delivery.${d.status}`, {
													n: d.attempts,
													when: formatRelative(d.at),
												})}
											</span>
											{d.error && <span className="break-all">{d.error}</span>}
										</li>
									))}
								</ul>
							)}
						</li>
					);
				})}
			</ul>
		</Card>
	);
}

export default function NotificationsSettings(): ReactNode {
	return (
		<>
			<DeviceCard />
			<ChannelsCard />
			<RecentCard />
		</>
	);
}
