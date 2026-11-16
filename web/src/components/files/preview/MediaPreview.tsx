/**
 * 音视频：直接用文件下载地址作 `src`，由浏览器按 Range 流式播放与拖动，不整份读入。
 * 浏览器解不了的编码（如部分 mkv / HEVC）给出提示，仍可下载。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../ui/icons";

export default function MediaPreview({
	src,
	media,
	name,
}: {
	src: string;
	media: "video" | "audio";
	name: string;
}): ReactNode {
	const { t } = useTranslation();
	const [failed, setFailed] = useState(false);
	if (failed)
		return (
			<p className="flex items-center gap-1.5 p-3 text-muted text-sm">
				<Icon name="alert" size={14} className="shrink-0 text-warn" />
				{t("files.preview.media.unsupported")}
			</p>
		);
	return (
		<div className="flex h-full items-center justify-center bg-inset p-3">
			{media === "video" ? (
				// biome-ignore lint/a11y/useMediaCaption: 预览任意视频文件，没有字幕轨
				<video
					src={src}
					controls
					preload="metadata"
					aria-label={name}
					className="max-h-full max-w-full bg-black"
					onError={() => setFailed(true)}
				/>
			) : (
				<div className="flex w-full max-w-md flex-col items-center gap-3">
					<span className="grid h-14 w-14 place-items-center rounded-full bg-panel">
						<Icon name="music" size={24} className="text-faint" />
					</span>
					<span className="max-w-full truncate font-medium text-sm">
						{name}
					</span>
					{/* biome-ignore lint/a11y/useMediaCaption: 预览任意音频文件，没有字幕轨 */}
					<audio
						src={src}
						controls
						preload="metadata"
						aria-label={name}
						className="w-full"
						onError={() => setFailed(true)}
					/>
				</div>
			)}
		</div>
	);
}
