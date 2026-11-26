/**
 * 设置 → Git 与 GitHub 的 GitHub 部分（M14 / M15），分组：
 * 1. OAuth App：一行（Client ID、webhook 基址）· 编辑 · ⋯（删除配置）；未配置时一行说明 + 「配置」。
 *    编辑对话框：需要在 GitHub 登记的回调地址（可复制，未配置时也由服务端给出）、Client ID、Client Secret（已配置时留空 = 沿用）、
 *    仓库 webhook 基址（默认 = public_url）。删除时服务端先删 Oryn 建的仓库 hook，再断开两个账号。
 * 2. GitHub 账号：「我的账号」「Agent 账号」两行，显示实际授予的 scope，缺 scope 时提示重新连接；连接时给出授权链接
 *    （可在本浏览器继续，也可复制到登录了目标账号的浏览器）。末行选择自动化任务（GitHub 触发与项目级定时）用哪个账号。
 * 3. 仓库 webhook：已绑定仓库的 hook 状态与最近投递；组织限制时给出申请批准的链接。
 * 4. 审计：agent 经出站代理发起的 GitHub 写操作，按仓库过滤、分页加载。
 * 后三组在配置了 OAuth App 之后才出现。OAuth 回调带着 query 回到本页：`GitHubCallbackNotice` 显示结果并清掉 query。
 */
import { type ReactNode, useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate, useSearchParams } from "react-router";
import {
	type GitHubHook,
	type GitHubHookStatus,
	type GitHubIdentity,
	type GitHubIdentitySlot,
	type GitHubStatus,
	githubOrgApprovalUrl,
	githubSsoUrl,
	useAuthorizeGitHubIdentity,
	useDeleteGitHubOAuthApp,
	useDisconnectGitHubIdentity,
	useGitHubAudit,
	useGitHubHooks,
	useGitHubStatus,
	useSetGitHubAutomation,
	useSetGitHubOAuthApp,
} from "../../api/github-queries";
import { CopyButton } from "../../components/ui/CopyButton";
import { Badge, Callout, type Tone } from "../../components/ui/data";
import { Icon } from "../../components/ui/icons";
import {
	Button,
	Dialog,
	ErrorText,
	IconButton,
	Menu,
	Select,
	Spinner,
	TextInput,
} from "../../components/ui/primitives";
import {
	FormField,
	Group,
	GroupList,
	GroupPanel,
	GroupRow,
	StatusCell,
	StatusText,
} from "../../components/ui/workbench";
import { confirmAction } from "../../lib/confirm";
import { formatDateTime, formatRelative } from "../../lib/format";
import { host } from "../../lib/host";
import { useIndex } from "../../state/stores";

/** 回调页带回来的错误码里有专门文案的；其余显示通用文案。 */
const KNOWN_ERRORS = [
	"state_invalid",
	"exchange_failed",
	"same_account",
	"access_denied",
	"github_not_configured",
] as const;

type KnownError = (typeof KNOWN_ERRORS)[number];

type Notice =
	| { tone: "err"; code: KnownError | "unknown" }
	| { tone: "ok"; slot: GitHubIdentitySlot };

/** 从回调 query 读出要显示的结果（纯函数）。 */
export function callbackNotice(params: URLSearchParams): Notice | null {
	const error = params.get("error");
	if (error)
		return {
			tone: "err",
			code: (KNOWN_ERRORS as readonly string[]).includes(error)
				? (error as KnownError)
				: "unknown",
		};
	const slot = params.get("connected");
	if (slot === "user" || slot === "agent") return { tone: "ok", slot };
	return null;
}

function NoticeText({ notice }: { notice: Notice }): ReactNode {
	const { t } = useTranslation();
	if (notice.tone === "err") return t(`github.error.${notice.code}`);
	return t("github.notice.connected", {
		slot: t(`github.identity.${notice.slot}`),
	});
}

/** OAuth 回调结果（?connected= / ?error=）：只在进入页面时读一次，随后清掉 query，提示留在本地状态里。 */
export function GitHubCallbackNotice(): ReactNode {
	const [params] = useSearchParams();
	const navigate = useNavigate();
	const [notice] = useState(() => callbackNotice(params));
	const handled = useRef(false);
	useEffect(() => {
		if (handled.current) return;
		handled.current = true;
		if ([...params.keys()].length > 0)
			navigate("/settings/git", { replace: true });
	}, [params, navigate]);
	if (!notice) return null;
	return (
		<div data-testid="github-notice">
			<Callout
				tone={notice.tone}
				role={notice.tone === "err" ? "alert" : "status"}
			>
				<NoticeText notice={notice} />
			</Callout>
		</div>
	);
}

/** 外链（新窗口打开，带外链图标）。 */
function ExternalLink({
	href,
	children,
}: {
	href: string;
	children: ReactNode;
}): ReactNode {
	return (
		<a
			href={href}
			target="_blank"
			rel="noopener noreferrer"
			className="inline-flex items-center gap-1 text-accent-ink underline-offset-2 hover:underline"
		>
			{children}
			<Icon name="external" size={11} aria-hidden />
		</a>
	);
}

// ---------------------------------------------------------------- OAuth App

function OAuthAppDialog({
	status,
	onClose,
}: {
	status: GitHubStatus;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const formId = useId();
	const save = useSetGitHubOAuthApp();
	const app = status.oauthApp;
	const [clientId, setClientId] = useState(app?.clientId ?? "");
	const [secret, setSecret] = useState("");
	const [webhookBase, setWebhookBase] = useState(status.webhookBase);
	const callback = status.callbackUrl;
	// 首次保存必须有 Secret；之后留空 = 沿用已存的值
	const valid = clientId.trim() !== "" && (!!app || secret.trim() !== "");
	return (
		<Dialog
			open
			onClose={onClose}
			title={t(app ? "github.oauth.editTitle" : "github.oauth.setupTitle")}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						type="submit"
						form={formId}
						variant="primary"
						disabled={!valid || save.isPending}
					>
						{save.isPending && <Spinner size={13} />}
						{t("common.save")}
					</Button>
				</>
			}
		>
			<form
				id={formId}
				className="flex flex-col gap-4"
				data-testid="github-oauth-app"
				onSubmit={(e) => {
					e.preventDefault();
					if (!valid || save.isPending) return;
					save.mutate(
						{
							clientId: clientId.trim(),
							...(secret.trim() ? { clientSecret: secret.trim() } : {}),
							...(webhookBase.trim()
								? { webhookBase: webhookBase.trim() }
								: {}),
						},
						{ onSuccess: onClose },
					);
				}}
			>
				<p className="text-[13px] text-muted">
					{t("github.oauth.intro")}{" "}
					<ExternalLink
						href={`${status.webUrl.replace(/\/+$/, "")}/settings/applications/new`}
					>
						{t("github.oauth.register")}
					</ExternalLink>
				</p>
				<FormField
					label={t("github.oauth.callback")}
					aside={t("github.oauth.callbackHint")}
				>
					{(p) => (
						<div className="flex items-start gap-2">
							<code
								id={p.id}
								data-testid="github-oauth-callback"
								className="min-w-0 flex-1 break-all rounded-sm bg-inset px-2 py-1 font-mono text-fg text-xs"
							>
								{callback}
							</code>
							<CopyButton
								text={callback}
								ariaLabel={t("github.oauth.copyCallback")}
							/>
						</div>
					)}
				</FormField>
				{/* Client ID 与 Secret 是一对凭据，宽屏并排一行 */}
				<div className="grid gap-4 sm:grid-cols-2">
					<FormField label={t("github.oauth.clientId")}>
						{(p) => (
							<TextInput
								{...p}
								required
								autoComplete="off"
								spellCheck={false}
								value={clientId}
								onChange={(e) => setClientId(e.target.value)}
							/>
						)}
					</FormField>
					<FormField
						label={t("github.oauth.clientSecret")}
						aside={app ? t("github.oauth.secretKeep") : undefined}
					>
						{(p) => (
							<TextInput
								{...p}
								type="password"
								required={!app}
								autoComplete="new-password"
								value={secret}
								onChange={(e) => setSecret(e.target.value)}
							/>
						)}
					</FormField>
				</div>
				<FormField label={t("github.oauth.webhookBase")}>
					{(p) => (
						<TextInput
							{...p}
							type="url"
							value={webhookBase}
							placeholder={status.webhookBase}
							onChange={(e) => setWebhookBase(e.target.value)}
						/>
					)}
				</FormField>
				<p className="-mt-2 text-faint text-xs">
					{t("github.oauth.webhookBaseHint")}
				</p>
				<ErrorText error={save.error} />
			</form>
		</Dialog>
	);
}

function OAuthAppGroup({ status }: { status: GitHubStatus }): ReactNode {
	const { t } = useTranslation();
	const del = useDeleteGitHubOAuthApp();
	const [editing, setEditing] = useState(false);
	const app = status.oauthApp;
	return (
		<Group title={t("github.oauth.title")}>
			<GroupList>
				<GroupRow
					icon="key"
					title={
						<span
							className="flex min-w-0 items-center gap-1.5"
							data-testid="github-oauth-row"
						>
							{app ? (
								<>
									<span className="shrink-0">{t("github.oauth.clientId")}</span>
									<span className="truncate font-mono font-normal text-[13px] text-muted">
										{app.clientId}
									</span>
								</>
							) : (
								t("github.oauth.notConfigured")
							)}
						</span>
					}
					meta={
						app ? (
							<span className="min-w-0 truncate">
								{t("github.oauth.webhookAt", { url: status.webhookBase })}
							</span>
						) : (
							t("github.oauth.setupHint")
						)
					}
					trailing={
						app ? (
							<>
								<Button size="sm" onClick={() => setEditing(true)}>
									{t("common.edit")}
								</Button>
								<Menu
									trigger={(p) => (
										<IconButton
											icon="more"
											box="sm"
											label={t("github.oauth.more")}
											{...p}
										/>
									)}
									items={[
										{
											key: "delete",
											icon: "trash",
											label: t("github.oauth.delete"),
											danger: true,
											disabled: del.isPending,
											onSelect: async () => {
												if (
													await confirmAction({
														title: t("github.oauth.deleteConfirm"),
														body: t("github.oauth.deleteConfirmBody"),
														action: t("common.delete"),
														danger: true,
													})
												)
													del.mutate();
											},
										},
									]}
								/>
							</>
						) : (
							<Button
								size="sm"
								variant="primary"
								onClick={() => setEditing(true)}
							>
								{t("github.oauth.setup")}
							</Button>
						)
					}
				>
					{del.error ? <ErrorText error={del.error} /> : undefined}
				</GroupRow>
			</GroupList>
			{editing && (
				<OAuthAppDialog status={status} onClose={() => setEditing(false)} />
			)}
		</Group>
	);
}

// ---------------------------------------------------------------- GitHub 账号

function Avatar({ identity }: { identity: GitHubIdentity }): ReactNode {
	if (!identity.avatarUrl) return null;
	return (
		<img
			src={identity.avatarUrl}
			alt=""
			width={18}
			height={18}
			className="h-[18px] w-[18px] shrink-0 rounded-full bg-inset"
		/>
	);
}

function IdentityRow({
	slot,
	identity,
	onConnect,
	connecting,
}: {
	slot: GitHubIdentitySlot;
	identity: GitHubIdentity | undefined;
	onConnect: () => void;
	connecting: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const disconnect = useDisconnectGitHubIdentity();
	const label = t(`github.identity.${slot}`);
	const missing = identity?.missingScopes ?? [];
	const needsReconnect =
		!!identity && (identity.status === "reauth" || missing.length > 0);
	const scopes = identity?.scopes ?? [];
	return (
		<GroupRow
			icon={slot === "agent" ? "robot" : "user"}
			title={
				<span
					className="flex min-w-0 items-center gap-1.5"
					data-testid={`github-identity-${slot}`}
				>
					<span className="shrink-0">{label}</span>
					{identity && <Avatar identity={identity} />}
					{identity && (
						<span className="truncate font-normal text-muted">
							@{identity.login}
						</span>
					)}
					{identity?.status === "reauth" && (
						<Badge tone="warn">{t("github.identity.reauth")}</Badge>
					)}
					{identity?.status === "ok" && missing.length > 0 && (
						<Badge tone="warn">{t("github.identity.scopesMissing")}</Badge>
					)}
				</span>
			}
			meta={
				identity ? (
					<span>
						{identity.name ? `${identity.name} · ` : ""}
						{t("github.identity.connectedAt", {
							when: formatRelative(identity.connectedAt),
						})}
					</span>
				) : (
					<span>
						{t("github.identity.notConnected")}
						{slot === "agent" && ` · ${t("github.identity.agentHint")}`}
					</span>
				)
			}
			trailing={
				<>
					{(!identity || needsReconnect) && (
						<Button
							size="sm"
							variant={identity ? "primary" : "secondary"}
							disabled={connecting}
							onClick={onConnect}
						>
							{identity
								? t("github.identity.reconnect")
								: t("github.identity.connect")}
						</Button>
					)}
					{identity && (
						<Menu
							trigger={(p) => (
								<IconButton
									icon="more"
									box="sm"
									label={t("github.identity.more", { slot: label })}
									{...p}
								/>
							)}
							items={[
								{
									key: "disconnect",
									icon: "trash",
									label: t("github.identity.disconnect", { slot: label }),
									danger: true,
									disabled: disconnect.isPending,
									onSelect: async () => {
										if (
											await confirmAction({
												title: t("github.identity.disconnectConfirm", {
													slot: label,
													login: identity.login,
												}),
												action: t("common.disconnect"),
												danger: true,
											})
										)
											disconnect.mutate(slot);
									},
								},
							]}
						/>
					)}
				</>
			}
		>
			{(scopes.length > 0 || missing.length > 0 || disconnect.error) && (
				<div className="flex flex-col gap-1 text-[12.5px] text-muted">
					{identity && scopes.length > 0 && (
						<span data-testid={`github-scopes-${slot}`}>
							{t("github.identity.scopes")}{" "}
							<span className="font-mono">{scopes.join(" ")}</span>
						</span>
					)}
					{missing.length > 0 && (
						<span className="text-warn-ink" role="alert">
							{t("github.identity.missingHint")}{" "}
							<span className="font-mono">{missing.join(" ")}</span>
						</span>
					)}
					<ErrorText error={disconnect.error} />
				</div>
			)}
		</GroupRow>
	);
}

/** 自动化任务（GitHub 触发、项目级定时）使用哪个 GitHub 账号；Agent 账号未连接时不可选。 */
function AutomationRow({ status }: { status: GitHubStatus }): ReactNode {
	const { t } = useTranslation();
	const set = useSetGitHubAutomation();
	const agentReady = !!status.identities.agent;
	const current = set.isPending ? set.variables : status.automationIdentity;
	return (
		<GroupRow
			icon="bolt"
			title={t("github.automation.title")}
			meta={
				agentReady
					? t("github.automation.legend")
					: t("github.automation.agentMissing")
			}
			trailing={
				<Select<GitHubIdentitySlot>
					className="w-36"
					aria-label={t("github.automation.title")}
					value={current ?? "user"}
					disabled={set.isPending}
					options={(["user", "agent"] as const).map((slot) => ({
						value: slot,
						label: t(`github.identity.${slot}`),
						disabled: slot === "agent" && !agentReady,
					}))}
					onChange={(slot) => set.mutate(slot)}
				/>
			}
		>
			{set.error ? <ErrorText error={set.error} /> : undefined}
		</GroupRow>
	);
}

function AccountsGroup({ status }: { status: GitHubStatus }): ReactNode {
	const { t } = useTranslation();
	const authorize = useAuthorizeGitHubIdentity();
	const [pending, setPending] = useState<{
		slot: GitHubIdentitySlot;
		url: string;
	} | null>(null);
	const connect = (slot: GitHubIdentitySlot) =>
		authorize.mutate(slot, {
			onSuccess: (res) => setPending({ slot, url: res.url }),
		});
	return (
		<Group title={t("github.identity.title")}>
			<GroupList>
				{(["user", "agent"] as const).map((slot) => (
					<IdentityRow
						key={slot}
						slot={slot}
						identity={status.identities[slot]}
						connecting={authorize.isPending}
						onConnect={() => connect(slot)}
					/>
				))}
				<AutomationRow status={status} />
			</GroupList>
			<ErrorText error={authorize.error} />
			<Dialog
				open={pending !== null}
				onClose={() => setPending(null)}
				title={
					pending
						? t("github.identity.dialogTitle", {
								slot: t(`github.identity.${pending.slot}`),
							})
						: ""
				}
				footer={
					pending && (
						<Button
							variant="primary"
							icon="external"
							onClick={() => {
								if (host.kind === "tauri") {
									window.open(pending.url, "_blank", "noopener");
								} else {
									window.location.assign(pending.url);
								}
							}}
						>
							{t("github.identity.continueHere")}
						</Button>
					)
				}
			>
				{pending && (
					<div className="flex flex-col gap-2">
						<p className="text-muted text-sm">
							{t("github.identity.copyHint")}
						</p>
						<div className="flex items-start gap-2">
							<code className="min-w-0 flex-1 break-all rounded-sm bg-inset px-2 py-1 font-mono text-fg text-xs">
								{pending.url}
							</code>
							<CopyButton
								text={pending.url}
								label={t("github.identity.copyLink")}
							/>
						</div>
					</div>
				)}
			</Dialog>
		</Group>
	);
}

// ---------------------------------------------------------------- 仓库 webhook

const HOOK_TONE: Record<GitHubHookStatus, Tone> = {
	active: "ok",
	no_admin: "neutral",
	org_restricted: "warn",
	sso_required: "warn",
	error: "err",
};

function HookRow({
	hook,
	approvalUrl,
}: {
	hook: GitHubHook;
	/** 组织批准 OAuth App 的申请页；没有 OAuth App 时为 undefined */
	approvalUrl: string | undefined;
}): ReactNode {
	const { t } = useTranslation();
	const sso =
		hook.status === "sso_required" ? githubSsoUrl(hook.error) : undefined;
	const approval = hook.status === "org_restricted" ? approvalUrl : undefined;
	return (
		<GroupRow
			icon="webhook"
			title={
				<span
					className="truncate font-mono"
					data-testid="github-hook"
					data-status={hook.status}
				>
					{hook.fullName}
				</span>
			}
			meta={
				<span>
					{hook.lastDeliveryAt
						? t("github.hooks.lastDelivery", {
								when: formatRelative(hook.lastDeliveryAt),
								source: t(
									`github.hooks.source.${hook.lastDeliverySource ?? "webhook"}`,
								),
							})
						: t("github.hooks.noDelivery")}
				</span>
			}
			trailing={
				<StatusCell>
					<StatusText tone={HOOK_TONE[hook.status]}>
						{t(`github.hooks.status.${hook.status}`)}
					</StatusText>
				</StatusCell>
			}
		>
			{/* 只有非正常状态才展开说明：正常行不必再念一遍 */}
			{hook.status !== "active" && (
				<div className="flex flex-col gap-1 text-[12.5px] text-muted">
					<span>{t(`github.hooks.explain.${hook.status}`)}</span>
					{approval && (
						<span>
							<ExternalLink href={approval}>
								{t("github.hooks.requestApproval")}
							</ExternalLink>
						</span>
					)}
					{sso && (
						<span>
							<ExternalLink href={sso}>
								{t("github.hooks.authorizeSso")}
							</ExternalLink>
						</span>
					)}
					{hook.status === "error" && hook.error && (
						<span className="break-all text-err">{hook.error}</span>
					)}
				</div>
			)}
		</GroupRow>
	);
}

function HooksGroup({ status }: { status: GitHubStatus }): ReactNode {
	const { t } = useTranslation();
	const hooks = useGitHubHooks();
	const list = hooks.data?.hooks ?? [];
	const clientId = status.oauthApp?.clientId;
	const approvalUrl = clientId
		? githubOrgApprovalUrl(status.webUrl, clientId)
		: undefined;
	return (
		<Group
			title={t("github.hooks.title")}
			meta={
				status.lastDeliveryAt !== undefined && (
					<span data-testid="github-last-delivery">
						{t("github.hooks.lastAny", {
							when: formatRelative(status.lastDeliveryAt),
							source: t(
								`github.hooks.source.${status.lastDeliverySource ?? "webhook"}`,
							),
						})}
					</span>
				)
			}
		>
			{hooks.isPending && <Spinner size={12} />}
			<ErrorText error={hooks.error} />
			{list.length > 0 && (
				<GroupList>
					{list.map((h) => (
						<HookRow key={h.repoId} hook={h} approvalUrl={approvalUrl} />
					))}
				</GroupList>
			)}
			<p className="px-1.5 text-faint text-xs">
				{hooks.isSuccess && list.length === 0
					? `${t("github.hooks.empty")} `
					: ""}
				{t("github.hooks.intro")}
			</p>
		</Group>
	);
}

// ---------------------------------------------------------------- 审计

function AuditThread({ id }: { id: string }): ReactNode {
	const title = useIndex((s) => s.threads[id]?.title);
	return (
		<Link
			to={`/t/${id}`}
			title={id}
			className="block max-w-48 truncate text-accent-ink hover:underline"
		>
			{title || id}
		</Link>
	);
}

function AuditGroup(): ReactNode {
	const { t } = useTranslation();
	const listId = useId();
	const hooks = useGitHubHooks();
	const [draft, setDraft] = useState("");
	const [repo, setRepo] = useState("");
	const audit = useGitHubAudit(repo ? { repo } : {});
	const entries = audit.data?.pages.flatMap((p) => p.entries) ?? [];
	const th = "px-1 py-1 font-medium";
	const td = "px-1 py-1.5 align-top";
	return (
		<Group title={t("github.audit.title")} meta={t("github.audit.intro")}>
			<GroupPanel>
				<form
					className="flex flex-wrap items-center gap-2"
					onSubmit={(e) => {
						e.preventDefault();
						setRepo(draft.trim());
					}}
				>
					<TextInput
						className="max-w-64"
						aria-label={t("github.audit.repoFilter")}
						placeholder="owner/repo"
						list={listId}
						spellCheck={false}
						value={draft}
						onChange={(e) => setDraft(e.target.value)}
					/>
					<datalist id={listId}>
						{hooks.data?.hooks.map((h) => (
							<option key={h.repoId} value={h.fullName} />
						))}
					</datalist>
					<Button type="submit" size="sm" icon="funnel">
						{t("github.audit.filter")}
					</Button>
					{repo && (
						<Button
							size="sm"
							variant="ghost"
							onClick={() => {
								setDraft("");
								setRepo("");
							}}
						>
							{t("github.audit.clearFilter")}
						</Button>
					)}
				</form>
				{audit.isPending && <Spinner size={12} />}
				<ErrorText error={audit.error} />
				{audit.isSuccess && entries.length === 0 && (
					<p className="text-faint text-xs">{t("github.audit.empty")}</p>
				)}
				{entries.length > 0 && (
					<div className="-mx-1 overflow-x-auto">
						<table data-testid="github-audit" className="w-full text-xs">
							<thead>
								<tr className="text-left text-faint">
									<th className={th}>{t("github.audit.col.time")}</th>
									<th className={th}>{t("github.audit.col.thread")}</th>
									<th className={th}>{t("github.audit.col.slot")}</th>
									<th className={th}>{t("github.audit.col.method")}</th>
									<th className={th}>{t("github.audit.col.op")}</th>
									<th className={th}>{t("github.audit.col.repo")}</th>
									<th className={`${th} text-right`}>
										{t("github.audit.col.status")}
									</th>
									<th className={th}>{t("github.audit.col.gate")}</th>
								</tr>
							</thead>
							<tbody>
								{entries.map((e) => (
									<tr
										key={e.id}
										data-testid="github-audit-row"
										className="border-border border-t text-muted"
									>
										<td className={`${td} whitespace-nowrap tabular-nums`}>
											{formatDateTime(e.at)}
										</td>
										<td className={td}>
											{e.threadId && <AuditThread id={e.threadId} />}
										</td>
										<td className={`${td} whitespace-nowrap`}>
											{t(`github.identity.${e.slot}`)}
										</td>
										<td className={`${td} font-mono text-fg`}>{e.method}</td>
										<td className={`${td} break-all font-mono text-fg`}>
											{e.op}
										</td>
										<td className={`${td} whitespace-nowrap font-mono`}>
											{e.repo}
										</td>
										<td
											className={`${td} text-right font-mono tabular-nums ${e.status >= 400 ? "text-err" : ""}`}
										>
											{e.status}
										</td>
										<td className={td}>{e.gate}</td>
									</tr>
								))}
							</tbody>
						</table>
					</div>
				)}
				{audit.hasNextPage && (
					<div>
						<Button
							size="sm"
							variant="ghost"
							disabled={audit.isFetchingNextPage}
							onClick={() => void audit.fetchNextPage()}
						>
							{audit.isFetchingNextPage && <Spinner size={12} />}
							{t("github.audit.more")}
						</Button>
					</div>
				)}
			</GroupPanel>
		</Group>
	);
}

export default function GitHubSettings(): ReactNode {
	const { t } = useTranslation();
	const status = useGitHubStatus();
	const data = status.data;
	return (
		<>
			{data ? (
				<OAuthAppGroup status={data} />
			) : (
				<Group title={t("github.oauth.title")}>
					{status.isPending && <Spinner />}
					<ErrorText error={status.error} />
				</Group>
			)}
			{data?.oauthApp && (
				<>
					<AccountsGroup status={data} />
					<HooksGroup status={data} />
					<AuditGroup />
				</>
			)}
		</>
	);
}
