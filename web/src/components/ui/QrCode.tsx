/**
 * 二维码（内联 SVG，每行连续的深色模块合成一个 `<rect>`）。
 * 要高对比才扫得出：浅色主题用纸面底 + 正文色码，深色主题反过来（正文色作底、纸面色作码），始终浅底深码；
 * 底色的内边距就是静区。
 */
import { type ReactNode, useMemo } from "react";
import { qrModules } from "../../lib/pair-qr";

export function QrCode({
	text,
	label,
	testId,
	className = "h-36 w-36",
}: {
	text: string;
	/** 读屏名 */
	label: string;
	testId?: string;
	/** 码区尺寸 */
	className?: string;
}): ReactNode {
	const qr = useMemo(() => qrModules(text), [text]);
	return (
		<div className="shrink-0 rounded-[12px] bg-panel p-3 text-fg ring-1 ring-border [[data-theme=dark]_&]:bg-fg [[data-theme=dark]_&]:text-panel">
			<svg
				data-testid={testId}
				data-qr-payload={text}
				role="img"
				aria-label={label}
				viewBox={`0 0 ${qr.size} ${qr.size}`}
				shapeRendering="crispEdges"
				className={`block ${className}`}
			>
				{qr.runs.map((r) => (
					<rect
						key={`${r.x},${r.y}`}
						x={r.x}
						y={r.y}
						width={r.w}
						height={1}
						fill="currentColor"
					/>
				))}
			</svg>
		</div>
	);
}
