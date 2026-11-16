/**
 * 项目页的「GitHub」卡片（M14）：仓库绑定状态与事件回流开关。
 * 仓库地址不是 GitHub 且从未解析出仓库时不渲染。已绑定（App 可访问该仓库）时给出三个回流开关，改动即 PATCH 项目；
 * 未绑定时提示把 App 安装到这个仓库（还没有 App 时链接到设置页）。
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { api, type Project, type Schemas, unwrap } from "../../api/client";
import { useGitHubStatus } from "../../api/github-queries";
import { qk } from "../../api/queries";
import { Card } from "../../components/ui/data";
import { Icon } from "../../components/ui/icons";
import { ErrorText, Toggle } from "../../components/ui/primitives";

type Listen = Schemas["GitHubListen"];

const LISTEN_KEYS = ["reviews", "comments", "ci"] as const;

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

export function ProjectGitHubCard({
	project,
}: {
	project: Project;
}): ReactNode {
	const { t } = useTranslation();
	const status = useGitHubStatus();
	const gh = project.github;
	if (!gh && !project.repoUrl?.startsWith("https://github.com/")) return null;
	const installUrl = status.data?.app?.installUrl;
	return (
		<Card title="GitHub" icon="github">
			<div className="flex flex-col gap-3" data-testid="project-github">
				{gh?.bound ? (
					<p className="text-sm">
						{t("github.project.bound")}{" "}
						<code className="font-mono text-xs">{gh.fullName}</code>
					</p>
				) : (
					<p className="text-muted text-sm">
						{t("github.project.unbound")}{" "}
						{installUrl ? (
							<a
								href={installUrl}
								target="_blank"
								rel="noopener noreferrer"
								className="inline-flex items-center gap-1 text-accent hover:underline"
							>
								{t("github.project.install")}
								<Icon name="external" size={11} aria-hidden />
							</a>
						) : (
							<Link
								to="/settings/github"
								className="text-accent hover:underline"
							>
								{t("github.project.connect")}
							</Link>
						)}
					</p>
				)}
				{gh?.bound && <ListenToggles project={project} />}
			</div>
		</Card>
	);
}
