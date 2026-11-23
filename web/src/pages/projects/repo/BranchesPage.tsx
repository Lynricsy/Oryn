/**
 * 分支子页与标签子页（M24 阶段 ②，`/projects/<id>/branches`、`…/tags`）：按最近提交排序（默认分支第一，带「默认」），
 * 每行是名字（进该 ref 的提交列表）、头提交（subject 进提交详情、短 sha、多久前）与「文件」入口（该 ref 的根目录）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Project } from "../../../api/client";
import {
	type RepoRef,
	type RepoRefKind,
	useRepoRefs,
} from "../../../api/repo-queries";
import { useNow } from "../../../components/layout/pane-parts";
import { Badge, Card } from "../../../components/ui/data";
import { Icon } from "../../../components/ui/icons";
import {
	Button,
	EmptyState,
	ErrorText,
	Spinner,
} from "../../../components/ui/primitives";
import { formatRelative } from "../../../lib/format";
import { emojify } from "../../../lib/gitmoji";
import { ProjectGate, ProjectShell } from "../ProjectShell";
import { RepoGate } from "./RepoGate";
import { RepoNav } from "./RepoNav";

function RefRow({
	projectId,
	r,
	now,
}: {
	projectId: string;
	r: RepoRef;
	now: number;
}): ReactNode {
	const { t } = useTranslation();
	const q = `?ref=${encodeURIComponent(r.name)}`;
	return (
		<li
			data-testid="repo-ref-row"
			data-ref={r.name}
			className="flex min-h-11 items-center gap-3 rounded-md px-2 py-1.5"
		>
			<Icon
				name={r.kind === "branch" ? "branch" : "tag"}
				size={14}
				aria-hidden
				className="shrink-0 text-faint"
			/>
			<div className="flex min-w-0 flex-1 flex-col gap-0.5">
				<div className="flex min-w-0 items-center gap-1.5">
					<Link
						to={`/projects/${projectId}/commits${q}`}
						className="min-w-0 truncate font-mono text-fg text-sm hover:underline"
					>
						{r.name}
					</Link>
					{r.default && <Badge>{t("projectRepo.refs.default")}</Badge>}
				</div>
				<div className="flex min-w-0 items-center gap-1.5 text-faint text-xs">
					<Link
						to={`/projects/${projectId}/commits/${r.commit.sha}${q}`}
						className="min-w-0 truncate hover:text-fg hover:underline"
					>
						{emojify(r.commit.subject)}
					</Link>
					<span className="shrink-0 font-mono">{r.commit.shortSha}</span>
					<span className="shrink-0 tabular-nums">
						{formatRelative(r.commit.ts, now)}
					</span>
				</div>
			</div>
			<Link
				to={`/projects/${projectId}/files${q}`}
				className="inline-flex shrink-0 items-center gap-1 rounded-md px-1.5 py-1 text-muted text-xs transition-colors duration-150 hover:bg-hover hover:text-fg"
			>
				<Icon name="folder" size={13} aria-hidden />
				{t("projectRepo.refs.files")}
			</Link>
		</li>
	);
}

function RefList({
	projectId,
	kind,
}: {
	projectId: string;
	kind: RepoRefKind;
}): ReactNode {
	const { t } = useTranslation();
	const now = useNow();
	const q = useRepoRefs(projectId, kind);
	// 只有第一页都没取到才整块换成错误；加载更多失败时列表保留，错误显示在按钮下面
	if (!q.data)
		return q.isError ? (
			<ErrorText error={q.error} />
		) : (
			<div className="flex justify-center p-6 text-muted">
				<Spinner size={18} />
			</div>
		);
	const refs = q.data.pages.flatMap((p) => p.refs);
	if (refs.length === 0)
		return (
			<Card>
				<EmptyState
					icon={kind === "branch" ? "branch" : "tag"}
					title={t("projectRepo.refs.none")}
				/>
			</Card>
		);
	return (
		<Card>
			<ul data-testid="repo-ref-list" className="-mx-2">
				{refs.map((r) => (
					<RefRow key={r.name} projectId={projectId} r={r} now={now} />
				))}
			</ul>
			{q.hasNextPage && (
				<div className="mt-2 flex justify-center">
					<Button
						size="sm"
						disabled={q.isFetchingNextPage}
						onClick={() => void q.fetchNextPage()}
					>
						{q.isFetchingNextPage && <Spinner size={12} />}
						{t("projectRepo.refs.loadMore")}
					</Button>
				</div>
			)}
			<ErrorText error={q.isFetchNextPageError ? q.error : null} />
		</Card>
	);
}

/** 分支或标签子页（两页同形，只差 kind）。 */
export function RefsPage({ kind }: { kind: RepoRefKind }): ReactNode {
	const { t } = useTranslation();
	return (
		<ProjectGate>
			{(p: Project) => (
				<ProjectShell
					project={p}
					wide
					crumbs={[
						{
							label: t(
								kind === "branch"
									? "projectRepo.nav.branches"
									: "projectRepo.nav.tags",
							),
						},
					]}
				>
					<RepoGate project={p}>
						{() => (
							<div data-testid="repo-refs" className="flex flex-col gap-3">
								<RepoNav projectId={p.id} />
								<RefList projectId={p.id} kind={kind} />
							</div>
						)}
					</RepoGate>
				</ProjectShell>
			)}
		</ProjectGate>
	);
}

export function BranchesPage(): ReactNode {
	return <RefsPage kind="branch" />;
}
