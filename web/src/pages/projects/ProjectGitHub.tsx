/**
 * 项目页的「GitHub」卡片（M14 / M15）：仓库绑定状态、仓库 webhook 状态与事件回流开关。
 * 仓库地址不是 GitHub 且从未解析出仓库时不渲染。已绑定（我的账号能读到该仓库）时给出 hook 状态徽标与三个回流开关，改动即 PATCH 项目；
 * 开关下方是触发规则列表（`GitHubTriggers`）。
 * 未绑定时说明原因（M15 `bindStatus`：读不到、组织限制并给出申请批准的链接、需要 SSO 授权）；
 * 还没配置 OAuth App 时沿用 M14 的提示（安装 App / 先连接 GitHub）。
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { api, type Project, type Schemas, unwrap } from "../../api/client";
import {
	type GitHubHookStatus,
	type GitHubStatus,
	githubOrgApprovalUrl,
	useGitHubStatus,
} from "../../api/github-queries";
import { qk } from "../../api/queries";
import { Badge, Card, type Tone } from "../../components/ui/data";
import { Icon } from "../../components/ui/icons";
import { ErrorText, Toggle } from "../../components/ui/primitives";
import { GitHubTriggers } from "./GitHubTriggers";

type Listen = Schemas["GitHubListen"];
type ProjectGitHub = Schemas["ProjectGitHub"];

const LISTEN_KEYS = ["reviews", "comments", "ci"] as const;

const HOOK_TONE: Record<GitHubHookStatus, Tone> = {
	active: "ok",
	no_admin: "neutral",
	org_restricted: "warn",
	sso_required: "warn",
	error: "err",
};

function ListenToggles({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const save = useMutation({
		mutationFn: (githubListen: Listen) =>
			unwrap(
				api.PATCH("/api/v1/projects/{id}", {
					params: { path: { id: project.id } },
					body: { githubListen },
				}),
			),
		onSuccess: () => qc.invalidateQueries({ queryKey: qk.projects }),
	});
	const listen = save.isPending
		? save.variables
		: (project.github?.listen ?? { reviews: true, comments: true, ci: true });
	return (
		<div className="flex flex-col gap-2">
			{LISTEN_KEYS.map((k) => (
				<Toggle
					key={k}
					checked={listen[k]}
					disabled={save.isPending}
					onChange={(v) => save.mutate({ ...listen, [k]: v })}
					label={t(`github.project.listen.${k}`)}
				/>
			))}
			<ErrorText error={save.error} />
		</div>
	);
}

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

/** 未绑定时的说明：有 `bindStatus` 时写原因，否则按 OAuth App / 旧 App 的连接情况给下一步。 */
function UnboundText({
	gh,
	status,
}: {
	gh: ProjectGitHub | undefined;
	status: GitHubStatus | undefined;
}): ReactNode {
	const { t } = useTranslation();
	const connect = (
		<Link to="/settings/github" className="text-accent hover:underline">
			{t("github.project.connect")}
		</Link>
	);
	if (gh?.bindStatus) {
		const clientId = status?.oauthApp?.clientId;
		return (
			<p
				className="text-muted text-sm"
				data-testid="project-github-unbound"
				data-reason={gh.bindStatus}
			>
				{t(`github.project.bindStatus.${gh.bindStatus}`, {
					repo: gh.fullName,
				})}{" "}
				{gh.bindStatus === "org_restricted" && clientId && (
					<ExternalLink href={githubOrgApprovalUrl(clientId)}>
						{t("github.hooks.requestApproval")}
					</ExternalLink>
				)}
			</p>
		);
	}
	if (status?.oauthApp)
		return (
			<p className="text-muted text-sm" data-testid="project-github-unbound">
				{status.identities.user ? (
					t("github.project.unboundPending")
				) : (
					<>
						{t("github.project.unboundNoUser")} {connect}
					</>
				)}
			</p>
		);
	const installUrl = status?.app?.installUrl;
	return (
		<p className="text-muted text-sm" data-testid="project-github-unbound">
			{t("github.project.unbound")}{" "}
			{installUrl ? (
				<ExternalLink href={installUrl}>
					{t("github.project.install")}
				</ExternalLink>
			) : (
				connect
			)}
		</p>
	);
}

export function ProjectGitHubCard({
	project,
}: {
	project: Project;
}): ReactNode {
	const { t } = useTranslation();
	const status = useGitHubStatus();
	const gh = project.github;
	if (!gh && !project.repoUrl?.startsWith("https://github.com/")) return null;
	const hook = gh?.hookStatus;
	return (
		<Card title="GitHub" icon="github">
			<div className="flex flex-col gap-3" data-testid="project-github">
				{gh?.bound ? (
					<div className="flex flex-col gap-1">
						<p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
							<span>
								{t("github.project.bound")}{" "}
								<code className="font-mono text-xs">{gh.fullName}</code>
							</span>
							{hook && (
								<span data-testid="project-github-hook" data-status={hook}>
									<Badge
										tone={HOOK_TONE[hook]}
										icon="webhook"
										title={t(`github.hooks.explain.${hook}`)}
									>
										{t(`github.hooks.status.${hook}`)}
									</Badge>
								</span>
							)}
						</p>
						{hook && hook !== "active" && (
							<p className="text-faint text-xs">
								{t(`github.hooks.explain.${hook}`)}
							</p>
						)}
					</div>
				) : (
					<UnboundText gh={gh} status={status.data} />
				)}
				{gh?.bound && (
					<>
						<ListenToggles project={project} />
						<GitHubTriggers project={project} />
					</>
				)}
			</div>
		</Card>
	);
}
