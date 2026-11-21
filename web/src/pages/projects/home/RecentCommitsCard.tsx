/**
 * 项目主页的「提交到 <基线>」（M24 阶段 ②）：基线分支最近 5 个提交，每行是 subject、短 sha、关联的线程与多久前；
 * 点整行进提交详情，线程链接单独可点（整行链接用伸展的 `::after`，线程链接排在它上面，不嵌套交互元素）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { useRepoCommits } from "../../../api/repo-queries";
import { useNow } from "../../../components/layout/pane-parts";
import { Card, ListRow } from "../../../components/ui/data";
import { Icon } from "../../../components/ui/icons";
import { ErrorText, Spinner } from "../../../components/ui/primitives";
import { formatRelative } from "../../../lib/format";
import { parseTitleCard } from "../../../lib/title";

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
							<li
								key={c.sha}
								data-testid="repo-commit-row"
								className="relative rounded-md transition-colors duration-150 hover:bg-hover"
							>
								<ListRow
									leading={
										<Icon
											name="commit"
											size={14}
											aria-hidden
											className="text-faint"
										/>
									}
									title={
										<Link
											to={`/projects/${projectId}/commits/${c.sha}`}
											className="min-w-0 truncate after:absolute after:inset-0 after:rounded-md"
										>
											{c.subject}
										</Link>
									}
									meta={
										<>
											<span className="font-mono">{c.shortSha}</span>
											{c.thread && (
												<Link
													to={`/t/${c.thread.id}`}
													className="relative inline-flex min-w-0 items-center gap-1 text-muted hover:text-fg hover:underline"
												>
													<Icon
														name="message"
														size={11}
														aria-hidden
														className="shrink-0"
													/>
													<span className="min-w-0 max-w-48 truncate">
														{parseTitleCard(c.thread.title).text ||
															t("thread.untitled")}
													</span>
												</Link>
											)}
											<span className="tabular-nums">
												{formatRelative(c.ts, now)}
											</span>
										</>
									}
								/>
							</li>
						))}
					</ul>
				)}
			</Card>
		</div>
	);
}
