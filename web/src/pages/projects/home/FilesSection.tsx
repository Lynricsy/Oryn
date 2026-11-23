/**
 * 项目主页的「文件」区块（M24 阶段 ②，仿 GitHub 仓库首页）：上方工具条是基线分支胶囊（进文件页）与分支 / 标签入口；
 * 下面的文件表以基线分支的最新提交为表头（作者与 Co-authored-by 共同作者的圆形头像叠放和名字、subject、短 sha、
 * 多久前，右侧「N 次提交」进提交子页），再是根目录条目。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import {
	type RepoCommit,
	useRepoCommits,
	useRepoTree,
} from "../../../api/repo-queries";
import { useNow } from "../../../components/layout/pane-parts";
import { Avatar } from "../../../components/ui/data";
import { Icon, type IconName } from "../../../components/ui/icons";
import { ErrorText, Spinner } from "../../../components/ui/primitives";
import { formatCount, formatRelative } from "../../../lib/format";
import { emojify } from "../../../lib/gitmoji";
import { RepoBox } from "./RepoBox";
import { RepoEntryList } from "./RepoEntryList";

const NAV = [
	["branches", "branch"],
	["tags", "tag"],
] as const satisfies readonly (readonly [string, IconName])[];

/** 头像最多叠放几个；越靠前（作者）层级越高，共同作者依次压在下面 */
const AVATAR_LAYERS = ["z-30", "z-20", "z-10"];

/** 作者与共同作者：「A」「A 和 B」「A 和其他 N 人」，名字加粗；悬停显示全部 `名字 <邮箱>`。 */
function Authors({ commit: c }: { commit: RepoCommit }): ReactNode {
	const { t } = useTranslation();
	const people = [
		{ name: c.authorName, email: c.authorEmail },
		...(c.coAuthors ?? []),
	];
	const [first, second] = people;
	return (
		<span
			className="flex min-w-0 shrink-0 items-center gap-2"
			title={people
				.map((p) => (p.email ? `${p.name} <${p.email}>` : p.name))
				.join("\n")}
		>
			{/* isolate：头像的 z 层级只在这一组里比较，不会压到页面上的弹层 */}
			<span className="isolate flex shrink-0 items-center">
				{people.slice(0, AVATAR_LAYERS.length).map((p, i) => (
					<Avatar
						key={`${p.email}\0${p.name}`}
						name={p.name}
						size={20}
						round
						// 叠放：后面的往左塞到前一个下面（不盖住首字），每个都描一圈表头底色隔开
						className={`relative ring-2 ring-raised ${AVATAR_LAYERS[i]} ${i ? "-ml-1" : ""}`}
					/>
				))}
			</span>
			<span className="min-w-0 max-w-72 truncate text-muted">
				<b className="font-medium text-fg">{first?.name}</b>
				{people.length === 2 && second ? (
					<>
						{` ${t("projectRepo.files.and")} `}
						<b className="font-medium text-fg">{second.name}</b>
					</>
				) : people.length > 2 ? (
					` ${t("projectRepo.files.andOthers", { count: people.length - 1 })}`
				) : null}
			</span>
		</span>
	);
}

function HeadCommit({
	projectId,
	commit: c,
	total,
	now,
}: {
	projectId: string;
	commit: RepoCommit;
	/** 基线分支的提交总数（服务端统计失败时缺省，只显示「提交」） */
	total?: number;
	now: number;
}): ReactNode {
	const { t } = useTranslation();
	const to = `/projects/${projectId}/commits/${c.sha}`;
	return (
		<>
			<div
				data-testid="repo-head-commit"
				className="flex min-w-0 flex-1 items-center gap-2"
			>
				<Authors commit={c} />
				<Link
					to={to}
					title={c.subject}
					className="min-w-0 flex-1 truncate text-muted hover:text-accent-ink hover:underline"
				>
					{emojify(c.subject)}
				</Link>
				<Link
					to={to}
					className="hidden shrink-0 font-mono text-faint text-xs hover:text-accent-ink hover:underline sm:inline"
				>
					{c.shortSha}
				</Link>
				<span className="shrink-0 text-faint text-xs tabular-nums">
					{formatRelative(c.ts, now)}
				</span>
			</div>
			<Link
				to={`/projects/${projectId}/commits`}
				className="inline-flex shrink-0 items-center gap-1 rounded-md px-1.5 py-1 font-medium text-fg text-xs transition-colors duration-150 hover:bg-hover"
			>
				<Icon name="history" size={14} aria-hidden className="text-faint" />
				{total === undefined
					? t("projectRepo.nav.commits")
					: t("projectRepo.files.commitCount", {
							count: total,
							n: formatCount(total),
						})}
			</Link>
		</>
	);
}

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
		<div data-testid="project-files" className="flex flex-col gap-2">
			<div className="flex flex-wrap items-center gap-x-4 gap-y-2">
				<Link
					to={`/projects/${projectId}/files`}
					title={t("projectRepo.nav.files")}
					className="inline-flex h-8 min-w-0 items-center gap-1.5 rounded-md border border-border bg-panel px-3 shadow-xs transition-colors duration-150 hover:bg-hover"
				>
					<Icon name="branch" size={14} aria-hidden className="text-faint" />
					<span className="min-w-0 truncate font-medium font-mono text-xs">
						{refName}
					</span>
				</Link>
				{NAV.map(([sub, icon]) => (
					<Link
						key={sub}
						to={`/projects/${projectId}/${sub}`}
						className="inline-flex items-center gap-1 text-muted text-sm hover:text-accent-ink"
					>
						<Icon name={icon} size={14} aria-hidden />
						{t(`projectRepo.nav.${sub}`)}
					</Link>
				))}
			</div>
			<RepoBox
				header={
					head && (
						<HeadCommit
							projectId={projectId}
							commit={head}
							total={commits.data?.pages[0]?.total}
							now={now}
						/>
					)
				}
			>
				{tree.isPending ? (
					<div className="flex justify-center py-4 text-faint">
						<Spinner />
					</div>
				) : tree.isError ? (
					<div className="px-4 py-3">
						<ErrorText error={tree.error} />
					</div>
				) : (
					<RepoEntryList
						projectId={projectId}
						refName={refName}
						tree={tree.data}
					/>
				)}
			</RepoBox>
		</div>
	);
}
