/**
 * 提交子页（M24 阶段 ②，`/projects/<id>/commits[/<sha>]`）：`?ref=` 选分支或标签（缺省 = 基线），列表滚到底自动加载
 * 更早的一页（底部的「加载更早的提交」按钮被看见即触发，也可手点；失败时停下等手点重试）。宽屏（expanded）左列表右详情，
 * 两栏各自内部滚动、页面不整页滚，没选提交时右侧是提示；其它宽度没有 `<sha>` 时只有列表（整页滚动），有时只有详情
 * （带返回钮）。链接都带着 `?ref=`，切分区回来与刷新都保持。
 */
import { type ReactNode, type RefObject, useEffect, useRef } from "react";
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

/**
 * 元素进入可视区（含各层滚动容器的裁剪）时调用 `onReach`。`key` 变化（新的一页到了）时重新观察：
 * 元素仍在可视区里会立刻再触发一次，短列表能一直加载到填满或到底。没有 IntersectionObserver（测试环境）时不做事。
 */
function useReachVisible(
	onReach: () => void,
	enabled: boolean,
	key: number,
): RefObject<HTMLDivElement | null> {
	const ref = useRef<HTMLDivElement | null>(null);
	const latest = useRef(onReach);
	latest.current = onReach;
	// biome-ignore lint/correctness/useExhaustiveDependencies: key 变化（新的一页到了）时要重新观察，让仍可见的元素再触发一次
	useEffect(() => {
		const el = ref.current;
		if (!enabled || !el || typeof IntersectionObserver === "undefined") return;
		const io = new IntersectionObserver((entries) => {
			if (entries.some((e) => e.isIntersecting)) latest.current();
		});
		io.observe(el);
		return () => io.disconnect();
	}, [enabled, key]);
	return ref;
}

function CommitList({
	projectId,
	refName,
	selected,
	href,
	className,
}: {
	projectId: string;
	refName: string;
	selected?: string;
	href: (sha: string) => string;
	className?: string;
}): ReactNode {
	const { t } = useTranslation();
	const now = useNow();
	const q = useRepoCommits(projectId, { ref: refName });
	const pages = q.data?.pages.length ?? 0;
	// 加载更早的页失败后不再自动触发（免得反复请求），等手点按钮重试
	const more = useReachVisible(
		() => void q.fetchNextPage(),
		q.hasNextPage && !q.isFetchingNextPage && !q.isFetchNextPageError,
		pages,
	);
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
		<Card className={className}>
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
				<div ref={more} className="mt-2 flex justify-center">
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
			// 宽屏左右分栏时两栏各自滚动；只有详情（窄屏选中提交）时照常整页滚
			fill={split}
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
							className={split ? "max-h-full overflow-y-auto" : undefined}
						/>
					);
					return (
						<div
							data-testid="repo-commits"
							className={`flex flex-col gap-3 ${split ? "min-h-0 flex-1" : ""}`}
						>
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
								<div className="grid min-h-0 flex-1 grid-cols-[minmax(0,24rem)_minmax(0,1fr)] grid-rows-[minmax(0,1fr)] items-start gap-3">
									{list}
									<div className="max-h-full min-h-0 overflow-y-auto rounded-lg">
										{detail || (
											<Card>
												<EmptyState
													icon="commit"
													title={t("projectRepo.commits.selectHint")}
												/>
											</Card>
										)}
									</div>
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
