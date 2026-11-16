/**
 * 新建评论的浮框（计划 M14 3.3）：评论模式下点页面元素后，在点击处弹出（宽 340）。
 * 定位在调用方的 `relative` 容器里：`at` 是容器坐标，浮框自己夹在 `bounds` 之内（下方放不下就翻到上方）。
 * `Mod+Enter` 提交，`Esc` 取消；正文超过 4000 字禁用提交。提交成功后由调用方收起。
 */
import {
	type KeyboardEvent,
	type ReactNode,
	useEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import {
	type ArtifactCommentAnchor,
	type ArtifactCommentThread,
	useCreateArtifactComment,
} from "../../api/artifact-queries";
import { runeCount } from "../../lib/artifacts/editable";
import { Icon } from "../ui/icons";
import { Button, describeError } from "../ui/primitives";
import { anchorLabel } from "./anchors";

export const COMMENT_BOX_WIDTH = 340;
/** 单条评论的码点上限（同服务端 `MaxCommentRunes`）。 */
export const MAX_COMMENT_RUNES = 4000;
/** 浮框离容器边缘与点击处的间距。 */
const GAP = 8;
/** 估计的浮框高度：下方放不下时翻到点击处上方。 */
const EST_HEIGHT = 176;

/** 评论正文输入框的样式（新建与回复共用）。 */
export const COMMENT_FIELD =
	"w-full resize-none rounded-md border border-border bg-inset px-2.5 py-1.5 text-sm text-fg placeholder:text-faint transition-[border-color,box-shadow] duration-150 focus-visible:border-accent focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-accent/20";

/** `Mod+Enter`（⌘ 或 Ctrl）。 */
export function isSubmitKey(e: KeyboardEvent): boolean {
	return e.key === "Enter" && (e.metaKey || e.ctrlKey);
}

/** 正文能否提交：非空且不超过上限。 */
export function commentSubmittable(body: string): boolean {
	return body.trim() !== "" && runeCount(body) <= MAX_COMMENT_RUNES;
}

/** 字数接近或超过上限时显示「n / 4000」。 */
export function CommentCount({ body }: { body: string }): ReactNode {
	const n = runeCount(body);
	if (n <= MAX_COMMENT_RUNES * 0.9) return null;
	return (
		<span
			data-testid="comment-count"
			className={`text-xs tabular-nums ${n > MAX_COMMENT_RUNES ? "text-err" : "text-faint"}`}
		>
			{n} / {MAX_COMMENT_RUNES}
		</span>
	);
}

/** 浮框在容器里的位置与宽度。 */
export function commentBoxPlacement(
	at: { x: number; y: number },
	bounds: { width: number; height: number },
): { left: number; top: number; width: number } {
	const width = Math.max(
		0,
		Math.min(COMMENT_BOX_WIDTH, bounds.width - 2 * GAP),
	);
	const left = Math.max(GAP, Math.min(at.x, bounds.width - width - GAP));
	const below = at.y + GAP;
	const top =
		below + EST_HEIGHT <= bounds.height - GAP
			? below
			: Math.max(GAP, at.y - EST_HEIGHT - GAP);
	return { left, top, width };
}

export function CommentBox({
	artifactId,
	anchor,
	at,
	bounds,
	onDone,
	onCancel,
}: {
	artifactId: string;
	anchor: ArtifactCommentAnchor;
	/** 点击处（容器坐标）。 */
	at: { x: number; y: number };
	/** 容器尺寸。 */
	bounds: { width: number; height: number };
	onDone(thread: ArtifactCommentThread): void;
	onCancel(): void;
}): ReactNode {
	const { t } = useTranslation();
	const [body, setBody] = useState("");
	const fieldRef = useRef<HTMLTextAreaElement>(null);
	// 点了元素就是要写评论：弹出即聚焦
	useEffect(() => fieldRef.current?.focus(), []);
	const create = useCreateArtifactComment();
	const ok = commentSubmittable(body) && !create.isPending;
	const place = commentBoxPlacement(at, bounds);
	const label = anchorLabel(anchor, t);

	const submit = () => {
		if (!ok) return;
		create.mutate(
			{ id: artifactId, anchor, body: body.trim() },
			{ onSuccess: onDone },
		);
	};

	return (
		<div
			role="dialog"
			aria-label={t("artifacts.comments.new")}
			data-testid="comment-box"
			style={place}
			className="absolute z-20 flex flex-col gap-2 rounded-lg border border-border bg-panel p-2.5 shadow-xl"
		>
			<div className="flex min-w-0 items-center gap-1.5 text-faint text-xs">
				<Icon name="message" size={12} className="shrink-0" />
				<span className="min-w-0 truncate" title={label}>
					{label}
				</span>
			</div>
			<textarea
				ref={fieldRef}
				rows={3}
				value={body}
				aria-label={t("artifacts.comments.new")}
				placeholder={t("artifacts.comments.placeholder")}
				onChange={(e) => setBody(e.target.value)}
				onKeyDown={(e) => {
					if (e.key === "Escape") {
						e.preventDefault();
						e.stopPropagation();
						onCancel();
					} else if (isSubmitKey(e)) {
						e.preventDefault();
						submit();
					}
				}}
				className={COMMENT_FIELD}
			/>
			{create.isError && (
				<p role="alert" className="break-words text-err text-xs">
					{describeError(create.error, t)}
				</p>
			)}
			<div className="flex items-center gap-2">
				<CommentCount body={body} />
				<span className="flex-1" />
				<Button size="sm" variant="ghost" onClick={onCancel}>
					{t("common.cancel")}
				</Button>
				<Button
					size="sm"
					variant="primary"
					data-testid="comment-submit"
					title={t("artifacts.comments.submitHint")}
					disabled={!ok}
					onClick={submit}
				>
					{t("artifacts.comments.submit")}
				</Button>
			</div>
		</div>
	);
}
