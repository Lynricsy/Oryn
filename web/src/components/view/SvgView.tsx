/** 视图的 `svg` 块：经 `sanitizeSvgMarkup` 清洗后内联，容器桥接 omp 的 SVG 主题变量。 */
import { type ReactNode, useEffect, useMemo, useRef } from "react";
import { useTranslation } from "react-i18next";
import { sanitizeSvgMarkup } from "../../lib/ask-form";
import { SVG_THEME_VARS } from "../../lib/svg-theme";
import type { SvgBlock } from "../../lib/view/spec";

export function SvgView({
	block,
	streaming,
}: {
	block: SvgBlock;
	streaming: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const hostRef = useRef<HTMLDivElement>(null);
	// 流式中的标记还不完整：不清洗、不挂载
	const clean = useMemo(
		() => (streaming ? null : sanitizeSvgMarkup(block.svg)),
		[streaming, block.svg],
	);
	useEffect(() => {
		if (hostRef.current) hostRef.current.innerHTML = clean ?? "";
	}, [clean]);

	if (streaming)
		return <div aria-hidden className="h-40 w-full rounded-md bg-inset" />;
	if (!clean)
		return (
			<p className="m-0 rounded-md bg-inset px-3 py-2 text-faint text-xs">
				{t("view.block.svgInvalid")}
			</p>
		);
	return (
		<figure
			className="m-0 flex min-w-0 flex-col gap-1.5"
			style={SVG_THEME_VARS}
		>
			<div
				ref={hostRef}
				role="img"
				aria-label={block.caption ?? t("view.block.svg")}
				className="flex justify-center overflow-auto text-fg [&_svg]:block [&_svg]:h-auto [&_svg]:max-w-full"
			/>
			{block.caption && (
				<figcaption className="text-center text-faint text-xs">
					{block.caption}
				</figcaption>
			)}
		</figure>
	);
}
