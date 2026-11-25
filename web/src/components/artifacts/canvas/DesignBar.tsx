/**
 * 设计画布的预览条（M29 T6）：左上角的「实时 / 静态」徽标；实时预览可用时附切换按钮（偏好存本机），实时帧握手失败
 * （WebKit，探针 P3）时换成「在新窗口授权实时预览」。不是设计画布时不显示。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "../../ui/primitives";
import type { DesignPreview } from "./use-design-preview";

export function DesignBar({ preview }: { preview: DesignPreview }): ReactNode {
	const { t } = useTranslation();
	if (!preview.design) return null;
	const live = preview.mode === "live";
	return (
		<div className="pointer-events-none absolute top-2 left-2 z-10 flex items-center gap-1.5">
			<span
				data-testid="canvas-design-mode"
				data-mode={preview.mode}
				title={t(
					live
						? "artifacts.design.liveHint"
						: preview.available
							? "artifacts.design.staticHint"
							: "artifacts.design.unavailableHint",
				)}
				className={`pointer-events-auto inline-flex items-center gap-1.5 rounded-full border border-border bg-panel px-2.5 py-0.5 text-xs shadow-sm ${live ? "text-fg" : "text-muted"}`}
			>
				<span
					aria-hidden
					className={`h-1.5 w-1.5 rounded-full ${live ? "bg-ok" : "bg-border-strong"}`}
				/>
				{t(live ? "artifacts.design.live" : "artifacts.design.static")}
			</span>
			{preview.blocked ? (
				<Button
					size="sm"
					icon="globe"
					className="pointer-events-auto"
					data-testid="canvas-design-authorize"
					onClick={preview.authorize}
				>
					{t("artifacts.design.authorize")}
				</Button>
			) : (
				preview.available && (
					<Button
						size="sm"
						variant="ghost"
						className="pointer-events-auto"
						data-testid="canvas-design-toggle"
						onClick={() =>
							preview.setPref(preview.pref === "live" ? "static" : "live")
						}
					>
						{t(
							preview.pref === "live"
								? "artifacts.design.useStatic"
								: "artifacts.design.useLive",
						)}
					</Button>
				)
			)}
		</div>
	);
}
