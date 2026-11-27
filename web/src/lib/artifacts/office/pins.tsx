// 评论图钉（步骤 7.4）：覆盖在页 / 幻灯片 / 块上的编号圆点；agent 回复过的串（answered）变灰。
// 只画宿主发来的未解决的串；打印与缩略图视图由调用方不传图钉。

import type { CSSProperties, ReactNode } from "react";
import type { AnchorPin } from "./types";
import "./pins.css";

/** 一个编号圆点；`style` 给出位置（相对最近的定位祖先）。 */
export function PinDot({
	pin,
	style,
}: {
	pin: AnchorPin;
	style?: CSSProperties;
}): ReactNode {
	return (
		<span
			className={`oryn-pin${pin.answered ? " oryn-pin-answered" : ""}`}
			style={style}
		>
			{pin.n}
		</span>
	);
}

/** 圆点直径（px），与帧运行时选择模式的图钉一致。 */
const PIN_SIZE = 20;

/**
 * 盖满一页的图钉层：带坐标的按归一化坐标落点；整页的（坐标为空）在左上角依次排开。
 * 父元素须是定位元素。
 */
export function PinLayer({ pins }: { pins: readonly AnchorPin[] }): ReactNode {
	if (pins.length === 0) return null;
	let whole = 0;
	return (
		<div className="oryn-pins" aria-hidden="true">
			{pins.map((p) => {
				const { x, y } = p.anchor;
				const style: CSSProperties =
					x !== null && y !== null
						? { left: `${x * 100}%`, top: `${y * 100}%` }
						: {
								left: PIN_SIZE / 2 + 6 + whole++ * (PIN_SIZE + 4),
								top: PIN_SIZE / 2 + 6,
							};
				return <PinDot key={p.n} pin={p} style={style} />;
			})}
		</div>
	);
}
