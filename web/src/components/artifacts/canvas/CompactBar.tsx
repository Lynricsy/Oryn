/**
 * 手机（`compact` 断点）上的画布视图条（计划 M14 9.2）。画布在手机上只读：不显示工具条、图层与属性面板，
 * 单指拖动平移、双指捏合缩放、点画框聚焦。这里只留三样：切换画布页（多于一页时）、评论开关（能评论时）、适合全部。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { CanvasDoc } from "../../../lib/artifacts/canvas-doc";
import { IconButton } from "../../ui/primitives";
import { SheetMenu } from "./Toolbar";

export function CompactBar({
	doc,
	canvasKey,
	commentable,
	commenting,
	onComment,
	onCanvas,
	onFit,
}: {
	doc: CanvasDoc;
	canvasKey: string;
	/** 跟随最新版本、不在回收站：能评论。 */
	commentable: boolean;
	/** 评论工具开着。 */
	commenting: boolean;
	onComment(): void;
	onCanvas(id: string): void;
	onFit(): void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div className="pointer-events-none absolute inset-x-0 bottom-2 z-10 flex justify-center px-2">
			<div
				role="toolbar"
				aria-label={t("artifacts.canvas.toolbar")}
				data-testid="canvas-compact-bar"
				className="pointer-events-auto flex max-w-full items-center gap-0.5 rounded-xl border border-border bg-panel p-1 shadow-md"
			>
				{doc.canvases.length > 1 && (
					<>
						<SheetMenu
							doc={doc}
							canvasKey={canvasKey}
							editable={false}
							onCanvas={onCanvas}
							commit={() => false}
						/>
						<span className="mx-1 h-5 w-px shrink-0 bg-border" />
					</>
				)}
				{commentable && (
					<IconButton
						icon="message"
						box="sm"
						size={15}
						active={commenting}
						data-testid="canvas-tool-comment"
						label={t("artifacts.canvas.tools.comment")}
						onClick={onComment}
					/>
				)}
				<IconButton
					icon="maximize"
					box="sm"
					size={15}
					data-testid="canvas-zoom-fit"
					label={t("artifacts.canvas.zoomFit")}
					onClick={onFit}
				/>
			</div>
		</div>
	);
}
