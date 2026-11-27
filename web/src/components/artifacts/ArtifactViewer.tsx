/**
 * artifact 查看器（计划 M14 2.3、2.5、2.6、3.3）：整页（`page`）与右面板（`panel`）共用。
 * 挂载时 `watchArtifact`（收 `artifact.version` 等观看者消息），卸载时取消。
 * 顶栏：返回（仅整页，`Esc`）、类型图标、标题、版本胶囊 `v{n}`（点开版本历史）、更多菜单（`ArtifactMenu`）；
 * 右侧选择（V）/ 评论（C）两个模式按钮、版本历史、重新加载、在浏览器中打开、访客数与「分享」（8.3）。右面板里由 `ArtifactPanel` 用 `lead` 换掉图标与标题
 * （换成 artifact 切换菜单）、用 `actions` 追加「放大查看」，两者合成一行工具条。页面试图跳走时帧上方出一条提示：「此页面试图打开 <主机>」。
 *
 * 侧栏列（宽 300）有「历史 / 评论」两段（右面板只有历史）：查看器窄于 900 时覆盖在内容上，`compact` 断点下占满并在顶部给
 * 「页面 / 历史 / 评论」分段切换。手机上或查看器本身窄于 520（右面板）时顶栏收起：历史、重新加载、在浏览器中打开在更多菜单里，
 * 分享只留图标；手机上元素编辑是帧下方的非模态底部抽屉。选一个旧版本：帧以该版本重新申请（服务端不注入运行时，只读），帧上方出状态条
 * 「版本 n / head · 时间 · 备注」与「恢复这个版本」；恢复以当前 head 为 `baseVersion`，409 时提示重试。
 *
 * 选择与评论（3.3）：页面类型的 artifact 跟随最新版本且不在回收站时才有两个模式，模式经 `select-mode` 下发。评论模式下点页面元素
 * 在点击处弹 `CommentBox`（点空白处是「整个页面」的锚点）；未解决评论的编号图钉只在评论模式或评论面板打开时下发。
 * 右面板里选择模式下选中的元素经 `onSelection` 交给输入区的引用条（3.5）。
 * 演示（6.8）：页面与画布类型在顶栏有「演示」（`Presenter`；画布的演示器在编辑器里，从画框名称行也能「从这页演示」）；
 * 画布的评论面板里点锚点会聚焦到它所在的画框。
 * Office 编辑（M32）：配置了引擎时 Word / Excel / PowerPoint 跟随最新版本时顶栏有「编辑」，帧换成 `OfficeEditor`，
 * 编辑态没有评论模式与图钉；别处有编辑占用（详情的 `editing`）且自己不在编辑时帧上方出提示条。
 */
import {
	type ReactNode,
	type RefObject,
	useEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import {
	MAX_ACCENT_CHARS,
	type PageSelection,
	type SelectMode,
} from "../../../sandbox/runtime/messages";
import {
	type ArtifactCommentAnchor,
	type ArtifactVersion,
	openArtifactLink,
	useArtifact,
	useArtifactCapabilities,
	useArtifactComments,
	useArtifactDesign,
	useRestoreArtifactVersion,
	useUndeleteArtifact,
} from "../../api/artifact-queries";
import { ApiError } from "../../api/client";
import { kindIcon, typeBadge } from "../../lib/artifacts/kinds";
import type { ArtifactSelectionRef } from "../../lib/artifacts/prompt";
import { typedAnchorKind, typedEntry } from "../../lib/artifacts/typed-anchor";
import { formatRelative } from "../../lib/format";
import { useBreakpoint } from "../../lib/viewport";
import { watchArtifact } from "../../realtime/socket";
import { showToast } from "../../state/toasts";
import { useSidebarShown } from "../../state/ui";
import { ChromeLead } from "../layout/WindowControls";
import { Icon } from "../ui/icons";
import { Button, describeError, IconButton, tabClass } from "../ui/primitives";
import { ArtifactMenu } from "./ArtifactMenu";
import {
	anchorOf,
	anchorOfViewer,
	anchorPinsOf,
	pinsOf,
	viewerAnchorOf,
} from "./anchors";
import { CommentBox } from "./CommentBox";
import { CommentsPanel } from "./CommentsPanel";
import {
	CanvasEditor,
	type CanvasSelectTarget,
	type PresentRequest,
} from "./canvas/CanvasEditor";
import { EditPanel } from "./EditPanel";
import { isOfficeEditable, OfficeEditor } from "./OfficeEditor";
import {
	type AnchorPickMessage,
	type FramePost,
	PageFrame,
	type PageSelectMessage,
} from "./PageFrame";
import { PresenceFaces } from "./PresenceFaces";
import { Presenter } from "./Presenter";
import { ShareControl } from "./SharePopover";
import { VersionHistory } from "./VersionHistory";
import {
	FrameErrorState,
	ViewerDeleted,
	ViewerLoading,
	ViewerNotFound,
} from "./viewer-states";

export type ViewerVariant = "page" | "panel";

/** 侧栏列固定宽度与「并排放得下」的查看器宽度（更窄时侧栏覆盖在内容上）。 */
export const SIDE_WIDTH = 300;
export const SIDE_INLINE_MIN = 900;
/** 查看器本身窄于这个宽度（如右面板）时顶栏与手机一样收起：放不下全部按钮。 */
export const TIGHT_HEADER_MAX = 520;

/** 侧栏列的分段。 */
type SideTab = "history" | "comments";

/** 页面 artifact 的入口文件：评论锚点在没有选中元素时落在这个页面上。 */
const ENTRY_PAGE = "index.html";

/** 下发给页面覆盖层的强调色（主题色的计算值）；取不到或超长时由运行时用缺省色。 */
function overlayAccent(): string {
	const v = getComputedStyle(document.documentElement)
		.getPropertyValue("--brand")
		.trim();
	return v.length <= MAX_ACCENT_CHARS ? v : "";
}

/** 正在写的新评论：锚点与浮框位置（帧容器坐标）。 */
interface CommentDraft {
	anchor: ArtifactCommentAnchor;
	at: { x: number; y: number };
	bounds: { width: number; height: number };
}

export interface ArtifactViewerProps {
	artifactId: string;
	variant: ViewerVariant;
	/** 换掉顶栏的类型图标与标题（右面板放 artifact 切换菜单）。 */
	lead?: ReactNode;
	/** 顶栏最右侧追加的按钮（右面板的「放大查看」）。 */
	actions?: ReactNode;
	/** 所在线程（右面板）：更多菜单的「打开线程」不指向它自己。 */
	threadId?: string;
	/** 进入时就打开版本历史（`/artifacts/<id>?history=1`）。 */
	initialHistory?: boolean;
	/** 整页的返回（按钮与 `Esc`）。 */
	onBack?(): void;
	/**
	 * 右面板（3.5）：选择模式下选中的元素交出去（写进输入区草稿）；`null` = 没有选中了（清除选中、退出模式、
	 * 帧重建、卸载）。只在交出过非空的选中之后才会报 `null`。
	 */
	onSelection?(selection: ArtifactSelectionRef | null): void;
	/** 交出去的选中仍被拿着（输入区的引用条还在）；由真变假（移除或已发出）时清掉页面上的选中。 */
	selectionHeld?: boolean;
}

/**
 * 页面报告的被拦截地址规整成 `http(s)` 的绝对 URL；别的协议（`javascript:`、`data:`…）一律丢弃——
 * 地址来自不可信的页面，之后要交给 `window.open`。
 */
export function safeBlockedUrl(raw: string): string | null {
	try {
		const url = new URL(raw);
		return url.protocol === "http:" || url.protocol === "https:"
			? url.href
			: null;
	} catch {
		return null;
	}
}

function isNotFound(error: unknown): boolean {
	return (
		error instanceof ApiError &&
		(error.status === 404 || error.code === "not_found")
	);
}

/** 焦点在输入框、可编辑区域或有对话框 / 菜单打开时，`Esc` 不归查看器。 */
function escOwnedElsewhere(e: KeyboardEvent): boolean {
	if (document.querySelector("[role=dialog], [role=menu]")) return true;
	const el = e.target as HTMLElement | null;
	if (!el || typeof el.closest !== "function") return false;
	return !!el.closest("input, textarea, select, [contenteditable]");
}

/** 查看器自身的宽度（侧栏列并排还是覆盖）。没有 ResizeObserver 的环境按窄处理。 */
function useWidth(ref: RefObject<HTMLElement | null>): number {
	const [width, setWidth] = useState(0);
	useEffect(() => {
		const el = ref.current;
		if (!el || typeof ResizeObserver === "undefined") return;
		const ro = new ResizeObserver(([entry]) => {
			if (entry) setWidth(entry.contentRect.width);
		});
		ro.observe(el);
		return () => ro.disconnect();
	}, [ref]);
	return width;
}

export function ArtifactViewer({
	artifactId,
	variant,
	lead,
	actions,
	threadId,
	initialHistory = false,
	onBack,
	onSelection,
	selectionHeld = false,
}: ArtifactViewerProps): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const detail = useArtifact(artifactId);
	const undelete = useUndeleteArtifact();
	const restore = useRestoreArtifactVersion();
	const caps = useArtifactCapabilities();
	const [reloadNonce, setReloadNonce] = useState(0);
	const [blocked, setBlocked] = useState<string | null>(null);
	const [openError, setOpenError] = useState<unknown>(null);
	const [side, setSide] = useState<SideTab | null>(
		initialHistory ? "history" : null,
	);
	// compact 下侧栏打开时显示页面还是侧栏
	const [segment, setSegment] = useState<"page" | "side">("side");
	// 选择 / 评论模式（只在 `canSelect` 时生效）
	const [mode, setMode] = useState<SelectMode>(null);
	// 向帧发消息；帧重建期间为 null
	const [post, setPost] = useState<FramePost | null>(null);
	// 页面最近一次报来的路径（帧内跳到别的页面后，整页锚点与图钉跟着它）
	const [pagePath, setPagePath] = useState(ENTRY_PAGE);
	const [draft, setDraft] = useState<CommentDraft | null>(null);
	// 页面最近一次报来的选中元素（选择模式下在侧栏直接编辑它）
	const [selection, setSelection] = useState<PageSelection | null>(null);
	// 画布右面板选择模式下选中的画框内元素（交给线程输入区引用条）
	const [canvasSelection, setCanvasSelection] =
		useState<CanvasSelectTarget | null>(null);
	const [canvasClearSeq, setCanvasClearSeq] = useState(0);
	const frameBoxRef = useRef<HTMLDivElement>(null);
	const [pinned, setPinned] = useState<ArtifactVersion | null>(null);
	// 类型化 artifact 的查看器在演示：帧容器铺满窗口
	const [presenting, setPresenting] = useState(false);
	// 页面与画布的演示（6.8）；评论面板里点锚点聚焦到画框（画布）
	const [show, setShow] = useState<PresentRequest | null>(null);
	const [focusFile, setFocusFile] = useState<{
		path: string;
		seq: number;
	} | null>(null);
	// 正在 Office 编辑器里编辑的 artifact（M32）；面板切到别的 artifact 时自然失效
	const [officeOpen, setOfficeOpen] = useState<string | null>(null);
	const chromeLeft = !useSidebarShown() && variant === "page";
	const rootRef = useRef<HTMLDivElement>(null);
	const width = useWidth(rootRef);
	const compact = bp === "compact";
	const inline = !compact && width >= SIDE_INLINE_MIN;
	// 顶栏收起：手机，或量得出的查看器宽度不够（还没量到时按宽的算）
	const tight = compact || (width > 0 && width < TIGHT_HEADER_MAX);

	useEffect(() => watchArtifact(artifactId), [artifactId]);

	const artifact = detail.data?.artifact;
	const live = artifact && artifact.deletedAt === undefined;
	const typed = artifact?.type !== undefined;
	const head = artifact?.version ?? 0;
	const designState = useArtifactDesign(
		artifactId,
		!!live && artifact?.kind === "canvas",
	);
	const isDesign = !!designState.data?.design;
	// 选中的是最新版本就回到跟随
	const pinnedVersion = pinned && pinned.version !== head ? pinned : null;
	// 页面支持选择（编辑 + 引用）与评论；画布在右面板有输入区回调时也支持选择元素引用给线程
	const canSelect = !!live && !pinnedVersion && artifact?.kind === "web";
	const canCanvasSelect =
		!!live && !pinnedVersion && artifact?.kind === "canvas" && !!onSelection;
	// Office 编辑（M32）：配置了引擎、跟随最新版本、可编辑的 Office 类型
	const canEdit =
		!!live &&
		!pinnedVersion &&
		caps.data?.office.edit === true &&
		isOfficeEditable(artifact?.type);
	// 编辑态：帧换成编辑器
	const inEditor = officeOpen === artifactId && canEdit;
	// 条件不再满足（选了旧版本、被删除、引擎下线）时编辑器卸载并关闭会话，之后要重新点「编辑」
	useEffect(() => {
		if (!canEdit) setOfficeOpen(null);
	}, [canEdit]);
	// 类型化 artifact 的查看器：只有评论模式（点选与图钉由查看器经 `__orynViewer` 桥做），锚点的 path 恒为入口；编辑态没有
	const entry = typedEntry(artifact?.type);
	const canAnchor =
		!!live && !pinnedVersion && typed && entry !== null && !inEditor;
	const activeMode = canSelect
		? mode
		: canAnchor && mode === "comment"
			? "comment"
			: canCanvasSelect && mode === "select"
				? "select"
				: null;
	const commentsTab = variant === "page" && !!live;
	const comments = useArtifactComments(artifactId, !!live);
	const commentThreads = comments.data?.threads ?? [];
	const unresolved = commentThreads.filter(
		(x) => x.resolvedAt === undefined,
	).length;
	const showSide =
		side !== null && !!live && (side === "history" || commentsTab);
	const sideOnly = showSide && compact && segment === "side";
	const pinsShown =
		(canSelect || canAnchor) &&
		(activeMode === "comment" || (showSide && side === "comments"));
	// 内容不变就不重发（JSON 作依赖键）
	const pinsKey = JSON.stringify(
		pinsShown && canSelect ? pinsOf(commentThreads, pagePath) : [],
	);
	const anchorPinsKey = JSON.stringify(
		pinsShown && canAnchor && entry ? anchorPinsOf(commentThreads, entry) : [],
	);
	// 画布的评论工具在编辑器里，跟随最新且不在回收站时可以评论
	const canvasComments =
		!!live && !pinnedVersion && artifact?.kind === "canvas";
	const shownDraft = activeMode === "comment" || canvasComments ? draft : null;
	// 元素直接编辑（3.6）：整页里选择模式下有选中元素时显示
	const editing =
		variant === "page" && activeMode === "select" ? selection : null;
	// 编辑面板在宽屏下占用侧栏列（历史 / 评论让位）；compact 下是帧下方的底部抽屉
	const sideVisible =
		showSide && !(editing && !compact) && (!compact || segment === "side");

	useEffect(() => {
		post?.({ kind: "select-mode", mode: activeMode, accent: overlayAccent() });
	}, [post, activeMode]);
	useEffect(() => {
		post?.({ kind: "select-pins", pins: JSON.parse(pinsKey) });
	}, [post, pinsKey]);
	useEffect(() => {
		if (typed) post?.({ kind: "anchor-pins", pins: JSON.parse(anchorPinsKey) });
	}, [post, typed, anchorPinsKey]);

	function toggleMode(m: Exclude<SelectMode, null>) {
		const entering = activeMode !== m;
		setMode(entering ? m : null);
		setDraft(null);
		setSelection(null);
		setCanvasSelection(null);
		if (entering && m === "comment" && commentsTab && !compact) {
			setSide("comments");
			setSegment("side");
		}
	}

	// Esc 依次：回到最新版本 → 退出选择 / 评论模式 → 关掉侧栏 → 返回（整页）
	const escRef = useRef<() => boolean>(() => false);
	escRef.current = () => {
		if (pinned) {
			setPinned(null);
			return true;
		}
		if (activeMode) {
			setMode(null);
			setDraft(null);
			setSelection(null);
			setCanvasSelection(null);
			return true;
		}
		if (showSide) {
			setSide(null);
			return true;
		}
		if (variant === "page" && onBack) {
			onBack();
			return true;
		}
		return false;
	};
	// V / C：无修饰键、焦点不在输入框、没有对话框或菜单时切换模式
	const modeKeyRef = useRef<(m: Exclude<SelectMode, null>) => boolean>(
		() => false,
	);
	modeKeyRef.current = (m) => {
		if (canSelect) {
			toggleMode(m);
			return true;
		}
		if (canAnchor && m === "comment") {
			toggleMode("comment");
			return true;
		}
		if (canCanvasSelect && m === "select") {
			toggleMode("select");
			return true;
		}
		return false;
	};
	useEffect(() => {
		const onKey = (e: KeyboardEvent) => {
			if (e.defaultPrevented || escOwnedElsewhere(e)) return;
			if (e.key === "Escape") {
				if (escRef.current()) e.preventDefault();
				return;
			}
			if (e.ctrlKey || e.metaKey || e.altKey || e.shiftKey) return;
			const m =
				e.key === "v" || e.key === "V"
					? "select"
					: e.key === "c" || e.key === "C"
						? "comment"
						: null;
			if (m && modeKeyRef.current(m)) e.preventDefault();
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, []);

	/** 在帧容器里点击处（视口坐标）开一个新评论。 */
	const startComment = (
		anchor: ArtifactCommentAnchor,
		client: { x: number; y: number },
	) => {
		const box = frameBoxRef.current?.getBoundingClientRect();
		if (!box) return;
		setDraft({
			anchor,
			at: { x: client.x - box.left, y: client.y - box.top },
			bounds: { width: box.width, height: box.height },
		});
	};

	const onPageSelect = (msg: PageSelectMessage) => {
		// 定位应答不是新的选择
		if (msg.located) return;
		setSelection(msg.selection);
		const path = msg.selection?.path || pagePath;
		if (msg.selection) setPagePath(path);
		if (activeMode !== "comment" || !msg.at) return;
		const frame = frameBoxRef.current
			?.querySelector("iframe")
			?.getBoundingClientRect();
		if (!frame) return;
		startComment(anchorOf(msg.selection, { version: head, path }), {
			x: frame.left + msg.at.x,
			y: frame.top + msg.at.y,
		});
	};

	// 查看器在评论模式下的点选；点在内容之外（`anchor` 为空）只对 Markdown 文档算整篇，其余类型锚点必须带页
	const onAnchorPick = (msg: AnchorPickMessage) => {
		if (activeMode !== "comment" || !entry) return;
		if (msg.anchor === null && typedAnchorKind(entry) !== "block") return;
		const frame = frameBoxRef.current
			?.querySelector("iframe")
			?.getBoundingClientRect();
		if (!frame) return;
		startComment(anchorOfViewer(msg.anchor, { version: head, path: entry }), {
			x: frame.left + msg.at.x,
			y: frame.top + msg.at.y,
		});
	};

	const closeDraft = () => {
		setDraft(null);
		// 页面上的选中框随之清掉
		post?.({ kind: "select-source", src: null });
	};

	// 关掉编辑面板或保存冲突：页面与这里一起清掉选中
	const clearSelection = () => {
		setSelection(null);
		setCanvasSelection(null);
		setCanvasClearSeq((n) => n + 1);
		post?.({ kind: "select-source", src: null });
	};

	// 右面板（3.5 / M29 K3）：选择模式下选中的元素交给输入区；内容不变就不重报（JSON 作键）
	const selectedAnchor =
		activeMode === "select"
			? selection
				? anchorOf(selection, { version: head, path: pagePath })
				: canvasSelection
					? anchorOf(canvasSelection.selection, canvasSelection.shown)
					: null
			: null;
	const selectedKey =
		onSelection && selectedAnchor && artifact
			? JSON.stringify({
					artifactId,
					title: artifact.title,
					version: head,
					anchor: selectedAnchor,
				} satisfies ArtifactSelectionRef)
			: "";
	const onSelectionRef = useRef(onSelection);
	onSelectionRef.current = onSelection;
	const reportedRef = useRef("");
	useEffect(() => {
		if (selectedKey === reportedRef.current) return;
		reportedRef.current = selectedKey;
		onSelectionRef.current?.(
			selectedKey ? (JSON.parse(selectedKey) as ArtifactSelectionRef) : null,
		);
	}, [selectedKey]);
	useEffect(
		() => () => {
			if (reportedRef.current) onSelectionRef.current?.(null);
		},
		[],
	);
	// 输入区不再拿着交出的选中（移除了引用条或已发出）：页面上的选中一起清掉
	const heldRef = useRef(selectionHeld);
	// biome-ignore lint/correctness/useExhaustiveDependencies: 只由 selectionHeld 由真变假触发
	useEffect(() => {
		const was = heldRef.current;
		heldRef.current = selectionHeld;
		if (was && !selectionHeld && (selection || canvasSelection))
			clearSelection();
	}, [selectionHeld]);

	const editPanel = (sel: PageSelection) => (
		<EditPanel
			artifactId={artifactId}
			selection={sel}
			version={head}
			block={pinnedVersion ? "old-version" : live ? null : "deleted"}
			post={post}
			onConflict={clearSelection}
			onClose={clearSelection}
		/>
	);

	const openInBrowser = (url?: string) => {
		setOpenError(null);
		if (url) {
			window.open(url, "_blank", "noopener");
			return;
		}
		openArtifactLink(artifactId, pinnedVersion?.version).then(
			(link) => window.open(link, "_blank", "noopener"),
			(err: unknown) => setOpenError(err),
		);
	};

	const openEditor = () => {
		setMode(null);
		setDraft(null);
		setPresenting(false);
		setOfficeOpen(artifactId);
	};

	const toggleHistory = () => {
		setSegment("side");
		setSide((s) => (s === "history" && showSide ? null : "history"));
	};

	const restorePinned = async () => {
		if (!pinnedVersion || restore.isPending) return;
		try {
			const r = await restore.mutateAsync({
				id: artifactId,
				version: pinnedVersion.version,
				baseVersion: head,
			});
			if (r.kind === "conflict") {
				showToast({ tone: "err", text: t("artifacts.history.conflict") });
				return;
			}
			setPinned(null);
			showToast({
				text: t("artifacts.history.restored", {
					n: pinnedVersion.version,
					version: r.artifact.version,
				}),
			});
		} catch (e) {
			showToast({ tone: "err", text: describeError(e, t) });
		}
	};

	const panel = variant === "panel";
	const title = artifact?.title ?? "";

	let body: ReactNode;
	if (detail.isPending) body = <ViewerLoading />;
	else if (detail.isError)
		body = isNotFound(detail.error) ? (
			<ViewerNotFound />
		) : (
			<FrameErrorState
				error={detail.error}
				onRetry={() => void detail.refetch()}
			/>
		);
	else if (artifact?.deletedAt !== undefined)
		body = (
			<ViewerDeleted
				deletedAt={artifact.deletedAt}
				restoring={undelete.isPending}
				onRestore={() => undelete.mutate(artifactId)}
			/>
		);
	else if (inEditor)
		body = (
			<OfficeEditor
				artifactId={artifactId}
				title={title}
				onClose={() => setOfficeOpen(null)}
			/>
		);
	else if (artifact?.kind === "canvas")
		body = (
			<CanvasEditor
				key={reloadNonce}
				artifactId={artifactId}
				version={pinnedVersion?.version}
				variant={variant}
				title={title}
				onComment={
					canvasComments
						? (target) => {
								const root = frameBoxRef.current
									?.querySelector("[data-testid=canvas-editor]")
									?.getBoundingClientRect();
								if (!root) return;
								startComment(anchorOf(target.selection, target.shown), {
									x: root.left + target.at.x,
									y: root.top + target.at.y,
								});
							}
						: undefined
				}
				selectElementMode={canCanvasSelect && activeMode === "select"}
				onSelectElement={canCanvasSelect ? setCanvasSelection : undefined}
				clearSelectionSeq={canvasClearSeq}
				pins={
					canvasComments ? (path) => pinsOf(commentThreads, path) : undefined
				}
				present={show}
				onPresentChange={setShow}
				focusFile={focusFile}
			/>
		);
	else
		body = (
			<PageFrame
				artifactId={artifactId}
				version={pinnedVersion?.version}
				reloadNonce={reloadNonce}
				title={t("artifacts.viewer.frameTitle", { title })}
				onBlocked={(url) => {
					const safe = safeBlockedUrl(url);
					if (safe) setBlocked(safe);
				}}
				onPresent={typed ? setPresenting : undefined}
				onLink={(p) => {
					setPost(() => p);
					// 帧重建后页面里没有选中
					if (!p) setSelection(null);
				}}
				onSelect={onPageSelect}
				onAnchorPick={canAnchor ? onAnchorPick : undefined}
			/>
		);

	const sideTitle =
		side === "comments"
			? t("artifacts.comments.title")
			: t("artifacts.history.title");

	return (
		<div
			ref={rootRef}
			data-testid="artifact-viewer"
			data-variant={variant}
			className="flex h-full min-h-0 flex-col"
		>
			<div
				role="toolbar"
				aria-label={t("artifacts.viewer.toolbar")}
				data-window-drag={panel ? undefined : ""}
				className={`flex shrink-0 items-center gap-1 border-border border-b px-2 ${panel ? "h-11" : "h-12 sm:px-3"}`}
			>
				<ChromeLead show={chromeLeft} />
				{variant === "page" && onBack && (
					<IconButton
						icon="back"
						label={t("artifacts.viewer.back")}
						onClick={onBack}
					/>
				)}
				{lead ?? (
					<h1 className="flex min-w-0 items-center gap-2 px-1 font-semibold text-sm">
						{artifact && !tight && (
							<Icon
								name={kindIcon(artifact.kind)}
								size={15}
								className="shrink-0 text-faint"
							/>
						)}
						<span data-testid="artifact-title" className="truncate">
							{title}
						</span>
						{artifact && typeBadge(artifact.type) && (
							<span
								data-testid="artifact-type-badge"
								className="shrink-0 rounded border border-border px-1 font-normal text-2xs text-muted leading-4"
							>
								{typeBadge(artifact.type)}
							</span>
						)}
					</h1>
				)}
				{artifact && (
					<button
						type="button"
						data-testid="artifact-version"
						title={t("artifacts.viewer.versionTitle", { n: artifact.version })}
						aria-pressed={showSide && side === "history"}
						disabled={!live}
						onClick={toggleHistory}
						className="shrink-0 rounded-full border border-border px-1.5 font-mono text-2xs text-muted tabular-nums leading-5 transition-colors duration-150 enabled:hover:border-border-strong enabled:hover:text-fg"
					>
						{t("artifacts.viewer.version", { n: artifact.version })}
					</button>
				)}
				{live && (
					<ArtifactMenu
						artifact={artifact}
						place="viewer"
						threadId={threadId}
						align="left"
						onReload={() => {
							setBlocked(null);
							setReloadNonce((n) => n + 1);
						}}
						onHistory={() => {
							setSegment("side");
							setSide("history");
						}}
					/>
				)}
				<span className="flex-1" />
				{live && (
					<>
						<PresenceFaces
							artifactId={artifactId}
							threads={detail.data?.threads}
							canFollow={artifact.kind === "canvas" && !pinnedVersion}
						/>
						{(canSelect || canCanvasSelect) && (
							<IconButton
								icon="cursor"
								box="sm"
								size={15}
								active={activeMode === "select"}
								data-testid="artifact-mode-select"
								label={t("artifacts.comments.selectMode")}
								onClick={() => toggleMode("select")}
							/>
						)}
						{(canSelect || canAnchor) && (
							<span className="relative inline-flex shrink-0">
								<IconButton
									icon="message"
									box="sm"
									size={15}
									active={activeMode === "comment"}
									data-testid="artifact-mode-comment"
									label={t("artifacts.comments.commentMode")}
									onClick={() => toggleMode("comment")}
								/>
								{unresolved > 0 && (
									<span
										aria-hidden
										className="pointer-events-none absolute -top-0.5 -right-0.5 inline-flex h-3.5 min-w-3.5 items-center justify-center rounded-full bg-accent px-0.5 font-semibold text-[9px] text-accent-fg tabular-nums leading-none"
									>
										{unresolved > 99 ? "99+" : unresolved}
									</span>
								)}
							</span>
						)}
						{canEdit &&
							!inEditor &&
							(tight ? (
								<IconButton
									icon="edit"
									box="sm"
									size={15}
									data-testid="artifact-office-edit"
									label={t("artifacts.office.editTitle")}
									onClick={openEditor}
								/>
							) : (
								<Button
									size="sm"
									icon="edit"
									data-testid="artifact-office-edit"
									title={t("artifacts.office.editTitle")}
									onClick={openEditor}
								>
									{t("artifacts.office.edit")}
								</Button>
							))}
						{(artifact.kind === "web" || artifact.kind === "canvas") && (
							<IconButton
								icon="play"
								box="sm"
								size={15}
								data-testid="artifact-present"
								label={t("artifacts.present.button")}
								onClick={() => setShow({ start: null })}
							/>
						)}
						{/* 顶栏收起时：历史、重新加载、在浏览器中打开都在更多菜单里（历史也可点版本胶囊） */}
						{!tight && (
							<>
								<IconButton
									icon="history"
									box="sm"
									size={15}
									active={showSide && side === "history"}
									data-testid="artifact-history-toggle"
									label={t("artifacts.history.title")}
									onClick={toggleHistory}
								/>
								<IconButton
									icon="refresh"
									box="sm"
									size={15}
									label={t("artifacts.viewer.reload")}
									onClick={() => {
										setBlocked(null);
										setReloadNonce((n) => n + 1);
									}}
								/>
								<IconButton
									icon="external"
									box="sm"
									size={15}
									label={t("artifacts.viewer.openInBrowser")}
									onClick={() => openInBrowser()}
								/>
							</>
						)}
						<ShareControl artifact={artifact} compact={tight} />
					</>
				)}
				{actions}
			</div>
			{(blocked !== null || openError !== null) && (
				<div
					role="status"
					data-testid={blocked ? "artifact-blocked" : "artifact-open-error"}
					className="flex shrink-0 items-center gap-2 border-border border-b bg-inset px-3 py-1.5 text-sm"
				>
					<Icon
						name={blocked ? "globe" : "alert"}
						size={14}
						className="shrink-0 text-muted"
					/>
					<span className="min-w-0 flex-1 truncate">
						{blocked
							? t("artifacts.viewer.blocked", { host: new URL(blocked).host })
							: t("artifacts.viewer.openFailed", {
									reason: describeError(openError, t),
								})}
					</span>
					{blocked && (
						<Button size="sm" onClick={() => openInBrowser(blocked)}>
							{t("artifacts.viewer.openInBrowser")}
						</Button>
					)}
					<Button
						size="sm"
						variant="ghost"
						onClick={() => {
							setBlocked(null);
							setOpenError(null);
						}}
					>
						{t("common.close")}
					</Button>
				</div>
			)}
			{showSide && compact && (
				<div
					role="tablist"
					aria-label={t("artifacts.history.segments")}
					className="flex shrink-0 items-center gap-0.5 border-border border-b px-2 py-1"
				>
					{(commentsTab
						? (["page", "history", "comments"] as const)
						: (["page", "history"] as const)
					).map((s) => {
						const selected =
							s === "page"
								? segment === "page"
								: segment === "side" && side === s;
						return (
							<button
								key={s}
								type="button"
								role="tab"
								aria-selected={selected}
								className={`${tabClass(selected)} ${selected ? "bg-hover" : ""}`}
								onClick={() => {
									if (s === "page") setSegment("page");
									else {
										setSide(s);
										setSegment("side");
									}
								}}
							>
								{s === "page"
									? t("artifacts.history.segmentPage")
									: s === "history"
										? t("artifacts.comments.tabHistory")
										: t("artifacts.comments.tabComments")}
								{s === "comments" && <UnresolvedCount n={unresolved} />}
							</button>
						);
					})}
				</div>
			)}
			<div className="relative flex min-h-0 flex-1">
				<div
					className={`flex min-h-0 min-w-0 flex-1 flex-col ${sideOnly ? "hidden" : ""}`}
				>
					{live && detail.data?.editing && !inEditor && (
						<div
							role="status"
							data-testid="artifact-office-editing"
							className="flex shrink-0 items-center gap-2 border-border border-b bg-inset px-3 py-1.5 text-sm"
						>
							<Icon name="edit" size={14} className="shrink-0 text-muted" />
							<span className="min-w-0 flex-1 truncate">
								{t("artifacts.office.editingElsewhere")}
							</span>
						</div>
					)}
					{pinnedVersion && live && (
						<div
							role="status"
							data-testid="artifact-pinned"
							className="flex shrink-0 flex-wrap items-center gap-2 border-border border-b bg-inset px-3 py-1.5 text-sm"
						>
							<Icon name="history" size={14} className="shrink-0 text-muted" />
							<span className="min-w-0 flex-1 truncate">
								{t(
									pinnedVersion.title || pinnedVersion.note
										? "artifacts.history.pinnedNote"
										: "artifacts.history.pinned",
									{
										n: pinnedVersion.version,
										head,
										when: formatRelative(pinnedVersion.createdAt),
										note: pinnedVersion.title || pinnedVersion.note || "",
									},
								)}
								{isDesign && (
									<span
										data-testid="artifact-pinned-static"
										className="ml-2 text-muted text-xs"
									>
										· {t("artifacts.history.pinnedStatic")}
									</span>
								)}
							</span>
							<Button size="sm" variant="ghost" onClick={() => setPinned(null)}>
								{t("artifacts.history.backToLatest")}
							</Button>
							<Button
								size="sm"
								icon="restart"
								data-testid="artifact-restore-version"
								disabled={restore.isPending}
								onClick={() => void restorePinned()}
							>
								{t("artifacts.history.restore")}
							</Button>
						</div>
					)}
					<div
						ref={frameBoxRef}
						data-testid="artifact-frame-box"
						className={
							presenting && typed && live
								? "fixed inset-0 z-50"
								: `relative min-h-0 flex-1 ${panel ? "p-2" : "p-2 sm:p-3"}`
						}
					>
						{body}
						{shownDraft && (
							<CommentBox
								artifactId={artifactId}
								anchor={shownDraft.anchor}
								at={shownDraft.at}
								bounds={shownDraft.bounds}
								onDone={closeDraft}
								onCancel={closeDraft}
							/>
						)}
					</div>
					{editing && compact && (
						// 手机：非模态的底部抽屉（与对话框的抽屉同一套外观），帧留在上方看实时预览
						<section
							data-testid="artifact-edit-drawer"
							aria-label={t("artifacts.edit.title")}
							className="relative z-10 flex max-h-[55%] min-h-0 shrink-0 flex-col overflow-hidden rounded-t-xl bg-panel pb-safe shadow-xl ring-1 ring-[var(--glass-line)]"
						>
							<span
								aria-hidden
								className="mx-auto mt-1.5 h-1 w-9 shrink-0 rounded-full bg-border-strong"
							/>
							{editPanel(editing)}
						</section>
					)}
				</div>
				{editing && !compact && (
					<aside
						data-testid="artifact-edit-side"
						aria-label={t("artifacts.edit.title")}
						style={{ width: SIDE_WIDTH }}
						className={
							inline
								? "flex min-h-0 shrink-0 flex-col border-border border-l bg-panel"
								: "absolute inset-y-0 right-0 z-10 flex max-w-full min-h-0 flex-col border-border border-l bg-panel shadow-xl"
						}
					>
						{editPanel(editing)}
					</aside>
				)}
				{sideVisible && (
					<aside
						data-testid="artifact-side"
						data-inline={inline ? "" : undefined}
						aria-label={sideTitle}
						style={compact ? undefined : { width: SIDE_WIDTH }}
						className={
							compact
								? "flex min-h-0 w-full flex-col bg-panel"
								: inline
									? "flex min-h-0 shrink-0 flex-col border-border border-l bg-panel"
									: "absolute inset-y-0 right-0 z-10 flex max-w-full min-h-0 flex-col border-border border-l bg-panel shadow-xl"
						}
					>
						<div className="flex h-10 shrink-0 items-center gap-1 border-border border-b pr-1.5 pl-2">
							{commentsTab && !compact ? (
								<div
									role="tablist"
									aria-label={t("artifacts.comments.sideTabs")}
									className="flex min-w-0 flex-1 items-center gap-0.5"
								>
									{(["history", "comments"] as const).map((s) => (
										<button
											key={s}
											type="button"
											role="tab"
											data-testid={`artifact-side-${s}`}
											aria-selected={side === s}
											className={`${tabClass(side === s)} ${side === s ? "bg-hover" : ""}`}
											onClick={() => setSide(s)}
										>
											{s === "history"
												? t("artifacts.comments.tabHistory")
												: t("artifacts.comments.tabComments")}
											{s === "comments" && <UnresolvedCount n={unresolved} />}
										</button>
									))}
								</div>
							) : (
								<h2 className="min-w-0 flex-1 truncate pl-1 font-semibold text-sm">
									{sideTitle}
								</h2>
							)}
							<IconButton
								icon="close"
								box="sm"
								size={14}
								label={t("artifacts.comments.closeSide")}
								onClick={() => setSide(null)}
							/>
						</div>
						<div className="min-h-0 flex-1 overflow-y-auto">
							{side === "comments" ? (
								<CommentsPanel
									artifactId={artifactId}
									threads={detail.data?.threads ?? []}
									canComment={canSelect || canAnchor || canvasComments}
									onAnchor={
										artifact?.kind === "canvas"
											? (a) => {
													setFocusFile((f) => ({
														path: a.path,
														seq: (f?.seq ?? 0) + 1,
													}));
													if (compact) setSegment("page");
												}
											: canAnchor
												? (a) => {
														if (a.missing || a.path !== entry) return;
														post?.({
															kind: "anchor-locate",
															anchor: viewerAnchorOf(a),
														});
														if (compact) setSegment("page");
													}
												: undefined
									}
								/>
							) : (
								<VersionHistory
									artifactId={artifactId}
									head={head}
									threads={detail.data?.threads ?? []}
									selected={pinnedVersion?.version ?? head}
									onSelect={(v) => {
										setPinned(v.version === head ? null : v);
										if (compact) setSegment("page");
									}}
								/>
							)}
						</div>
					</aside>
				)}
			</div>
			{show && live && artifact.kind === "web" && (
				<Presenter
					title={title}
					pages={[{ id: artifactId, name: title }]}
					renderPage={() => (
						<PageFrame
							artifactId={artifactId}
							version={pinnedVersion?.version}
							title={t("artifacts.viewer.frameTitle", { title })}
							onBlocked={(url) => {
								const safe = safeBlockedUrl(url);
								if (safe) setBlocked(safe);
							}}
						/>
					)}
					onClose={() => setShow(null)}
				/>
			)}
		</div>
	);
}

/** 未解决评论数的徽标（强调色）；没有时不显示。 */
function UnresolvedCount({ n }: { n: number }): ReactNode {
	if (n === 0) return null;
	return (
		<span
			data-testid="comments-unresolved"
			className="inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-accent px-1 font-semibold text-[10px] text-accent-fg tabular-nums"
		>
			{n > 99 ? "99+" : n}
		</span>
	);
}
