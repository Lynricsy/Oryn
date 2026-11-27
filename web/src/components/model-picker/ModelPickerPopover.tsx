/**
 * 选择器的容器：桌面是锚在触发元素上的弹层（固定尺寸，放得下三栏），窄屏是底部抽屉（对话框）。
 * 线程的模型对话框自己就是对话框，直接把 `ModelPicker` 放进去，不用这个。
 * 放在触发按钮的同一个 `relative` 容器里（弹层按 DOM 父元素定位）。
 */
import type { ReactNode } from "react";
import { useBreakpoint } from "../../lib/viewport";
import { Popover } from "../ui/PickerPopover";
import { Dialog } from "../ui/primitives";

export function ModelPickerPopover({
	title,
	onClose,
	side = "bottom",
	align = "start",
	testId,
	children,
}: {
	/** 窄屏抽屉的标题 */
	title: string;
	onClose: () => void;
	side?: "top" | "bottom";
	align?: "start" | "end";
	/** 内容容器的 `data-testid` */
	testId?: string;
	/** 一个 `ModelPicker` */
	children: ReactNode;
}): ReactNode {
	const compact = useBreakpoint() === "compact";
	if (compact)
		return (
			<Dialog open title={title} onClose={onClose}>
				<div data-testid={testId} className="-mx-1 flex h-[70dvh] min-h-0">
					{children}
				</div>
			</Dialog>
		);
	return (
		<Popover
			side={side}
			align={align}
			onClose={onClose}
			bare
			maxHeight={480}
			className="w-[720px] overflow-hidden"
		>
			{/* 非模态对话框语义：读屏与测试都能按标题找到它（窄屏抽屉本身就是同名对话框） */}
			<div
				role="dialog"
				aria-label={title}
				data-testid={testId}
				className="flex h-[460px] min-h-0 shrink"
			>
				{children}
			</div>
		</Popover>
	);
}
