/**
 * 仓库目录的条目列表（M24 阶段 ②）：类型图标、名字（Git LFS 文件带「LFS」徽标）、该条目最后一次提交的 subject 与
 * 多久前；点进文件页。子模块不可点（镜像里没有它的内容）。条目被截断、或服务端没来得及找全最后提交时，有说明 / 「—」。
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
import { repoFileHref } from "../repo/paths";

const ICON: Record<RepoTreeEntry["type"], IconName> = {
	dir: "folder",
	file: "file",
	symlink: "link",
	submodule: "box",
};

const ROW = "flex min-h-9 items-center gap-3 rounded-md px-2 py-1.5 text-sm";

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
	return (
		<>
			<Icon
				name={ICON[entry.type]}
				size={15}
				aria-hidden
				className={`shrink-0 ${entry.type === "dir" ? "text-muted" : "text-faint"}`}
			/>
			<span className="flex min-w-0 flex-1 items-center gap-1.5 sm:flex-none sm:basis-2/5">
				<span className="min-w-0 truncate">{entry.name}</span>
				{entry.lfs && (
					<Badge title={t("projectRepo.lfs.badgeTitle")}>
						{t("projectRepo.lfs.badge")}
					</Badge>
				)}
			</span>
			<span
				className="hidden min-w-0 flex-1 truncate text-faint text-xs sm:block"
				title={c?.subject}
			>
				{c ? c.subject : missing}
			</span>
			<span className="shrink-0 text-2xs text-faint tabular-nums">
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
		<div className="flex flex-col gap-2">
			<ul data-testid="repo-entry-list" className="-mx-2">
				{tree.entries.map((e) => (
					<li key={e.path} data-testid="repo-entry" data-path={e.path}>
						{e.type === "submodule" ? (
							<div className={`${ROW} text-muted`}>
								<EntryRow entry={e} missing={missing} now={now} />
							</div>
						) : (
							<Link
								to={repoFileHref(projectId, e.path, refName)}
								className={`${ROW} transition-colors duration-150 hover:bg-hover`}
							>
								<EntryRow entry={e} missing={missing} now={now} />
							</Link>
						)}
					</li>
				))}
			</ul>
			{tree.truncated && (
				<p className="text-faint text-xs">{t("projectRepo.files.truncated")}</p>
			)}
		</div>
	);
}
