/**
 * 设计画布的原型预览（M29 R1）：铺满窗口的覆盖层，从 `doc.design.start`（缺省第一个带 source 的画框，再缺省第一个画框）
 * 打开单帧可交互窗口并套上 `DeviceShell`。
 *
 * - 复用 `Presenter` 的 `fitScale`、`ScaledPage`、`useBoxSize` 做居中缩放（不改 `Presenter.tsx`）。
 * - 通过 `FrameLink` 监听运行时的 `navigated {frame}` 与 `ready.design`，帧内点击 `<Link>` 跳转后同步当前帧名、尺寸与设备外壳，
 *   不重载 iframe；顶栏下拉切帧或重置起始帧时优先发 `{kind:"navigate", frame}`，非设计帧或模式不同则直接切源。
 * - 支持静态产物帧与实时预览帧（`LiveFrame` / `PORTAL_SANDBOX`，目标源为 portal 源）。
 * - 落选方案的画框（`archivedPages`，M34 C6）不可达：不做起始帧、不进切帧下拉；帧内 `<Link>` 跳到落选帧时立即跳回当前帧，
 *   与指向不存在的帧一样停在原处。
 */
import {
	type ReactNode,
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import {
	archivedPages,
	type CanvasDoc,
	type CanvasPage,
	DEVICES,
	type Device,
} from "../../../lib/artifacts/canvas-doc";
import { IconButton, Select } from "../../ui/primitives";
import type { FrameLink } from "../frame-link";
import { ARTIFACT_SANDBOX } from "../PageFrame";
import { fitScale, ScaledPage, useBoxSize } from "../Presenter";
import { DeviceShell, deviceShellSize } from "./DeviceShell";
import type { PageContent } from "./frames";
import { frameIdOf, LiveFrame } from "./LiveFrame";
import { useFrameLink } from "./use-frame-link";

const DESIGN_DIST = /^dist\/([a-z0-9][a-z0-9-]{0,63})\.html$/;

/** 推出画框对应的设计帧 id（优先 `source`，其次 `dist/<id>.html`）；不是设计帧时为 null。 */
export function pageFrameId(
	page: Pick<CanvasPage, "source" | "file">,
): string | null {
	return frameIdOf(page.source) ?? DESIGN_DIST.exec(page.file)?.[1] ?? null;
}

/** 按设计帧 id 找到对应画框（先匹配源码帧 id / dist 文件名，再退回画框 id）。 */
export function findPageByFrame(
	pages: readonly CanvasPage[],
	frame: string,
): CanvasPage | undefined {
	return (
		pages.find((p) => pageFrameId(p) === frame) ??
		pages.find((p) => p.id === frame)
	);
}

/** 原型可达的画框：去掉落选方案的画框（M34 C6）。 */
export function reachablePages(doc: CanvasDoc): CanvasPage[] {
	const archived = archivedPages(doc);
	return doc.pages.filter((p) => !archived.has(p.id));
}

/** 选出原型起始画框（只在可达的画框里选）：`doc.design.start` → 第一个带 `source` 的画框 → 第一个画框。 */
export function resolveStartPage(doc: CanvasDoc): CanvasPage | null {
	const pages = reachablePages(doc);
	const start = doc.design?.start;
	if (start) {
		const hit =
			pages.find((p) => p.id === start) ?? findPageByFrame(pages, start);
		if (hit) return hit;
	}
	return pages.find((p) => frameIdOf(p.source) !== null) ?? pages[0] ?? null;
}

export interface PrototypeProps {
	/** artifact 标题（对话框读屏名）。 */
	title: string;
	doc: CanvasDoc;
	/** 按画框取内容（支持静态帧与实时帧）。 */
	contentOf(page: CanvasPage): PageContent;
	/** 实时帧握手超时回调（可选）。 */
	onLiveTimeout?(): void;
	onClose(): void;
}

const NOOP = () => undefined;

function StaticProtoFrame({
	pageId,
	src,
	title,
	onLink,
}: {
	pageId: string;
	src: string;
	title: string;
	onLink(pageId: string, link: FrameLink | null): void;
}): ReactNode {
	const ref = useRef<HTMLIFrameElement>(null);
	useFrameLink(ref, {
		pageId,
		origin: "*",
		mode: null,
		accent: "",
		pins: [],
		onLink,
		onReady: NOOP,
	});
	return (
		<iframe
			ref={ref}
			className="canvas-solo"
			data-testid="prototype-frame"
			title={title}
			src={src}
			sandbox={ARTIFACT_SANDBOX}
			allow=""
			referrerPolicy="no-referrer"
		/>
	);
}

export function Prototype({
	title,
	doc,
	contentOf,
	onLiveTimeout = NOOP,
	onClose,
}: PrototypeProps): ReactNode {
	const { t } = useTranslation();
	const pages = useMemo(() => reachablePages(doc), [doc]);
	const startPage = useMemo(() => resolveStartPage(doc), [doc]);
	const [currentId, setCurrentId] = useState<string>(() => startPage?.id ?? "");
	// `srcPageId` 仅绑定 iframe 初始 src，帧内跳转时保持不变以免二次加载
	const [srcPageId, setSrcPageId] = useState<string>(() => startPage?.id ?? "");
	const [navNonce, setNavNonce] = useState(0);
	const [deviceOverride, setDeviceOverride] = useState<Device | null>(null);

	const rootRef = useRef<HTMLDivElement>(null);
	const stageRef = useRef<HTMLDivElement>(null);
	const area = useBoxSize(stageRef);

	const linkRef = useRef<FrameLink | null>(null);
	const linkOffsRef = useRef<(() => void)[]>([]);
	const pagesRef = useRef(pages);
	pagesRef.current = pages;
	const allPagesRef = useRef(doc.pages);
	allPagesRef.current = doc.pages;

	const currentPage =
		pages.find((p) => p.id === currentId) ?? startPage ?? pages[0] ?? null;
	const currentRef = useRef(currentPage);
	currentRef.current = currentPage;
	const srcPage = pages.find((p) => p.id === srcPageId) ?? currentPage;
	const srcContent: PageContent = srcPage
		? contentOf(srcPage)
		: { kind: "empty" };
	const device: Device = deviceOverride ?? currentPage?.device ?? "none";

	const deviceRef = useRef(device);
	deviceRef.current = device;
	const overrideRef = useRef(deviceOverride);
	overrideRef.current = deviceOverride;

	const syncFrame = useCallback((frame: string, linked: boolean) => {
		const matched = findPageByFrame(pagesRef.current, frame);
		if (!matched) {
			// 帧内 <Link> 跳到了落选画框：跳回当前帧，停在原处（同指向不存在的帧）
			const back = currentRef.current ? pageFrameId(currentRef.current) : null;
			if (
				linked &&
				back !== null &&
				back !== frame &&
				findPageByFrame(allPagesRef.current, frame)
			)
				linkRef.current?.post({ kind: "navigate", frame: back });
			return;
		}
		const prevHasShell = deviceRef.current !== "none";
		const nextDevice: Device = overrideRef.current ?? matched.device ?? "none";
		const nextHasShell = nextDevice !== "none";
		setCurrentId(matched.id);
		// `none` 与有外壳之间切换会改变包装层级导致 iframe 重挂，同步 srcPageId 确保落在目标帧上
		if (prevHasShell !== nextHasShell) setSrcPageId(matched.id);
	}, []);

	const onProtoLink = useCallback(
		(_pageId: string, link: FrameLink | null) => {
			for (const off of linkOffsRef.current) off();
			linkOffsRef.current = [];
			linkRef.current = link;
			if (!link) return;
			linkOffsRef.current = [
				link.on("navigated", (msg) => syncFrame(msg.frame, true)),
				link.on("ready", (msg) => {
					if (msg.design) syncFrame(msg.design.frame, false);
				}),
			];
		},
		[syncFrame],
	);

	useEffect(
		() => () => {
			for (const off of linkOffsRef.current) off();
			linkOffsRef.current = [];
		},
		[],
	);

	// 捕获阶段截住 Esc，不落到画布或查看器的快捷键
	const closeRef = useRef(onClose);
	closeRef.current = onClose;
	useEffect(() => {
		const onKey = (e: KeyboardEvent) => {
			if (e.ctrlKey || e.metaKey || e.altKey || e.key !== "Escape") return;
			e.preventDefault();
			e.stopPropagation();
			closeRef.current();
		};
		window.addEventListener("keydown", onKey, true);
		return () => window.removeEventListener("keydown", onKey, true);
	}, []);

	// 打开时把焦点移入对话框，关闭后还给原元素
	useEffect(() => {
		const before = document.activeElement as HTMLElement | null;
		rootRef.current?.focus({ preventScroll: true });
		return () => before?.focus?.({ preventScroll: true });
	}, []);

	const navigateTo = (targetId: string, forceReload = false) => {
		const target = pages.find((p) => p.id === targetId);
		if (!target) return;
		const targetFrame = pageFrameId(target);
		const currentFrame = currentPage ? pageFrameId(currentPage) : null;
		const targetContent = contentOf(target);
		const activeLink = linkRef.current;

		const prevHasShell = device !== "none";
		const nextHasShell = (deviceOverride ?? target.device ?? "none") !== "none";

		setCurrentId(target.id);

		if (
			activeLink &&
			targetFrame !== null &&
			currentFrame !== null &&
			prevHasShell === nextHasShell &&
			(srcContent.kind === "frame" || srcContent.kind === "live") &&
			targetContent.kind === srcContent.kind
		) {
			if (targetFrame !== currentFrame) {
				activeLink.post({ kind: "navigate", frame: targetFrame });
			} else if (forceReload) {
				activeLink.post({ kind: "design-reload" });
			}
			return;
		}

		setSrcPageId(target.id);
		if (forceReload || srcPageId === target.id) setNavNonce((n) => n + 1);
	};

	const onDeviceSelect = (val: string) => {
		const nextOverride: Device | null = val === "auto" ? null : (val as Device);
		const prevHasShell = device !== "none";
		const nextHasShell =
			(nextOverride ?? currentPage?.device ?? "none") !== "none";
		setDeviceOverride(nextOverride);
		if (prevHasShell !== nextHasShell && currentPage) {
			setSrcPageId(currentPage.id);
		}
	};

	const margin = area.width > 0 && area.width < 520 ? 12 : 24;
	const shellSize = currentPage
		? deviceShellSize(device, currentPage.width, currentPage.height)
		: { width: 0, height: 0 };
	const scale = fitScale(shellSize.width, shellSize.height, {
		width: area.width - 2 * margin,
		height: area.height - 2 * margin,
	});

	const frameTitle = currentPage
		? t("artifacts.canvas.frameTitle", { name: currentPage.name })
		: title;

	const frameNode =
		srcPage && srcContent.kind === "live" ? (
			<LiveFrame
				key={`live:${srcPage.id}:${srcContent.nonce}:${navNonce}`}
				pageId={srcPage.id}
				src={srcContent.src}
				origin={srcContent.origin}
				title={frameTitle}
				mode={null}
				accent=""
				pins={[]}
				onLink={onProtoLink}
				onTimeout={onLiveTimeout}
			/>
		) : srcPage && srcContent.kind === "frame" ? (
			<StaticProtoFrame
				key={`frame:${srcPage.id}:${navNonce}`}
				pageId={srcPage.id}
				src={srcContent.src}
				title={frameTitle}
				onLink={onProtoLink}
			/>
		) : currentPage ? (
			<div className="canvas-solo canvas-solo-empty">
				<span>
					{srcContent.kind === "empty"
						? t("artifacts.canvas.emptyFrame")
						: currentPage.name}
				</span>
				<span className="tabular-nums">
					{currentPage.width} × {currentPage.height}
				</span>
			</div>
		) : null;

	return createPortal(
		<div
			ref={rootRef}
			role="dialog"
			aria-modal="true"
			aria-label={t("artifacts.design.prototype.dialogTitle", { title })}
			tabIndex={-1}
			data-testid="canvas-prototype"
			className="fixed inset-0 z-50 flex flex-col bg-inset text-fg outline-none"
		>
			<header className="flex min-h-11 shrink-0 flex-wrap items-center justify-between gap-2 border-border border-b bg-panel px-2.5 py-1.5">
				<div className="flex min-w-0 items-center gap-1.5">
					<IconButton
						icon="close"
						box="sm"
						size={15}
						label={t("artifacts.design.prototype.close")}
						data-testid="prototype-close"
						onClick={onClose}
					/>
					<span className="hidden text-faint text-xs sm:inline">Esc</span>
					<span
						data-testid="prototype-title"
						className="min-w-0 truncate px-1 font-medium text-sm"
					>
						{currentPage?.name ?? title}
					</span>
				</div>
				<div className="flex min-w-0 flex-wrap items-center gap-1.5">
					{pages.length > 0 && (
						<Select
							aria-label={t("artifacts.design.prototype.frameSelect")}
							size="sm"
							className="w-auto max-w-[160px]"
							value={currentPage?.id ?? ""}
							onChange={(v) => navigateTo(v)}
							options={pages.map((p) => ({ value: p.id, label: p.name }))}
						/>
					)}
					<Select
						aria-label={t("artifacts.design.prototype.deviceSelect")}
						size="sm"
						className="w-auto max-w-[130px]"
						value={deviceOverride ?? "auto"}
						onChange={onDeviceSelect}
						options={[
							{
								value: "auto",
								label: t("artifacts.design.prototype.deviceAuto"),
							},
							...DEVICES.map((d) => ({
								value: d,
								label: t(`artifacts.design.device.${d}`),
							})),
						]}
					/>
					{startPage && (
						<IconButton
							icon="restart"
							box="sm"
							size={15}
							label={t("artifacts.design.prototype.reset")}
							data-testid="prototype-reset"
							onClick={() => navigateTo(startPage.id, true)}
						/>
					)}
				</div>
			</header>
			<div
				ref={stageRef}
				className="prototype-stage relative flex min-h-0 flex-1 items-center justify-center overflow-hidden"
			>
				{currentPage && (
					<ScaledPage
						width={shellSize.width}
						height={shellSize.height}
						scale={scale}
					>
						<DeviceShell
							device={device}
							title={currentPage.name}
							width={currentPage.width}
							height={currentPage.height}
						>
							{frameNode}
						</DeviceShell>
					</ScaledPage>
				)}
			</div>
		</div>,
		document.body,
	);
}
