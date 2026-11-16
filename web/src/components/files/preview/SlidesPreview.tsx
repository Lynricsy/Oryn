/**
 * 演示文稿（pptx / pptm / ppsx / potx）：pptx-preview 以列表模式把每页渲染成固定宽度的 HTML（图表用 echarts 画在 canvas 上），
 * 渲染目标是惰性 iframe 里的文档；整体用 CSS `zoom` 缩放到帧宽。
 * pptx-preview 遇到不完整的包（如清单里登记了不存在的部件）会静默得到 0 页，此时给出提示而不是空白。
 */
import { init } from "pptx-preview";
import { type ReactNode, useCallback, useState } from "react";
import { useTranslation } from "react-i18next";
import { ErrorText } from "../../ui/primitives";
import { fitToFrameWidth, InertFrame } from "./InertFrame";
import { PreviewNote } from "./PreviewNote";
import type { BlobPreviewProps } from "./types";

/** 渲染宽度（px）；之后按帧宽整体缩放（最多放大到 2 倍）。 */
const RENDER_W = 960;
const PAD = 16;

const FRAME_HTML = `<!doctype html><html><head><style>html,body{margin:0;background:#e8e8e8}body{padding:${PAD}px 0}.pptx-preview-wrapper{background:transparent!important}.pptx-preview-slide-wrapper{box-shadow:0 1px 4px rgba(0,0,0,.25);margin-bottom:${PAD}px!important}</style></head><body></body></html>`;

export default function SlidesPreview({
	blob,
	name,
}: BlobPreviewProps): ReactNode {
	const { t } = useTranslation();
	const [error, setError] = useState<unknown>(null);
	const [empty, setEmpty] = useState(false);
	const onLoad = useCallback(
		(doc: Document) => {
			let alive = true;
			setError(null);
			setEmpty(false);
			const stopFit = fitToFrameWidth(doc, RENDER_W, PAD, 2);
			const previewer = init(doc.body, { width: RENDER_W, mode: "list" });
			blob
				.arrayBuffer()
				.then((buf) => previewer.preview(buf))
				.then(
					() => alive && setEmpty(previewer.slideCount === 0),
					(e: unknown) => alive && setError(e),
				);
			return () => {
				alive = false;
				stopFit();
				previewer.destroy();
			};
		},
		[blob],
	);
	return (
		<div className="flex h-full min-h-0 flex-col">
			{error ? (
				<div className="shrink-0 p-3">
					<ErrorText error={error} />
				</div>
			) : null}
			{empty && <PreviewNote>{t("files.preview.slides.empty")}</PreviewNote>}
			<div className="min-h-0 flex-1">
				<InertFrame html={FRAME_HTML} title={name} onLoad={onLoad} />
			</div>
		</div>
	);
}
