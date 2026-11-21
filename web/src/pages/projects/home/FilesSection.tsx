/**
 * 项目主页的「文件」区块（M24 阶段 ②）：标题行右侧是提交 / 分支 / 标签三个子页入口；正文先是基线分支的最新提交条
 * （subject、短 sha、多久前，点进提交详情），再是根目录的条目列表。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { useRepoCommits, useRepoTree } from "../../../api/repo-queries";
import { useNow } from "../../../components/layout/pane-parts";
import { Card } from "../../../components/ui/data";
import { Icon, type IconName } from "../../../components/ui/icons";
import { ErrorText, Spinner } from "../../../components/ui/primitives";
import { formatRelative } from "../../../lib/format";
import { RepoEntryList } from "./RepoEntryList";

const NAV = [
	["commits", "commit"],
	["branches", "branch"],
	["tags", "tag"],
] as const satisfies readonly (readonly [string, IconName])[];

export function FilesSection({
	projectId,
	refName,
}: {
	projectId: string;
	refName: string;
}): ReactNode {
	const { t } = useTranslation();
	const now = useNow();
	const tree = useRepoTree(projectId, refName, "");
	// 与「提交到 <基线>」同一个查询（同 key），不多发请求
	const commits = useRepoCommits(projectId, { ref: refName });
	const list = commits.data?.pages[0]?.commits ?? [];
	const head = list.find((c) => c.sha === tree.data?.commit) ?? list[0];
	return (
		<div data-testid="project-files">
			<Card
				title={t("projectRepo.files.title")}
				icon="folder"
				action={
					<nav className="flex items-center gap-3 font-normal">
						{NAV.map(([sub, icon]) => (
							<Link
								key={sub}
								to={`/projects/${projectId}/${sub}`}
								className="inline-flex items-center gap-1 text-muted hover:text-fg"
							>
								<Icon name={icon} size={13} aria-hidden />
								{t(`projectRepo.nav.${sub}`)}
							</Link>
						))}
					</nav>
				}
			>
				{head && (
					<Link
						to={`/projects/${projectId}/commits/${head.sha}`}
						data-testid="repo-head-commit"
						className="-mx-2 mb-1 flex min-h-9 items-center gap-2 rounded-md bg-inset px-2 py-1.5 text-xs transition-colors duration-150 hover:bg-hover"
					>
						<Icon
							name="commit"
							size={14}
							aria-hidden
							className="shrink-0 text-faint"
						/>
						<span className="min-w-0 flex-1 truncate text-fg">
							{head.subject}
						</span>
						<span className="shrink-0 font-mono text-faint">
							{head.shortSha}
						</span>
						<span className="shrink-0 text-faint tabular-nums">
							{formatRelative(head.ts, now)}
						</span>
					</Link>
				)}
				{tree.isPending ? (
					<div className="flex justify-center py-3 text-faint">
						<Spinner />
					</div>
				) : tree.isError ? (
					<ErrorText error={tree.error} />
				) : (
					<RepoEntryList
						projectId={projectId}
						refName={refName}
						tree={tree.data}
					/>
				)}
			</Card>
		</div>
	);
}
