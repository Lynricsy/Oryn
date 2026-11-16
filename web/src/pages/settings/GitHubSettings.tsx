/**
 * 设置页「GitHub」（M14，default export，由设置页接入）：
 * 1. GitHub App：未连接时按 manifest 一键创建（可填组织、可设公开、「高级」里可改 webhook 地址）；已连接时显示名称、
 *    所有者、webhook 地址与最近一次投递（实时 / 轮询），可删除连接。
 * 2. 安装：App 装到的账号与仓库范围；可跳到 GitHub 安装页、可手动同步。
 * 3. 账号：「我的账号」「Agent 账号」两个身份槽位；连接时给出授权链接（可在本浏览器继续，也可复制到登录了目标账号的浏览器）。
 * 4. 自动化：GitHub 触发与项目级定时使用的身份。
 * 未创建 App 时只有第 1 张卡片。GitHub 回调（manifest / OAuth / 安装完成）带着 query 回到本页：这里显示结果并清掉 query。
 */
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useSearchParams } from "react-router";
import {
	type GitHubIdentity,
	type GitHubIdentitySlot,
	type GitHubInstallation,
	type GitHubStatus,
	useAuthorizeGitHubIdentity,
	useBeginGitHubApp,
	useDeleteGitHubApp,
	useDisconnectGitHubIdentity,
	useGitHubStatus,
	useSetGitHubAutomation,
	useSyncGitHubInstallations,
} from "../../api/github-queries";
import { CopyButton } from "../../components/ui/CopyButton";
import {
	Badge,
	Callout,
	Card,
	KeyValue,
	ListRow,
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
	Toggle,
} from "../../components/ui/primitives";
import { formatRelative } from "../../lib/format";

/** 回调页带回来的错误码里有专门文案的；其余显示通用文案。 */
const KNOWN_ERRORS = [
	"state_invalid",
	"exchange_failed",
	"same_account",
	"access_denied",
	"conflict",
] as const;

type KnownError = (typeof KNOWN_ERRORS)[number];

type Notice =
	| { tone: "err"; code: KnownError | "unknown" }
	| { tone: "ok"; kind: "created" | "installed" }
	| { tone: "ok"; kind: "connected"; slot: GitHubIdentitySlot };

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
	if (params.get("created") === "1") return { tone: "ok", kind: "created" };
	const slot = params.get("connected");
	if (slot === "user" || slot === "agent")
		return { tone: "ok", kind: "connected", slot };
	if (params.has("installation_id")) return { tone: "ok", kind: "installed" };
	return null;
}

function NoticeText({ notice }: { notice: Notice }): ReactNode {
	const { t } = useTranslation();
	if (notice.tone === "err") return t(`github.error.${notice.code}`);
	if (notice.kind === "connected")
		return t("github.notice.connected", {
			slot: t(`github.identity.${notice.slot}`),
		});
	return t(`github.notice.${notice.kind}`);
}

function LeadIcon({ name }: { name: "github" | "user" | "robot" }): ReactNode {
	return (
		<span className="grid h-8 w-8 place-items-center rounded-md border border-border bg-raised text-muted">
			<Icon name={name} size={15} />
		</span>
	);
}

function AppCreateForm({ status }: { status: GitHubStatus }): ReactNode {
	const { t } = useTranslation();
	const begin = useBeginGitHubApp();
	const [org, setOrg] = useState("");
	const [isPublic, setPublic] = useState(false);
	const [webhook, setWebhook] = useState("");
	return (
		<form
			className="flex flex-col gap-3"
			data-testid="github-app-create"
			onSubmit={(e) => {
				e.preventDefault();
				begin.mutate(
					{
						public: isPublic,
						org: org.trim() || undefined,
						webhookUrl: webhook.trim() || undefined,
					},
					{ onSuccess: (res) => window.location.assign(res.url) },
				);
			}}
		>
			<p className="text-muted text-xs">{t("github.app.intro")}</p>
			<Field label={t("github.app.org")} hint={t("github.app.orgHint")}>
				<TextInput
					className="max-w-72"
					value={org}
					maxLength={39}
					pattern="[A-Za-z0-9\-]+"
					placeholder={t("github.app.orgPlaceholder")}
					onChange={(e) => setOrg(e.target.value)}
				/>
			</Field>
			<Toggle
				checked={isPublic}
				onChange={setPublic}
				label={t("github.app.public")}
			/>
			<details className="text-sm">
				<summary className="cursor-pointer select-none text-muted text-xs">
					{t("github.app.advanced")}
				</summary>
				<div className="mt-2">
					<Field
						label={t("github.app.webhook")}
						hint={t("github.app.webhookHint")}
					>
						<TextInput
							type="url"
							value={webhook}
							placeholder={status.webhookUrl}
							onChange={(e) => setWebhook(e.target.value)}
						/>
					</Field>
				</div>
			</details>
			<div>
				<Button
					type="submit"
					variant="primary"
					icon="github"
					disabled={begin.isPending}
				>
					{begin.isPending && <Spinner size={13} />}
					{t("github.app.create")}
				</Button>
			</div>
			<ErrorText error={begin.error} />
		</form>
	);
}

function AppDetails({ status }: { status: GitHubStatus }): ReactNode {
	const { t } = useTranslation();
	const del = useDeleteGitHubApp();
	const app = status.app;
	if (!app) return null;
	return (
		<div className="flex flex-col gap-3" data-testid="github-app">
			<KeyValue
				rows={[
					{
						key: "name",
						label: t("github.app.name"),
						value: (
							<a
								href={app.htmlUrl}
								target="_blank"
								rel="noopener noreferrer"
								className="inline-flex items-center gap-1 text-accent hover:underline"
							>
								{app.name}
								<Icon name="external" size={11} aria-hidden />
							</a>
						),
					},
					{
						key: "owner",
						label: t("github.app.owner"),
						value: `@${app.ownerLogin}`,
					},
					{
						key: "webhook",
						label: t("github.app.webhook"),
						value: (
							<code className="break-all font-mono text-xs">
								{status.webhookUrl}
							</code>
						),
					},
					{
						key: "delivery",
						label: t("github.app.lastDelivery"),
						value: status.lastDeliveryAt ? (
							<span>
								{formatRelative(status.lastDeliveryAt)}
								{status.lastDeliverySource && (
									<span className="text-faint">
										{" · "}
										{t(`github.app.source.${status.lastDeliverySource}`)}
									</span>
								)}
							</span>
						) : (
							<span className="text-faint">{t("github.app.noDelivery")}</span>
						),
					},
				]}
			/>
			<div>
				<Button
					size="sm"
					variant="danger"
					icon="trash"
					disabled={del.isPending}
					onClick={() => {
						if (
							window.confirm(t("github.app.deleteConfirm", { name: app.name }))
						)
							del.mutate();
					}}
				>
					{t("github.app.delete")}
				</Button>
			</div>
			<ErrorText error={del.error} />
		</div>
	);
}

function InstallationRow({ inst }: { inst: GitHubInstallation }): ReactNode {
	const { t } = useTranslation();
	const scope =
		inst.repositorySelection === "all"
			? t("github.installations.all")
			: t("github.installations.selected");
	return (
		<li data-testid="github-installation">
			<ListRow
				leading={<LeadIcon name="github" />}
				title={
					<>
						<span className="truncate font-medium">{inst.accountLogin}</span>
						{inst.suspended && (
							<Badge tone="warn">{t("github.installations.suspended")}</Badge>
						)}
					</>
				}
				meta={
					<>
						<span>{inst.accountType}</span>
						<span>·</span>
						<span>{scope}</span>
					</>
				}
			/>
		</li>
	);
}

function InstallationsCard({ status }: { status: GitHubStatus }): ReactNode {
	const { t } = useTranslation();
	const sync = useSyncGitHubInstallations();
	const app = status.app;
	return (
		<Card title={t("github.installations.title")} icon="globe">
			{status.installations.length > 0 ? (
				<ul className="-mx-2 flex flex-col">
					{status.installations.map((i) => (
						<InstallationRow key={i.id} inst={i} />
					))}
				</ul>
			) : (
				<p className="text-faint text-xs">{t("github.installations.empty")}</p>
			)}
			<div className="mt-3 flex flex-wrap gap-2">
				<Button
					size="sm"
					icon="external"
					disabled={!app}
					onClick={() =>
						app && window.open(app.installUrl, "_blank", "noopener")
					}
				>
					{t("github.installations.install")}
				</Button>
				<Button
					size="sm"
					variant="ghost"
					icon="refresh"
					disabled={sync.isPending}
					onClick={() => sync.mutate()}
				>
					{sync.isPending && <Spinner size={12} />}
					{t("github.installations.sync")}
				</Button>
			</div>
			<ErrorText error={sync.error} />
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
						{(!identity || identity.status === "reauth") && (
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
								onClick={() => {
									if (
										window.confirm(
											t("github.identity.disconnectConfirm", {
												slot: label,
												login: identity.login,
											}),
										)
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
		</Card>
	);
}

function AutomationCard({ status }: { status: GitHubStatus }): ReactNode {
	const { t } = useTranslation();
	const set = useSetGitHubAutomation();
	const agentReady = !!status.identities.agent;
	const current = set.isPending ? set.variables : status.automationIdentity;
	return (
		<Card title={t("github.automation.title")} icon="bolt">
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
		</Card>
	);
}

export default function GitHubSettings(): ReactNode {
	const { t } = useTranslation();
	const status = useGitHubStatus();
	const sync = useSyncGitHubInstallations();
	const [params] = useSearchParams();
	const navigate = useNavigate();
	// 回调结果只在进入页面时读一次：随后清掉 query，提示留在本地状态里
	const [notice] = useState(() => callbackNotice(params));
	const handled = useRef(false);
	useEffect(() => {
		if (handled.current) return;
		handled.current = true;
		if (params.has("installation_id")) sync.mutate();
		if ([...params.keys()].length > 0)
			navigate("/settings/github", { replace: true });
	}, [params, navigate, sync]);
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
			<Card title={t("github.app.title")} icon="github">
				{status.isPending && <Spinner />}
				<ErrorText error={status.error} />
				{data && !data.app && <AppCreateForm status={data} />}
				{data?.app && <AppDetails status={data} />}
			</Card>
			{data?.app && (
				<>
					<InstallationsCard status={data} />
					<IdentitiesCard status={data} />
					<AutomationCard status={data} />
				</>
			)}
		</>
	);
}
