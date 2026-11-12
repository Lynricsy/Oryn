/** 图片附件（粘贴 / 拖放 / 选择），按整条消息预算压缩。新建线程框与线程输入区共用。 */
import { useCallback, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	checkBudget,
	formatBytes,
	ImageBudgetError,
	type PreparedImage,
	prepareImage,
} from "../../lib/images";
import type { ImageContent } from "../../omp/types";
import { Icon } from "../ui/icons";

export function useImageAttachments() {
	const { t } = useTranslation();
	const [images, setImages] = useState<PreparedImage[]>([]);
	const [error, setError] = useState<string | null>(null);

	const addFiles = useCallback(
		async (files: Iterable<File>) => {
			setError(null);
			let current = images;
			for (const file of files) {
				if (!file.type.startsWith("image/")) continue;
				const { remaining } = checkBudget(
					current.map((i) => i.inlineBytes),
					0,
				);
				try {
					const img = await prepareImage(file, file.name, remaining);
					current = [...current, img];
					setImages(current);
				} catch (e) {
					if (e instanceof ImageBudgetError) {
						setError(
							t("composer.imageTooLarge", {
								name: file.name,
								size: formatBytes(e.needed),
								remaining: formatBytes(e.remaining),
							}),
						);
					} else setError(t("composer.imageFailed", { name: file.name }));
				}
			}
		},
		[images, t],
	);

	const remove = useCallback(
		(i: number) => setImages((prev) => prev.filter((_, idx) => idx !== i)),
		[],
	);
	const clear = useCallback(() => {
		setImages([]);
		setError(null);
	}, []);
	const contents: ImageContent[] = images.map((i) => ({
		type: "image",
		data: i.data,
		mimeType: i.mimeType,
	}));
	return { images, contents, error, addFiles, remove, clear };
}

export function AttachmentStrip({
	images,
	onRemove,
}: {
	images: PreparedImage[];
	onRemove: (i: number) => void;
}) {
	const { t } = useTranslation();
	if (images.length === 0) return null;
	return (
		<div className="flex flex-wrap gap-2 px-3 pt-2">
			{images.map((img, i) => (
				<div key={`${img.name}-${img.inlineBytes}`} className="group relative">
					<img
						src={`data:${img.mimeType};base64,${img.data}`}
						alt={img.name}
						className="h-14 w-14 rounded-md border border-border object-cover"
					/>
					<button
						type="button"
						aria-label={t("composer.removeImage")}
						className="absolute -top-1.5 -right-1.5 hidden h-5 w-5 items-center justify-center rounded-full bg-fg text-bg group-hover:flex"
						onClick={() => onRemove(i)}
					>
						<Icon name="close" size={11} />
					</button>
				</div>
			))}
		</div>
	);
}
