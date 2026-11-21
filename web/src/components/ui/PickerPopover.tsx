/**
 * 选项弹层（首页新建线程框的胶囊、模型选择）：玻璃弹层 + 小节标题 + 选项行。
 * 选项的主文字紧跟 hint 文字、同在一个按钮里（项目选项的无障碍名 = 项目名 + 仓库地址，验收脚本按此匹配）。
 * `leading` 只能放无文字的图标，一律 aria-hidden；`trailing` 放徽标组。
 */
import { type ReactNode, useEffect, useRef } from "react";
import { Floating } from "./Floating";
import { Icon } from "./icons";

export function Popover({
	children,
	onClose,
	side = "top",
	align = "start",
	bodyTestId,
}: {
	children: ReactNode;
	onClose: () => void;
	/** 向上（首页托盘下方没有空间）或向下（对话框里）展开 */
	side?: "top" | "bottom";
	/** 与打开它的元素左对齐（缺省）或右对齐（贴在工具条右端的按钮） */
	align?: "start" | "end";
	/** 滚动体的 `data-testid`（截图的溢出断言用），原样转给 `Floating` */
	bodyTestId?: string;
}): ReactNode {
	const ref = useRef<HTMLDivElement>(null);
	// 打开它的元素（首次渲染时的焦点 = 胶囊）；Escape 关闭后焦点回到这里，不掉到 body
	const openerRef = useRef<Element | null | undefined>(undefined);
	if (openerRef.current === undefined)
		openerRef.current = document.activeElement;
	useEffect(() => {
		const onDown = (e: MouseEvent) => {
			if (ref.current && !ref.current.contains(e.target as Node)) onClose();
		};
		// 捕获阶段拦下 Escape：只关这个弹层，不再传给外层（对话框在 window 上监听 Escape，否则会被一起关掉）
		const onKey = (e: KeyboardEvent) => {
			if (e.key !== "Escape") return;
			e.stopPropagation();
			onClose();
			const opener = openerRef.current;
			if (opener instanceof HTMLElement && opener.isConnected) opener.focus();
		};
		document.addEventListener("mousedown", onDown);
		document.addEventListener("keydown", onKey, true);
		return () => {
			document.removeEventListener("mousedown", onDown);
			document.removeEventListener("keydown", onKey, true);
		};
	}, [onClose]);
	return (
		<Floating
			ref={ref}
			side={side}
			align={align}
			gap={side === "top" ? 8 : 4}
			className="w-80 max-w-[calc(100vw-2rem)]"
			bodyClassName="p-1"
			bodyTestId={bodyTestId}
		>
			{children}
		</Floating>
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
	disabled,
}: {
	selected: boolean;
	onClick: () => void;
	children: ReactNode;
	leading?: ReactNode;
	hint?: ReactNode;
	trailing?: ReactNode;
	mono?: boolean;
	/** 看得见、点不了（如停着的 box）：整行半透明、没有悬停底色 */
	disabled?: boolean;
}): ReactNode {
	return (
		<button
			type="button"
			onClick={onClick}
			disabled={disabled}
			aria-disabled={disabled || undefined}
			className={`flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm transition-colors duration-150 ${disabled ? "opacity-50" : "hover:bg-hover"} ${selected ? "bg-hover text-fg" : "text-fg"}`}
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
			{/* 勾跟在文字后、行尾图标前：占的是文字区的余量，行尾的徽标、logo 不被挤开 */}
			{selected && (
				<Icon name="check" size={13} className="shrink-0 text-muted" />
			)}
			{trailing && (
				<span className="flex shrink-0 items-center gap-1">{trailing}</span>
			)}
		</button>
	);
}
