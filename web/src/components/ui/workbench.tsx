/**
 * 工作台原生（D）控件：分组列表与行、L2 表单字段。输入框、下拉、开关、按钮等基础件在 primitives.tsx。
 *
 * - 分组：标题在外（12.5/600 次要灰），行在内（22px 圆角的悬浮毛玻璃面板 `.glass`，行间一条贯穿整行的发丝线）。
 * - 行：28px 中性图标方块 + 标题 / 说明 + 行尾；`Reveal` 包住的动作平时不占位，悬停或 Tab 进这一行时出现，
 *   `StatusCell` 是定宽状态列，所以出现的动作与状态文字长短无关，后面的 ⋯ 和开关也不动。
 * - 字段标题（L2）：标题 13/550，说明并到标题行右侧，只标「可选」；说明与「可选」经 aria-describedby 读给读屏。
 */
import { type ReactNode, useId } from "react";
import { StatusDot, type Tone } from "./data";
import { Icon, type IconName } from "./icons";

// ---------------------------------------------------------------- 分组与行

/** 分组：标题行在面板外（可带右侧说明与动作），内容通常是一个 `GroupList`。 */
export function Group({
	title,
	meta,
	action,
	children,
}: {
	title: ReactNode;
	meta?: ReactNode;
	action?: ReactNode;
	children: ReactNode;
}): ReactNode {
	const id = useId();
	return (
		<section aria-labelledby={id} className="flex flex-col gap-1.5">
			<header className="flex min-h-[22px] items-center gap-2.5 px-1.5">
				<h3
					id={id}
					className="min-w-0 flex-1 truncate font-semibold text-[12.5px] text-muted"
				>
					{title}
				</h3>
				{meta && <span className="text-muted text-xs">{meta}</span>}
				{action}
			</header>
			{children}
		</section>
	);
}

/** 分组面板：一列 `GroupRow`。 */
export function GroupList({ children }: { children: ReactNode }): ReactNode {
	return (
		<ul className="glass divide-y divide-border overflow-hidden rounded-[22px]">
			{children}
		</ul>
	);
}

/** 分组面板里放非列表内容（表单、预览、说明块）：与 `GroupList` 同一块毛玻璃，四周 16px 内边距。 */
export function GroupPanel({ children }: { children: ReactNode }): ReactNode {
	return (
		<div className="glass flex flex-col gap-3 rounded-[22px] px-4 py-3.5">
			{children}
		</div>
	);
}

/** 行首的中性图标方块：不给对象上色。 */
export function Tile({ icon }: { icon: IconName }): ReactNode {
	return (
		<span
			aria-hidden
			className="grid h-7 w-7 shrink-0 place-items-center rounded-[8px] bg-inset text-muted"
		>
			<Icon name={icon} size={15} />
		</span>
	);
}

/**
 * 列表行；`children` 渲染在行下方、与文字列对齐（就地结果、错误）。
 * 行宽放不下「文字列至少 10rem + 行尾」时（手机竖屏），行尾整体换到下一行靠右，标题不被挤没、尾部不被裁切；
 * 桌面宽度下行尾仍在同一行，各行的定宽状态列照常对齐。
 */
export function GroupRow({
	icon,
	title,
	meta,
	trailing,
	children,
}: {
	icon?: IconName;
	title: ReactNode;
	meta?: ReactNode;
	trailing?: ReactNode;
	children?: ReactNode;
}): ReactNode {
	return (
		<li className="group/row transition-colors duration-150 hover:bg-raised">
			<div className="flex min-h-14 flex-wrap items-center gap-x-3 gap-y-2 py-2 pr-3 pl-3.5">
				{icon && <Tile icon={icon} />}
				<div className="flex min-w-40 flex-1 flex-col gap-0.5">
					<div className="flex min-w-0 items-center gap-1.5 font-[550] text-fg text-sm">
						{title}
					</div>
					{meta && (
						<div className="flex min-w-0 flex-wrap items-center gap-x-1.5 text-[12.5px] text-muted leading-normal">
							{meta}
						</div>
					)}
				</div>
				{trailing && (
					<div className="ml-auto flex shrink-0 items-center gap-2">
						{trailing}
					</div>
				)}
			</div>
			{children && <div className="pr-3 pb-2.5 pl-[54px]">{children}</div>}
		</li>
	);
}

/**
 * 悬停才出现的行尾动作：平时宽 0、透明（不占位，仍可 Tab 到），行悬停或行内有焦点时展开淡入；
 * 触屏没有悬停，始终显示。展开时取消裁切，焦点描边不被切掉。
 */
export function Reveal({ children }: { children: ReactNode }): ReactNode {
	return (
		<span className="inline-flex w-0 overflow-hidden opacity-0 transition-opacity duration-150 group-focus-within/row:w-auto group-focus-within/row:overflow-visible group-focus-within/row:opacity-100 group-hover/row:w-auto group-hover/row:overflow-visible group-hover/row:opacity-100 pointer-coarse:w-auto pointer-coarse:overflow-visible pointer-coarse:opacity-100">
			{children}
		</span>
	);
}

/** 定宽状态列：左对齐，各行的状态点竖向对齐。 */
export function StatusCell({ children }: { children: ReactNode }): ReactNode {
	return <span className="flex w-28 min-w-0 shrink-0">{children}</span>;
}

/** 状态：点 + 字，不加框；出错时字也用错误色。 */
export function StatusText({
	tone,
	children,
}: {
	tone: Tone;
	children: ReactNode;
}): ReactNode {
	return (
		<span
			className={`inline-flex min-w-0 items-center gap-1.5 text-[12.5px] ${tone === "err" ? "text-err" : "text-muted"}`}
		>
			<StatusDot tone={tone} />
			<span className="truncate">{children}</span>
		</span>
	);
}

// ---------------------------------------------------------------- 表单

/** 交给控件的属性：`id` 对上标题的 `htmlFor`，说明与「可选」经 `aria-describedby` 读出。 */
export interface FieldControlProps {
	id: string;
	"aria-labelledby": string;
	"aria-describedby"?: string;
}

/**
 * 字段（L2）：标题 13/550 正文色；说明并到标题行右侧（12、faint、超长省略），只标「可选」不标「必填」。
 * 控件以 render prop 给出，拿到 id 与描述关联。
 */
export function FormField({
	label,
	optional,
	aside,
	children,
}: {
	label: ReactNode;
	/** 「可选」标记的文案；必填字段不传 */
	optional?: string;
	aside?: string;
	children: (control: FieldControlProps) => ReactNode;
}): ReactNode {
	const base = useId();
	const id = `${base}-c`;
	const labelId = `${base}-l`;
	const optId = `${base}-o`;
	const asideId = `${base}-a`;
	const described =
		[optional && optId, aside && asideId].filter(Boolean).join(" ") ||
		undefined;
	return (
		<div className="flex flex-col gap-[7px]">
			<div className="flex min-w-0 items-baseline justify-between gap-3">
				<span className="flex min-w-0 items-baseline gap-1.5">
					<label
						id={labelId}
						htmlFor={id}
						className="font-[550] text-[13px] text-fg"
					>
						{label}
					</label>
					{optional && (
						<span id={optId} className="text-faint text-xs">
							{optional}
						</span>
					)}
				</span>
				{aside && (
					<span
						id={asideId}
						title={aside}
						className="min-w-0 max-w-[62%] truncate text-faint text-xs"
					>
						{aside}
					</span>
				)}
			</div>
			{children({
				id,
				"aria-labelledby": labelId,
				"aria-describedby": described,
			})}
		</div>
	);
}
