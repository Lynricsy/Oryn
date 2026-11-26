/**
 * 设置 → 通知与预算（M5 计划 6.3），工作台原生（D）分组列表（components/ui/workbench.tsx）。
 * 1. 此设备：系统通知权限、「后台推送」开关（Web Push 订阅 / 退订）、其它已订阅的浏览器。
 *    桌面壳（M6）里系统通知由壳发送：只有一句说明与「发送测试通知」，不渲染权限与推送开关；
 *    移动壳（M7）换成 `MobilePushCard`（壳注册的 UnifiedPush / FCM 推送状态）。
 * 2. 渠道：Telegram / Webhook / ntfy / Bark 的列表、启停、测试、编辑、删除与「添加渠道 ▾」。
 *    行尾：测试（悬停出现）· 定宽状态列 · ⋯（编辑 / 发送测试 / 删除）· 开关；出错时点状态看原文。
 *    机密字段只写不读：编辑时「连接配置」整组留空 = 不变（不提交配置对象），填了任一项 = 整体替换。
 *    Telegram 创建后进入绑定向导：显示 `/start <码>`，每 2s 拉一次渠道列表直到已绑定。
 * 3. 预算（`BudgetGroup`，BudgetSettings.tsx）：告警开关、日 / 月上限、预警线、时区；统计页只看。
 * 4. 最近通知：最近 20 条与各投递的状态。
 */
import { useQueryClient } from "@tanstack/react-query";
import {
	type FormEvent,
	type ReactNode,
	useEffect,
	useId,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../api/client";
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
import { kindIcon } from "../../components/notify/NotificationBell";
import { ActionError, DeliveryFailure } from "../../components/ui/ActionError";
import { CopyButton } from "../../components/ui/CopyButton";
import { Callout, StatusDot } from "../../components/ui/data";
import { Floating } from "../../components/ui/Floating";
import { Icon, type IconName } from "../../components/ui/icons";
import {
	Button,
	Checkbox,
	Dialog,
	ErrorText,
	IconButton,
	Menu,
	Select,
	Spinner,
	TextArea,
	TextInput,
	Toggle,
} from "../../components/ui/primitives";
import {
	FormField,
	Group,
	GroupList,
	GroupRow,
	Reveal,
	StatusCell,
	StatusText,
} from "../../components/ui/workbench";
import type { ActionStep } from "../../lib/action-error";
import { confirmAction } from "../../lib/confirm";
import { formatDateTime, formatRelative } from "../../lib/format";
import { host } from "../../lib/host";
import {
	needsHomeScreen,
	pushRegistration,
	pushSupported,
	subscribePush,
	unsubscribePush,
} from "../../lib/push";
import {
	notificationsStore,
	useNotifications,
} from "../../state/notifications";
import { BudgetGroup } from "./BudgetSettings";
import MobilePushCard from "./MobilePushCard";

type Kind = Schemas["NotificationChannelKind"];
type EventKind = Schemas["NotificationKind"];
type Locale = Schemas["NotificationLocale"];

const KINDS: readonly Kind[] = ["telegram", "webhook", "ntfy", "bark"];
const EVENTS: readonly EventKind[] = ["done", "input", "error", "budget"];
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

/** 行说明里的分隔点 */
const SEP = (
	<span aria-hidden className="text-faint">
		·
	</span>
);

function DeviceCard() {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const [permission, setPermission] = useState<Permission>(permissionNow);
	const [ownEndpoint, setOwnEndpoint] = useState<string | null>(null);
	const pushActive = useNotifications((s) => s.pushActive);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<{
		step: ActionStep;
		error: unknown;
	} | null>(null);
	const subs = usePushSubscriptions();
	const remove = useDeletePushSubscription();
	const supported = pushSupported();

	useEffect(() => {
		let live = true;
		// 开关只在服务端也登记着本浏览器的 endpoint 时显示为开（另一台设备可能已移除它）
		void pushRegistration().then(
			(r) => {
				if (!live) return;
				setOwnEndpoint(r.endpoint);
				notificationsStore.getState().setPushActive(r.registered);
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
			setError({ step: on ? "pushEnable" : "pushDisable", error: e });
		} finally {
			setBusy(false);
		}
	};

	const others =
		subs.data?.subscriptions.filter((s) => s.endpoint !== ownEndpoint) ?? [];
	const secure = typeof window !== "undefined" && window.isSecureContext;
	const blocked = permission === "unsupported" || !secure;
	const homeScreen = needsHomeScreen();
	return (
		<>
			<Group title={t("notifySettings.device.title")}>
				<GroupList>
					<GroupRow
						icon="bell"
						title={t("notifySettings.device.permission")}
						meta={
							blocked
								? t(
										secure
											? "notifySettings.device.unsupported"
											: "notifySettings.device.insecure",
									)
								: permission === "denied"
									? t("notifySettings.device.deniedHint")
									: undefined
						}
						trailing={
							blocked ? undefined : (
								<>
									<StatusText tone={PERMISSION_TONE[permission]}>
										{t(`notifySettings.device.perm.${permission}`)}
									</StatusText>
									{permission === "default" && (
										<Button size="sm" onClick={() => void request()}>
											{t("notifySettings.device.allow")}
										</Button>
									)}
								</>
							)
						}
					/>
					{(supported || homeScreen) && (
						<GroupRow
							icon="broadcast"
							title={t("notifySettings.device.push")}
							meta={
								homeScreen
									? t("notifySettings.device.homeScreen")
									: t("notifySettings.device.pushHint")
							}
							trailing={
								supported && (
									<Toggle
										hideLabel
										checked={pushActive}
										disabled={busy || permission === "denied"}
										onChange={(v) => void setPush(v)}
										label={t("notifySettings.device.push")}
									/>
								)
							}
						/>
					)}
				</GroupList>
				{error ? (
					<ActionError step={error.step} error={error.error} />
				) : subs.error ? (
					<ActionError step="pushList" error={subs.error} />
				) : (
					<ActionError step="pushRemove" error={remove.error} />
				)}
			</Group>
			{others.length > 0 && (
				<Group title={t("notifySettings.device.others")}>
					<GroupList>
						{others.map((s) => (
							<GroupRow
								key={s.id}
								icon={s.kind === "gateway" ? "phone" : "browser"}
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
										{s.kind === "gateway" && (
											<>
												{SEP}
												<span data-testid="push-sub-gateway">
													{t("notifySettings.device.viaGateway", {
														host: URL.canParse(s.endpoint)
															? new URL(s.endpoint).host
															: s.endpoint,
													})}
												</span>
											</>
										)}
										{s.lastError && (
											<>
												{SEP}
												<span className="text-err">{s.lastError}</span>
											</>
										)}
									</>
								}
								trailing={
									<Button
										size="sm"
										disabled={remove.isPending}
										onClick={() => remove.mutate(s.id)}
									>
										{t("notifySettings.device.remove")}
									</Button>
								}
							/>
						))}
					</GroupList>
				</Group>
			)}
		</>
	);
}

/** 桌面壳里的「此设备」：系统通知由壳发送（窗口隐藏或最小化时），这里只能发一条测试通知。 */
function DesktopDeviceCard() {
	const { t } = useTranslation();
	const [sent, setSent] = useState(false);
	return (
		<Group title={t("notifySettings.device.title")}>
			<GroupList>
				<GroupRow
					icon="monitor"
					title={t("notifySettings.device.permission")}
					meta={t("notifySettings.device.desktopApp")}
					trailing={
						<>
							{sent && (
								<span role="status" className="text-faint text-xs">
									{t("notifySettings.device.testSent")}
								</span>
							)}
							<Button
								size="sm"
								onClick={() => {
									host.notify({
										id: `test-${Date.now()}`,
										title: t("notifySettings.device.testTitle"),
										body: t("notifySettings.device.testBody"),
										path: "/settings/notifications",
									});
									setSent(true);
								}}
							>
								{t("notifySettings.device.testSend")}
							</Button>
						</>
					}
				/>
			</GroupList>
		</Group>
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
	const configId = useId();
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
				className="flex flex-col gap-4"
				onSubmit={(e) => void submit(e).catch(() => {})}
			>
				<FormField label={t("notifySettings.name")}>
					{(p) => (
						<TextInput
							{...p}
							required
							value={form.name}
							onChange={(e) => setForm({ ...form, name: e.target.value })}
						/>
					)}
				</FormField>
				<fieldset className="flex flex-col">
					<legend className="mb-[7px] font-[550] text-[13px] text-fg">
						{t("notifySettings.events")}
					</legend>
					<div className="flex flex-wrap gap-x-[18px] gap-y-1.5">
						{EVENTS.map((ev) => (
							// biome-ignore lint/a11y/noLabelWithoutControl: 控件是包在里面的 Checkbox（原生 input）
							<label
								key={ev}
								className="inline-flex items-center gap-[7px] text-[13.5px]"
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
				<FormField
					label={t("notifySettings.locale")}
					aside={t("notifySettings.localeHint")}
				>
					{(p) => (
						<Select
							{...p}
							value={form.locale}
							options={LOCALES.map((l) => ({
								value: l,
								label: t(
									`notifySettings.localeName.${l === "en" ? "en" : "zhCN"}`,
								),
							}))}
							onChange={(locale) => setForm({ ...form, locale })}
						/>
					)}
				</FormField>
				{/* 连接配置：一组，整组替换或整组保持 */}
				<section
					aria-labelledby={configId}
					className="flex flex-col gap-3 rounded-[14px] bg-inset px-3.5 py-3 ring-1 ring-border"
				>
					<div className="flex min-h-6 items-center gap-2">
						<h3 id={configId} className="font-semibold text-[13px] text-fg">
							{t("notifySettings.config.title")}
						</h3>
						{editing && (
							<span className="text-[12.5px] text-muted">
								{t("notifySettings.config.hidden")}
							</span>
						)}
						{editing && config !== undefined && (
							<Button
								size="sm"
								variant="ghost"
								className="ml-auto"
								onClick={() => setForm((f) => ({ ...f, values: {} }))}
							>
								{t("notifySettings.config.reset")}
							</Button>
						)}
					</div>
					{FIELDS[kind].map((d) => {
						const value = form.values[d.key] ?? "";
						const set = (v: string) =>
							setForm((f) => ({ ...f, values: { ...f.values, [d.key]: v } }));
						return (
							<FormField
								key={d.key}
								label={t(
									`notifySettings.field.${kind}.${d.key}` as "notifySettings.name",
								)}
								optional={d.required ? undefined : t("notifySettings.optional")}
								aside={
									d.key === "headers"
										? t("notifySettings.headersHint")
										: d.key === "apiBase"
											? t("notifySettings.apiBaseHint")
											: undefined
								}
							>
								{(p) =>
									d.multiline ? (
										<TextArea
											{...p}
											rows={3}
											placeholder={d.placeholder}
											value={value}
											onChange={(e) => set(e.target.value)}
										/>
									) : (
										<TextInput
											{...p}
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
									)
								}
							</FormField>
						);
					})}
					{editing ? (
						<p className="flex items-center gap-1.5 border-border border-t pt-2.5 text-[12.5px] text-muted">
							<Icon name="lock" size={13} aria-hidden className="shrink-0" />
							{t("notifySettings.config.note")}
						</p>
					) : (
						kind === "telegram" && (
							<p className="border-border border-t pt-2.5 text-[12.5px] text-muted">
								{t("notifySettings.telegramHint")}
							</p>
						)
					)}
				</section>
				<ActionError step="channelSave" error={create.error ?? update.error} />
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

/** 测试结果：送达 / 渠道投递失败（服务端回传的发送错误）/ 请求本身失败（未绑定、网络等）。 */
type TestResult =
	| { ok: true }
	| { ok: false; delivery: string }
	| { ok: false; request: unknown };

/**
 * 出错状态：状态本身是按钮，点开在下方浮出渠道原文与「发送测试」；行高与其它行一致，不铺红字。
 * Esc 或点外面收起，Esc 时焦点回到状态按钮。
 */
function FailureStatus({
	error,
	detail,
	onTest,
}: {
	error: string;
	detail?: string;
	onTest: () => void;
}) {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const wrapRef = useRef<HTMLSpanElement>(null);
	const buttonRef = useRef<HTMLButtonElement>(null);
	const popId = useId();
	useEffect(() => {
		if (!open) return;
		const onDown = (e: MouseEvent) => {
			if (!wrapRef.current?.contains(e.target as Node)) setOpen(false);
		};
		const onKey = (e: KeyboardEvent) => {
			if (e.key !== "Escape") return;
			setOpen(false);
			buttonRef.current?.focus();
		};
		document.addEventListener("mousedown", onDown);
		window.addEventListener("keydown", onKey);
		return () => {
			document.removeEventListener("mousedown", onDown);
			window.removeEventListener("keydown", onKey);
		};
	}, [open]);
	return (
		<span ref={wrapRef} className="relative inline-flex min-w-0">
			<button
				ref={buttonRef}
				type="button"
				aria-expanded={open}
				aria-controls={open ? popId : undefined}
				onClick={() => setOpen((v) => !v)}
				className="-mx-1.5 -my-0.5 inline-flex min-w-0 items-center gap-1.5 rounded-[7px] px-1.5 py-0.5 text-[12.5px] text-err transition-colors duration-150 hover:bg-err/10 aria-expanded:bg-err/10"
			>
				<StatusDot tone="err" />
				<span className="truncate">{t("notifySettings.state.failed")}</span>
				<Icon name="chevronDown" size={10} className="shrink-0" />
			</button>
			{open && (
				<Floating
					id={popId}
					role="dialog"
					aria-label={t("notifySettings.state.failed")}
					side="bottom"
					align="end"
					gap={6}
					className="w-72"
					bodyClassName="flex flex-col gap-1 p-3"
				>
					<code className="break-all font-mono text-[12.5px] text-fg">
						{error}
					</code>
					{detail && <span className="text-muted text-xs">{detail}</span>}
					<div className="mt-2 flex">
						<Button
							size="sm"
							icon="paperPlane"
							onClick={() => {
								setOpen(false);
								buttonRef.current?.focus();
								onTest();
							}}
						>
							{t("notifySettings.sendTest")}
						</Button>
					</div>
				</Floating>
			)}
		</span>
	);
}

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
			const r = await test.mutateAsync(ch.id);
			setResult(r.ok ? { ok: true } : { ok: false, delivery: r.error ?? "" });
		} catch (e) {
			setResult({ ok: false, request: e });
		}
	};
	const remove = async () => {
		if (
			await confirmAction({
				title: t("notifySettings.deleteConfirm", { name: ch.name }),
				body: t("notifySettings.deleteConfirmBody"),
				action: t("common.delete"),
				danger: true,
			})
		)
			del.mutate(ch.id);
	};
	const tg = ch.telegram;
	const kindName = t(`notifySettings.kind.${ch.kind}`);
	const last = ch.lastDelivery;
	const status = !ch.enabled ? (
		<StatusText tone="neutral">{t("notifySettings.status.neutral")}</StatusText>
	) : ch.lastError ? (
		<FailureStatus
			error={ch.lastError}
			detail={
				last?.status === "pending"
					? t("notifySettings.state.nextRetry", {
							when: formatRelative(last.at),
						})
					: last && formatDateTime(last.at)
			}
			onTest={() => void runTest()}
		/>
	) : last?.status === "pending" ? (
		<StatusText tone="warn">{t("notifySettings.state.pending")}</StatusText>
	) : last?.status === "sent" ? (
		<StatusText tone="ok">
			{t("notifySettings.state.sent", { when: formatRelative(last.at) })}
		</StatusText>
	) : (
		<StatusText tone="ok">{t("notifySettings.status.ok")}</StatusText>
	);
	const failure = update.error ?? del.error;
	return (
		<GroupRow
			icon={CHANNEL_ICON[ch.kind]}
			title={
				<>
					<span className="truncate">{ch.name}</span>
					{/* 名称就是渠道类型时不再重复 */}
					{ch.name !== kindName && (
						<span className="shrink-0 font-medium text-faint text-xs">
							{kindName}
						</span>
					)}
				</>
			}
			meta={
				<>
					<span className="truncate font-mono text-xs">{ch.summary}</span>
					{tg && SEP}
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
				</>
			}
			trailing={
				<>
					<Reveal>
						<Button
							size="sm"
							disabled={test.isPending}
							onClick={() => void runTest()}
						>
							{t("notifySettings.test")}
						</Button>
					</Reveal>
					<StatusCell>{status}</StatusCell>
					<Menu
						trigger={(p) => (
							<IconButton
								icon="more"
								box="sm"
								label={t("notifySettings.more")}
								{...p}
							/>
						)}
						items={[
							{
								key: "edit",
								icon: "edit",
								label: t("common.edit"),
								onSelect: onEdit,
							},
							{
								key: "test",
								icon: "paperPlane",
								label: t("notifySettings.sendTest"),
								disabled: test.isPending,
								onSelect: () => void runTest(),
							},
							{
								key: "delete",
								icon: "trash",
								label: t("common.delete"),
								danger: true,
								disabled: del.isPending,
								onSelect: () => void remove(),
							},
						]}
					/>
					<Toggle
						hideLabel
						checked={ch.enabled}
						disabled={update.isPending}
						onChange={(v) =>
							update.mutate({ id: ch.id, patch: { enabled: v } })
						}
						label={t("notifySettings.enabled")}
					/>
				</>
			}
		>
			{(result || test.isPending || failure) && (
				<>
					{(result || test.isPending) &&
						(test.isPending || result?.ok ? (
							<p
								role="status"
								className={`flex items-center gap-1.5 text-xs ${test.isPending ? "text-muted" : "text-ok-ink"}`}
							>
								{test.isPending ? (
									<Spinner size={11} />
								) : (
									<Icon name="checkCircle" size={12} />
								)}
								{test.isPending
									? t("notifySettings.testing")
									: t("notifySettings.testOk")}
							</p>
						) : result && "delivery" in result ? (
							<DeliveryFailure message={result.delivery} />
						) : (
							<ActionError
								step="channelTest"
								error={result && "request" in result ? result.request : null}
								compact
							/>
						))}
					<ErrorText error={failure} />
				</>
			)}
		</GroupRow>
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
		<Group
			title={t("notifySettings.channels")}
			action={
				<Menu
					trigger={(p) => (
						<Button size="sm" icon="plus" {...p}>
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
				<p className="px-1.5 text-faint text-sm">
					{t("notifySettings.noChannels")}
				</p>
			)}
			{list.length > 0 && (
				<GroupList>
					{list.map((ch) => (
						<ChannelRow
							key={ch.id}
							ch={ch}
							onEdit={() => setEditor({ mode: "edit", channel: ch })}
							onLink={() => setEditor({ mode: "link", channelId: ch.id })}
						/>
					))}
				</GroupList>
			)}
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
		</Group>
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
		<Group title={t("notifySettings.recent")}>
			{recent.isPending && <Spinner />}
			<ErrorText error={recent.error} />
			{recent.data && list.length === 0 && (
				<p className="px-1.5 text-faint text-sm">{t("notify.empty")}</p>
			)}
			{list.length > 0 && (
				<GroupList>
					{list.map((n) => (
						<GroupRow
							key={n.id}
							icon={kindIcon(n.kind).icon}
							title={
								<>
									<span className="sr-only">{t(`notify.kind.${n.kind}`)} </span>
									<span className="truncate">
										{n.title || t("notify.untitled")}
									</span>
								</>
							}
							meta={
								n.deliveries.length > 0 && (
									<ul className="flex min-w-0 flex-col gap-0.5">
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
												{d.error && (
													<span className="break-all">{d.error}</span>
												)}
											</li>
										))}
									</ul>
								)
							}
							trailing={
								<span className="text-faint text-xs tabular-nums">
									{formatRelative(n.createdAt)}
								</span>
							}
						/>
					))}
				</GroupList>
			)}
		</Group>
	);
}

export default function NotificationsSettings(): ReactNode {
	// 分组间距（30px）由设置页外壳给出
	return (
		<>
			{host.mobile ? (
				<MobilePushCard />
			) : host.desktop ? (
				<DesktopDeviceCard />
			) : (
				<DeviceCard />
			)}
			<ChannelsCard />
			<BudgetGroup />
			<RecentCard />
		</>
	);
}
