/**
 * 仓库文件页（M24 阶段 ②，`/projects/<id>/files/<路径>?ref=`）：路径取 URL 的剩余部分，ref 缺省 = 基线。
 * 页内一条路径导航（根名是项目名，各段都可点）与 ref 选择；目录显示条目列表（带最后提交），文件与符号链接用
 * 只读的 `FileViewer` 预览（可下载）。不用 `FileBrowser`：它的选中项存在 localStorage、不进 URL，做不了深链接。
 * Git LFS 文件：能预览时在预览上方写明真实大小；取对象失败或超过预览上限时换成 `LfsFileState`（重试 / 只能下载）。
 */
import { Fragment, type ReactNode, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import type { Project } from "../../../api/client";
import { LFS_PREVIEW_MAX_BYTES, useRepoTree } from "../../../api/repo-queries";
import { FileViewer } from "../../../components/files/FileViewer";
import { Badge, Card } from "../../../components/ui/data";
import {
	EmptyState,
	ErrorText,
	Spinner,
} from "../../../components/ui/primitives";
import { downloadUrl } from "../../../lib/artifacts/download";
import {
	baseName,
	parentPath,
	projectRepoSource,
} from "../../../lib/file-source";
import { formatBytes } from "../../../lib/format";
import { RepoEntryList } from "../home/RepoEntryList";
import { ProjectGate, ProjectShell } from "../ProjectShell";
import { LfsFileState } from "./LfsFileState";
import { repoFileHref, repoRelativeUrls } from "./paths";
import { RefPicker } from "./RefPicker";
import { RepoGate } from "./RepoGate";
import { RepoNav } from "./RepoNav";

export function FilesPage(): ReactNode {
	return <ProjectGate>{(p) => <Files project={p} />}</ProjectGate>;
}

/** 文件页的地址；根目录不带结尾的 `/`。 */
function filesHref(projectId: string, path: string, ref: string): string {
	return path
		? repoFileHref(projectId, path, ref)
		: `/projects/${projectId}/files?ref=${encodeURIComponent(ref)}`;
}

function PathNav({
	project,
	path,
	refName,
}: {
	project: Project;
	path: string;
	refName: string;
}): ReactNode {
	const { t } = useTranslation();
	const segs = path ? path.split("/") : [];
	const link = "min-w-0 truncate hover:text-fg hover:underline";
	return (
		<nav
			aria-label={t("projectRepo.files.path")}
			data-testid="repo-path"
			className="flex min-w-0 flex-wrap items-center gap-x-1 text-muted text-xs"
		>
			<Link
				to={filesHref(project.id, "", refName)}
				aria-current={segs.length === 0 ? "page" : undefined}
				className={`${link} ${segs.length === 0 ? "font-medium text-fg" : ""}`}
			>
				{project.name}
			</Link>
			{segs.map((s, i) => {
				const last = i === segs.length - 1;
				return (
					<Fragment key={segs.slice(0, i + 1).join("/")}>
						<span aria-hidden className="font-mono text-faint">
							/
						</span>
						<Link
							to={filesHref(
								project.id,
								segs.slice(0, i + 1).join("/"),
								refName,
							)}
							aria-current={last ? "page" : undefined}
							className={`${link} font-mono ${last ? "font-medium text-fg" : ""}`}
						>
							{s}
						</Link>
					</Fragment>
				);
			})}
		</nav>
	);
}

function FilesBody({
	project,
	path,
	refName,
}: {
	project: Project;
	path: string;
	refName: string;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const tree = useRepoTree(project.id, refName, path, true);
	const source = useMemo(
		() => projectRepoSource(project.id, refName),
		[project.id, refName],
	);
	// Markdown 预览里的相对链接与图片相对文件所在目录
	const relativeTo = useMemo(
		() => repoRelativeUrls(project.id, refName, parentPath(path)),
		[project.id, refName, path],
	);
	let body: ReactNode;
	if (tree.isPending)
		body = (
			<div className="flex justify-center p-6 text-muted">
				<Spinner size={18} />
			</div>
		);
	else if (tree.isError) body = <ErrorText error={tree.error} />;
	else if (tree.data.self.type === "dir")
		body = (
			<Card>
				<RepoEntryList
					projectId={project.id}
					refName={refName}
					tree={tree.data}
				/>
			</Card>
		);
	else if (tree.data.self.type === "submodule")
		body = (
			<Card>
				<EmptyState icon="box" title={tree.data.self.name}>
					<span className="font-mono text-xs">{tree.data.self.oid}</span>
				</EmptyState>
			</Card>
		);
	else {
		const self = tree.data.self;
		const lfsKind = self.lfs?.error
			? "error"
			: self.lfs && self.size > LFS_PREVIEW_MAX_BYTES
				? "large"
				: null;
		body = lfsKind ? (
			<LfsFileState
				kind={lfsKind}
				projectId={project.id}
				refName={refName}
				path={path}
				entry={self}
				onRetry={() => void tree.refetch()}
			/>
		) : (
			<>
				{self.lfs && (
					<p
						data-testid="repo-lfs-note"
						className="flex items-center gap-2 text-faint text-xs"
					>
						<Badge title={t("projectRepo.lfs.badgeTitle")}>
							{t("projectRepo.lfs.badge")}
						</Badge>
						{t("projectRepo.lfs.stored", { size: formatBytes(self.size) })}
					</p>
				)}
				<div
					data-testid="file-viewer"
					className="overflow-hidden rounded-lg border border-border bg-panel"
				>
					<FileViewer
						source={source}
						path={path}
						readOnly
						onDownload={(p) => {
							const url = source.downloadUrl(p);
							if (url) downloadUrl(url, baseName(p));
						}}
						relativeTo={relativeTo}
						fitContent
					/>
				</div>
			</>
		);
	}
	return (
		<div data-testid="repo-files" className="flex flex-col gap-3">
			<div className="flex flex-wrap items-center gap-2">
				<RepoNav projectId={project.id} />
				<RefPicker
					projectId={project.id}
					value={refName}
					onChange={(ref) => navigate(filesHref(project.id, path, ref))}
				/>
			</div>
			<PathNav project={project} path={path} refName={refName} />
			{body}
		</div>
	);
}

function Files({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const params = useParams();
	const [sp] = useSearchParams();
	// 结尾的 `/`（`…/files/` 或 `…/docs/`）不算一段
	const path = (params["*"] ?? "").replace(/\/+$/, "");
	return (
		<ProjectShell
			project={project}
			wide
			crumbs={[{ label: t("projectRepo.files.title") }]}
		>
			<RepoGate project={project}>
				{(status) => (
					<FilesBody
						project={project}
						path={path}
						refName={sp.get("ref") || status.baseBranch || ""}
					/>
				)}
			</RepoGate>
		</ProjectShell>
	);
}
