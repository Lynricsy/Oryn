/**
 * 画布编辑器（计划 M14 6.3）：画布 artifact 在整页查看器与右面板里都用它。
 *
 * 不引入画布库：世界层是一个 `transform: translate() scale()` 的容器，画框是其中绝对定位的 `<iframe>`（见 frames.tsx），
 * 标注画在画框之上的一层 SVG 里（notes.tsx），最上面是接管指针的覆盖层。镜头见 camera.ts，实时文档见 use-canvas-doc.ts，
 * 选择与变换见 selection.ts / snapping.ts，快捷键见 keys.ts。
 *
 * - 工具：移动 V、抓手 H、画框 F、文字 T、便签 S、画笔 P（再按一次切荧光笔）、形状 R（再按依次切矩形 / 椭圆 / 线 / 箭头）、评论 C。
 * - 选择与变换：点选、`Shift` 加选、空白处框选；拖动移动、四角手柄缩放（显示「宽 × 高」）；对其它画框的边与中线吸附（6 屏幕像素，
 *   画参考线）；锁定的对象不能移动或缩放。双击画框进入「内部」：覆盖层放行指针，可以和页面交互，`Esc` 或点画框外退出。
 * - 评论工具：点画框里的元素，经 `select-pick` 让那个画框的运行时回报选中，`onComment` 交出选中与带画框序号的锚点上下文。
 * - 只读：看旧版本或 `compact` 断点（手机上只能平移、缩放与评论）。
 * - 6.4：工具条（Toolbar.tsx）、图层面板（LayersPanel.tsx）、属性面板（PropsPanel.tsx）、画框名称行（FrameLabel.tsx）。
 *   图层与属性面板只在整页查看器里出现（右面板里隐藏），窗口窄于 900 时覆盖在画布上（同一时间只显示一个）；
 *   只读时不显示编辑类控件。
 * - 6.6：交给 Agent（handoff.ts）。6.8：聚焦单个画框（`Enter` / 名称行「聚焦」/ 评论面板点锚点，FocusedPage.tsx）与演示
 *   （状态在查看器里，`present` / `onPresentChange`；演示器 `Presenter` 由这里按阅读顺序渲染）。
 */
import {
	type CSSProperties,
	type KeyboardEvent as ReactKeyboardEvent,
	type MouseEvent as ReactMouseEvent,
	type ReactNode,
	type PointerEvent as ReactPointerEvent,
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
	useSyncExternalStore,
} from "react";
import { useTranslation } from "react-i18next";
import type {
	PageSelection,
	SelectMode,
	SelectPin,
} from "../../../../sandbox/runtime/messages";
import {
	type ArtifactFrame,
	closeArtifactFrame,
	openArtifactFrame,
	useArtifact,
	useArtifactCapabilities,
} from "../../../api/artifact-queries";
import {
	applyCanvasOps,
	type Box,
	CANVAS_LIMITS,
	type CanvasDoc,
	type CanvasNote,
	type CanvasOp,
	type CanvasPage,
	canvasId,
	inverseOps,
	NOTE_TEXT_CHARS,
	PAGE_MIN,
	parseCanvasDoc,
	type ShapeKind,
	STICKY_WIDTH,
	TEXT_SIZE,
} from "../../../lib/artifacts/canvas-doc";
import { useBreakpoint } from "../../../lib/viewport";
import { realtime, watchArtifact } from "../../../realtime/socket";
import { showToast } from "../../../state/toasts";
import { Button, describeError } from "../../ui/primitives";
import type { FrameLink } from "../frame-link";
import { Presenter } from "../Presenter";
import { FrameErrorState, ViewerLoading } from "../viewer-states";
import { AgentPointers, FollowBar, useAgentPresence } from "./AgentPointer";
import {
	type Camera,
	fitBox,
	IDENTITY,
	type Point,
	panBy,
	reveal,
	stepZoom,
	toScreen,
	toWorld,
	unionBox,
	viewBox,
	wheelCamera,
	zoomAt,
} from "./camera";
import { FocusedPage, SoloPage } from "./FocusedPage";
import {
	downloadFramePng,
	FrameLabel,
	LABEL_GAP,
	LABEL_HEIGHT,
} from "./FrameLabel";
import {
	CanvasFrame,
	type FileVersions,
	fileVersions,
	isBlankPage,
	nearestPages,
	type PageContent,
	pageUrl,
} from "./frames";
import { HandOffBar } from "./HandOffBar";
import { useHandOff } from "./handoff";
import { isTypingTarget, keyAction, type Tool, type ZoomTarget } from "./keys";
import { LayersPanel } from "./LayersPanel";
import { NotesLayer } from "./notes";
import { PropsPanel, type PropsTab } from "./PropsPanel";
import {
	attachAt,
	type CanvasItem,
	deleteOps,
	duplicateOps,
	findItem,
	type Handle,
	hitTest,
	idsInRect,
	itemBox,
	itemsOn,
	movable,
	moveOps,
	pageAt,
	readingOrder,
	rectOf,
	resizeBox,
	resizeOps,
} from "./selection";
import { type Guide, SNAP_PX, snapBox } from "./snapping";
import {
	DEFAULT_STYLES,
	type DrawFamily,
	type DrawStyle,
	type PenKind,
	SHAPES,
	Toolbar,
} from "./Toolbar";
import { TweaksPanel, useFrameParams } from "./TweaksPanel";
import { useCanvasDoc } from "./use-canvas-doc";
import "./canvas.css";

/** 评论工具点中的位置：交给评论界面构造锚点（`anchorOf(selection, shown)`）并在 `at` 弹出评论框。 */
export interface CanvasCommentTarget {
	/** 页面运行时回报的选中；点在空白处或画框还没有内容时为 null（整个画框）。 */
	selection: PageSelection | null;
	/** 锚点的上下文：版本、画框文件、画框在阅读顺序里的序号（从 1 起）与名字。 */
	shown: {
		version: number;
		path: string;
		page: { number: number; name: string };
	};
	/** 点击处，相对于编辑器根元素（`relative`）的坐标。 */
	at: { x: number; y: number };
}

export interface CanvasEditorProps {
	artifactId: string;
	/** 钉住的旧版本（只读）；缺省 = 跟随最新版本、实时编辑。 */
	version?: number;
	variant: "page" | "panel";
	/** artifact 标题（帧的读屏标题）。 */
	title: string;
	/** 评论工具点中了画框；不给时没有评论工具。 */
	onComment?(target: CanvasCommentTarget): void;
	/** 某个画框文件上要画的评论图钉。 */
	pins?(path: string): SelectPin[];
	/** 演示（6.8，状态在查看器里）：`null` / 不给 = 没在演示。 */
	present?: PresentRequest | null;
	/** 开始（从某个画框）或结束（`null`）演示；不给时画框名称行没有「从这页演示」。 */
	onPresentChange?(next: PresentRequest | null): void;
	/** 聚焦到文件为 `path` 的画框（评论面板里点锚点）；`seq` 每次请求递增。 */
	focusFile?: { path: string; seq: number } | null;
}

/** 演示请求：从 `start` 画框开始（`null` = 当前画布页阅读顺序的第一个），顺序是所在画布页的阅读顺序。 */
export interface PresentRequest {
	start: string | null;
}

/** 拖动途中提交中间态的间隔（别的窗口跟着动）。 */
const LIVE_COMMIT_MS = 100;
/** 按下后移动超过这么多屏幕像素才算拖动。 */
const DRAG_PX = 3;
/** 手柄的命中半径（屏幕像素）。 */
const HANDLE_PX = 6;
/** 命中标注的容差（屏幕像素）。 */
const HIT_PX = 4;
/** 单击画框工具时的缺省尺寸。 */
const CLICK_FRAME = { width: 1440, height: 900 } as const;
/** 等画框运行时回报评论选中的上限；超时按整个画框。 */
const PICK_WAIT_MS = 1500;
/** 笔迹相邻两点的最小间距（屏幕像素）。 */
const PEN_STEP_PX = 1.5;

type Gesture =
	| { kind: "pan"; sx: number; sy: number; cam: Camera }
	| { kind: "pinch"; dist: number; mid: Point; cam: Camera }
	| { kind: "marquee"; start: Point; base: string[] }
	| {
			kind: "move";
			start: Point;
			startDoc: CanvasDoc;
			ids: string[];
			box: Box;
			others: Box[];
			ops: CanvasOp[];
			dragging: boolean;
			sentAt: number;
	  }
	| {
			kind: "resize";
			item: CanvasItem;
			handle: Handle;
			startDoc: CanvasDoc;
			from: Box;
			others: Box[];
			ops: CanvasOp[];
			sentAt: number;
	  }
	| { kind: "frame"; start: Point }
	| { kind: "pen"; points: number[] }
	| { kind: "shape"; start: Point }
	| { kind: "comment"; start: Point };

interface Preview {
	doc?: CanvasDoc;
	marquee?: Box;
	frame?: Box;
	draft?: CanvasNote;
	guides?: Guide[];
	/** 「宽 × 高」标签：世界坐标里的矩形。 */
	size?: Box;
}

/** 就地编辑的文字：`id` 为空是还没提交的新文字。 */
interface Editing {
	id: string | null;
	kind: "text" | "sticky";
	x: number;
	y: number;
	width: number;
	size: number;
	text: string;
	original: string;
}

function brandColor(): string {
	const v = getComputedStyle(document.documentElement)
		.getPropertyValue("--brand")
		.trim();
	return v && v.length <= 64 ? v : "#2563eb";
}

function round2(v: number): number {
	return Math.round(v * 100) / 100;
}

/** 窗口窄于这个宽度时，图层与属性面板覆盖在画布上。 */
const PANEL_OVERLAY_BELOW = 900;

function subscribeResize(cb: () => void) {
	window.addEventListener("resize", cb);
	return () => window.removeEventListener("resize", cb);
}

function useNarrowWindow(): boolean {
	return useSyncExternalStore(
		subscribeResize,
		() => window.innerWidth < PANEL_OVERLAY_BELOW,
		() => false,
	);
}

/** 当前画布页上全部对象的包围盒。 */
function canvasBounds(doc: CanvasDoc, canvas: string): Box | null {
	return unionBox(itemsOn(doc, canvas).map((it) => itemBox(doc, it)));
}

export function CanvasEditor({
	artifactId,
	version,
	variant,
	title,
	onComment,
	pins,
	present = null,
	onPresentChange,
	focusFile = null,
}: CanvasEditorProps): ReactNode {
	const { t, i18n } = useTranslation();
	const compact = useBreakpoint() === "compact";
	const narrow = useNarrowWindow();
	const caps = useArtifactCapabilities();
	const pinned = version !== undefined;
	const live = !pinned;
	const readOnly = pinned || compact;
	const commentable = !!onComment && live;

	const detail = useArtifact(artifactId, version);
	const { sync, snapshot } = useCanvasDoc(artifactId, live, (err) =>
		showToast({
			tone: "err",
			text: t("artifacts.canvas.rejected", { reason: describeError(err, t) }),
		}),
	);

	// 旧版本：文档取自那个版本的详情
	const pinnedDoc = useMemo(() => {
		const raw = pinned ? detail.data?.canvas : undefined;
		if (!raw) return null;
		try {
			return parseCanvasDoc(JSON.stringify(raw));
		} catch {
			return null;
		}
	}, [pinned, detail.data?.canvas]);

	// ------------------------------------------------------------ 帧令牌
	const [frame, setFrame] = useState<ArtifactFrame | null>(null);
	const [frameError, setFrameError] = useState<unknown>(null);
	const [attempt, setAttempt] = useState(0);
	const retried = useRef(false);
	const locale = i18n.language;
	// biome-ignore lint/correctness/useExhaustiveDependencies: attempt 变化 = 重新申请帧
	useEffect(() => {
		let alive = true;
		let token: string | null = null;
		setFrame(null);
		setFrameError(null);
		openArtifactFrame(artifactId, version, locale).then(
			(f) => {
				if (!alive) {
					void closeArtifactFrame(f.token);
					return;
				}
				token = f.token;
				setFrame(f);
			},
			(err: unknown) => {
				if (alive) setFrameError(err);
			},
		);
		return () => {
			alive = false;
			if (token) void closeArtifactFrame(token);
		};
	}, [artifactId, version, locale, attempt]);
	const onStale = useCallback(() => {
		if (retried.current) return;
		retried.current = true;
		setAttempt((n) => n + 1);
	}, []);
	const onReady = useCallback(() => {
		retried.current = false;
	}, []);
	// 同意请求落定时页面已经不在等（use 已超时回 null）：重载各画框让页面重新 use（同 PageFrame）
	const [consentReloads, setConsentReloads] = useState(0);
	useEffect(() => {
		if (!live) return;
		return realtime.onArtifactConsent((msg) => {
			if (
				msg.kind === "settled" &&
				msg.reload === true &&
				msg.artifactId === artifactId
			)
				setConsentReloads((n) => n + 1);
		});
	}, [artifactId, live]);

	// ------------------------------------------------------------ 各文件的版本号
	const files = detail.data?.files;
	const head = detail.data?.version ?? 0;
	// 画框回报的调节参数声明（6.5）与属性面板的当前标签（画框名称行的「调节」直接切过去）
	const frameParams = useFrameParams(head);
	const [propsTab, setPropsTab] = useState<PropsTab>("props");
	const [fv, setFv] = useState<FileVersions | null>(null);
	let versions = fv;
	if (files && !pinned && fv?.head !== head) {
		versions = fileVersions(fv, head, files);
		setFv(versions);
	}
	const fileMap = useMemo(
		() => new Map((files ?? []).map((f) => [f.path, f])),
		[files],
	);

	// ------------------------------------------------------------ 视图状态
	const [cam, setCam] = useState<Camera>(IDENTITY);
	const [size, setSize] = useState({ width: 0, height: 0 });
	const [canvasSel, setCanvasSel] = useState<string | null>(null);
	const [selected, setSelected] = useState<string[]>([]);
	const [tool, setTool] = useState<Tool>("move");
	const [penKind, setPenKind] = useState<PenKind>("pen");
	const [shape, setShape] = useState<ShapeKind>("rect");
	const [inside, setInside] = useState<string | null>(null);
	const [preview, setPreview] = useState<Preview | null>(null);
	const [editing, setEditing] = useState<Editing | null>(null);
	const [space, setSpace] = useState(false);
	const [accent] = useState(brandColor);
	const [styles, setStyles] = useState(DEFAULT_STYLES);
	const [layersOpen, setLayersOpen] = useState(false);
	// 聚焦的画框（6.8）
	const [focused, setFocused] = useState<string | null>(null);

	const baseDoc = live ? snapshot.doc : pinnedDoc;
	const doc = preview?.doc ?? baseDoc;
	const canvas =
		doc?.canvases.find((c) => c.id === canvasSel) ?? doc?.canvases[0] ?? null;
	const canvasKey = canvas?.id ?? "";
	// 文档到手之前渲染的是加载态，画布区还不存在：依赖画布区的副作用等它出现再装
	const stageShown = canvas !== null;

	const stageRef = useRef<HTMLDivElement>(null);
	const gesture = useRef<Gesture | null>(null);
	const touches = useRef(new Map<number, Point>());
	const links = useRef(new Map<string, FrameLink>());
	const hoverPage = useRef<string | null>(null);
	// 事件处理里读最新值
	const state = useRef({ cam, doc: baseDoc, canvasKey, selected, readOnly });
	state.current = { cam, doc: baseDoc, canvasKey, selected, readOnly };
	const editingRef = useRef(editing);
	editingRef.current = editing;

	// 告诉服务端这个观看者在哪个画布页（在场与 agent 的 viewing 用）
	useEffect(() => {
		if (!live || !canvasKey) return;
		return watchArtifact(artifactId, canvasKey);
	}, [artifactId, live, canvasKey]);

	// 画布区尺寸
	// biome-ignore lint/correctness/useExhaustiveDependencies: 画布区出现后才有元素可量
	useEffect(() => {
		const el = stageRef.current;
		if (!el) return;
		const measure = () =>
			setSize({ width: el.clientWidth, height: el.clientHeight });
		measure();
		if (typeof ResizeObserver === "undefined") return;
		const ro = new ResizeObserver(measure);
		ro.observe(el);
		return () => ro.disconnect();
	}, [stageShown]);

	// 每个画布页第一次显示时适合全部
	const fitted = useRef<string | null>(null);
	useEffect(() => {
		if (!baseDoc || !canvasKey || size.width === 0) return;
		if (fitted.current === canvasKey) return;
		fitted.current = canvasKey;
		setCam(fitBox(canvasBounds(baseDoc, canvasKey), size.width, size.height));
	}, [baseDoc, canvasKey, size.width, size.height]);

	// 只读时只剩移动（选中）、抓手与评论
	useEffect(() => {
		if (readOnly && !["move", "hand", "comment"].includes(tool))
			setTool("move");
		if (tool === "comment" && !commentable) setTool("move");
	}, [readOnly, tool, commentable]);

	// 滚轮：平移；Ctrl / ⌘（含触控板捏合）缩放。React 的 onWheel 是被动监听，不能 preventDefault
	// biome-ignore lint/correctness/useExhaustiveDependencies: 画布区出现后才有元素可挂
	useEffect(() => {
		const el = stageRef.current;
		if (!el) return;
		const onWheel = (e: WheelEvent) => {
			e.preventDefault();
			const r = el.getBoundingClientRect();
			setCam((c) =>
				wheelCamera(c, e, e.clientX - r.left, e.clientY - r.top, r.height),
			);
		};
		el.addEventListener("wheel", onWheel, { passive: false });
		return () => el.removeEventListener("wheel", onWheel);
	}, [stageShown]);

	const onLink = useCallback(
		(pageId: string, link: FrameLink | null) => {
			if (link) links.current.set(pageId, link);
			else links.current.delete(pageId);
			frameParams.track(pageId, link);
		},
		[frameParams.track],
	);

	const commit = (ops: CanvasOp[], inverse?: CanvasOp[]): boolean =>
		!!sync && !readOnly && sync.commit(ops, inverse ? { inverse } : {});

	// ------------------------------------------------------------ 坐标
	const screenPoint = (e: { clientX: number; clientY: number }): Point => {
		const r = stageRef.current?.getBoundingClientRect();
		return { x: e.clientX - (r?.left ?? 0), y: e.clientY - (r?.top ?? 0) };
	};

	const pagesOnCanvas = useMemo(
		() => (doc ? doc.pages.filter((p) => p.canvas === canvasKey) : []),
		[doc, canvasKey],
	);
	const loaded = useMemo(
		() => nearestPages(pagesOnCanvas, viewBox(cam, size.width, size.height)),
		[pagesOnCanvas, cam, size.width, size.height],
	);

	const shownVersion = version ?? head;
	/** 画框的内容；`parkable` 为假时不因离视口远而停放（聚焦与演示）。 */
	const contentOf = (p: CanvasPage, parkable = true): PageContent => {
		const f = fileMap.get(p.file);
		if (!f || isBlankPage(f)) return { kind: "empty" };
		if (!frame || (parkable && !loaded.has(p.id))) return { kind: "parked" };
		const n = pinned ? shownVersion : (versions?.n.get(p.file) ?? head);
		return { kind: "frame", src: pageUrl(frame.url, n, p.file) };
	};

	// ------------------------------------------------------------ 评论
	const pickComment = (page: CanvasPage, world: Point, at: Point) => {
		if (!onComment || !doc) return;
		const number =
			readingOrder(doc, page.canvas).findIndex((p) => p.id === page.id) + 1;
		const shown = {
			version: shownVersion,
			path: page.file,
			page: { number, name: page.name },
		};
		const link = links.current.get(page.id);
		if (!link) {
			onComment({ selection: null, shown, at });
			return;
		}
		let done = false;
		const finish = (selection: PageSelection | null) => {
			if (done) return;
			done = true;
			off();
			clearTimeout(timer);
			onComment({ selection, shown, at });
		};
		const off = link.on("select", (m) => {
			if (m.at) finish(m.selection);
		});
		const timer = setTimeout(() => finish(null), PICK_WAIT_MS);
		link.post({
			kind: "select-pick",
			x: world.x - page.x,
			y: world.y - page.y,
		});
	};

	const hoverComment = (world: Point | null) => {
		const d = state.current.doc;
		const page = world && d ? pageAt(d, state.current.canvasKey, world) : null;
		const prev = hoverPage.current;
		if (prev && prev !== page?.id)
			links.current.get(prev)?.post({ kind: "select-leave" });
		hoverPage.current = page?.id ?? null;
		if (page && world)
			links.current.get(page.id)?.post({
				kind: "select-point",
				x: world.x - page.x,
				y: world.y - page.y,
			});
	};

	// ------------------------------------------------------------ 就地编辑文字
	const startEditing = (note: CanvasNote) => {
		if (note.kind !== "text" && note.kind !== "sticky") return;
		setEditing({
			id: note.id,
			kind: note.kind,
			x: note.x,
			y: note.y,
			width: note.kind === "sticky" ? note.width : 0,
			size: note.kind === "text" ? note.size : 16,
			text: note.text,
			original: note.text,
		});
	};

	const finishEditing = () => {
		// 失焦与按下指针可能先后各调一次：只认第一次
		const ed = editingRef.current;
		if (!ed) return;
		editingRef.current = null;
		setEditing(null);
		const text = ed.text;
		if (ed.id === null) {
			if (text.trim() !== "")
				commit([
					{
						op: "add",
						object: {
							type: "note",
							id: canvasId("n"),
							canvas: canvasKey,
							kind: "text",
							x: round2(ed.x),
							y: round2(ed.y),
							text,
							size: ed.size,
							color: "ink",
						},
					},
				]);
		} else if (ed.kind === "text" && text.trim() === "")
			commit([{ op: "remove", id: ed.id }]);
		else if (text !== ed.original)
			commit([{ op: "set", id: ed.id, key: "text", value: text }]);
		stageRef.current?.focus({ preventScroll: true });
	};

	// ------------------------------------------------------------ 指针
	const sendLive = (g: { ops: CanvasOp[]; sentAt: number }) => {
		const now = performance.now();
		if (now - g.sentAt < LIVE_COMMIT_MS || !sync) return;
		g.sentAt = now;
		sync.commit(g.ops, { record: false });
	};

	const previewOps = (ops: CanvasOp[]): CanvasDoc | undefined => {
		const d = state.current.doc;
		if (!d) return undefined;
		try {
			return applyCanvasOps(d, ops);
		} catch {
			return undefined;
		}
	};

	const onPointerDown = (e: ReactPointerEvent<HTMLDivElement>) => {
		const d = state.current.doc;
		if (!d) return;
		if (editing) finishEditing();
		const s = screenPoint(e);
		const p = toWorld(cam, s.x, s.y);
		if (e.pointerType === "touch") {
			touches.current.set(e.pointerId, s);
			if (touches.current.size === 2) {
				const [a, b] = [...touches.current.values()] as [Point, Point];
				gesture.current = {
					kind: "pinch",
					dist: Math.max(1, Math.hypot(a.x - b.x, a.y - b.y)),
					mid: { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 },
					cam,
				};
				setPreview(null);
				return;
			}
		}
		e.currentTarget.setPointerCapture?.(e.pointerId);
		if (tool !== "text") stageRef.current?.focus({ preventScroll: true });
		const tol = HIT_PX / cam.zoom;
		const pan =
			e.button === 1 ||
			space ||
			tool === "hand" ||
			(compact && e.pointerType === "touch" && tool !== "comment");
		if (pan) {
			gesture.current = { kind: "pan", sx: s.x, sy: s.y, cam };
			return;
		}
		if (e.button !== 0) return;
		switch (tool) {
			case "move": {
				// 单选、可缩放时先看手柄
				const [only] = selected;
				const single = selected.length === 1 && only ? findItem(d, only) : null;
				if (single && !single.value.locked && !readOnly) {
					const b = itemBox(d, single);
					const r = HANDLE_PX / cam.zoom;
					const corners: [Handle, number, number][] = [
						["nw", b.x, b.y],
						["ne", b.x + b.width, b.y],
						["sw", b.x, b.y + b.height],
						["se", b.x + b.width, b.y + b.height],
					];
					const hit = corners.find(
						([, cx, cy]) => Math.abs(p.x - cx) <= r && Math.abs(p.y - cy) <= r,
					);
					if (hit) {
						gesture.current = {
							kind: "resize",
							item: single,
							handle: hit[0],
							startDoc: d,
							from: b,
							others: d.pages
								.filter(
									(pg) => pg.canvas === canvasKey && pg.id !== single.value.id,
								)
								.map((pg) => itemBox(d, { type: "page", value: pg })),
							ops: [],
							sentAt: 0,
						};
						return;
					}
				}
				const hit = hitTest(d, canvasKey, p, tol);
				if (!hit) {
					const base = e.shiftKey ? selected : [];
					if (!e.shiftKey) setSelected([]);
					gesture.current = { kind: "marquee", start: p, base };
					return;
				}
				const id = hit.value.id;
				let ids = selected;
				if (e.shiftKey)
					ids = selected.includes(id)
						? selected.filter((x) => x !== id)
						: [...selected, id];
				else if (!selected.includes(id)) ids = [id];
				setSelected(ids);
				if (readOnly || !ids.includes(id)) return;
				const moving = movable(d, ids);
				const boxes = moving
					.map((x) => findItem(d, x))
					.filter((x): x is CanvasItem => x !== null)
					.map((it) => itemBox(d, it));
				const box = unionBox(boxes);
				if (!box) return;
				gesture.current = {
					kind: "move",
					start: p,
					startDoc: d,
					ids: moving,
					box,
					others: d.pages
						.filter((pg) => pg.canvas === canvasKey && !moving.includes(pg.id))
						.map((pg) => itemBox(d, { type: "page", value: pg })),
					ops: [],
					dragging: false,
					sentAt: 0,
				};
				return;
			}
			case "frame":
				gesture.current = { kind: "frame", start: p };
				return;
			case "text": {
				e.preventDefault();
				const hit = hitTest(d, canvasKey, p, tol);
				if (hit?.type === "note" && hit.value.kind === "text") {
					if (!hit.value.locked) startEditing(hit.value);
					return;
				}
				setEditing({
					id: null,
					kind: "text",
					x: p.x,
					y: p.y,
					width: 0,
					size: TEXT_SIZE.default,
					text: "",
					original: "",
				});
				return;
			}
			case "sticky": {
				e.preventDefault();
				const id = canvasId("n");
				const width = STICKY_WIDTH.default;
				const ok = commit([
					{
						op: "add",
						object: {
							type: "note",
							id,
							canvas: canvasKey,
							kind: "sticky",
							x: round2(p.x),
							y: round2(p.y),
							width,
							text: "",
							color: "yellow",
						},
					},
				]);
				if (ok) {
					setSelected([id]);
					setTool("move");
					setEditing({
						id,
						kind: "sticky",
						x: round2(p.x),
						y: round2(p.y),
						width,
						size: 16,
						text: "",
						original: "",
					});
				}
				return;
			}
			case "pen":
				gesture.current = {
					kind: "pen",
					points: [round2(p.x), round2(p.y), round2(e.pressure || 0.5)],
				};
				return;
			case "shape":
				gesture.current = { kind: "shape", start: p };
				return;
			case "comment":
				gesture.current = { kind: "comment", start: p };
				return;
			default:
				return;
		}
	};

	const penDraft = (points: number[]): CanvasNote => ({
		id: "draft",
		canvas: canvasKey,
		kind: "pen",
		color: styles[penKind].color,
		locked: false,
		by: null,
		points,
		width: styles[penKind].width,
		marker: penKind === "marker",
	});

	const shapeDraft = (a: Point, b: Point): CanvasNote => ({
		id: "draft",
		canvas: canvasKey,
		kind: shape,
		color: styles.shape.color,
		locked: false,
		by: null,
		x1: round2(a.x),
		y1: round2(a.y),
		x2: round2(b.x),
		y2: round2(b.y),
		width: styles.shape.width,
		from: null,
		to: null,
	});

	const onPointerMove = (e: ReactPointerEvent<HTMLDivElement>) => {
		const s = screenPoint(e);
		const p = toWorld(cam, s.x, s.y);
		if (e.pointerType === "touch" && touches.current.has(e.pointerId))
			touches.current.set(e.pointerId, s);
		const g = gesture.current;
		if (!g) {
			if (tool === "comment") hoverComment(p);
			return;
		}
		switch (g.kind) {
			case "pan":
				setCam(panBy(g.cam, s.x - g.sx, s.y - g.sy));
				return;
			case "pinch": {
				if (touches.current.size < 2) return;
				const [a, b] = [...touches.current.values()] as [Point, Point];
				const mid = { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
				const dist = Math.max(1, Math.hypot(a.x - b.x, a.y - b.y));
				const moved = panBy(g.cam, mid.x - g.mid.x, mid.y - g.mid.y);
				setCam(zoomAt(moved, (g.cam.zoom * dist) / g.dist, mid.x, mid.y));
				return;
			}
			case "marquee": {
				const r = rectOf(g.start, p);
				const d = state.current.doc;
				if (d)
					setSelected([...new Set([...g.base, ...idsInRect(d, canvasKey, r)])]);
				setPreview({ marquee: r });
				return;
			}
			case "move": {
				let dx = p.x - g.start.x;
				let dy = p.y - g.start.y;
				if (!g.dragging && Math.hypot(dx, dy) * cam.zoom < DRAG_PX) return;
				g.dragging = true;
				const snap = snapBox(
					{ ...g.box, x: g.box.x + dx, y: g.box.y + dy },
					g.others,
					SNAP_PX / cam.zoom,
				);
				dx += snap.dx;
				dy += snap.dy;
				g.ops = moveOps(g.startDoc, g.ids, dx, dy);
				setPreview({ doc: previewOps(g.ops), guides: snap.guides });
				sendLive(g);
				return;
			}
			case "resize": {
				const isPage = g.item.type === "page";
				let box = resizeBox(g.from, g.handle, p, isPage ? PAGE_MIN : 4);
				const west = g.handle === "nw" || g.handle === "sw";
				const north = g.handle === "nw" || g.handle === "ne";
				const snap = snapBox(box, g.others, SNAP_PX / cam.zoom, {
					x: [west ? "start" : "end"],
					y: [north ? "start" : "end"],
				});
				box = {
					x: west ? box.x + snap.dx : box.x,
					y: north ? box.y + snap.dy : box.y,
					width: box.width + (west ? -snap.dx : snap.dx),
					height: box.height + (north ? -snap.dy : snap.dy),
				};
				g.ops = resizeOps(g.startDoc, g.item, g.from, box);
				setPreview({
					doc: previewOps(g.ops),
					guides: snap.guides,
					size: box,
				});
				sendLive(g);
				return;
			}
			case "frame": {
				const r = rectOf(g.start, p);
				setPreview({ frame: r, size: r });
				return;
			}
			case "pen": {
				const n = g.points.length;
				const lx = g.points[n - 3] ?? p.x;
				const ly = g.points[n - 2] ?? p.y;
				if (Math.hypot(p.x - lx, p.y - ly) * cam.zoom < PEN_STEP_PX) return;
				if (n + 3 > CANVAS_LIMITS.penNumbers) return;
				g.points.push(round2(p.x), round2(p.y), round2(e.pressure || 0.5));
				setPreview({ draft: penDraft([...g.points]) });
				return;
			}
			case "shape":
				setPreview({ draft: shapeDraft(g.start, p) });
				return;
			case "comment":
				hoverComment(p);
				return;
		}
	};

	const onPointerUp = (e: ReactPointerEvent<HTMLDivElement>) => {
		touches.current.delete(e.pointerId);
		const g = gesture.current;
		gesture.current = null;
		const d = state.current.doc;
		if (!g || !d) {
			setPreview(null);
			return;
		}
		const s = screenPoint(e);
		const p = toWorld(cam, s.x, s.y);
		switch (g.kind) {
			case "move":
			case "resize":
				if (g.ops.length > 0) commit(g.ops, inverseOps(g.startDoc, g.ops));
				break;
			case "frame": {
				const r = rectOf(g.start, p);
				const click = Math.max(r.width, r.height) * cam.zoom < DRAG_PX;
				const box = click
					? { x: g.start.x, y: g.start.y, ...CLICK_FRAME }
					: {
							x: r.x,
							y: r.y,
							width: Math.max(PAGE_MIN, r.width),
							height: Math.max(PAGE_MIN, r.height),
						};
				const id = canvasId("p");
				const ok = commit([
					{
						op: "add",
						object: {
							type: "page",
							id,
							canvas: canvasKey,
							name: t("artifacts.canvas.newFrame", {
								n: pagesOnCanvas.length + 1,
							}),
							x: round2(box.x),
							y: round2(box.y),
							width: Math.round(box.width),
							height: Math.round(box.height),
						},
					},
				]);
				if (ok) {
					setSelected([id]);
					setTool("move");
				}
				break;
			}
			case "pen":
				commit([
					{
						op: "add",
						object: {
							...penDraft(g.points),
							type: "note",
							id: canvasId("n"),
						},
					},
				]);
				break;
			case "shape": {
				const r = rectOf(g.start, p);
				if (Math.max(r.width, r.height) * cam.zoom < DRAG_PX) break;
				const object: { type: "note" } & Record<string, unknown> = {
					...shapeDraft(g.start, p),
					type: "note",
					id: canvasId("n"),
				};
				if (shape === "line" || shape === "arrow") {
					const tolerance = SNAP_PX / cam.zoom;
					object.from = attachAt(d, canvasKey, g.start, tolerance)?.id ?? null;
					object.to = attachAt(d, canvasKey, p, tolerance)?.id ?? null;
					if (object.from !== null && object.from === object.to)
						object.to = null;
				}
				commit([{ op: "add", object }]);
				break;
			}
			case "comment": {
				if (Math.hypot(p.x - g.start.x, p.y - g.start.y) * cam.zoom > DRAG_PX)
					break;
				const page = pageAt(d, canvasKey, p);
				if (page) pickComment(page, p, s);
				break;
			}
			default:
				break;
		}
		setPreview(null);
	};

	const onPointerCancel = (e: ReactPointerEvent<HTMLDivElement>) => {
		touches.current.delete(e.pointerId);
		gesture.current = null;
		setPreview(null);
	};

	const onDoubleClick = (e: ReactMouseEvent<HTMLDivElement>) => {
		const d = state.current.doc;
		if (!d || tool !== "move") return;
		const s = screenPoint(e);
		const p = toWorld(cam, s.x, s.y);
		const hit = hitTest(d, canvasKey, p, HIT_PX / cam.zoom);
		if (!hit) return;
		if (hit.type === "note") {
			if (!readOnly && !hit.value.locked) startEditing(hit.value);
			return;
		}
		if (!compact) {
			setSelected([hit.value.id]);
			setInside(hit.value.id);
		}
	};

	// ------------------------------------------------------------ 键盘
	const zoomTo = (to: ZoomTarget) => {
		const d = state.current.doc;
		const { width, height } = size;
		switch (to) {
			case "fit":
				if (d) setCam(fitBox(canvasBounds(d, canvasKey), width, height));
				return;
			case "selection": {
				if (!d) return;
				const box = unionBox(
					selected
						.map((id) => findItem(d, id))
						.filter((x): x is CanvasItem => x !== null)
						.map((it) => itemBox(d, it)),
				);
				if (box) setCam(fitBox(box, width, height, 4));
				return;
			}
			case "actual":
				setCam((c) => zoomAt(c, 1, width / 2, height / 2));
				return;
			case "in":
				setCam((c) => stepZoom(c, 1, width, height));
				return;
			case "out":
				setCam((c) => stepZoom(c, -1, width, height));
				return;
		}
	};

	const chooseTool = (next: Tool) => {
		if (readOnly && next !== "move" && next !== "hand" && next !== "comment")
			return;
		if (next === "comment" && !commentable) return;
		if (next === "pen" && tool === "pen")
			setPenKind((k) => (k === "pen" ? "marker" : "pen"));
		if (next === "shape" && tool === "shape")
			setShape(
				(k) => SHAPES[(SHAPES.indexOf(k) + 1) % SHAPES.length] ?? "rect",
			);
		if (next !== "comment") {
			hoverComment(null);
		}
		setTool(next);
	};

	const switchCanvas = (id: string) => {
		setCanvasSel(id);
		setSelected([]);
		setInside(null);
	};

	// 评论面板里点锚点：聚焦到它所在的画框（文件对得上的第一个）
	const focusSeq = useRef(focusFile?.seq ?? 0);
	useEffect(() => {
		if (!focusFile || focusFile.seq === focusSeq.current || !baseDoc) return;
		focusSeq.current = focusFile.seq;
		const p = baseDoc.pages.find((x) => x.file === focusFile.path);
		if (!p) return;
		setCanvasSel(p.canvas);
		setSelected([p.id]);
		setFocused(p.id);
	}, [focusFile, baseDoc]);

	// 在场（6.7）：各线程的指针、工作中的画框与「跟随 {线程}」
	const presence = useAgentPresence({
		artifactId,
		live,
		doc: baseDoc,
		canvasKey,
		cam,
		size,
		setCam,
		switchCanvas,
	});
	// 交给 Agent（6.6）
	const handOff = useHandOff({
		artifactId,
		detail: detail.data,
		render: caps.data?.render === true,
	});

	/** 图层面板与名称行：选中并把镜头移到 focus 上。 */
	const selectAndReveal = (ids: string[], focus: string) => {
		setSelected(ids);
		const d = state.current.doc;
		const it = d ? findItem(d, focus) : null;
		if (d && it)
			setCam((c) => reveal(c, itemBox(d, it), size.width, size.height));
	};

	const onKeyDown = (e: ReactKeyboardEvent<HTMLElement>) => {
		// 门户里的对话框（改名、删页确认）冒泡上来的按键不归画布
		if (!e.currentTarget.contains(e.target as Node)) return;
		if (e.defaultPrevented || isTypingTarget(e.target)) return;
		// 空格在按钮上是点击；只有画布区获得焦点时才是按住平移
		if (e.key === " " && e.target === stageRef.current) {
			if (!e.repeat) setSpace(true);
			e.preventDefault();
			return;
		}
		const action = keyAction(e);
		if (!action) return;
		const d = state.current.doc;
		switch (action.type) {
			case "tool":
				chooseTool(action.tool);
				break;
			case "delete":
				if (readOnly || selected.length === 0) return;
				commit(deleteOps(selected));
				setSelected([]);
				break;
			case "nudge":
				if (readOnly || !d || selected.length === 0) return;
				commit(moveOps(d, selected, action.dx, action.dy));
				break;
			case "undo":
				if (readOnly) return;
				sync?.undo();
				break;
			case "redo":
				if (readOnly) return;
				sync?.redo();
				break;
			case "duplicate": {
				if (readOnly || !d || selected.length === 0) return;
				const dup = duplicateOps(d, selected);
				if (commit(dup.ops)) setSelected(dup.ids);
				break;
			}
			case "escape":
				if (inside) setInside(null);
				else if (selected.length > 0) setSelected([]);
				else if (tool !== "move") chooseTool("move");
				else return;
				break;
			case "zoom":
				zoomTo(action.to);
				break;
			case "focus": {
				// Enter 在按钮上是点击；只有画布区获得焦点、单选一个画框时才聚焦
				const [only] = selected;
				const it =
					d && only && selected.length === 1 ? findItem(d, only) : null;
				if (e.target !== stageRef.current || it?.type !== "page") return;
				setFocused(it.value.id);
				break;
			}
		}
		e.preventDefault();
	};

	const onKeyUp = (e: ReactKeyboardEvent<HTMLDivElement>) => {
		if (e.key === " ") setSpace(false);
	};

	// ------------------------------------------------------------ 渲染
	if (live && !snapshot.doc) {
		if (snapshot.error)
			return (
				<FrameErrorState
					error={snapshot.error}
					onRetry={() => sync?.resync()}
				/>
			);
		return <ViewerLoading />;
	}
	if (pinned && !pinnedDoc) {
		if (detail.isError)
			return (
				<FrameErrorState
					error={detail.error}
					onRetry={() => void detail.refetch()}
				/>
			);
		return <ViewerLoading />;
	}
	if (!doc || !canvas) return <ViewerLoading />;

	const items = selected
		.map((id) => findItem(doc, id))
		.filter((x): x is CanvasItem => x !== null && x.value.canvas === canvasKey);
	const handles =
		items.length === 1 && items[0] && !items[0].value.locked && !readOnly
			? itemBox(doc, items[0])
			: null;
	const z = cam.zoom;
	const worldStyle = {
		transform: `translate(${cam.x}px, ${cam.y}px) scale(${z})`,
		"--z": String(z),
	} as CSSProperties;
	const mode: SelectMode = tool === "comment" ? "comment" : null;
	const cursor =
		gesture.current?.kind === "pan"
			? "grabbing"
			: space || tool === "hand"
				? "grab"
				: tool === "move"
					? "default"
					: tool === "comment"
						? "pointer"
						: "crosshair";
	const sizeLabel = preview?.size
		? toScreen(cam, preview.size.x, preview.size.y + preview.size.height)
		: null;
	// 图层与属性面板只在整页查看器、非手机断点里出现；属性面板只在可编辑且有选中时出现。
	// 窄窗口里两者都覆盖在画布上，放不下两个：图层面板开着时属性面板让位
	const showPanels = variant === "page" && !compact;
	const showLayers = showPanels && layersOpen;
	const showProps =
		showPanels && !readOnly && items.length > 0 && !(narrow && showLayers);
	const shown = viewBox(cam, size.width, size.height);
	const labelPad = (LABEL_HEIGHT + LABEL_GAP) / z;
	const labelled = pagesOnCanvas.filter(
		(p) =>
			p.x <= shown.x + shown.width &&
			p.x + p.width >= shown.x &&
			p.y - labelPad <= shown.y + shown.height &&
			p.y >= shown.y,
	);
	const selectedIds = items.map((it) => it.value.id);
	const render = caps.data?.render === true;
	// 聚焦与演示（6.8）：顺序都是所在画布页的阅读顺序
	const focusedPage = focused
		? doc.pages.find((p) => p.id === focused)
		: undefined;
	const focusOrder = focusedPage ? readingOrder(doc, focusedPage.canvas) : [];
	const presentSheet =
		doc.pages.find((p) => p.id === present?.start)?.canvas ?? canvasKey;
	const presentOrder = present ? readingOrder(doc, presentSheet) : [];

	return (
		<section
			aria-label={t("artifacts.canvas.stage", { title })}
			data-testid="canvas-editor"
			data-variant={variant}
			data-readonly={readOnly ? "" : undefined}
			className="canvas-editor relative flex h-full overflow-hidden rounded-lg border border-border"
			onKeyDown={onKeyDown}
			onKeyUp={onKeyUp}
		>
			{showLayers && (
				<LayersPanel
					doc={doc}
					canvasKey={canvasKey}
					selected={selectedIds}
					overlay={narrow}
					onSelect={selectAndReveal}
					onRename={
						readOnly
							? undefined
							: (id, name) =>
									commit([{ op: "set", id, key: "name", value: name }])
					}
					onClose={() => setLayersOpen(false)}
				/>
			)}
			<div className="relative min-w-0 flex-1 overflow-hidden">
				<div
					ref={stageRef}
					// biome-ignore lint/a11y/noNoninteractiveTabindex: 画布区接管键盘快捷键，需要能获得焦点
					tabIndex={0}
					role="application"
					aria-label={t("artifacts.canvas.stage", { title })}
					className="canvas-stage absolute inset-0"
					onPointerDownCapture={(e) => {
						if (!inside) return;
						const el = e.target as HTMLElement;
						if (!el.closest?.(`[data-page="${inside}"]`)) setInside(null);
					}}
				>
					<div className="canvas-world" style={worldStyle}>
						{pagesOnCanvas.map((p) => (
							<CanvasFrame
								key={p.id}
								page={p}
								content={contentOf(p)}
								title={t("artifacts.canvas.frameTitle", { name: p.name })}
								inside={inside === p.id}
								mode={mode}
								accent={accent}
								pins={live && pins ? pins(p.file) : []}
								onLink={onLink}
								onStale={onStale}
								onReady={onReady}
								detectStale={live}
								reloadNonce={consentReloads}
							/>
						))}
						<NotesLayer
							doc={doc}
							canvas={canvasKey}
							editing={editing?.id ?? null}
							draft={preview?.draft ?? null}
						/>
						<svg className="canvas-layer canvas-chrome" aria-hidden="true">
							{items.map((it) => {
								const b = itemBox(doc, it);
								return (
									<rect
										key={it.value.id}
										className="sel"
										x={b.x}
										y={b.y}
										width={b.width}
										height={b.height}
									/>
								);
							})}
							{handles &&
								(
									[
										[handles.x, handles.y],
										[handles.x + handles.width, handles.y],
										[handles.x, handles.y + handles.height],
										[handles.x + handles.width, handles.y + handles.height],
									] as const
								).map(([hx, hy]) => (
									<rect
										key={`${hx}:${hy}`}
										className="handle"
										x={hx - 4 / z}
										y={hy - 4 / z}
										width={8 / z}
										height={8 / z}
									/>
								))}
							{preview?.guides?.map((g) =>
								g.axis === "x" ? (
									<line
										key={`x${g.pos}`}
										className="guide"
										x1={g.pos}
										x2={g.pos}
										y1={g.from}
										y2={g.to}
									/>
								) : (
									<line
										key={`y${g.pos}`}
										className="guide"
										y1={g.pos}
										y2={g.pos}
										x1={g.from}
										x2={g.to}
									/>
								),
							)}
							{preview?.marquee && (
								<rect
									className="marquee"
									x={preview.marquee.x}
									y={preview.marquee.y}
									width={preview.marquee.width}
									height={preview.marquee.height}
								/>
							)}
							{preview?.frame && (
								<rect
									className="draft"
									x={preview.frame.x}
									y={preview.frame.y}
									width={preview.frame.width}
									height={preview.frame.height}
								/>
							)}
						</svg>
					</div>
					{/* biome-ignore lint/a11y/noStaticElementInteractions: 指针覆盖层；键盘操作由外层获得焦点的画布区处理 */}
					<div
						data-testid="canvas-overlay"
						className="absolute inset-0"
						style={{ cursor, pointerEvents: inside ? "none" : "auto" }}
						onPointerDown={onPointerDown}
						onPointerMove={onPointerMove}
						onPointerUp={onPointerUp}
						onPointerCancel={onPointerCancel}
						onPointerLeave={() => {
							if (!gesture.current && tool === "comment") hoverComment(null);
						}}
						onDoubleClick={onDoubleClick}
					/>
					<div
						data-testid="canvas-frame-labels"
						className="pointer-events-none absolute inset-0"
					>
						{labelled.map((p) => {
							const s = toScreen(cam, p.x, p.y);
							return (
								<FrameLabel
									key={p.id}
									page={p}
									left={s.x}
									top={s.y}
									width={p.width * z}
									selected={selectedIds.includes(p.id)}
									onSelect={(additive) => {
										setSelected((cur) =>
											!additive
												? [p.id]
												: cur.includes(p.id)
													? cur.filter((x) => x !== p.id)
													: [...cur, p.id],
										);
										stageRef.current?.focus({ preventScroll: true });
									}}
									onTweaks={
										showPanels && !readOnly && frameParams.declared(p.id)
											? () => {
													setSelected([p.id]);
													setPropsTab("tweaks");
												}
											: undefined
									}
									onDownloadPng={
										render
											? () => void downloadFramePng(artifactId, p, version, t)
											: undefined
									}
									onHandOff={
										live ? () => handOff.run(doc, canvasKey, [p.id]) : undefined
									}
									onPresent={
										onPresentChange
											? () => onPresentChange({ start: p.id })
											: undefined
									}
									onFocus={() => {
										setSelected([p.id]);
										setFocused(p.id);
									}}
								/>
							);
						})}
					</div>
					<AgentPointers presence={presence} />
					{editing && (
						<div
							className="canvas-world"
							style={{ ...worldStyle, pointerEvents: "none" }}
						>
							<EditBox
								editing={editing}
								onChange={(text) =>
									setEditing((ed) => (ed ? { ...ed, text } : ed))
								}
								onDone={finishEditing}
							/>
						</div>
					)}
					{sizeLabel && preview?.size && (
						<span
							className="canvas-size"
							style={{ left: sizeLabel.x, top: sizeLabel.y + 6 }}
						>
							{Math.round(preview.size.width)} ×{" "}
							{Math.round(preview.size.height)}
						</span>
					)}
				</div>
				{pagesOnCanvas.length === 0 && (
					<div className="pointer-events-none absolute inset-x-0 top-1/3 flex flex-col items-center gap-2 text-sm text-muted">
						<span>{t("artifacts.canvas.emptyCanvas")}</span>
						{!readOnly && (
							<Button
								size="sm"
								icon="frameCorners"
								className="pointer-events-auto"
								onClick={() => chooseTool("frame")}
							>
								{t("artifacts.canvas.frameTool")}
							</Button>
						)}
					</div>
				)}
				{frameError !== null && (
					<div
						role="status"
						className="absolute inset-x-2 top-2 flex items-center gap-2 rounded-md border border-border bg-panel px-3 py-1.5 text-sm shadow-sm"
					>
						<span className="min-w-0 flex-1 truncate">
							{t("artifacts.canvas.frameFailed", {
								reason: describeError(frameError, t),
							})}
						</span>
						<Button size="sm" onClick={() => setAttempt((n) => n + 1)}>
							{t("artifacts.viewer.retry")}
						</Button>
					</div>
				)}
				{inside && (
					<div className="pointer-events-none absolute inset-x-0 top-2 flex justify-center">
						<span className="rounded-full border border-border bg-panel px-2.5 py-0.5 text-muted text-xs shadow-sm">
							{t("artifacts.canvas.inside")}
						</span>
					</div>
				)}
				<FollowBar presence={presence} />
				{live && items.length > 0 && (
					<HandOffBar
						count={items.length}
						busy={handOff.busy}
						onHandOff={() => handOff.run(doc, canvasKey, selectedIds)}
					/>
				)}
				<Toolbar
					doc={doc}
					canvasKey={canvasKey}
					readOnly={readOnly}
					commentable={commentable}
					tool={tool}
					penKind={penKind}
					shape={shape}
					styles={styles}
					onTool={chooseTool}
					onPenKind={setPenKind}
					onShape={setShape}
					onStyle={(family: DrawFamily, patch: Partial<DrawStyle>) =>
						setStyles((s) => ({ ...s, [family]: { ...s[family], ...patch } }))
					}
					onCanvas={switchCanvas}
					commit={(ops) => commit(ops)}
					zoom={z}
					hasSelection={items.length > 0}
					onZoom={zoomTo}
					layers={
						showPanels
							? { open: layersOpen, onToggle: () => setLayersOpen((o) => !o) }
							: undefined
					}
				/>
				{focusedPage && (
					<FocusedPage
						pages={focusOrder}
						index={focusOrder.findIndex((p) => p.id === focusedPage.id)}
						contentOf={(p) => contentOf(p, false)}
						onIndex={(i) => {
							const next = focusOrder[i];
							if (next) {
								setSelected([next.id]);
								setFocused(next.id);
							}
						}}
						onClose={() => {
							setFocused(null);
							stageRef.current?.focus({ preventScroll: true });
						}}
					/>
				)}
			</div>
			{showProps && (
				<PropsPanel
					doc={doc}
					selected={selectedIds}
					threads={detail.data?.threads ?? []}
					overlay={narrow}
					commit={(ops) => commit(ops)}
					onSelect={setSelected}
					tab={propsTab}
					onTab={setPropsTab}
					tweaks={(page) => (
						<TweaksPanel
							key={page.id}
							artifactId={artifactId}
							page={page}
							frame={frameParams.byPage[page.id]}
							post={(body) => frameParams.post(page.id, body)}
						/>
					)}
				/>
			)}
			{present && (
				<Presenter
					title={title}
					pages={presentOrder}
					start={Math.max(
						0,
						presentOrder.findIndex((p) => p.id === present.start),
					)}
					renderPage={(p) => (
						<SoloPage page={p} content={contentOf(p, false)} />
					)}
					onClose={() => onPresentChange?.(null)}
				/>
			)}
		</section>
	);
}

/** 就地编辑文字或便签：世界坐标里的 `<textarea>`。`Esc` / `Mod+Enter` / 失焦完成。 */
function EditBox({
	editing,
	onChange,
	onDone,
}: {
	editing: Editing;
	onChange(text: string): void;
	onDone(): void;
}): ReactNode {
	const { t } = useTranslation();
	const ref = useRef<HTMLTextAreaElement>(null);
	useEffect(() => {
		ref.current?.focus({ preventScroll: true });
	}, []);
	const sticky = editing.kind === "sticky";
	const lines = editing.text.split("\n");
	const longest = Math.max(4, ...lines.map((l) => [...l].length + 1));
	const style: CSSProperties = sticky
		? {
				left: editing.x,
				top: editing.y,
				width: editing.width,
				height: Math.max(120, editing.width * 0.75),
				padding: 12,
				fontSize: 16,
				lineHeight: 1.4,
				color: "var(--text)",
			}
		: {
				left: editing.x,
				top: editing.y,
				width: longest * editing.size * 0.6,
				height: lines.length * editing.size * 1.3 + editing.size * 0.3,
				fontSize: editing.size,
				lineHeight: 1.3,
				color: "var(--note-ink)",
			};
	return (
		<textarea
			ref={ref}
			className="canvas-edit"
			aria-label={t(
				sticky ? "artifacts.canvas.editSticky" : "artifacts.canvas.editText",
			)}
			maxLength={NOTE_TEXT_CHARS}
			value={editing.text}
			style={style}
			onChange={(e) => onChange(e.target.value)}
			onBlur={onDone}
			onKeyDown={(e) => {
				if (
					e.key === "Escape" ||
					(e.key === "Enter" && (e.metaKey || e.ctrlKey))
				) {
					e.preventDefault();
					e.stopPropagation();
					onDone();
				}
			}}
		/>
	);
}
