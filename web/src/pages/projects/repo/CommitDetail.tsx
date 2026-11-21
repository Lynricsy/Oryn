/**
 * 提交详情（M24 阶段 ②）：subject、正文、作者与时间、关联线程、父提交、复制 SHA；改动文件逐个折叠，展开后是该文件的
 * diff（自带滚动的盒子，虚拟列表）。不超过 5 个文件时默认全部展开。窄屏有返回钮（回到提交列表）。
 */
import { type ReactNode, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Schemas } from "../../../api/client";
import { useRepoCommit, useRepoCommitDiff } from "../../../api/repo-queries";
import {
	FileChangeLabel,
	PatchView,
} from "../../../components/changes/FileDiff";
import { Callout, ListRow } from "../../../components/ui/data";
import { Icon } from "../../../components/ui/icons";
import {
	ErrorText,
	IconButton,
	Spinner,
} from "../../../components/ui/primitives";
import { formatDateTime } from "../../../lib/format";
import { parseTitleCard } from "../../../lib/title";
import { CopyIconButton } from "../home/HeaderActions";

type FileChange = Schemas["GitFileChange"];

/** 不超过这么多个改动文件时默认全部展开。 */
const OPEN_ALL_MAX = 5;

function FilePatch({
	projectId,
	sha,
	file,
}: {
	projectId: string;
	sha: string;
	file: FileChange;
}): ReactNode {
	const scrollRef = useRef<HTMLDivElement | null>(null);
	const q = useRepoCommitDiff(projectId, sha, file);
	return (
		<div
			ref={scrollRef}
			className="max-h-[70dvh] overflow-auto border-border border-t p-2"
		>
			<PatchView diff={q.data ?? null} error={q.error} scrollRef={scrollRef} />
		</div>
	);
}

function FileItem({
	projectId,
	sha,
	file,
	open,
	onToggle,
}: {
	projectId: string;
	sha: string;
	file: FileChange;
	open: boolean;
	onToggle: () => void;
}): ReactNode {
	return (
		<li
			data-testid="repo-commit-file"
			data-path={file.path}
			className="overflow-hidden rounded-md border border-border"
		>
			<button
				type="button"
				aria-expanded={open}
				onClick={onToggle}
				className="flex min-h-9 w-full items-center gap-2 px-2 py-1.5 text-left transition-colors duration-150 hover:bg-hover"
			>
				<Icon
					name={open ? "chevronDown" : "chevronRight"}
					size={13}
					aria-hidden
					className="shrink-0 text-faint"
				/>
				<FileChangeLabel file={file} />
			</button>
			{open && <FilePatch projectId={projectId} sha={sha} file={file} />}
		</li>
	);
}

export function CommitDetail({
	projectId,
	sha,
	parentHref,
	onBack,
}: {
	projectId: string;
	sha: string;
	/** 父提交的站内地址（保留列表的 `?ref=`） */
	parentHref: (sha: string) => string;
	/** 窄屏：回到提交列表 */
	onBack?: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const q = useRepoCommit(projectId, sha);
	// 用户点过的文件以用户为准；没点过的按文件数决定是否展开
	const [toggled, setToggled] = useState<Record<string, boolean>>({});
	const c = q.data;
	const back = onBack && (
		<IconButton icon="back" label={t("common.back")} onClick={onBack} />
	);
	if (!c)
		return (
			<div data-testid="repo-commit-detail" className="flex flex-col gap-2">
				{back}
				{q.isError ? (
					<ErrorText error={q.error} />
				) : (
					<div className="flex justify-center p-6 text-muted">
						<Spinner size={18} />
					</div>
				)}
			</div>
		);
	const openByDefault = c.files.length <= OPEN_ALL_MAX;
	const body = c.body.trim();
	return (
		<div
			data-testid="repo-commit-detail"
			className="flex min-w-0 flex-col gap-3"
		>
			<section className="flex flex-col gap-3 rounded-lg border border-border bg-panel p-4">
				<div className="flex items-start gap-2">
					{back}
					<h2 className="min-w-0 flex-1 break-words pt-1 font-medium text-base text-fg">
						{c.subject}
					</h2>
					<CopyIconButton
						icon="copy"
						label={t("projectRepo.commit.copySha")}
						text={c.sha}
						failText={t("projectRepo.copyFailed")}
					/>
				</div>
				{body && (
					<p className="whitespace-pre-wrap break-words text-muted text-sm">
						{body}
					</p>
				)}
				<dl className="flex flex-wrap items-center gap-x-4 gap-y-1 text-faint text-xs">
					<div className="flex min-w-0 items-center gap-1.5">
						<dt className="sr-only">{t("projectRepo.commit.author")}</dt>
						<dd className="min-w-0 truncate text-muted" title={c.authorEmail}>
							{c.authorName}
						</dd>
					</div>
					<div className="flex items-center gap-1.5">
						<dt className="sr-only">{t("projectRepo.commit.date")}</dt>
						<dd className="tabular-nums">{formatDateTime(c.ts)}</dd>
					</div>
					<div className="flex items-center gap-1.5">
						<dt className="sr-only">SHA</dt>
						<dd className="font-mono" title={c.sha}>
							{c.shortSha}
						</dd>
					</div>
					{c.parents.length > 0 && (
						<div className="flex items-center gap-1.5">
							<dt>{t("projectRepo.commit.parents")}</dt>
							<dd className="flex items-center gap-1.5 font-mono">
								{c.parents.map((p) => (
									<Link
										key={p}
										to={parentHref(p)}
										className="text-accent-ink hover:underline"
									>
										{p.slice(0, 7)}
									</Link>
								))}
							</dd>
						</div>
					)}
				</dl>
				{c.thread && (
					<div className="flex flex-col gap-1">
						<h3 className="font-medium text-muted text-xs">
							{t("projectRepo.commit.thread")}
						</h3>
						<div
							data-testid="repo-commit-thread"
							className="-mx-2 relative rounded-md transition-colors duration-150 hover:bg-hover"
						>
							<ListRow
								leading={
									<Icon
										name="message"
										size={14}
										aria-hidden
										className="text-faint"
									/>
								}
								title={
									<Link
										to={`/t/${c.thread.id}`}
										className="min-w-0 truncate text-accent-ink after:absolute after:inset-0 after:rounded-md"
									>
										{parseTitleCard(c.thread.title).text ||
											t("thread.untitled")}
									</Link>
								}
								meta={t(`projectRepo.commit.source.${c.thread.source}`)}
							/>
						</div>
					</div>
				)}
			</section>
			<div className="flex items-center gap-2 px-1 text-muted text-xs">
				<span className="font-medium">
					{t("projectRepo.commit.filesChanged", { count: c.files.length })}
				</span>
				<span className="font-mono text-2xs tabular-nums">
					<span className="text-ok">{`+${c.additions}`}</span>
					<span className="ml-1.5 text-err">{`−${c.deletions}`}</span>
				</span>
			</div>
			{c.truncated && (
				<Callout tone="neutral" icon="info">
					{t("projectRepo.commit.truncated")}
				</Callout>
			)}
			<ul className="flex flex-col gap-2">
				{c.files.map((f) => (
					<FileItem
						key={f.path}
						projectId={projectId}
						sha={c.sha}
						file={f}
						open={toggled[f.path] ?? openByDefault}
						onToggle={() =>
							setToggled((s) => ({
								...s,
								[f.path]: !(s[f.path] ?? openByDefault),
							}))
						}
					/>
				))}
			</ul>
		</div>
	);
}
