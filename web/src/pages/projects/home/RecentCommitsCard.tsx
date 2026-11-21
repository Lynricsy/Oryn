/**
 * 项目主页的「提交到 <基线>」（M24 阶段 ②）：基线分支最近 5 个提交（行见 `CommitRow`），点整行进提交详情。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { useRepoCommits } from "../../../api/repo-queries";
import { useNow } from "../../../components/layout/pane-parts";
import { Card } from "../../../components/ui/data";
import { ErrorText, Spinner } from "../../../components/ui/primitives";
import { CommitRow } from "../repo/CommitRow";

const RECENT = 5;

export function RecentCommitsCard({
	projectId,
	branch,
}: {
	projectId: string;
	branch: string;
}): ReactNode {
	const { t } = useTranslation();
	const now = useNow();
	const q = useRepoCommits(projectId, { ref: branch });
	const commits = q.data?.pages[0]?.commits.slice(0, RECENT) ?? [];
	return (
		<div data-testid="repo-commits-card" className="min-w-0">
			<Card
				title={t("projectRepo.commits.title", { branch })}
				icon="commit"
				action={
					<Link
						to={`/projects/${projectId}/commits`}
						className="font-normal text-accent-ink text-xs hover:underline"
					>
						{t("projectRepo.commits.viewAll")}
					</Link>
				}
			>
				{q.isPending ? (
					<div className="flex justify-center py-3 text-faint">
						<Spinner />
					</div>
				) : q.isError ? (
					<ErrorText error={q.error} />
				) : commits.length === 0 ? (
					<p className="py-1 text-faint text-sm">
						{t("projectRepo.commits.none")}
					</p>
				) : (
					<ul className="-mx-2">
						{commits.map((c) => (
							<CommitRow
								key={c.sha}
								commit={c}
								to={`/projects/${projectId}/commits/${c.sha}`}
								now={now}
							/>
						))}
					</ul>
				)}
			</Card>
		</div>
	);
}
