/**
 * 设置页「GitHub」（M14 / M15，default export，由设置页接入），四张卡片：
 * 1. OAuth App：Client ID、Client Secret（已配置时留空 = 沿用）、需要在 GitHub 登记的回调地址（可复制，未配置时也由服务端给出）、
 *    仓库 webhook 基址（默认 = public_url）；可删除配置（服务端先删 Oryn 建的仓库 hook，再断开两个身份）。
 * 2. 身份：「我的账号」「Agent 账号」两个身份槽位；显示实际授予的 scope，缺 scope 时提示重新授权；
 *    连接时给出授权链接（可在本浏览器继续，也可复制到登录了目标账号的浏览器）。卡片下部选择自动化任务
 *    （GitHub 触发与项目级定时）使用的身份。
 * 3. 仓库 webhook：已绑定仓库的 hook 状态与最近投递；组织限制时给出申请批准的链接。
 * 4. 审计：agent 经出站代理发起的 GitHub 写操作，按仓库过滤、分页加载。
 * 后三张卡片在配置了 OAuth App 之后才出现。OAuth 回调带着 query 回到本页：这里显示结果并清掉 query。
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
import {
	Badge,
	Callout,
	Card,
	ListRow,
	type Tone,
} from "../../components/ui/data";
import { Icon } from "../../components/ui/icons";
import {
	Button,
	Dialog,
	ErrorText,
	Field,
	IconButton,
	Radio,
	Spinner,
	TextInput,
} from "../../components/ui/primitives";
import { confirmAction } from "../../lib/confirm";
import { formatDateTime, formatRelative } from "../../lib/format";
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

function LeadIcon({
	name,
}: {
	name: "github" | "user" | "robot" | "webhook";
}): ReactNode {
	return (
		<span className="grid h-8 w-8 place-items-center rounded-md border border-border bg-raised text-muted">
			<Icon name={name} size={15} />
		</span>
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
			className="inline-flex items-center gap-1 text-accent hover:underline"
		>
			{children}
			<Icon name="external" size={11} aria-hidden />
		</a>
	);
}

function OAuthAppCard({ status }: { status: GitHubStatus }): ReactNode {
	const { t } = useTranslation();
	const save = useSetGitHubOAuthApp();
	const del = useDeleteGitHubOAuthApp();
	const app = status.oauthApp;
	const [clientId, setClientId] = useState(app?.clientId ?? "");
	const [secret, setSecret] = useState("");
	const [webhookBase, setWebhookBase] = useState(status.webhookBase);
	const [saved, setSaved] = useState(false);
	const callback = status.callbackUrl;
	// 首次保存必须有 Secret；之后留空 = 沿用已存的值
	const valid = clientId.trim() !== "" && (!!app || secret.trim() !== "");
	const busy = save.isPending || del.isPending;
	return (
		<Card title={t("github.oauth.title")} icon="key">
			<form
				className="flex flex-col gap-3"
				data-testid="github-oauth-app"
				onSubmit={(e) => {
					e.preventDefault();
					if (!valid || busy) return;
					setSaved(false);
					save.mutate(
						{
							clientId: clientId.trim(),
							...(secret.trim() ? { clientSecret: secret.trim() } : {}),
							...(webhookBase.trim()
								? { webhookBase: webhookBase.trim() }
								: {}),
						},
						{
							onSuccess: () => {
								setSecret("");
								setSaved(true);
							},
						},
					);
				}}
			>
				<p className="text-muted text-xs">
					{t("github.oauth.intro")}{" "}
					<ExternalLink
						href={`${status.webUrl.replace(/\/+$/, "")}/settings/applications/new`}
					>
						{t("github.oauth.register")}
					</ExternalLink>
				</p>
				<div className="flex flex-col gap-1 text-sm">
					<span className="font-medium text-muted text-xs">
						{t("github.oauth.callback")}
					</span>
					<div className="flex items-start gap-2">
						<code
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
					<span className="text-faint text-xs">
						{t("github.oauth.callbackHint")}
					</span>
				</div>
				{/* Client ID 与 Secret 是一对凭据，宽屏并排一行 */}
				<div className="grid gap-3 sm:grid-cols-2">
					<Field label={t("github.oauth.clientId")}>
						<TextInput
							required
							autoComplete="off"
							spellCheck={false}
							value={clientId}
							onChange={(e) => setClientId(e.target.value)}
						/>
					</Field>
					<Field
						label={t("github.oauth.clientSecret")}
						hint={app ? t("github.oauth.secretKeep") : undefined}
					>
						<TextInput
							type="password"
							required={!app}
							autoComplete="new-password"
							value={secret}
							onChange={(e) => setSecret(e.target.value)}
						/>
					</Field>
				</div>
				<Field
					label={t("github.oauth.webhookBase")}
					hint={t("github.oauth.webhookBaseHint")}
				>
					<TextInput
						type="url"
						value={webhookBase}
						placeholder={status.webhookBase}
						onChange={(e) => setWebhookBase(e.target.value)}
					/>
				</Field>
				<div className="flex flex-wrap items-center gap-2">
					<Button type="submit" variant="primary" disabled={!valid || busy}>
						{save.isPending && <Spinner size={13} />}
						{t("common.save")}
					</Button>
					{app && (
						<Button
							variant="danger"
							icon="trash"
							disabled={busy}
							onClick={async () => {
								if (
									await confirmAction({
										title: t("github.oauth.deleteConfirm"),
										body: t("github.oauth.deleteConfirmBody"),
										action: t("common.delete"),
										danger: true,
									})
								)
									del.mutate();
							}}
						>
							{t("github.oauth.delete")}
						</Button>
					)}
					{saved && (
						<span role="status" className="text-ok text-xs">
							{t("github.oauth.saved")}
						</span>
					)}
				</div>
				<ErrorText error={save.error ?? del.error} />
			</form>
		</Card>
	);
}

function Avatar({ identity }: { identity: GitHubIdentity }): ReactNode {
	if (!identity.avatarUrl) return <LeadIcon name="user" />;
	return (
		<img
			src={identity.avatarUrl}
			alt=""
			width={32}
			height={32}
			className="h-8 w-8 rounded-md border border-border bg-raised"
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
	return (
		<li data-testid={`github-identity-${slot}`}>
			<ListRow
				leading={
					identity ? (
						<Avatar identity={identity} />
					) : (
						<LeadIcon name={slot === "agent" ? "robot" : "user"} />
					)
				}
				title={
					<>
						<span className="truncate font-medium">{label}</span>
						{identity && (
							<span className="truncate text-muted">@{identity.login}</span>
						)}
						{identity?.status === "reauth" && (
							<Badge tone="warn">{t("github.identity.reauth")}</Badge>
						)}
						{identity?.status === "ok" && missing.length > 0 && (
							<Badge tone="warn">{t("github.identity.scopesMissing")}</Badge>
						)}
					</>
				}
				meta={
					<>
						{identity ? (
							<span>
								{identity.name ? `${identity.name} · ` : ""}
								{t("github.identity.connectedAt", {
									when: formatRelative(identity.connectedAt),
								})}
							</span>
						) : (
							<span>{t("github.identity.notConnected")}</span>
						)}
						{identity && (identity.scopes?.length ?? 0) > 0 && (
							<span className="basis-full" data-testid="github-scopes">
								{t("github.identity.scopes")}{" "}
								<span className="font-mono">{identity.scopes?.join(" ")}</span>
							</span>
						)}
						{missing.length > 0 && (
							<span className="basis-full text-warn-ink" role="alert">
								{t("github.identity.missingHint")}{" "}
								<span className="font-mono">{missing.join(" ")}</span>
							</span>
						)}
						{slot === "agent" && (
							<span className="basis-full">
								{t("github.identity.agentHint")}
							</span>
						)}
						<ErrorText error={disconnect.error} />
					</>
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
							<IconButton
								icon="trash"
								label={t("github.identity.disconnect", { slot: label })}
								disabled={disconnect.isPending}
								onClick={async () => {
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
								}}
							/>
						)}
					</>
				}
			/>
		</li>
	);
}

function IdentitiesCard({ status }: { status: GitHubStatus }): ReactNode {
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
		<Card title={t("github.identity.title")} icon="user">
			<ul className="-mx-2 flex flex-col">
				{(["user", "agent"] as const).map((slot) => (
					<IdentityRow
						key={slot}
						slot={slot}
						identity={status.identities[slot]}
						connecting={authorize.isPending}
						onConnect={() => connect(slot)}
					/>
				))}
			</ul>
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
							onClick={() => window.location.assign(pending.url)}
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
			<AutomationSection status={status} />
		</Card>
	);
}

/** 身份卡片下部：自动化任务（GitHub 触发、项目级定时）使用的 GitHub 身份。 */
function AutomationSection({ status }: { status: GitHubStatus }): ReactNode {
	const { t } = useTranslation();
	const set = useSetGitHubAutomation();
	const agentReady = !!status.identities.agent;
	const current = set.isPending ? set.variables : status.automationIdentity;
	return (
		<section
			className="mt-3 border-border border-t pt-3"
			data-testid="github-automation"
		>
			<h3 className="mb-1 font-medium text-sm">
				{t("github.automation.title")}
			</h3>
			<fieldset
				className="m-0 flex flex-col gap-2 border-0 p-0"
				disabled={set.isPending}
			>
				<legend className="mb-2 text-muted text-xs">
					{t("github.automation.legend")}
				</legend>
				{(["user", "agent"] as const).map((slot) => {
					const disabled = slot === "agent" && !agentReady;
					return (
						// biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Radio 组件（内含原生 input）
						<label
							key={slot}
							className={`inline-flex items-center gap-2 text-sm ${disabled ? "text-faint" : "cursor-pointer"}`}
						>
							<Radio
								name="github-automation"
								value={slot}
								checked={current === slot}
								disabled={disabled}
								onChange={() => set.mutate(slot)}
							/>
							{t(`github.identity.${slot}`)}
						</label>
					);
				})}
				{!agentReady && (
					<p className="text-faint text-xs">
						{t("github.automation.agentMissing")}
					</p>
				)}
			</fieldset>
			<ErrorText error={set.error} />
		</section>
	);
}

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
	return (
		<li data-testid="github-hook" data-status={hook.status}>
			<ListRow
				leading={<LeadIcon name="webhook" />}
				title={
					<>
						<span className="truncate font-medium font-mono">
							{hook.fullName}
						</span>
						<Badge tone={HOOK_TONE[hook.status]}>
							{t(`github.hooks.status.${hook.status}`)}
						</Badge>
					</>
				}
				meta={
					<>
						<span className="basis-full">
							{t(`github.hooks.explain.${hook.status}`)}
						</span>
						{hook.status === "org_restricted" && approvalUrl && (
							<span>
								<ExternalLink href={approvalUrl}>
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
							<span className="basis-full break-all text-err">
								{hook.error}
							</span>
						)}
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
					</>
				}
			/>
		</li>
	);
}

function HooksCard({ status }: { status: GitHubStatus }): ReactNode {
	const { t } = useTranslation();
	const hooks = useGitHubHooks();
	const list = hooks.data?.hooks ?? [];
	const clientId = status.oauthApp?.clientId;
	const approvalUrl = clientId
		? githubOrgApprovalUrl(status.webUrl, clientId)
		: undefined;
	return (
		<Card title={t("github.hooks.title")} icon="webhook">
			<p className="mb-2 text-faint text-xs">{t("github.hooks.intro")}</p>
			{status.lastDeliveryAt !== undefined && (
				<p
					className="mb-2 text-muted text-xs"
					data-testid="github-last-delivery"
				>
					{t("github.hooks.lastAny", {
						when: formatRelative(status.lastDeliveryAt),
						source: t(
							`github.hooks.source.${status.lastDeliverySource ?? "webhook"}`,
						),
					})}
				</p>
			)}
			{hooks.isPending && <Spinner size={12} />}
			<ErrorText error={hooks.error} />
			{hooks.isSuccess && list.length === 0 && (
				<p className="text-faint text-xs">{t("github.hooks.empty")}</p>
			)}
			{list.length > 0 && (
				<ul className="-mx-2 flex flex-col">
					{list.map((h) => (
						<HookRow key={h.repoId} hook={h} approvalUrl={approvalUrl} />
					))}
				</ul>
			)}
		</Card>
	);
}

function AuditThread({ id }: { id: string }): ReactNode {
	const title = useIndex((s) => s.threads[id]?.title);
	return (
		<Link
			to={`/t/${id}`}
			title={id}
			className="block max-w-48 truncate text-accent hover:underline"
		>
			{title || id}
		</Link>
	);
}

function AuditCard(): ReactNode {
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
		<Card title={t("github.audit.title")} icon="list">
			<p className="mb-2 text-faint text-xs">{t("github.audit.intro")}</p>
			<form
				className="mb-3 flex flex-wrap items-center gap-2"
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
				<div className="mt-2">
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
		</Card>
	);
}

export default function GitHubSettings(): ReactNode {
	const { t } = useTranslation();
	const status = useGitHubStatus();
	const [params] = useSearchParams();
	const navigate = useNavigate();
	// 回调结果只在进入页面时读一次：随后清掉 query，提示留在本地状态里
	const [notice] = useState(() => callbackNotice(params));
	const handled = useRef(false);
	useEffect(() => {
		if (handled.current) return;
		handled.current = true;
		if ([...params.keys()].length > 0)
			navigate("/settings/github", { replace: true });
	}, [params, navigate]);
	const data = status.data;
	return (
		<>
			{notice && (
				<div data-testid="github-notice">
					<Callout
						tone={notice.tone}
						role={notice.tone === "err" ? "alert" : "status"}
					>
						<NoticeText notice={notice} />
					</Callout>
				</div>
			)}
			{data ? (
				<OAuthAppCard key={data.oauthApp?.clientId ?? ""} status={data} />
			) : (
				<Card title={t("github.oauth.title")} icon="key">
					{status.isPending && <Spinner />}
					<ErrorText error={status.error} />
				</Card>
			)}
			{data?.oauthApp && (
				<>
					<IdentitiesCard status={data} />
					<HooksCard status={data} />
					<AuditCard />
				</>
			)}
		</>
	);
}
