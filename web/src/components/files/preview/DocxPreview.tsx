/**
 * Word 文档（docx / docm / dotx）：docx-preview 把文档渲染成分页的 HTML，渲染目标是惰性 iframe 里的文档
 * （帧内不执行脚本、不发网络请求，文档里的超链接与 altChunk HTML 都是死的）。
 * 图片与嵌入字体用 `data:` URL（`useBase64URL`），满足帧内 CSP。页面按实际页宽渲染，帧比页窄时整体缩小到帧宽。
 */
import { renderAsync } from "docx-preview";
import { type ReactNode, useCallback, useState } from "react";
import { ErrorText } from "../../ui/primitives";
import { fitToFrameWidth, InertFrame } from "./InertFrame";
import type { BlobPreviewProps } from "./types";

const PAD = 16;

// 帧底色与 docx-preview 的页外灰底一致，内容短于帧高时下方不露白
const FRAME_HTML = `<!doctype html><html style="background:#808080"><head><style>body{margin:0}.docx-wrapper{padding:${PAD}px!important}</style></head><body></body></html>`;

export default function DocxPreview({
	blob,
	name,
}: BlobPreviewProps): ReactNode {
	const [error, setError] = useState<unknown>(null);
	const onLoad = useCallback(
		(doc: Document) => {
			let alive = true;
			let stopFit: (() => void) | undefined;
			setError(null);
			renderAsync(blob, doc.body, doc.head, {
				className: "docx",
				inWrapper: true,
				breakPages: true,
				ignoreLastRenderedPageBreak: true,
				useBase64URL: true,
				renderHeaders: true,
				renderFooters: true,
				renderFootnotes: true,
				renderEndnotes: true,
				renderComments: false,
				renderChanges: false,
				renderAltChunks: false,
				experimental: false,
			}).then(
				() => {
					if (!alive) return;
					// 最宽的一页（横向页可能更宽）决定缩放
					const widest = Math.max(
						0,
						...[...doc.querySelectorAll<HTMLElement>("section.docx")].map(
							(s) => s.offsetWidth,
						),
					);
					stopFit = fitToFrameWidth(doc, widest, PAD, 1);
				},
				(e: unknown) => alive && setError(e),
			);
			return () => {
				alive = false;
				stopFit?.();
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
			<div className="min-h-0 flex-1">
				<InertFrame html={FRAME_HTML} title={name} onLoad={onLoad} />
			</div>
		</div>
	);
}
