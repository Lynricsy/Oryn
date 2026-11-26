/**
 * 应用内图片查看器：基于 `ImageLightbox` 渲染（只用 `<img>` 显示，不导航到原始字节、不新开窗口，
 * 因此即便图片数据可疑也不会成为同源的活动文档）。
 * 同时支持单图（`src` + `alt`）与多图画廊（`images` + `initialIndex`）。
 */
import type { HTMLAttributeReferrerPolicy, ReactNode } from "react";
import { ImageLightbox, type LightboxImage } from "./ImageLightbox";

export type { LightboxImage };

export type ImageViewerProps =
	| {
			src: string;
			alt: string;
			caption?: string;
			filename?: string;
			referrerPolicy?: HTMLAttributeReferrerPolicy;
			onClose: () => void;
			images?: undefined;
			initialIndex?: undefined;
			onIndexChange?: undefined;
	  }
	| {
			images: readonly LightboxImage[];
			initialIndex?: number;
			onClose: () => void;
			onIndexChange?: (index: number) => void;
			src?: undefined;
			alt?: undefined;
			caption?: undefined;
			filename?: undefined;
			referrerPolicy?: undefined;
	  };

export function ImageViewer(props: ImageViewerProps): ReactNode {
	if (props.images) {
		return (
			<ImageLightbox
				images={props.images}
				initialIndex={props.initialIndex}
				onClose={props.onClose}
				onIndexChange={props.onIndexChange}
			/>
		);
	}
	return (
		<ImageLightbox
			images={[
				{
					src: props.src,
					alt: props.alt,
					caption: props.caption,
					filename: props.filename,
					referrerPolicy: props.referrerPolicy,
				},
			]}
			onClose={props.onClose}
		/>
	);
}
