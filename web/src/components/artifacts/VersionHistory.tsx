/**
 * 版本历史（计划 M14 2.6）：查看器侧栏列里的版本列表，从新到旧。每行：`v{n}`、主标题（有备注用备注，否则用作者名）、
 * 小字（作者 + 相对时间），最新一行带「最新」标签；选中的一行高亮。点一行 → `onSelect`（查看器以该版本只读显示）。标题与关闭按钮
 * 在查看器侧栏的页头（与评论面板共用），这里只有列表。
 * 作者名：没有作者线程 = 「你」；线程还在显示它当前的标题；不在了显示写入时的标题快照，再没有显示「已删除的线程」。
 */
import type { TFunction } from "i18next";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	type ArtifactDetail,
	type ArtifactVersion,
	useArtifactVersions,
} from "../../api/artifact-queries";
import { formatRelative } from "../../lib/format";
import { useIndex } from "../../state/stores";
import { Badge } from "../ui/data";
import { Button, describeError, Spinner } from "../ui/primitives";

type DetailThread = ArtifactDetail["threads"][number];

/** 版本作者的显示名（规则见文件头）。`liveTitle` 查当前还在的线程标题。 */
export function authorName(
	v: Pick<ArtifactVersion, "authorThreadId" | "authorLabel">,
	liveTitle: (threadId: string) => string | undefined,
	t: TFunction,
): string {
	if (!v.authorThreadId) return t("artifacts.history.you");
	return (
		liveTitle(v.authorThreadId) ||
		v.authorLabel ||
		t("artifacts.history.deletedThread")
	);
}

export function VersionHistory({
	artifactId,
	head,
	threads,
	selected,
	onSelect,
}: {
	artifactId: string;
	head: number;
	/** 详情里还在的相关线程（创建者与写过版本的）。 */
	threads: readonly DetailThread[];
	/** 正在看的版本号（跟随最新时 = head）。 */
	selected: number;
	onSelect(v: ArtifactVersion): void;
}): ReactNode {
	const { t } = useTranslation();
	const versions = useArtifactVersions(artifactId);
	const index = useIndex((s) => s.threads);
	const liveTitle = (id: string) =>
		threads.find((x) => x.threadId === id)?.title ?? index[id]?.title;
	// 服务端已按新 → 旧返回；这里再排一次，不依赖顺序
	const list = (versions.data ?? []).toSorted((a, b) => b.version - a.version);

	let body: ReactNode;
	if (versions.isPending)
		body = (
			<div className="flex justify-center py-8 text-faint">
				<Spinner />
			</div>
		);
	else if (versions.isError)
		body = (
			<div className="flex flex-col items-center gap-3 px-4 py-8 text-center text-muted text-sm">
				<span className="break-words">{describeError(versions.error, t)}</span>
				<Button
					size="sm"
					icon="refresh"
					onClick={() => void versions.refetch()}
				>
					{t("artifacts.viewer.retry")}
				</Button>
			</div>
		);
	else
		body = (
			<ol
				aria-label={t("artifacts.history.title")}
				data-testid="version-list"
				className="flex flex-col gap-0.5 p-1.5"
			>
				{list.map((v) => {
					const author = authorName(v, liveTitle, t);
					const current = v.version === selected;
					return (
						<li key={v.version}>
							<button
								type="button"
								data-version={v.version}
								aria-current={current ? "true" : undefined}
								onClick={() => onSelect(v)}
								className={`flex w-full items-start gap-2.5 rounded-md px-2 py-2 text-left transition-colors duration-150 hover:bg-hover ${current ? "bg-active" : ""}`}
							>
								<span className="mt-px w-8 shrink-0 font-mono text-muted text-xs tabular-nums">
									{t("artifacts.viewer.version", { n: v.version })}
								</span>
								<span className="flex min-w-0 flex-1 flex-col gap-0.5">
									<span className="flex min-w-0 items-center gap-1.5">
										<span className="min-w-0 truncate font-medium text-fg text-sm">
											{v.note || author}
										</span>
										{v.version === head && (
											<Badge tone="neutral">
												{t("artifacts.history.latest")}
											</Badge>
										)}
									</span>
									<span className="truncate text-faint text-xs">
										{t("artifacts.history.byline", {
											author,
											when: formatRelative(v.createdAt),
										})}
									</span>
								</span>
							</button>
						</li>
					);
				})}
			</ol>
		);

	return <div data-testid="version-history">{body}</div>;
}
