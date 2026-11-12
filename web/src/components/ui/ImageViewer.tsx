/**
 * 应用内图片查看器：只用 `<img>` 显示（不导航到原始字节、不新开窗口），
 * 因此即便图片数据可疑也不会成为同源的活动文档。调用方传入的 src 应来自 `imageSrc()`（栅格图白名单）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Dialog } from "./primitives";

export function ImageViewer({
	src,
	alt,
	onClose,
}: {
	src: string;
	alt: string;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<Dialog open wide title={t("image.viewer")} onClose={onClose}>
			<img
				src={src}
				alt={alt}
				className="mx-auto block max-h-[70dvh] max-w-full object-contain"
			/>
		</Dialog>
	);
}
