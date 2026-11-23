/**
 * 仓库目录的条目列表（M24 阶段 ②，仿 GitHub 文件表的行）：类型图标（目录是实心的蓝灰文件夹）、名字（Git LFS 文件带
 * 「LFS」徽标）、该条目最后一次提交的 subject 与多久前；行间发丝线，点进文件页。放在 `RepoBox` 里，自身不带外框与内边距。
 * 子模块不可点（镜像里没有它的内容）。条目被截断、或服务端没来得及找全最后提交时，有说明 / 「—」。
 * 主页「文件」区块与文件页的目录视图共用。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { RepoTree, RepoTreeEntry } from "../../../api/repo-queries";
import { useNow } from "../../../components/layout/pane-parts";
import { Badge } from "../../../components/ui/data";
import { Icon, type IconName } from "../../../components/ui/icons";
import { formatRelative } from "../../../lib/format";
import { emojify } from "../../../lib/gitmoji";
import { repoFileHref } from "../repo/paths";

const ICON: Record<RepoTreeEntry["type"], IconName> = {
	dir: "folder",
	file: "file",
	symlink: "link",
	submodule: "box",
};

const ROW = "flex min-h-10 items-center gap-3 px-4 py-2 text-sm";

function EntryRow({
	entry,
	missing,
	now,
}: {
	entry: RepoTreeEntry;
	missing: string;
	now: number;
}): ReactNode {
	const { t } = useTranslation();
	const c = entry.lastCommit;
	const dir = entry.type === "dir";
	return (
		<>
			<Icon
				name={ICON[entry.type]}
				size={16}
				weight={dir ? "fill" : undefined}
				aria-hidden
				className={`shrink-0 ${dir ? "text-frost" : "text-faint"}`}
			/>
			<span className="flex min-w-0 flex-1 items-center gap-1.5 sm:flex-none sm:basis-2/5">
				<span className="min-w-0 truncate group-hover:text-accent-ink group-hover:underline">
					{entry.name}
				</span>
				{entry.lfs && (
					<Badge title={t("projectRepo.lfs.badgeTitle")}>
						{t("projectRepo.lfs.badge")}
					</Badge>
				)}
			</span>
			<span
				className="hidden min-w-0 flex-1 truncate text-muted sm:block"
				title={c?.subject}
			>
				{c ? emojify(c.subject) : missing}
			</span>
			<span className="shrink-0 text-faint text-xs tabular-nums">
				{c ? formatRelative(c.ts, now) : missing}
			</span>
		</>
	);
}

export function RepoEntryList({
	projectId,
	refName,
	tree,
}: {
	projectId: string;
	refName: string;
	tree: RepoTree;
}): ReactNode {
	const { t } = useTranslation();
	const now = useNow();
	// 服务端没找全最后提交时缺值显示「—」；找全了仍没有（不会发生）就留空
	const missing = tree.lastCommitsComplete ? "" : "—";
	return (
		<>
			<ul data-testid="repo-entry-list">
				{tree.entries.map((e) => (
					<li
						key={e.path}
						data-testid="repo-entry"
						data-path={e.path}
						className="border-border border-t first:border-t-0"
					>
						{e.type === "submodule" ? (
							<div className={`${ROW} text-muted`}>
								<EntryRow entry={e} missing={missing} now={now} />
							</div>
						) : (
							<Link
								to={repoFileHref(projectId, e.path, refName)}
								className={`group ${ROW} transition-colors duration-150 hover:bg-hover`}
							>
								<EntryRow entry={e} missing={missing} now={now} />
							</Link>
						)}
					</li>
				))}
			</ul>
			{tree.truncated && (
				<p className="border-border border-t px-4 py-2 text-faint text-xs">
					{t("projectRepo.files.truncated")}
				</p>
			)}
		</>
	);
}
