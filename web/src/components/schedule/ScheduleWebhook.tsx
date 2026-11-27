/**
 * 定时的入站 Webhook（M31），放在「每次新建线程」定时的对话框里：
 * - `ScheduleWebhookSection`：没有时「开启 Webhook」；有时显示是否要求签名与创建 / 生成地址 / 上次调用的时间，
 *   操作有「换新地址」（确认后 `rotateKey`）、设置 / 更换 / 去掉签名密钥、「关闭」（确认后 DELETE）。
 *   修改立即生效，不随表单的「保存」。服务端只在生成新密钥时返回一次完整地址 `url`：放在组件状态里、
 *   在一次性展示框里给出（带复制），对话框关掉即丢弃，不进查询缓存。
 * - `WebhookPayload`：运行记录里 Webhook 运行收到的请求（不可信的外部内容）。只按等宽纯文本显示，不渲染 HTML、
 *   不把链接变成可点的地址；请求体是 JSON 时缩进美化。
 */
import { type ReactNode, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../api/client";
import {
	type Schedule,
	useDeleteScheduleWebhook,
	usePutScheduleWebhook,
} from "../../api/schedule-queries";
import { confirmAction } from "../../lib/confirm";
import { formatDateTime } from "../../lib/format";
import { CopyButton } from "../ui/CopyButton";
import { Badge, Callout } from "../ui/data";
import { Icon } from "../ui/icons";
import { Button, ErrorText, Field, TextInput } from "../ui/primitives";

/** 签名密钥上限（字符，同 `ScheduleWebhookInput.secret`）。 */
const SECRET_MAX = 256;

export function ScheduleWebhookSection({
	schedule,
}: {
	/** 已保存的 `new_thread` 定时（最新定义，`webhook` 随查询刷新） */
	schedule: Schedule;
}): ReactNode {
	const { t } = useTranslation();
	const put = usePutScheduleWebhook(schedule.id);
	const del = useDeleteScheduleWebhook(schedule.id);
	// 本次生成的完整地址：只在这里显示一次
	const [url, setUrl] = useState<string | null>(null);
	const [editing, setEditing] = useState(false);
	const [secret, setSecret] = useState("");
	const hook = schedule.webhook;
	const busy = put.isPending || del.isPending;

	const apply = async (body: Schemas["ScheduleWebhookInput"]) => {
		let res: Schemas["ScheduleWebhookResult"];
		try {
			res = await put.mutateAsync(body);
		} catch {
			return false;
		}
		if (res.url) setUrl(res.url);
		return true;
	};
	const rotate = async () => {
		if (
			await confirmAction({
				title: t("schedule.webhook.rotateConfirm"),
				body: t("schedule.webhook.rotateConfirmBody"),
				action: t("schedule.webhook.rotate"),
				danger: true,
			})
		)
			await apply({ rotateKey: true });
	};
	const saveSecret = async () => {
		if (!secret || busy) return;
		if (await apply({ secret })) {
			setSecret("");
			setEditing(false);
		}
	};
	const removeSecret = async () => {
		if (
			await confirmAction({
				title: t("schedule.webhook.removeSecretConfirm"),
				body: t("schedule.webhook.removeSecretConfirmBody"),
				action: t("schedule.webhook.removeSecretAction"),
				danger: true,
			})
		)
			await apply({ secret: "" });
	};
	const disable = async () => {
		if (
			!(await confirmAction({
				title: t("schedule.webhook.disableConfirm"),
				body: t("schedule.webhook.disableConfirmBody"),
				action: t("schedule.webhook.disable"),
				danger: true,
			}))
		)
			return;
		try {
			await del.mutateAsync();
		} catch {
			return;
		}
		setUrl(null);
		setEditing(false);
		setSecret("");
	};

	return (
		<section
			data-testid="schedule-webhook"
			className="flex flex-col gap-2 rounded-md border border-border px-3 py-2.5"
		>
			<h3 className="flex items-center gap-1.5 font-medium text-muted text-xs">
				<Icon name="webhook" size={13} aria-hidden />
				{t("schedule.webhook.title")}
			</h3>
			{!hook ? (
				<>
					<p className="text-faint text-xs">{t("schedule.webhook.intro")}</p>
					<Button
						size="sm"
						icon="webhook"
						className="self-start"
						disabled={busy}
						onClick={() => void apply({})}
					>
						{t("schedule.webhook.enable")}
					</Button>
				</>
			) : (
				<>
					<div className="flex flex-col gap-1 text-xs">
						<span className="flex flex-wrap items-center gap-x-3 gap-y-1">
							{hook.hasSecret ? (
								<Badge tone="ok" icon="lock">
									{t("schedule.webhook.signed")}
								</Badge>
							) : (
								<Badge tone="warn" icon="lockOpen">
									{t("schedule.webhook.unsigned")}
								</Badge>
							)}
							<span className="text-faint tabular-nums">
								{t("schedule.webhook.createdAt", {
									when: formatDateTime(hook.createdAt),
								})}
							</span>
							<span className="text-faint tabular-nums">
								{t("schedule.webhook.rotatedAt", {
									when: formatDateTime(hook.rotatedAt),
								})}
							</span>
							<span
								data-testid="schedule-webhook-last-call"
								className="text-faint tabular-nums"
							>
								{hook.lastCalledAt !== undefined
									? t("schedule.webhook.lastCalledAt", {
											when: formatDateTime(hook.lastCalledAt),
										})
									: t("schedule.webhook.neverCalled")}
							</span>
						</span>
						{hook.hasSecret && (
							<p className="text-faint">{t("schedule.webhook.signedHint")}</p>
						)}
					</div>
					<div className="flex flex-wrap gap-1.5">
						<Button
							size="sm"
							icon="refresh"
							disabled={busy}
							onClick={() => void rotate()}
						>
							{t("schedule.webhook.rotate")}
						</Button>
						<Button
							size="sm"
							icon="key"
							disabled={busy}
							aria-expanded={editing}
							onClick={() => setEditing((v) => !v)}
						>
							{hook.hasSecret
								? t("schedule.webhook.changeSecret")
								: t("schedule.webhook.setSecret")}
						</Button>
						{hook.hasSecret && (
							<Button
								size="sm"
								disabled={busy}
								onClick={() => void removeSecret()}
							>
								{t("schedule.webhook.removeSecret")}
							</Button>
						)}
						<Button
							size="sm"
							variant="danger"
							disabled={busy}
							onClick={() => void disable()}
						>
							{t("schedule.webhook.disable")}
						</Button>
					</div>
					{editing && (
						<div className="flex flex-col gap-2">
							<Field
								label={t("schedule.webhook.secretLabel")}
								hint={t("schedule.webhook.secretHint")}
							>
								<TextInput
									type="password"
									autoComplete="new-password"
									className="font-mono"
									maxLength={SECRET_MAX}
									value={secret}
									onChange={(e) => setSecret(e.target.value)}
									onKeyDown={(e) => {
										// 在定时表单里：回车只保存密钥，不提交整张表单
										if (e.key !== "Enter") return;
										e.preventDefault();
										void saveSecret();
									}}
								/>
							</Field>
							<span className="flex gap-1.5 self-end">
								<Button
									size="sm"
									onClick={() => {
										setEditing(false);
										setSecret("");
									}}
								>
									{t("common.cancel")}
								</Button>
								<Button
									size="sm"
									variant="primary"
									disabled={!secret || busy}
									onClick={() => void saveSecret()}
								>
									{t("schedule.webhook.secretSave")}
								</Button>
							</span>
						</div>
					)}
				</>
			)}
			{url && (
				<div data-testid="schedule-webhook-url">
					<Callout tone="warn" title={t("schedule.webhook.urlTitle")}>
						<span className="flex flex-col gap-1.5">
							<span>{t("schedule.webhook.urlHint")}</span>
							<span className="flex items-center justify-between gap-2">
								<span className="font-medium">
									{t("schedule.webhook.urlLabel")}
								</span>
								<CopyButton
									text={url}
									label={t("common.copy")}
									testId="schedule-webhook-copy"
								/>
							</span>
							<code className="select-all break-all rounded-sm bg-inset px-2 py-1 font-mono text-fg">
								{url}
							</code>
							<span>{t("schedule.webhook.usage")}</span>
						</span>
					</Callout>
				</div>
			)}
			<ErrorText error={put.error ?? del.error} />
			<Callout tone="neutral" icon="shield">
				{t("schedule.webhook.trust")}
			</Callout>
			{hook && (
				<p className="text-faint text-xs">{t("schedule.webhook.immediate")}</p>
			)}
		</section>
	);
}

/** 请求体是 JSON 时缩进两格，否则原文。 */
function prettyBody(body: string, contentType: string | undefined): string {
	const trimmed = body.trim();
	const looksJson =
		/json/i.test(contentType ?? "") ||
		trimmed.startsWith("{") ||
		trimmed.startsWith("[");
	if (!looksJson) return body;
	try {
		return JSON.stringify(JSON.parse(trimmed), null, 2);
	} catch {
		return body;
	}
}

/** `键: 值` 一行一个（按键排序，便于对照）。 */
function lines(map: Record<string, string> | undefined): string {
	return Object.entries(map ?? {})
		.sort(([a], [b]) => a.localeCompare(b))
		.map(([k, v]) => `${k}: ${v}`)
		.join("\n");
}

const PRE =
	"max-h-60 overflow-auto whitespace-pre-wrap break-all rounded-sm bg-inset px-2 py-1 font-mono text-2xs text-fg";

/** Webhook 运行收到的请求：默认收起，展开后按等宽纯文本显示。 */
export function WebhookPayload({
	payload,
}: {
	payload: Schemas["ScheduleWebhookPayload"];
}): ReactNode {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const bodyId = useId();
	const headers = lines(payload.headers);
	const query = lines(payload.query);
	return (
		<div className="flex flex-col gap-1">
			<button
				type="button"
				aria-expanded={open}
				aria-controls={bodyId}
				onClick={() => setOpen((v) => !v)}
				className="inline-flex items-center gap-1 self-start text-faint hover:text-fg"
			>
				<Icon name={open ? "chevronDown" : "chevronRight"} size={12} />
				{open ? t("schedule.payload.hide") : t("schedule.payload.show")}
			</button>
			{open && (
				<div
					id={bodyId}
					data-testid="schedule-run-payload"
					className="flex flex-col gap-1.5 border-border border-l-2 pl-2"
				>
					<span className="inline-flex items-center gap-1 text-warn">
						<Icon name="alert" size={12} aria-hidden />
						{t("schedule.payload.untrusted")}
					</span>
					{payload.contentType && (
						<span className="text-muted">
							{t("schedule.payload.contentType")}{" "}
							<code className="font-mono text-fg">{payload.contentType}</code>
						</span>
					)}
					{headers && (
						<>
							<span className="text-muted">
								{t("schedule.payload.headers")}
							</span>
							<pre className={PRE}>{headers}</pre>
						</>
					)}
					{query && (
						<>
							<span className="text-muted">{t("schedule.payload.query")}</span>
							<pre className={PRE}>{query}</pre>
						</>
					)}
					<span className="text-muted">{t("schedule.payload.body")}</span>
					<pre className={PRE}>
						{payload.body
							? prettyBody(payload.body, payload.contentType)
							: t("schedule.payload.empty")}
					</pre>
				</div>
			)}
		</div>
	);
}
