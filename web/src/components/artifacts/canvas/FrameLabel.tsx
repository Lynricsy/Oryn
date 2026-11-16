/**
 * 画框名称行（计划 M14 6.4）：画框上方一行，名字加几个按钮，画在覆盖层之上（屏幕坐标，字号不随缩放变化）。
 *
 * 按钮依次是：调节（6.5，只给声明了参数的画框）、交给 Agent（6.6）、下载 PNG、从这页演示（6.8）、聚焦（6.8）。
 * 每个按钮由对应的回调决定是否出现：没给回调就没有这个按钮。行宽（= 画框在屏幕上的宽）放不下时从右往左隐藏按钮。
 * 按钮在画框选中、悬停或获得焦点时显示。点名字选中画框（`Shift` 加选）。
 */
import type { TFunction } from "i18next";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { fetchArtifactPng } from "../../../api/artifact-queries";
import type { CanvasPage } from "../../../lib/artifacts/canvas-doc";
import { downloadBlob } from "../../../lib/artifacts/download";
import { showToast } from "../../../state/toasts";
import type { IconName } from "../../ui/icons";
import { describeError, IconButton } from "../../ui/primitives";

/** 名称行的高度（屏幕像素）与它和画框之间的间距。 */
export const LABEL_HEIGHT = 22;
export const LABEL_GAP = 4;
/** 按钮宽（含间距）与名字至少留的宽度。 */
const BUTTON_PX = 24;
const NAME_MIN_PX = 48;

/** 画框名称行上可选的动作：给了回调才显示对应按钮。 */
export interface FrameLabelActions {
	/** 调节参数（6.5）：只对声明了参数的画框给。 */
	onTweaks?(): void;
	/** 把这个画框与其上的标注交给 Agent（6.6）。 */
	onHandOff?(): void;
	/** 下载这个画框的 PNG（渲染不可用时不给）。 */
	onDownloadPng?(): void;
	/** 从这个画框开始演示（6.8）。 */
	onPresent?(): void;
	/** 聚焦到这个画框（6.8）。 */
	onFocus?(): void;
}

export interface FrameLabelProps extends FrameLabelActions {
	page: CanvasPage;
	/** 画框左上角在画布区里的屏幕坐标，与画框在屏幕上的宽。 */
	left: number;
	top: number;
	width: number;
	selected: boolean;
	onSelect(additive: boolean): void;
}

export function FrameLabel({
	page,
	left,
	top,
	width,
	selected,
	onSelect,
	...actions
}: FrameLabelProps): ReactNode {
	const { t } = useTranslation();
	const all: { key: string; icon: IconName; label: string; run?(): void }[] = [
		{
			key: "tweaks",
			icon: "sliders",
			label: t("artifacts.canvas.frameLabel.tweaks"),
			run: actions.onTweaks,
		},
		{
			key: "handoff",
			icon: "sparkle",
			label: t("artifacts.canvas.frameLabel.handOff"),
			run: actions.onHandOff,
		},
		{
			key: "png",
			icon: "download",
			label: t("artifacts.canvas.frameLabel.png"),
			run: actions.onDownloadPng,
		},
		{
			key: "present",
			icon: "play",
			label: t("artifacts.canvas.frameLabel.present"),
			run: actions.onPresent,
		},
		{
			key: "focus",
			icon: "maximize",
			label: t("artifacts.canvas.frameLabel.focus"),
			run: actions.onFocus,
		},
	];
	const given = all.filter((b) => b.run);
	const fits = Math.max(0, Math.floor((width - NAME_MIN_PX) / BUTTON_PX));
	const shown = given.slice(0, fits);
	return (
		<div
			data-frame-label={page.id}
			className={`group pointer-events-auto absolute flex items-center gap-0 ${selected ? "text-fg" : "text-muted"}`}
			style={{
				left,
				top: top - LABEL_HEIGHT - LABEL_GAP,
				width,
				height: LABEL_HEIGHT,
			}}
		>
			<button
				type="button"
				title={page.name}
				aria-label={t("artifacts.canvas.frameLabel.select", {
					name: page.name,
				})}
				onClick={(e) => onSelect(e.shiftKey)}
				className="min-w-0 flex-1 truncate text-left text-xs hover:text-fg"
			>
				{page.name}
			</button>
			{shown.length > 0 && (
				<div
					className={`flex shrink-0 items-center transition-opacity duration-150 group-focus-within:opacity-100 group-hover:opacity-100 ${selected ? "opacity-100" : "opacity-0"}`}
				>
					{shown.map((b) => (
						<IconButton
							key={b.key}
							icon={b.icon}
							box="xs"
							size={13}
							label={b.label}
							data-testid={`canvas-frame-${b.key}`}
							onClick={() => b.run?.()}
						/>
					))}
				</div>
			)}
		</div>
	);
}

/** 下载一个画框的 PNG：控制面按画框的尺寸渲染它的 HTML 文件。 */
export async function downloadFramePng(
	artifactId: string,
	page: Pick<CanvasPage, "name" | "file" | "width" | "height">,
	version: number | undefined,
	t: TFunction,
): Promise<void> {
	const id = `artifact-png:${artifactId}:${page.file}`;
	showToast({
		id,
		busy: true,
		text: t("artifacts.png.running", { title: page.name }),
	});
	try {
		const { blob } = await fetchArtifactPng(artifactId, {
			...(version ? { version } : {}),
			path: page.file,
			width: page.width,
			height: page.height,
		});
		// 服务端的文件名取的是 artifact 标题；画框用自己的名字
		downloadBlob(blob, `${page.name}.png`);
		showToast({ id, text: t("artifacts.png.done", { title: page.name }) });
	} catch (e) {
		showToast({
			id,
			tone: "err",
			text: t("artifacts.png.failed", {
				title: page.name,
				reason: describeError(e, t),
			}),
			action: {
				label: t("artifacts.export.retry"),
				run: () => void downloadFramePng(artifactId, page, version, t),
			},
		});
	}
}
