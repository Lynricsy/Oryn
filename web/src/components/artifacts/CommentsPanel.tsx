/**
 * 评论面板（计划 M14 3.3、3.4）：查看器侧栏列的「评论」分段。未解决的串在前并编号（编号与页面上的图钉一致），
 * 已解决的折叠成「显示 N 条已解决」。每张卡：编号、锚点标签（元素已不在时加标记）、各条评论的作者（「你」或线程标题）
 * 与时间、回复框（`Mod+Enter`，只对未解决的串）、「标为已解决 / 重新打开」。
 * 底部「把 N 条未处理的评论发给 [线程] [发送]」：未处理 = 未解决且还没发出；发给已有线程后记下已发送，
 * 发给「新线程」只把消息交给首屏输入框（不记已发送，那时还没有线程）。
 * 设计画布（M29 I3）顶部多一个「自动投递」开关：开着时未处理的评论标「已排队」，由控制面在创建线程空闲时自动发出；
 * 手动发送仍然可用。
 */
import { type ReactNode, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import {
	type ArtifactCommentAnchor,
	type ArtifactCommentThread,
	type ArtifactDetail,
	type ArtifactThreadActivity,
	type ArtifactVersion,
	useArtifact,
	useArtifactComments,
	useArtifactDesign,
	useArtifactVersions,
	useMarkArtifactCommentsSent,
	usePatchArtifactDesign,
	useReplyArtifactComment,
	useResolveArtifactComment,
} from "../../api/artifact-queries";
import type { Thread } from "../../api/client";
import { commentsPrompt } from "../../lib/artifacts/prompt";
import { formatRelative } from "../../lib/format";
import { sendGeneratedPrompt } from "../../state/actions";
import { useIndex } from "../../state/stores";
import { showToast } from "../../state/toasts";
import { uiStore } from "../../state/ui";
import { Badge } from "../ui/data";
import { Icon } from "../ui/icons";
import {
	Button,
	describeError,
	EmptyState,
	Select,
	Spinner,
	Toggle,
} from "../ui/primitives";
import { anchorLabel, orderThreads } from "./anchors";
import {
	COMMENT_FIELD,
	CommentCount,
	commentSubmittable,
	isSubmitKey,
} from "./CommentBox";
import { useArtifactActivity } from "./use-activity";
import { authorName } from "./VersionHistory";

type DetailThread = ArtifactDetail["threads"][number];

/** 下拉里「新线程」一项的值（线程 id 不会是空串）。 */
const NEW_THREAD = "";

export interface SendTarget {
	threadId: string;
	title: string;
}

/**
 * 评论可以发给的线程：详情里未归档的相关线程（创建者与写过版本的），加上同项目里未归档、没有父线程的工作线程。
 * 排序：正在改这个 artifact 的（在场信息里 `doing` 不是 `read`）→ 最近写过版本的 → 最近活动的。
 */
export function sendTargets({
	detailThreads,
	index,
	projectId,
	activity,
	versions,
}: {
	detailThreads: readonly DetailThread[];
	index: Readonly<Record<string, Thread>>;
	projectId?: string;
	activity: readonly ArtifactThreadActivity[];
	versions: readonly ArtifactVersion[];
}): SendTarget[] {
	const titles = new Map<string, string>();
	for (const d of detailThreads)
		if (!d.archived && !index[d.threadId]?.archivedAt)
			titles.set(d.threadId, index[d.threadId]?.title ?? d.title);
	if (projectId)
		for (const th of Object.values(index))
			if (
				th.projectId === projectId &&
				!th.archivedAt &&
				!th.parentThreadId &&
				(th.kind ?? "work") === "work"
			)
				titles.set(th.id, th.title);
	const working = new Set(
		activity.filter((a) => a.doing !== "read").map((a) => a.threadId),
	);
	const wrote = new Map<string, number>();
	for (const v of versions)
		if (v.authorThreadId)
			wrote.set(
				v.authorThreadId,
				Math.max(wrote.get(v.authorThreadId) ?? 0, v.createdAt),
			);
	const recent = (id: string) => {
		const th = index[id];
		return th ? (th.lastMessageAt ?? th.updatedAt) : 0;
	};
	return [...titles]
		.map(([threadId, title]) => ({ threadId, title }))
		.toSorted(
			(a, b) =>
				Number(working.has(b.threadId)) - Number(working.has(a.threadId)) ||
				(wrote.get(b.threadId) ?? 0) - (wrote.get(a.threadId) ?? 0) ||
				recent(b.threadId) - recent(a.threadId),
		);
}

export function CommentsPanel({
	artifactId,
	threads,
	canComment,
	onAnchor,
}: {
	artifactId: string;
	/** 详情里还在的相关线程（作者显示名用）。 */
	threads: readonly DetailThread[];
	/** 能新建评论（跟随最新版本且不在回收站）：空态里给出操作提示。 */
	canComment: boolean;
	/** 点评论串的锚点（画布：聚焦到它所在的画框）；不给时锚点只是文字。 */
	onAnchor?(anchor: ArtifactCommentAnchor): void;
}): ReactNode {
	const { t } = useTranslation();
	const comments = useArtifactComments(artifactId);
	const index = useIndex((s) => s.threads);
	const [showResolved, setShowResolved] = useState(false);
	const isCanvas = useArtifact(artifactId).data?.artifact.kind === "canvas";
	const design = useArtifactDesign(artifactId, isCanvas).data;
	// 设计画布且开着自动投递：未发出的评论由控制面排队投递
	const auto = design?.design === true && design.autoNotes;
	const liveTitle = (id: string) =>
		threads.find((x) => x.threadId === id)?.title ?? index[id]?.title;
	const autoBar =
		design?.design === true ? (
			<AutoNotesBar artifactId={artifactId} on={design.autoNotes} />
		) : null;
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
			<div className="flex min-h-full flex-col gap-2 p-2">
				{autoBar}
				<EmptyState icon="message" title={t("artifacts.comments.empty")}>
					{canComment ? t("artifacts.comments.emptyHint") : null}
				</EmptyState>
			</div>
		);
	return (
		<div
			data-testid="comments-panel"
			className="flex min-h-full flex-col gap-2 p-2"
		>
			{autoBar}
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
							queued={auto}
							onAnchor={onAnchor}
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
										onAnchor={onAnchor}
									/>
								</li>
							))}
						</ol>
					)}
				</>
			)}
			{open.length > 0 && (
				<SendBar
					artifactId={artifactId}
					version={comments.data?.version ?? 0}
					open={open.map((x) => x.thread)}
					detailThreads={threads}
				/>
			)}
		</div>
	);
}

/** 设计画布的「自动投递」开关：新评论与画布便签在去抖后自动发给创建线程。 */
function AutoNotesBar({
	artifactId,
	on,
}: {
	artifactId: string;
	on: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const patch = usePatchArtifactDesign();
	return (
		<div
			data-testid="comments-auto-notes"
			className="flex flex-col gap-1 rounded-lg border border-border bg-panel p-2.5"
		>
			<Toggle
				checked={patch.isPending ? (patch.variables?.autoNotes ?? on) : on}
				disabled={patch.isPending}
				label={t("artifacts.comments.autoNotes")}
				onChange={(v) =>
					patch.mutate(
						{ id: artifactId, autoNotes: v },
						{
							onError: (err) =>
								showToast({
									tone: "err",
									text: t("artifacts.comments.autoNotesFailed", {
										reason: describeError(err, t),
									}),
								}),
						},
					)
				}
			/>
			<p className="text-muted text-xs">
				{on
					? t("artifacts.comments.autoNotesOn")
					: t("artifacts.comments.autoNotesOff")}
			</p>
		</div>
	);
}

/** 「把 N 条未处理的评论发给 [线程] [发送]」。 */
function SendBar({
	artifactId,
	version,
	open,
	detailThreads,
}: {
	artifactId: string;
	/** 评论列表对应的最新版本。 */
	version: number;
	/** 未解决的串（编号顺序）。 */
	open: readonly ArtifactCommentThread[];
	detailThreads: readonly DetailThread[];
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const selectId = useId();
	const artifact = useArtifact(artifactId).data?.artifact;
	const versions = useArtifactVersions(artifactId).data;
	const activity = useArtifactActivity(artifactId);
	const index = useIndex((s) => s.threads);
	const markSent = useMarkArtifactCommentsSent();
	const [choice, setChoice] = useState<string | null>(null);
	const [busy, setBusy] = useState(false);

	const unsent = open.filter((th) => th.sentAt === undefined);
	const targets = sendTargets({
		detailThreads,
		index,
		projectId: artifact?.projectId,
		activity: activity?.threads ?? [],
		versions: versions ?? [],
	});
	// 选过的线程不在候选里了（归档、删除）就回到排在最前的那个
	const target =
		choice !== null &&
		(choice === NEW_THREAD || targets.some((x) => x.threadId === choice))
			? choice
			: (targets[0]?.threadId ?? NEW_THREAD);
	const titleOf = (x: SendTarget) => x.title || t("thread.untitled");
	const disabled = unsent.length === 0 || busy || !artifact;

	const send = async () => {
		if (disabled || !artifact) return;
		const text = commentsPrompt(
			{ id: artifactId, title: artifact.title },
			version,
			unsent,
			t,
		);
		if (target === NEW_THREAD) {
			uiStore
				.getState()
				.seedHome(
					artifact.projectId
						? { text, projectId: artifact.projectId }
						: { text },
				);
			navigate("/");
			return;
		}
		const to = targets.find((x) => x.threadId === target);
		setBusy(true);
		try {
			await sendGeneratedPrompt(target, text);
			await markSent.mutateAsync({
				id: artifactId,
				threadIds: unsent.map((th) => th.id),
				sentTo: target,
			});
			showToast({
				text: t("artifacts.comments.sentToast", {
					count: unsent.length,
					thread: to ? titleOf(to) : target,
				}),
			});
		} catch (err) {
			showToast({
				tone: "err",
				text: t("artifacts.comments.sendFailed", {
					reason: describeError(err, t),
				}),
			});
		} finally {
			setBusy(false);
		}
	};

	return (
		<div
			data-testid="comments-send"
			className="sticky bottom-0 -mx-2 -mb-2 mt-auto flex flex-col gap-1.5 border-border border-t bg-panel p-2"
		>
			{unsent.length === 0 ? (
				<p className="flex items-center gap-1.5 text-muted text-xs">
					<Icon name="check" size={12} />
					{t("artifacts.comments.allSent")}
				</p>
			) : (
				<label htmlFor={selectId} className="text-muted text-xs">
					{t("artifacts.comments.sendLead", { count: unsent.length })}
				</label>
			)}
			<div className="flex items-center gap-1.5">
				<Select
					id={selectId}
					wrapperClassName="min-w-0 flex-1"
					aria-label={t("artifacts.comments.sendTarget")}
					value={target}
					disabled={disabled}
					onChange={(e) => setChoice(e.target.value)}
				>
					{targets.map((x) => (
						<option key={x.threadId} value={x.threadId}>
							{titleOf(x)}
						</option>
					))}
					<option value={NEW_THREAD}>
						{t("artifacts.comments.sendNewThread")}
					</option>
				</Select>
				<Button
					variant="primary"
					icon={busy ? undefined : "send"}
					disabled={disabled}
					onClick={() => void send()}
				>
					{busy ? <Spinner size={12} /> : null}
					{t("artifacts.comments.send")}
				</Button>
			</div>
		</div>
	);
}

function ThreadCard({
	artifactId,
	thread,
	n,
	liveTitle,
	queued = false,
	onAnchor,
}: {
	artifactId: string;
	thread: ArtifactCommentThread;
	/** 未解决的串的编号；已解决的没有。 */
	n?: number;
	liveTitle(threadId: string): string | undefined;
	/** 自动投递开着：还没发出的未解决串显示「已排队」。 */
	queued?: boolean;
	onAnchor?(anchor: ArtifactCommentAnchor): void;
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
				{onAnchor ? (
					<button
						type="button"
						data-testid="comment-anchor"
						className="min-w-0 flex-1 truncate text-left text-muted text-xs hover:text-fg hover:underline"
						title={t("artifacts.comments.focusAnchor", { label })}
						onClick={() => onAnchor(thread.anchor)}
					>
						{label}
					</button>
				) : (
					<span
						className="min-w-0 flex-1 truncate text-muted text-xs"
						title={label}
					>
						{label}
					</span>
				)}
				{thread.anchor.missing && (
					<Badge tone="warn">{t("artifacts.comments.missing")}</Badge>
				)}
				{thread.sentAt !== undefined && !isResolved && (
					<span
						title={
							thread.sentTo
								? t("artifacts.comments.sentTo", {
										thread: liveTitle(thread.sentTo) ?? t("thread.untitled"),
									})
								: undefined
						}
					>
						<Badge>{t("artifacts.comments.sent")}</Badge>
					</span>
				)}
				{queued && thread.sentAt === undefined && !isResolved && (
					<span
						data-testid="comment-queued"
						title={t("artifacts.comments.queuedHint")}
					>
						<Badge>{t("artifacts.comments.queued")}</Badge>
					</span>
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
