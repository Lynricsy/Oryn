/**
 * 聚焦单个画框（计划 M14 6.8）：画布区换成这一个画框的全尺寸视图（按原始尺寸排版、缩放到放得下），页面可交互。
 * 顶栏：返回画布（`Esc`）、画框名、上一页 `←`、「n / N」、下一页 `→`；顺序是所在画布页的阅读顺序。
 * 盖在画布区之上（缩放控件与工具条随之隐藏）；按键在自己身上处理并截住，不落到画布与查看器的快捷键上。
 *
 * `SoloPage` 是单独显示一个画框的帧（聚焦与演示共用）：没有内容时显示占位。
 */
import {
	type KeyboardEvent as ReactKeyboardEvent,
	type ReactNode,
	useEffect,
	useRef,
} from "react";
import { useTranslation } from "react-i18next";
import type { CanvasPage } from "../../../lib/artifacts/canvas-doc";
import { Button, IconButton } from "../../ui/primitives";
import { ARTIFACT_SANDBOX } from "../PageFrame";
import { fitScale, ScaledPage, useBoxSize } from "../Presenter";
import type { PageContent } from "./frames";

/** 聚焦视图里画框四周的留白。 */
const FOCUS_MARGIN = 16;

/** 单独显示的一个画框：内容是帧地址，或空白页 / 还没拿到帧的占位。 */
export function SoloPage({
	page,
	content,
}: {
	page: Pick<CanvasPage, "name" | "width" | "height">;
	content: PageContent;
}): ReactNode {
	const { t } = useTranslation();
	if (content.kind === "frame")
		return (
			<iframe
				className="canvas-solo"
				title={t("artifacts.canvas.frameTitle", { name: page.name })}
				src={content.src}
				sandbox={ARTIFACT_SANDBOX}
				allow=""
				referrerPolicy="no-referrer"
			/>
		);
	return (
		<div className="canvas-solo canvas-solo-empty">
			<span>
				{content.kind === "empty"
					? t("artifacts.canvas.emptyFrame")
					: page.name}
			</span>
			<span className="tabular-nums">
				{page.width} × {page.height}
			</span>
		</div>
	);
}

export interface FocusedPageProps {
	/** 所在画布页的画框，阅读顺序。 */
	pages: readonly CanvasPage[];
	/** 正在聚焦的那个在 `pages` 里的位置。 */
	index: number;
	contentOf(page: CanvasPage): PageContent;
	onIndex(index: number): void;
	onClose(): void;
}

export function FocusedPage({
	pages,
	index,
	contentOf,
	onIndex,
	onClose,
}: FocusedPageProps): ReactNode {
	const { t } = useTranslation();
	const rootRef = useRef<HTMLElement>(null);
	const stageRef = useRef<HTMLDivElement>(null);
	const area = useBoxSize(stageRef);
	const page = pages[index];
	const total = pages.length;
	const go = (delta: number) =>
		onIndex(Math.max(0, Math.min(total - 1, index + delta)));

	useEffect(() => {
		rootRef.current?.focus({ preventScroll: true });
	}, []);

	const onKeyDown = (e: ReactKeyboardEvent<HTMLElement>) => {
		if (e.ctrlKey || e.metaKey || e.altKey || e.shiftKey) return;
		if (e.key === "Escape") onClose();
		else if (e.key === "ArrowLeft") go(-1);
		else if (e.key === "ArrowRight") go(1);
		else return;
		e.preventDefault();
		e.stopPropagation();
	};

	if (!page) return null;
	return (
		<section
			ref={rootRef}
			aria-label={t("artifacts.canvas.focus.title", { name: page.name })}
			tabIndex={-1}
			data-testid="canvas-focused"
			className="absolute inset-0 z-20 flex flex-col bg-inset outline-none"
			onKeyDown={onKeyDown}
		>
			<div className="flex h-10 shrink-0 items-center gap-1 border-border border-b bg-panel px-1.5">
				<Button
					size="sm"
					variant="ghost"
					icon="back"
					data-testid="canvas-focus-back"
					onClick={onClose}
				>
					{t("artifacts.canvas.focus.back")}
				</Button>
				<span className="hidden text-faint text-xs sm:inline">Esc</span>
				<span className="min-w-0 flex-1 truncate px-2 text-center font-medium text-sm">
					{page.name}
				</span>
				<IconButton
					icon="back"
					box="sm"
					size={15}
					label={t("artifacts.canvas.focus.prev")}
					data-testid="canvas-focus-prev"
					disabled={index === 0}
					onClick={() => go(-1)}
				/>
				<span
					data-testid="canvas-focus-position"
					className="text-muted text-xs tabular-nums"
				>
					{t("artifacts.canvas.focus.position", { n: index + 1, total })}
				</span>
				<IconButton
					icon="forward"
					box="sm"
					size={15}
					label={t("artifacts.canvas.focus.next")}
					data-testid="canvas-focus-next"
					disabled={index >= total - 1}
					onClick={() => go(1)}
				/>
			</div>
			<div
				ref={stageRef}
				className="flex min-h-0 flex-1 items-center justify-center"
			>
				<ScaledPage
					key={page.id}
					width={page.width}
					height={page.height}
					scale={fitScale(page.width, page.height, {
						width: area.width - 2 * FOCUS_MARGIN,
						height: area.height - 2 * FOCUS_MARGIN,
					})}
				>
					<SoloPage page={page} content={contentOf(page)} />
				</ScaledPage>
			</div>
		</section>
	);
}
