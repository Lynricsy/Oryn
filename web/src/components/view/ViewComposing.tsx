/**
 * 视图生成中的占位（M33）：参数还没流到第一个能显示的内容时显示。模型 API 不边写边发工具参数时，整个视图要等到参数
 * 写完才出现，这段时间可能有十几秒，所以占位要看得出「正在画」：几行骨架块，一道主题色微光从左扫到右，下方一行文字
 * 跟着三个依次跃动的点。减少动效时只留静止的骨架与文字。
 */
import type { ReactNode } from "react";

export function ViewComposing({
	label,
	height,
}: {
	label: string;
	/** 指定时占位撑满这个高度（HTML 视图沿用帧的高度，挂帧时不跳动）。 */
	height?: number;
}): ReactNode {
	return (
		<div
			role="status"
			aria-label={label}
			data-testid="view-composing"
			className="relative flex flex-col justify-between gap-4 overflow-hidden rounded-md"
			style={height === undefined ? undefined : { height }}
		>
			<div aria-hidden className="flex flex-col gap-2.5">
				<div className="h-3 w-2/5 rounded-full bg-fg/8" />
				<div className="grid grid-cols-3 gap-2">
					<div className="h-12 rounded-md bg-fg/6" />
					<div className="h-12 rounded-md bg-fg/6" />
					<div className="h-12 rounded-md bg-fg/6" />
				</div>
				<div className="h-3 w-4/5 rounded-full bg-fg/8" />
				<div className="h-3 w-3/5 rounded-full bg-fg/8" />
			</div>
			<p
				aria-hidden
				className="m-0 flex items-center gap-1.5 text-faint text-xs"
			>
				{label}
				<span className="flex items-center gap-0.5">
					{[0, 1, 2].map((i) => (
						<span
							key={i}
							className="typing-dot h-1 w-1 rounded-full bg-current"
							style={{ animationDelay: `${i * 160}ms` }}
						/>
					))}
				</span>
			</p>
			<span aria-hidden className="view-composing-sweep" />
		</div>
	);
}
