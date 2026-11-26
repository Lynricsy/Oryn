/**
 * 图片查看器：适应窗口 / 实际大小 / 缩放，棋盘格底显示透明区域，标出像素尺寸。
 * 浏览器不能直接显示的格式先在本地解码成 PNG：HEIC/HEIF（libheif WASM）、TIFF（UTIF，多页可切换）、
 * PSD（ag-psd 读合成图）。SVG 用 `data:` URL（不透明源；`<img>` 里的 SVG 本就不执行脚本、不加载外链）。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { BlobViewer } from "../../../lib/preview-kind";
import { imageMime } from "../../../lib/preview-kind";
import { ImageViewer } from "../../ui/ImageViewer";
import { ErrorText, IconButton, Spinner } from "../../ui/primitives";
import type { BlobPreviewProps } from "./types";

type Decoder = Extract<BlobViewer, "image" | "heic" | "tiff" | "psd">;

interface Decoded {
	url: string;
	/** object URL 需要回收；data: URL 不需要 */
	revoke: boolean;
	/** TIFF 的页数（其余为 1） */
	pages: number;
}

function canvasBlob(canvas: HTMLCanvasElement): Promise<Blob> {
	return new Promise((resolve, reject) =>
		canvas.toBlob(
			(b) => (b ? resolve(b) : reject(new Error("canvas.toBlob failed"))),
			"image/png",
		),
	);
}

function readDataUrl(blob: Blob): Promise<string> {
	return new Promise((resolve, reject) => {
		const r = new FileReader();
		r.onload = () => resolve(String(r.result));
		r.onerror = () => reject(r.error);
		r.readAsDataURL(blob);
	});
}

// 三个解码器（libheif WASM 约 3MB、UTIF、ag-psd）只在打开对应格式时按需加载，不进图片查看器本身的分块，所以用动态 import。
async function decodeTiff(
	blob: Blob,
	page: number,
): Promise<{ png: Blob; pages: number }> {
	const UTIF = await import("utif2");
	const buf = await blob.arrayBuffer();
	// 只取真正带像素的目录（缩略图 / EXIF 子目录没有宽高）
	const ifds = UTIF.decode(buf).filter((d) => d.t256 !== undefined);
	const ifd = ifds[Math.min(page, ifds.length - 1)];
	if (!ifd) throw new Error("no image in TIFF");
	UTIF.decodeImage(buf, ifd);
	const rgba = UTIF.toRGBA8(ifd);
	const canvas = document.createElement("canvas");
	canvas.width = ifd.width;
	canvas.height = ifd.height;
	const ctx = canvas.getContext("2d");
	if (!ctx) throw new Error("2d canvas unavailable");
	ctx.putImageData(
		new ImageData(new Uint8ClampedArray(rgba), ifd.width, ifd.height),
		0,
		0,
	);
	return { png: await canvasBlob(canvas), pages: ifds.length };
}

async function decodePsd(blob: Blob): Promise<Blob> {
	const { readPsd } = await import("ag-psd");
	const psd = readPsd(await blob.arrayBuffer(), {
		skipLayerImageData: true,
		skipThumbnail: true,
	});
	if (!psd.canvas) throw new Error("PSD has no composite image");
	return canvasBlob(psd.canvas);
}

async function decode(
	blob: Blob,
	name: string,
	decoder: Decoder,
	page: number,
): Promise<Decoded> {
	switch (decoder) {
		case "heic": {
			const { heicTo } = await import("heic-to/csp");
			const png = await heicTo({ blob, type: "image/png" });
			return { url: URL.createObjectURL(png), revoke: true, pages: 1 };
		}
		case "tiff": {
			const { png, pages } = await decodeTiff(blob, page);
			return { url: URL.createObjectURL(png), revoke: true, pages };
		}
		case "psd":
			return {
				url: URL.createObjectURL(await decodePsd(blob)),
				revoke: true,
				pages: 1,
			};
		default: {
			// 下载接口一律回 application/octet-stream：类型按扩展名补上
			const type = name.toLowerCase().endsWith(".svg")
				? "image/svg+xml"
				: (imageMime(name) ?? blob.type);
			const typed = new Blob([blob], { type });
			if (type === "image/svg+xml")
				return { url: await readDataUrl(typed), revoke: false, pages: 1 };
			return { url: URL.createObjectURL(typed), revoke: true, pages: 1 };
		}
	}
}

const ZOOM_STEPS = [0.1, 0.25, 0.5, 0.75, 1, 1.5, 2, 3, 4, 6, 8];

/** 棋盘格底：显示透明区域，颜色随主题令牌。 */
const CHECKER = {
	backgroundColor: "var(--color-inset)",
	backgroundImage:
		"conic-gradient(var(--color-border) 25%, transparent 0 50%, var(--color-border) 0 75%, transparent 0)",
	backgroundSize: "16px 16px",
};

export default function ImagePreview({
	blob,
	name,
	decoder = "image",
}: BlobPreviewProps & { decoder?: Decoder }): ReactNode {
	const { t } = useTranslation();
	const [decoded, setDecoded] = useState<Decoded | null>(null);
	const [error, setError] = useState<unknown>(null);
	const [page, setPage] = useState(0);
	/** null = 适应窗口 */
	const [zoom, setZoom] = useState<number | null>(null);
	const [natural, setNatural] = useState<{ w: number; h: number } | null>(null);
	const [lightboxOpen, setLightboxOpen] = useState(false);

	useEffect(() => {
		let alive = true;
		let made: Decoded | null = null;
		setDecoded(null);
		setNatural(null);
		setError(null);
		decode(blob, name, decoder, page).then(
			(d) => {
				made = d;
				if (alive) setDecoded(d);
				else if (d.revoke) URL.revokeObjectURL(d.url);
			},
			(e) => alive && setError(e),
		);
		return () => {
			alive = false;
			if (made?.revoke) URL.revokeObjectURL(made.url);
		};
	}, [blob, name, decoder, page]);

	const step = (dir: 1 | -1) => {
		const cur = zoom ?? 1;
		const next =
			dir > 0
				? ZOOM_STEPS.find((z) => z > cur + 1e-6)
				: ZOOM_STEPS.toReversed().find((z) => z < cur - 1e-6);
		setZoom(next ?? cur);
	};

	if (error)
		return (
			<div className="p-3">
				<ErrorText error={error} />
			</div>
		);
	if (!decoded)
		return (
			<div className="flex items-center gap-2 p-3 text-faint text-xs">
				<Spinner />
				{decoder !== "image" && t("files.preview.image.decoding")}
			</div>
		);
	return (
		<div className="flex h-full min-h-0 flex-col">
			<div className="flex h-8 shrink-0 items-center gap-1 border-border border-b px-2 text-faint text-xs">
				{natural && (
					<span className="tabular-nums">
						{natural.w} × {natural.h}
					</span>
				)}
				{decoded.pages > 1 && (
					<span className="ml-2 flex items-center gap-0.5 tabular-nums">
						<IconButton
							icon="chevronLeft"
							label={t("files.preview.prevPage")}
							disabled={page === 0}
							onClick={() => setPage((p) => p - 1)}
						/>
						{t("files.preview.pageOf", { n: page + 1, total: decoded.pages })}
						<IconButton
							icon="chevronRight"
							label={t("files.preview.nextPage")}
							disabled={page >= decoded.pages - 1}
							onClick={() => setPage((p) => p + 1)}
						/>
					</span>
				)}
				<span className="flex-1" />
				<IconButton
					icon="zoomOut"
					label={t("files.preview.zoomOut")}
					onClick={() => step(-1)}
				/>
				<button
					type="button"
					className="min-w-12 rounded px-1 tabular-nums hover:bg-hover"
					title={t("files.preview.zoomReset")}
					onClick={() => setZoom(zoom === null ? 1 : null)}
				>
					{zoom === null
						? t("files.preview.fit")
						: `${Math.round(zoom * 100)}%`}
				</button>
				<IconButton
					icon="zoomIn"
					label={t("files.preview.zoomIn")}
					onClick={() => step(1)}
				/>
				<span aria-hidden className="mx-0.5 h-3.5 w-px bg-border" />
				<IconButton
					icon="maximize"
					label={t("image.lightbox.fullscreen")}
					onClick={() => setLightboxOpen(true)}
				/>
			</div>
			<div
				className={`min-h-0 flex-1 overflow-auto ${zoom === null ? "flex items-center justify-center p-3" : "p-3"}`}
				style={CHECKER}
			>
				<img
					src={decoded.url}
					alt={name}
					draggable={false}
					onLoad={(e) =>
						setNatural({
							w: e.currentTarget.naturalWidth,
							h: e.currentTarget.naturalHeight,
						})
					}
					onDoubleClick={() => setZoom(zoom === null ? 1 : null)}
					className={
						zoom === null
							? "max-h-full max-w-full object-contain"
							: "max-w-none"
					}
					style={
						zoom !== null && natural
							? { width: natural.w * zoom, height: natural.h * zoom }
							: undefined
					}
				/>
			</div>
			{lightboxOpen && (
				<ImageViewer
					src={decoded.url}
					alt={name}
					caption={name}
					filename={name}
					onClose={() => setLightboxOpen(false)}
				/>
			)}
		</div>
	);
}
