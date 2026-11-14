/**
 * 选项弹层（首页新建线程框的胶囊、模型选择）：玻璃弹层 + 小节标题 + 选项行。
 * 选项的主文字紧跟 hint 文字、同在一个按钮里（项目选项的无障碍名 = 项目名 + 仓库地址，验收脚本按此匹配）。
 * `leading` 只能放无文字的图标，一律 aria-hidden；`trailing` 放徽标组。
 */
import { type ReactNode, useEffect, useRef } from "react";
import { Icon } from "./icons";

export function Popover({
	children,
	onClose,
	side = "top",
}: {
	children: ReactNode;
	onClose: () => void;
	/** 向上（首页托盘下方没有空间）或向下（对话框里）展开 */
	side?: "top" | "bottom";
}): ReactNode {
	const ref = useRef<HTMLDivElement>(null);
	useEffect(() => {
		const onDown = (e: MouseEvent) => {
			if (ref.current && !ref.current.contains(e.target as Node)) onClose();
		};
		document.addEventListener("mousedown", onDown);
		return () => document.removeEventListener("mousedown", onDown);
	}, [onClose]);
	return (
		<div
			ref={ref}
			className={`glass pop-in absolute left-0 z-30 max-h-80 w-80 max-w-[calc(100vw-2rem)] overflow-y-auto rounded-lg p-1 ${side === "top" ? "bottom-full mb-2" : "top-full mt-1"}`}
		>
			{children}
		</div>
	);
}

export function PopoverLabel({ children }: { children: ReactNode }): ReactNode {
	return (
		<div className="px-2 pt-2 pb-1 font-medium text-2xs text-faint first:pt-1">
			{children}
		</div>
	);
}

export function Option({
	selected,
	onClick,
	children,
	leading,
	hint,
	trailing,
	mono,
}: {
	selected: boolean;
	onClick: () => void;
	children: ReactNode;
	leading?: ReactNode;
	hint?: ReactNode;
	trailing?: ReactNode;
	mono?: boolean;
}): ReactNode {
	return (
		<button
			type="button"
			onClick={onClick}
			className={`flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm transition-colors duration-150 hover:bg-hover ${selected ? "bg-hover text-fg" : "text-fg"}`}
		>
			{leading && (
				<span
					aria-hidden
					className="inline-flex w-[18px] shrink-0 justify-center text-faint"
				>
					{leading}
				</span>
			)}
			<span className="flex min-w-0 flex-1 items-baseline gap-2">
				<span
					className={`min-w-0 shrink truncate ${mono ? "font-mono text-xs" : ""}`}
				>
					{children}
				</span>
				{hint && (
					<span className="min-w-0 shrink-[2] truncate text-faint text-xs">
						{hint}
					</span>
				)}
			</span>
			{trailing && (
				<span className="flex shrink-0 items-center gap-1">{trailing}</span>
			)}
			{selected && (
				<Icon name="check" size={13} className="shrink-0 text-muted" />
			)}
		</button>
	);
}
