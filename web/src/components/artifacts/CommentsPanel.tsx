/**
 * 评论面板（计划 M14 3.3）：查看器侧栏列的「评论」分段。未解决的串在前并编号（编号与页面上的图钉一致），
 * 已解决的折叠成「显示 N 条已解决」。每张卡：编号、锚点标签（元素已不在时加标记）、各条评论的作者（「你」或线程标题）
 * 与时间、回复框（`Mod+Enter`，只对未解决的串）、「标为已解决 / 重新打开」。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type ArtifactCommentThread,
	type ArtifactDetail,
	useArtifactComments,
	useReplyArtifactComment,
	useResolveArtifactComment,
} from "../../api/artifact-queries";
import { formatRelative } from "../../lib/format";
import { useIndex } from "../../state/stores";
import { Badge } from "../ui/data";
import { Button, describeError, EmptyState, Spinner } from "../ui/primitives";
import { anchorLabel, orderThreads } from "./anchors";
import {
	COMMENT_FIELD,
	CommentCount,
	commentSubmittable,
	isSubmitKey,
} from "./CommentBox";
import { authorName } from "./VersionHistory";

type DetailThread = ArtifactDetail["threads"][number];

export function CommentsPanel({
	artifactId,
	threads,
	canComment,
}: {
	artifactId: string;
	/** 详情里还在的相关线程（作者显示名用）。 */
	threads: readonly DetailThread[];
	/** 能新建评论（跟随最新版本且不在回收站）：空态里给出操作提示。 */
	canComment: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const comments = useArtifactComments(artifactId);
	const index = useIndex((s) => s.threads);
	const [showResolved, setShowResolved] = useState(false);
	const liveTitle = (id: string) =>
		threads.find((x) => x.threadId === id)?.title ?? index[id]?.title;

	if (comments.isPending)
		return (
			<div className="flex justify-center py-8 text-faint">
				<Spinner />
			</div>
		);
	if (comments.isError)
		return (
			<div className="flex flex-col items-center gap-3 px-4 py-8 text-center text-muted text-sm">
				<span className="break-words">{describeError(comments.error, t)}</span>
				<Button
					size="sm"
					icon="refresh"
					onClick={() => void comments.refetch()}
				>
					{t("artifacts.viewer.retry")}
				</Button>
			</div>
		);

	const { open, resolved } = orderThreads(comments.data?.threads ?? []);
	if (open.length === 0 && resolved.length === 0)
		return (
			<EmptyState icon="message" title={t("artifacts.comments.empty")}>
				{canComment ? t("artifacts.comments.emptyHint") : null}
			</EmptyState>
		);
	return (
		<div data-testid="comments-panel" className="flex flex-col gap-2 p-2">
			{open.length === 0 && (
				<p className="px-1 py-2 text-center text-muted text-sm">
					{t("artifacts.comments.allResolved")}
				</p>
			)}
			<ol
				aria-label={t("artifacts.comments.open")}
				className="flex flex-col gap-2"
			>
				{open.map(({ thread, n }) => (
					<li key={thread.id}>
						<ThreadCard
							artifactId={artifactId}
							thread={thread}
							n={n}
							liveTitle={liveTitle}
						/>
					</li>
				))}
			</ol>
			{resolved.length > 0 && (
				<>
					<button
						type="button"
						data-testid="comments-resolved-toggle"
						aria-expanded={showResolved}
						onClick={() => setShowResolved((s) => !s)}
						className="self-start rounded-md px-2 py-1 text-muted text-xs transition-colors duration-150 hover:bg-hover hover:text-fg"
					>
						{showResolved
							? t("artifacts.comments.hideResolved")
							: t("artifacts.comments.showResolved", {
									count: resolved.length,
								})}
					</button>
					{showResolved && (
						<ol
							aria-label={t("artifacts.comments.resolved")}
							data-testid="comments-resolved"
							className="flex flex-col gap-2"
						>
							{resolved.map((thread) => (
								<li key={thread.id}>
									<ThreadCard
										artifactId={artifactId}
										thread={thread}
										liveTitle={liveTitle}
									/>
								</li>
							))}
						</ol>
					)}
				</>
			)}
		</div>
	);
}

function ThreadCard({
	artifactId,
	thread,
	n,
	liveTitle,
}: {
	artifactId: string;
	thread: ArtifactCommentThread;
	/** 未解决的串的编号；已解决的没有。 */
	n?: number;
	liveTitle(threadId: string): string | undefined;
}): ReactNode {
	const { t } = useTranslation();
	const resolve = useResolveArtifactComment();
	const isResolved = thread.resolvedAt !== undefined;
	const label = anchorLabel(thread.anchor, t);
	return (
		<article
			data-testid="comment-thread"
			data-thread={thread.id}
			data-n={n}
			className={`flex flex-col gap-2 rounded-lg border border-border bg-panel p-2.5 ${isResolved ? "opacity-70" : ""}`}
		>
			<header className="flex min-w-0 items-center gap-1.5">
				{n !== undefined && (
					<span
						data-testid="comment-number"
						className="inline-flex h-5 min-w-5 shrink-0 items-center justify-center rounded-full bg-ink px-1 font-semibold text-2xs text-ink-fg tabular-nums"
					>
						{n}
					</span>
				)}
				<span
					className="min-w-0 flex-1 truncate text-muted text-xs"
					title={label}
				>
					{label}
				</span>
				{thread.anchor.missing && (
					<Badge tone="warn">{t("artifacts.comments.missing")}</Badge>
				)}
			</header>
			<ol className="flex flex-col gap-2">
				{thread.comments.map((c) => (
					<li key={c.n} className="flex flex-col gap-0.5">
						<span className="flex min-w-0 items-baseline gap-1.5 text-xs">
							<span className="min-w-0 truncate font-medium text-fg">
								{authorName(c, liveTitle, t)}
							</span>
							<span className="shrink-0 text-faint">
								{formatRelative(c.createdAt)}
							</span>
						</span>
						<p className="whitespace-pre-wrap break-words text-fg text-sm">
							{c.body}
						</p>
					</li>
				))}
			</ol>
			{!isResolved && <ReplyBox artifactId={artifactId} threadId={thread.id} />}
			<div className="flex items-center gap-2">
				{resolve.isError && (
					<span role="alert" className="min-w-0 truncate text-err text-xs">
						{describeError(resolve.error, t)}
					</span>
				)}
				<span className="flex-1" />
				<Button
					size="sm"
					variant="ghost"
					icon={isResolved ? "restart" : "check"}
					disabled={resolve.isPending}
					onClick={() =>
						resolve.mutate({
							id: artifactId,
							threadId: thread.id,
							resolved: !isResolved,
						})
					}
				>
					{isResolved
						? t("artifacts.comments.reopen")
						: t("artifacts.comments.resolve")}
				</Button>
			</div>
		</article>
	);
}

function ReplyBox({
	artifactId,
	threadId,
}: {
	artifactId: string;
	threadId: string;
}): ReactNode {
	const { t } = useTranslation();
	const [body, setBody] = useState("");
	const reply = useReplyArtifactComment();
	const ok = commentSubmittable(body) && !reply.isPending;
	const submit = () => {
		if (!ok) return;
		reply.mutate(
			{ id: artifactId, threadId, body: body.trim() },
			{ onSuccess: () => setBody("") },
		);
	};
	return (
		<div className="flex flex-col gap-1.5">
			<textarea
				rows={1}
				value={body}
				aria-label={t("artifacts.comments.reply")}
				placeholder={t("artifacts.comments.replyPlaceholder")}
				onChange={(e) => setBody(e.target.value)}
				onKeyDown={(e) => {
					if (isSubmitKey(e)) {
						e.preventDefault();
						submit();
					}
				}}
				className={`${COMMENT_FIELD} field-sizing-content max-h-40`}
			/>
			{reply.isError && (
				<p role="alert" className="break-words text-err text-xs">
					{describeError(reply.error, t)}
				</p>
			)}
			{body !== "" && (
				<div className="flex items-center gap-2">
					<CommentCount body={body} />
					<span className="flex-1" />
					<Button
						size="sm"
						title={t("artifacts.comments.submitHint")}
						disabled={!ok}
						onClick={submit}
					>
						{t("artifacts.comments.replySend")}
					</Button>
				</div>
			)}
		</div>
	);
}
