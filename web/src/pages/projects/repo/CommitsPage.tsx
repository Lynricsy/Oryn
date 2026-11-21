/**
 * 提交子页（M24 阶段 ②，`/projects/<id>/commits[/<sha>]`）：`?ref=` 选分支或标签（缺省 = 基线），列表分页
 * 「加载更早的提交」。宽屏（expanded）左列表右详情，没选提交时右侧是提示；其它宽度没有 `<sha>` 时只有列表，
 * 有时只有详情（带返回钮）。链接都带着 `?ref=`，切分区回来与刷新都保持。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams, useSearchParams } from "react-router";
import type { Project } from "../../../api/client";
import { useRepoCommits } from "../../../api/repo-queries";
import { useNow } from "../../../components/layout/pane-parts";
import { Card } from "../../../components/ui/data";
import {
	Button,
	EmptyState,
	ErrorText,
	Spinner,
} from "../../../components/ui/primitives";
import { useBreakpoint } from "../../../lib/viewport";
import { ProjectGate, ProjectShell } from "../ProjectShell";
import { CommitDetail } from "./CommitDetail";
import { CommitRow } from "./CommitRow";
import { RefPicker } from "./RefPicker";
import { RepoGate } from "./RepoGate";
import { RepoNav } from "./RepoNav";

export function CommitsPage(): ReactNode {
	return <ProjectGate>{(p) => <Commits project={p} />}</ProjectGate>;
}

function CommitList({
	projectId,
	refName,
	selected,
	href,
}: {
	projectId: string;
	refName: string;
	selected?: string;
	href: (sha: string) => string;
}): ReactNode {
	const { t } = useTranslation();
	const now = useNow();
	const q = useRepoCommits(projectId, { ref: refName });
	// 只有第一页都没取到才整块换成错误；加载更早的页失败时列表保留，错误显示在按钮下面
	if (!q.data)
		return q.isError ? (
			<ErrorText error={q.error} />
		) : (
			<div className="flex justify-center p-6 text-muted">
				<Spinner size={18} />
			</div>
		);
	const commits = q.data.pages.flatMap((p) => p.commits);
	return (
		<Card>
			{commits.length === 0 ? (
				<p className="py-1 text-faint text-sm">
					{t("projectRepo.commits.none")}
				</p>
			) : (
				<ul data-testid="repo-commit-list" className="-mx-2">
					{commits.map((c) => (
						<CommitRow
							key={c.sha}
							commit={c}
							to={href(c.sha)}
							now={now}
							selected={c.sha === selected}
							author
						/>
					))}
				</ul>
			)}
			{q.hasNextPage && (
				<div className="mt-2 flex justify-center">
					<Button
						size="sm"
						disabled={q.isFetchingNextPage}
						onClick={() => void q.fetchNextPage()}
					>
						{q.isFetchingNextPage && <Spinner size={12} />}
						{t("projectRepo.commits.loadMore")}
					</Button>
				</div>
			)}
			<ErrorText error={q.isFetchNextPageError ? q.error : null} />
		</Card>
	);
}

function Commits({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const { sha } = useParams();
	const [sp] = useSearchParams();
	const navigate = useNavigate();
	const split = useBreakpoint() === "expanded";
	const base = `/projects/${project.id}/commits`;
	const search = sp.toString() ? `?${sp}` : "";
	const href = (s: string) => `${base}/${s}${search}`;
	return (
		<ProjectShell
			project={project}
			wide
			crumbs={
				sha
					? [
							{ label: t("projectRepo.nav.commits"), to: base + search },
							{ label: sha.slice(0, 7) },
						]
					: [{ label: t("projectRepo.nav.commits") }]
			}
		>
			<RepoGate project={project}>
				{(status) => {
					const refName = sp.get("ref") || status.baseBranch || "";
					const detail = sha && (
						<CommitDetail
							key={sha}
							projectId={project.id}
							sha={sha}
							parentHref={href}
							onBack={split ? undefined : () => navigate(base + search)}
						/>
					);
					if (detail && !split) return detail;
					const list = (
						<CommitList
							projectId={project.id}
							refName={refName}
							selected={sha}
							href={href}
						/>
					);
					return (
						<div data-testid="repo-commits" className="flex flex-col gap-3">
							<div className="flex flex-wrap items-center gap-2">
								<RepoNav projectId={project.id} />
								<RefPicker
									projectId={project.id}
									value={refName}
									onChange={(ref) =>
										navigate(`${base}?ref=${encodeURIComponent(ref)}`)
									}
								/>
							</div>
							{split ? (
								<div className="grid grid-cols-[minmax(0,24rem)_minmax(0,1fr)] items-start gap-3">
									{list}
									{detail || (
										<Card>
											<EmptyState
												icon="commit"
												title={t("projectRepo.commits.selectHint")}
											/>
										</Card>
									)}
								</div>
							) : (
								list
							)}
						</div>
					);
				}}
			</RepoGate>
		</ProjectShell>
	);
}
