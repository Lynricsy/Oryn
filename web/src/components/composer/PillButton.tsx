/**
 * 首屏新建线程框的选项胶囊（执行位置、Profile、项目、role）：快捷键只放进悬停提示，输入框里不显示按键提示。
 */
import type { ReactElement, Ref } from "react";
import { Badge } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";

/** 胶囊文案：主文字 + 可选徽标；`label` 是整体无障碍名（保持原拼接字符串）。 */
export interface PillText {
	label: string;
	main: string;
	badge?: string;
}

/**
 * 底部选项按钮。`pill`：图标 + 名字，放不下时文字截断；`circle`：窄屏上 orb（以及浏览器 profile）只留一个
 * 32px 小圆圈，名字靠 aria-label 与悬停提示，把横向空间让给项目名。
 */
export function PillButton({
	ref,
	icon,
	text,
	shortcut,
	onClick,
	active,
	shape = "pill",
	grow = false,
}: {
	ref?: Ref<HTMLButtonElement>;
	/** 图标名，或现成的装饰节点（如头像） */
	icon: IconName | ReactElement;
	text: PillText;
	/** 悬停提示里的快捷键；没有时只写名字 */
	shortcut?: string;
	onClick: () => void;
	active: boolean;
	shape?: "pill" | "circle";
	/** 不设 240px 上限，有多少剩余宽度用多少（窄屏的项目名） */
	grow?: boolean;
}) {
	const circle = shape === "circle";
	return (
		<button
			ref={ref}
			type="button"
			onClick={onClick}
			aria-expanded={active}
			aria-label={text.label}
			title={shortcut ? `${text.label} (${shortcut})` : text.label}
			className={`inline-flex h-8 items-center rounded-full border text-xs shadow-xs transition-colors duration-150 ${circle ? "w-8 shrink-0 justify-center" : `min-w-0 shrink gap-1.5 px-3 ${grow ? "max-w-none" : "max-w-60"}`} ${active ? "border-border-strong bg-hover text-fg" : "border-border bg-panel/70 text-muted hover:bg-hover hover:text-fg"}`}
		>
			<span className="inline-flex shrink-0">
				{typeof icon === "string" ? (
					<Icon name={icon} size={circle ? 15 : 13} />
				) : (
					icon
				)}
			</span>
			{!circle && <span className="min-w-0 truncate">{text.main}</span>}
			{!circle && text.badge && (
				<span className="inline-flex shrink-0">
					<Badge>{text.badge}</Badge>
				</span>
			)}
		</button>
	);
}
