/** 通用界面元件：按钮、快捷键胶囊、对话框、菜单、表单控件（含复选框/单选）、迷你曲线、状态图标、标签页样式。 */

import type { TFunction } from "i18next";
import {
	type ButtonHTMLAttributes,
	type ComponentProps,
	type InputHTMLAttributes,
	type KeyboardEvent as ReactKeyboardEvent,
	type ReactNode,
	type Ref,
	type TextareaHTMLAttributes,
	useCallback,
	useEffect,
	useId,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { ApiError, type ThreadState } from "../../api/client";
import { focusablesIn, isTopModal, pushModal, trapTab } from "../../lib/modal";
import type { NativeHint } from "../../lib/native-header";
import { useCoarsePointer } from "../../lib/viewport";
import { Floating } from "./Floating";
import { Icon, type IconName } from "./icons";
import { OrbBead } from "./OrbBead";

type Variant = "primary" | "accent" | "secondary" | "ghost" | "danger";

const VARIANT: Record<Variant, string> = {
	primary: "bg-ink text-ink-fg shadow-xs hover:opacity-90",
	accent: "bg-accent text-accent-fg shadow-xs hover:brightness-110",
	secondary: "border border-border bg-panel text-fg shadow-xs hover:bg-hover",
	ghost: "text-muted hover:bg-hover hover:text-fg",
	danger: "border border-err/30 bg-err/5 text-err hover:bg-err/10",
};

/**
 * 禁用态：墨色主按钮半透明后在深色下成了浑浊的灰块，改成内嵌井底 + 细描边 + faint 字（明暗都读作「暂不可用」）；
 * 强调色按钮只降不透明度，保留强调色身份；其余照常半透明。
 */
const DISABLED: Record<Variant, string> = {
	primary:
		"disabled:bg-inset disabled:text-faint disabled:shadow-none disabled:inset-ring-1 disabled:inset-ring-border",
	accent: "disabled:opacity-50",
	secondary: "disabled:opacity-50",
	ghost: "disabled:opacity-50",
	danger: "disabled:opacity-50",
};

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
	variant?: Variant;
	size?: "sm" | "md";
	icon?: IconName;
}

export function Button({
	variant = "secondary",
	size = "md",
	icon,
	className = "",
	children,
	...rest
}: ButtonProps) {
	const sz =
		size === "sm" ? "h-7 px-2.5 text-xs gap-1" : "h-8 px-3 text-sm gap-1.5";
	return (
		<button
			type="button"
			// 按钮高度固定，文字换行只会溢出；中文在任意两字之间都可断行，不禁止换行时 flex 行里的按钮会被挤成一字一行
			className={`inline-flex select-none items-center justify-center whitespace-nowrap rounded-[10px] font-medium transition-[background-color,box-shadow,scale,color] duration-150 ease-out active:scale-[0.98] disabled:pointer-events-none ${sz} ${VARIANT[variant]} ${DISABLED[variant]} ${className}`}
			{...rest}
		>
			{icon && <Icon name={icon} size={size === "sm" ? 13 : 15} />}
			{children}
		</button>
	);
}

const BOX = { xs: "h-6 w-6", sm: "h-7 w-7", md: "h-8 w-8" } as const;

export interface IconButtonProps
	extends ButtonHTMLAttributes<HTMLButtonElement> {
	ref?: Ref<HTMLButtonElement>;
	icon: IconName;
	label: string;
	active?: boolean;
	/** 图标尺寸（px） */
	size?: number;
	/** 按钮外框：md=32px（默认）、sm=28px、xs=24px。className 里的 h-/w- 不能覆盖它，需改尺寸时用这个 */
	box?: "xs" | "sm" | "md";
}

export function IconButton({
	icon,
	label,
	active,
	size = 16,
	box = "md",
	className = "",
	...rest
}: IconButtonProps) {
	return (
		<button
			type="button"
			aria-label={label}
			title={label}
			aria-pressed={active}
			className={`inline-flex ${BOX[box]} shrink-0 items-center justify-center rounded-[9px] text-muted transition-[background-color,color,scale] duration-150 ease-out hover:bg-hover hover:text-fg active:scale-95 disabled:opacity-40 ${active ? "bg-active text-fg" : ""} ${className}`}
			{...rest}
		>
			<Icon name={icon} size={size} />
		</button>
	);
}

export function Kbd({ children }: { children: ReactNode }) {
	return (
		<kbd className="rounded border border-border bg-panel px-1 font-mono text-2xs text-faint leading-4 shadow-xs">
			{children}
		</kbd>
	);
}

export function Spinner({ size = 14 }: { size?: number }) {
	return (
		<span
			role="status"
			className="inline-block animate-spin rounded-full border-[1.5px] border-current border-t-transparent"
			style={{ width: size, height: size }}
		/>
	);
}

/**
 * 模态对话框：Esc / 点遮罩关闭；compact 屏上为底部抽屉（贴屏幕底边，下两角与屏幕圆角同心，见 index.css 的 `.dc-*`）。
 * 版式：一整块纸面，标题、正文、按钮之间不画分隔线，只靠留白分层；四边 20px 内边距配 32px 圆角（正文里的卡片用
 * 12px 圆角，与外框同心）；标题 16px 半粗，关闭按钮是右上角的小圆钮。正文超高时只有正文滚动。
 * 渲染到 body 下；打开时把焦点移入对话框、Tab 在其中循环、背景 `inert`，关闭后焦点回到打开前的元素。
 * 不给 `onClose` 即不可关闭（必须在正文或按钮里作出选择）：没有关闭钮，Esc 与点遮罩都不起作用。
 */
export function Dialog({
	open,
	onClose,
	title,
	children,
	footer,
	wide,
	size,
	wrapTitle,
	bodyTestId,
}: {
	open: boolean;
	onClose?: () => void;
	title: ReactNode;
	children: ReactNode;
	footer?: ReactNode;
	wide?: boolean;
	/** `xl`：分栏类对话框（≈1024px，如 btw 话题列表 + 详情）；优先于 `wide`，窄屏同样全宽底部弹出。 */
	size?: "xl";
	/** 标题折行而不是单行截断：确认框的标题是带名字的问句，截断会吞掉关键信息 */
	wrapTitle?: boolean;
	/** 正文滚动体的 `data-testid`（截图的溢出断言量的是真正的滚动体） */
	bodyTestId?: string;
}) {
	const { t } = useTranslation();
	const hostRef = useRef<HTMLDivElement>(null);
	const panelRef = useRef<HTMLDivElement>(null);
	const bodyRef = useRef<HTMLDivElement>(null);
	const onCloseRef = useRef(onClose);
	onCloseRef.current = onClose;
	// 触发者要在提交之前取：内容里的 autoFocus 在子元素提交时就抢走焦点，早于下面的 layout effect。
	// 打开那次渲染里记下（关闭时清掉），对话框随打开才挂载（`open` 恒为 true）也成立。
	const openerRef = useRef<HTMLElement | null | undefined>(undefined);
	if (!open) openerRef.current = undefined;
	else if (openerRef.current === undefined)
		openerRef.current =
			document.activeElement instanceof HTMLElement
				? document.activeElement
				: null;
	useLayoutEffect(() => {
		const host = hostRef.current;
		const panel = panelRef.current;
		if (!open || !host || !panel) return;
		// 兜底：记下的元素已在对话框里（不该发生），就不还原到它
		const opener = openerRef.current;
		const previous = opener && !host.contains(opener) ? opener : null;
		const release = pushModal(host);
		// 内容里的 autoFocus 已生效则不动；否则聚焦正文第一个可聚焦元素，再退到面板本身
		if (!panel.contains(document.activeElement)) {
			const first =
				(bodyRef.current && focusablesIn(bodyRef.current)[0]) ??
				focusablesIn(panel)[0];
			(first ?? panel).focus();
		}
		const onKey = (e: KeyboardEvent) => {
			if (!isTopModal(host)) return;
			if (e.key === "Escape") onCloseRef.current?.();
			else trapTab(e, panel);
		};
		window.addEventListener("keydown", onKey);
		return () => {
			window.removeEventListener("keydown", onKey);
			release();
			if (previous?.isConnected) previous.focus();
		};
	}, [open]);
	if (!open) return null;
	return createPortal(
		<div
			ref={hostRef}
			className="fixed inset-0 z-50 flex items-end justify-center sm:items-center"
		>
			{onClose ? (
				<button
					type="button"
					tabIndex={-1}
					aria-label={t("common.close")}
					className="absolute inset-0 bg-scrim backdrop-blur-[3px]"
					onClick={onClose}
				/>
			) : (
				<div
					aria-hidden
					className="absolute inset-0 bg-scrim backdrop-blur-[3px]"
				/>
			)}
			<div
				ref={panelRef}
				role="dialog"
				aria-modal="true"
				tabIndex={-1}
				aria-label={typeof title === "string" ? title : undefined}
				className={`pop-in dc-bl dc-br relative flex max-h-[85dvh] w-full flex-col rounded-t-xl bg-panel shadow-xl outline-none ring-1 ring-[var(--glass-line)] [--dc-gap:0px] [--dc-r:0px] sm:rounded-xl sm:[--dc-gap:100vmax] sm:[--dc-r:var(--r-xl)] ${size === "xl" ? "sm:max-w-5xl" : wide ? "sm:max-w-3xl" : "sm:max-w-lg"} pb-safe`}
			>
				<div className="flex items-start gap-3 pt-5 pr-4 pb-2 pl-5">
					<h2
						className={`min-w-0 flex-1 pt-0.5 font-semibold text-base text-fg tracking-tight ${wrapTitle ? "text-pretty break-words" : "truncate"}`}
					>
						{title}
					</h2>
					{onClose && (
						<IconButton
							icon="close"
							size={14}
							box="sm"
							label={t("common.close")}
							className="-mt-0.5 rounded-full! bg-inset"
							onClick={onClose}
						/>
					)}
				</div>
				<div
					ref={bodyRef}
					data-testid={bodyTestId}
					className={`min-h-0 flex-1 overflow-y-auto px-5 pt-1 ${footer ? "pb-2" : "pb-5"}`}
				>
					{children}
				</div>
				{footer && (
					<div className="flex flex-wrap justify-end gap-2 px-5 pt-3 pb-5">
						{footer}
					</div>
				)}
			</div>
		</div>,
		document.body,
	);
}

export interface MenuItem {
	key: string;
	label: ReactNode;
	icon?: IconName;
	/** 自定义前置图形（如思考级别圆圈）；与 `icon` 二选一 */
	leading?: ReactNode;
	/** 单选菜单项：给出时为 `menuitemradio`，选中项右侧打勾 */
	checked?: boolean;
	danger?: boolean;
	disabled?: boolean;
	onSelect?: () => void;
	children?: MenuItem[];
	hint?: ReactNode;
	/** 额外的 `data-*` 属性（测试与验收脚本的选择器，如统计页范围菜单的 `data-range`） */
	attrs?: Record<`data-${string}`, string>;
	/** 悬停提示（如置灰的原因）。 */
	title?: string;
	/** iOS 原生页头菜单用的补充（lib/native-header.ts）：label 不是字符串时必须给 native.label */
	native?: NativeHint;
}

/**
 * 菜单按钮模式：选中或 Esc 关闭后焦点回到触发钮（容器里的第一个可聚焦元素）。选中的菜单项随菜单卸载，
 * 焦点留在它上面会掉到 body；随后弹出的确认框等也会把这个已卸载的元素记成「打开者」，关闭后无处可还。
 */
function focusMenuTrigger(root: HTMLElement | null) {
	root
		?.querySelector<HTMLElement>(
			"button, a[href], [tabindex]:not([tabindex='-1'])",
		)
		?.focus({ preventScroll: true });
}

/**
 * 下拉菜单（支持一级子菜单）；`side="top"` 时向上展开（贴近输入坞的消息菜单）。
 * `footer`：菜单项之下的一段说明（不可点，只在顶层显示）。`className` 加在根元素上（默认是块级的 relative 容器）。
 */
export function Menu({
	trigger,
	items,
	align = "right",
	side = "bottom",
	footer,
	className = "",
}: {
	trigger: (props: {
		onClick: () => void;
		"aria-expanded": boolean;
		"aria-haspopup": "menu";
	}) => ReactNode;
	items: MenuItem[];
	align?: "left" | "right";
	side?: "top" | "bottom";
	footer?: ReactNode;
	className?: string;
}) {
	const [open, setOpen] = useState(false);
	const [sub, setSub] = useState<string | null>(null);
	const ref = useRef<HTMLDivElement>(null);
	useEffect(() => {
		if (!open) return;
		const onDown = (e: MouseEvent) => {
			if (ref.current && !ref.current.contains(e.target as Node))
				setOpen(false);
		};
		const onKey = (e: KeyboardEvent) => {
			if (e.key !== "Escape") return;
			if (ref.current?.contains(document.activeElement))
				focusMenuTrigger(ref.current);
			setOpen(false);
		};
		document.addEventListener("mousedown", onDown);
		window.addEventListener("keydown", onKey);
		return () => {
			document.removeEventListener("mousedown", onDown);
			window.removeEventListener("keydown", onKey);
		};
	}, [open]);
	const list = sub ? (items.find((i) => i.key === sub)?.children ?? []) : items;
	return (
		<div ref={ref} className={`relative ${className}`}>
			{trigger({
				onClick: () => {
					setOpen((v) => !v);
					setSub(null);
				},
				"aria-expanded": open,
				"aria-haspopup": "menu",
			})}
			{open && (
				<Floating
					role="menu"
					side={side}
					align={align === "right" ? "end" : "start"}
					className="w-max min-w-52 max-w-72"
					bodyClassName="p-1"
				>
					{sub && (
						<button
							type="button"
							className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-faint text-xs hover:bg-hover"
							onClick={() => setSub(null)}
						>
							<Icon name="back" size={13} />
							{items.find((i) => i.key === sub)?.label}
						</button>
					)}
					{list.map((item) => (
						<button
							key={item.key}
							type="button"
							{...item.attrs}
							{...(item.checked === undefined
								? { role: "menuitem" }
								: { role: "menuitemradio", "aria-checked": item.checked })}
							disabled={item.disabled}
							title={item.title}
							className={`flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-hover disabled:opacity-40 ${item.danger ? "text-err" : "text-fg"} ${item.checked ? "font-medium" : ""}`}
							onClick={() => {
								if (item.children) {
									setSub(item.key);
									return;
								}
								setOpen(false);
								focusMenuTrigger(ref.current);
								item.onSelect?.();
							}}
						>
							{item.leading ? (
								// 前置图形只是装饰（如思考级别圆圈自带 role=img），菜单项的名字就是文案
								<span
									aria-hidden
									className="inline-flex w-[14px] shrink-0 justify-center text-muted"
								>
									{item.leading}
								</span>
							) : (
								item.icon && (
									<Icon
										name={item.icon}
										size={14}
										className="shrink-0 text-muted"
									/>
								)
							)}
							<span className="min-w-0 flex-1 truncate">{item.label}</span>
							{/* 勾跟在文字后、行尾提示前：占的是文字区的余量，提示与箭头不被挤开 */}
							{item.checked && (
								<Icon
									name="check"
									size={13}
									weight="bold"
									className="shrink-0 text-accent-ink"
								/>
							)}
							{item.hint && (
								<span className="text-faint text-xs">{item.hint}</span>
							)}
							{item.children && (
								<Icon name="chevronRight" size={13} className="text-faint" />
							)}
						</button>
					))}
					{footer && !sub && (
						<div className="mt-1 border-border border-t pt-1">{footer}</div>
					)}
				</Floating>
			)}
		</div>
	);
}

/**
 * 输入框（D）：圆角 11、半透明底 + 细外缘；聚焦「提亮浮起」——底色变实、外缘加深、上浮 1px，不加主题色，
 * 只有光标是主题色（令牌 `--field*`、`--sh-field*`，见 index.css）。强制色模式下补系统高亮轮廓。
 */
export const FIELD =
	"w-full rounded-[11px] bg-[var(--field)] px-3 text-[13.5px] text-fg caret-[var(--brand)] shadow-[var(--sh-field)] outline-none transition-[background-color,box-shadow,translate] duration-150 placeholder:text-faint focus:-translate-y-px focus:bg-[var(--field-focus)] focus:shadow-[var(--sh-field-focus)] disabled:opacity-60 motion-reduce:focus:translate-y-0 forced-colors:focus-visible:outline-2 forced-colors:focus-visible:outline-offset-1 forced-colors:focus-visible:outline-[Highlight]";

export function TextInput(props: InputHTMLAttributes<HTMLInputElement>) {
	return (
		<input
			{...props}
			className={`${FIELD} py-[7px] leading-5 ${props.className ?? ""}`}
		/>
	);
}

/** 多行输入；默认等宽字体（配置 / 代码类内容），`mono={false}` 用正文字体（提示词等自然语言）。 */
export function TextArea({
	mono = true,
	className = "",
	...props
}: TextareaHTMLAttributes<HTMLTextAreaElement> & { mono?: boolean }) {
	return (
		<textarea
			{...props}
			className={`${FIELD} py-2 ${mono ? "font-mono" : ""} ${className}`}
		/>
	);
}

export interface SelectOption<T extends string> {
	value: T;
	label: string;
	/** 选项右侧的次要说明（如英文名的中文） */
	hint?: string;
	disabled?: boolean;
}

/**
 * 下拉选择（D）。鼠标 / 键盘：combobox 触发钮 + 毛玻璃 listbox（`Floating`），焦点始终留在触发钮上（aria-activedescendant）；
 * 关闭时 ↓ ↑ Enter Space 打开，打开时 ↑ ↓ Home End 移动、首字母跳转、Enter / Space 选中，
 * Esc 关闭不改值（不冒泡到外层对话框），Tab 关闭并照常前进，点外面关闭。粗指针（触屏）用原生 select。
 * 可读名称三选一：外层 `<label>` 包住、`aria-labelledby`（FormField 给出）、`aria-label`。
 * `className` 加在外包上，缺省占满一行；`value` 不在选项里时显示 `placeholder`。
 * `size="sm"`：工具条里的紧凑款（高 28、12px 字）。
 */
export function Select<T extends string>({
	value,
	options,
	onChange,
	placeholder,
	disabled,
	size = "md",
	className = "w-full",
	id,
	...aria
}: {
	value: T | "";
	options: readonly SelectOption<T>[];
	onChange: (v: T) => void;
	placeholder?: string;
	disabled?: boolean;
	size?: "sm" | "md";
	className?: string;
	id?: string;
	"aria-label"?: string;
	"aria-labelledby"?: string;
	"aria-describedby"?: string;
	"aria-invalid"?: boolean;
}): ReactNode {
	// FIELD 自带 13.5px 字号：紧凑款用 `!` 压过它
	const box = size === "sm" ? "h-7 text-xs!" : "h-[34px]";
	const coarse = useCoarsePointer();
	const [open, setOpen] = useState(false);
	const [active, setActive] = useState(0);
	const wrapRef = useRef<HTMLDivElement>(null);
	const ownId = useId();
	const buttonId = id ?? `${ownId}-b`;
	const listId = `${ownId}-l`;
	const selected = options.findIndex((o) => o.value === value);

	useEffect(() => {
		if (!open) return;
		const onDown = (e: MouseEvent) => {
			if (!wrapRef.current?.contains(e.target as Node)) setOpen(false);
		};
		document.addEventListener("mousedown", onDown);
		return () => document.removeEventListener("mousedown", onDown);
	}, [open]);

	if (coarse)
		return (
			<span className={`relative flex min-w-0 ${className}`}>
				<select
					{...aria}
					id={buttonId}
					disabled={disabled}
					value={value}
					onChange={(e) => onChange(e.target.value as T)}
					className={`${box} appearance-none pr-9 ${FIELD}`}
				>
					{selected < 0 && (
						<option value="" disabled>
							{placeholder ?? ""}
						</option>
					)}
					{options.map((o) => (
						<option key={o.value} value={o.value} disabled={o.disabled}>
							{o.label}
						</option>
					))}
				</select>
				<Icon
					name="caretUpDown"
					size={13}
					className="pointer-events-none absolute top-1/2 right-3 -translate-y-1/2 text-faint"
				/>
			</span>
		);

	const show = () => {
		setActive(Math.max(selected, 0));
		setOpen(true);
	};
	const choose = (i: number) => {
		const o = options[i];
		if (!o || o.disabled) return;
		onChange(o.value);
		setOpen(false);
	};
	const move = (i: number) => setActive((i + options.length) % options.length);

	const onKeyDown = (e: ReactKeyboardEvent<HTMLButtonElement>) => {
		if (!open) {
			if (["ArrowDown", "ArrowUp", "Enter", " "].includes(e.key)) {
				e.preventDefault();
				show();
			}
			return;
		}
		switch (e.key) {
			case "ArrowDown":
				e.preventDefault();
				move(active + 1);
				return;
			case "ArrowUp":
				e.preventDefault();
				move(active - 1);
				return;
			case "Home":
				e.preventDefault();
				move(0);
				return;
			case "End":
				e.preventDefault();
				move(options.length - 1);
				return;
			case "Enter":
			case " ":
				e.preventDefault();
				choose(active);
				return;
			case "Escape":
				// 只关列表：不让外层对话框的 Esc 一起关掉
				e.preventDefault();
				e.stopPropagation();
				setOpen(false);
				return;
			case "Tab":
				setOpen(false);
				return;
		}
		if (e.key.length === 1 && !e.nativeEvent.isComposing) {
			const k = e.key.toLowerCase();
			for (let s = 1; s <= options.length; s++) {
				const i = (active + s) % options.length;
				if (options[i]?.label.toLowerCase().startsWith(k)) {
					setActive(i);
					return;
				}
			}
		}
	};

	const labelledBy = aria["aria-labelledby"];
	return (
		<div ref={wrapRef} className={`relative min-w-0 ${className}`}>
			<button
				type="button"
				role="combobox"
				aria-haspopup="listbox"
				aria-expanded={open}
				aria-controls={listId}
				aria-activedescendant={open ? `${listId}-${active}` : undefined}
				{...aria}
				// 读出「标题 + 当前值」：标题之后再引用触发钮自己
				aria-labelledby={labelledBy ? `${labelledBy} ${buttonId}` : undefined}
				id={buttonId}
				disabled={disabled}
				onClick={() => (open ? setOpen(false) : show())}
				onKeyDown={onKeyDown}
				className={`flex ${box} cursor-pointer items-center justify-between gap-2 text-left disabled:cursor-default aria-expanded:-translate-y-px aria-expanded:bg-[var(--field-focus)] aria-expanded:shadow-[var(--sh-field-focus)] ${FIELD}`}
			>
				<span
					className={`min-w-0 truncate ${selected < 0 ? "text-faint" : ""}`}
				>
					{options[selected]?.label ?? placeholder}
				</span>
				<Icon name="caretUpDown" size={13} className="shrink-0 text-faint" />
			</button>
			{open && (
				<Floating
					id={listId}
					role="listbox"
					aria-labelledby={labelledBy}
					aria-label={aria["aria-label"]}
					side="bottom"
					align="stretch"
					gap={4}
					bodyClassName="p-1"
				>
					{options.map((o, i) => (
						// 焦点留在触发钮上（aria-activedescendant），选项不进 Tab 序列
						<button
							key={o.value}
							type="button"
							tabIndex={-1}
							id={`${listId}-${i}`}
							role="option"
							aria-selected={i === selected}
							aria-disabled={o.disabled}
							onMouseMove={() => setActive(i)}
							onMouseDown={(e) => e.preventDefault()}
							onClick={() => choose(i)}
							className={`flex h-[30px] w-full cursor-default items-center gap-2 rounded-[9px] px-2.5 text-left text-[13.5px] ${o.disabled ? "text-faint" : "text-fg"} ${i === active ? "bg-hover" : ""}`}
						>
							<span className="min-w-0 flex-1 truncate">{o.label}</span>
							{o.hint && <span className="text-muted text-xs">{o.hint}</span>}
							<Icon
								name="check"
								size={13}
								weight="bold"
								className={`shrink-0 text-fg ${i === selected ? "" : "invisible"}`}
							/>
						</button>
					))}
				</Floating>
			)}
		</div>
	);
}

/** 字段（L2）：标题 13/550 正文色，说明在控件下方（12、faint）。整块是 label，点标题即聚焦控件。 */
export function Field({
	label,
	hint,
	children,
}: {
	label: ReactNode;
	hint?: ReactNode;
	children: ReactNode;
}) {
	return (
		// biome-ignore lint/a11y/noLabelWithoutControl: 控件以 children 传入
		<label className="flex flex-col gap-[7px]">
			<span className="font-[550] text-[13px] text-fg">{label}</span>
			{children}
			{hint && <span className="text-faint text-xs">{hint}</span>}
		</label>
	);
}

/**
 * 开关（D）：原生 checkbox + `role=switch`；开态墨色轨道 + 反色圆钮，深色里整体反色。
 * `hideLabel`：行标题已在视觉上说明时，文案只给读屏。
 */
export function Toggle({
	checked,
	onChange,
	label,
	hideLabel = false,
	disabled,
}: {
	checked: boolean;
	onChange: (v: boolean) => void;
	label: ReactNode;
	hideLabel?: boolean;
	disabled?: boolean;
}) {
	return (
		<label className="inline-flex shrink-0 cursor-pointer items-center gap-2 text-sm has-disabled:cursor-default">
			<input
				type="checkbox"
				role="switch"
				aria-checked={checked}
				className="peer sr-only"
				checked={checked}
				disabled={disabled}
				onChange={(e) => onChange(e.target.checked)}
			/>
			<span
				aria-hidden
				className="relative h-5 w-9 shrink-0 rounded-full bg-border-strong transition-colors duration-150 after:absolute after:top-0.5 after:left-0.5 after:h-4 after:w-4 after:rounded-full after:bg-white after:shadow-xs after:transition-[translate,background-color] after:duration-150 peer-checked:bg-ink peer-checked:after:translate-x-4 peer-checked:after:bg-ink-fg peer-focus-visible:outline-2 peer-focus-visible:outline-[var(--ring)] peer-focus-visible:outline-offset-2 peer-disabled:opacity-50 motion-reduce:after:transition-none"
			/>
			<span className={hideLabel ? "sr-only" : ""}>{label}</span>
		</label>
	);
}

/** 合并内部 ref 与调用方传入的 ref。 */
function useMergedRef<T>(outer: Ref<T> | undefined): {
	inner: { current: T | null };
	set: (el: T | null) => void;
} {
	const inner = useRef<T | null>(null);
	const set = useCallback(
		(el: T | null) => {
			inner.current = el;
			if (typeof outer === "function") outer(el);
			else if (outer) outer.current = el;
		},
		[outer],
	);
	return { inner, set };
}

/** 复选框：原生 input（保留 checkbox 语义与 aria-label），外观为方形墨色勾；支持半选态。 */
export function Checkbox({
	indeterminate = false,
	className = "",
	ref,
	...props
}: ComponentProps<"input"> & { indeterminate?: boolean }) {
	const { inner, set } = useMergedRef<HTMLInputElement>(ref);
	// 绘制前同步 DOM 属性：避免先画出非半选态再跳变
	useLayoutEffect(() => {
		if (inner.current) inner.current.indeterminate = indeterminate;
	}, [indeterminate, inner]);
	return (
		<span className={`relative inline-flex h-4 w-4 shrink-0 ${className}`}>
			<input
				{...props}
				ref={set}
				type="checkbox"
				className="peer h-4 w-4 cursor-pointer appearance-none rounded-[5px] border border-border-strong bg-panel transition-colors duration-150 checked:border-ink checked:bg-ink indeterminate:border-ink indeterminate:bg-ink disabled:cursor-default disabled:opacity-50"
			/>
			<Icon
				name="check"
				size={10}
				weight="bold"
				className="pointer-events-none absolute inset-0 m-auto hidden text-ink-fg peer-checked:block peer-indeterminate:hidden"
			/>
			<span className="pointer-events-none absolute inset-0 m-auto hidden h-[1.5px] w-2 rounded-full bg-ink-fg peer-indeterminate:block" />
		</span>
	);
}

/** 单选：原生 input（保留 radio 语义），选中时墨色圆内嵌小圆点。 */
export function Radio({ className = "", ...props }: ComponentProps<"input">) {
	return (
		<span className={`relative inline-flex h-4 w-4 shrink-0 ${className}`}>
			<input
				{...props}
				type="radio"
				className="peer h-4 w-4 cursor-pointer appearance-none rounded-full border border-border-strong bg-panel transition-colors duration-150 checked:border-ink checked:bg-ink disabled:cursor-default disabled:opacity-50"
			/>
			<span className="pointer-events-none absolute inset-0 m-auto hidden h-1.5 w-1.5 rounded-full bg-ink-fg peer-checked:block" />
		</span>
	);
}

/**
 * 标签页按钮样式；不带滑块的场合选中再加 `bg-hover`。胶囊形：标签条大多贴着纸面 / 面板的大圆角（32px 左右、内缩 8–10px），
 * 同心的半径比 28px 高的按钮的一半还大，取胶囊才与外角同心（以前的 12px 圆角在角落里显得没处理）。
 * 分段选择器（`bg-inset p-0.5` 槽 + `bg-panel` 滑块）的槽也必须是 `rounded-full`：槽只比滑块高 4px，
 * 圆角小于槽高一半时滑块的胶囊头会和槽的角对不上。
 */
export function tabClass(selected: boolean): string {
	return `relative z-10 inline-flex h-7 shrink-0 items-center gap-1.5 rounded-full px-2.5 text-xs font-medium transition-colors duration-150 ${selected ? "text-fg" : "text-muted hover:text-fg"}`;
}

/** SVG 迷你曲线；values 为 0..max。横向拉伸铺满容器宽度（描边不随拉伸变粗），下方渐隐填充，颜色取 currentColor。 */
export function Sparkline({
	values,
	max,
	className = "",
	label,
}: {
	values: readonly number[];
	max?: number;
	className?: string;
	label: string;
}) {
	const fillId = useId();
	const w = 120;
	const h = 24;
	const top = max ?? Math.max(1, ...values);
	const points = values.map((v, i) => {
		const x = values.length > 1 ? (i / (values.length - 1)) * w : w;
		const y = h - (Math.min(v, top) / top) * (h - 2) - 1;
		return `${x.toFixed(1)},${y.toFixed(1)}`;
	});
	return (
		<svg
			viewBox={`0 0 ${w} ${h}`}
			preserveAspectRatio="none"
			className={`block h-6 w-full ${className}`}
			role="img"
			aria-label={label}
		>
			{points.length > 1 && (
				<>
					<defs>
						<linearGradient id={fillId} x1="0" y1="0" x2="0" y2="1">
							<stop offset="0" stopColor="currentColor" stopOpacity={0.28} />
							<stop offset="1" stopColor="currentColor" stopOpacity={0} />
						</linearGradient>
					</defs>
					<polyline
						points={`0,${h} ${points.join(" ")} ${w},${h}`}
						fill={`url(#${fillId})`}
						stroke="none"
					/>
					<polyline
						points={points.join(" ")}
						fill="none"
						stroke="currentColor"
						strokeWidth={1.6}
						strokeLinejoin="round"
						strokeLinecap="round"
						vectorEffect="non-scaling-stroke"
					/>
				</>
			)}
		</svg>
	);
}

function ThreadStateGlyph({
	state,
	size,
}: {
	state: ThreadState;
	size: number;
}): ReactNode {
	switch (state) {
		case "provisioning":
			return <OrbBead state="spawning" size={size} label="" />;
		case "awaiting_input":
			return (
				<Icon
					name="message"
					weight="fill"
					size={size}
					className="text-accent"
				/>
			);
		case "working":
			return (
				<span className="inline-flex text-muted">
					<Spinner size={size - 2} />
				</span>
			);
		case "error":
			return (
				<Icon name="alert" weight="fill" size={size} className="text-err" />
			);
		default:
			return <Icon name="check" size={size} className="text-ghost" />;
	}
}

export function ThreadStateIcon({
	state,
	size = 14,
}: {
	state: ThreadState;
	size?: number;
}) {
	const { t } = useTranslation();
	// 槽宽固定为 size：工作中的转圈比其它字形小 2px，状态切换时后面的标题不能跟着挪动
	return (
		<span
			className="inline-flex shrink-0 items-center justify-center"
			style={{ width: size }}
			title={t(`threadState.${state}`)}
		>
			<ThreadStateGlyph state={state} size={size} />
		</span>
	);
}

export function EmptyState({
	icon,
	title,
	children,
}: {
	icon: IconName;
	title: ReactNode;
	children?: ReactNode;
}) {
	return (
		<div className="flex flex-col items-center justify-center gap-2 px-6 py-12 text-center text-muted">
			<span className="grid h-11 w-11 place-items-center rounded-full bg-inset">
				<Icon name={icon} size={20} className="text-faint" />
			</span>
			<div className="font-medium text-fg text-sm">{title}</div>
			{children && <div className="max-w-sm text-sm">{children}</div>}
		</div>
	);
}

/** 有专门提示文案的错误码（REST `{code}` 与 omp 命令失败码）。 */
const FRIENDLY_CODES = [
	"rate_limited",
	"unauthorized",
	"forbidden",
	"managed_by_oryn",
	"use_oryn_fork",
	"queue_full",
	"orb_unavailable",
	"disconnected",
	"timeout",
	"invalid_frame",
	"orbd_bad_response",
	"orbd_timeout",
	"orbd_outdated",
	// 浏览器 Profile（M8）
	"profile_not_found",
	"profile_in_use",
	"profile_name_taken",
	"profile_limit",
	"no_profile",
	"profile_readonly",
	"profile_too_large",
	"profile_save_failed",
	// 定时与协调（M9）
	"schedule_busy",
	"thread_archived",
	"space_thread",
	"coordinator_restricted",
	"invalid_target",
	// runner（M11b）；worktree_failed / worktree_dirty 不在此列：它们的消息是 git 的原文，要原样显示
	"runner_offline",
	"runner_unsupported",
	"dir_not_served",
	"thread_required",
	// 中央 agent 配置（M12.2）
	"omfg_in_progress",
	// Boxes（M23）
	"box_not_found",
	"app_not_found",
	"release_not_found",
	"deploy_not_found",
	"box_exists",
	"box_limit",
	"box_stopped",
	"box_not_ready",
	"box_unsupported",
	"orb_is_box",
	"app_exists_elsewhere",
	"app_repo_mismatch",
	"app_not_deployed",
	"app_limit",
	"deploy_in_progress",
	"box_changes",
	"release_too_large",
	"app_unavailable",
	// Passkey 与密码登录管理（设置→账户）
	"reauth_required",
	"last_passkey",
	"last_login_method",
	"reauth_failed",
] as const;

type FriendlyCode = (typeof FRIENDLY_CODES)[number];

function friendlyCode(error: unknown): FriendlyCode | null {
	if (
		!(error instanceof Error) ||
		!("code" in error) ||
		typeof error.code !== "string"
	)
		return null;
	const code = error.code;
	return (FRIENDLY_CODES as readonly string[]).includes(code)
		? (code as FriendlyCode)
		: null;
}

/** 控制面拦下受管命令时的原文：`<命令> is managed by Oryn`（命令是 RPC 类型或斜杠命令，如 `/session delete`）。 */
const MANAGED_RE = /^(.+?) is managed by Oryn\b/;

/**
 * 错误的用户可读文案：已知错误码走 i18n（429 带上 Retry-After 秒数），其余用原始消息。
 * managed_by_oryn 点名被拦的命令并保留服务端原文（同一错误码覆盖 /move、/session delete、switch_session 等，笼统一句说不清）。
 */
export function describeError(error: unknown, t: TFunction): string {
	const code = friendlyCode(error);
	if (
		code === "rate_limited" &&
		error instanceof ApiError &&
		error.retryAfter
	) {
		return t("errors.rate_limited_after", { s: error.retryAfter });
	}
	if (code === "managed_by_oryn" && error instanceof Error) {
		const raw = error.message.trim();
		const name = MANAGED_RE.exec(raw)?.[1];
		if (name) return t("errors.managed_by_oryn_named", { name, raw });
		// 没有原文时 OmpCommandError 的 message 退成错误码本身，不重复显示
		if (raw && raw !== code) return t("errors.managed_by_oryn_raw", { raw });
	}
	if (code) return t(`errors.${code}`);
	return error instanceof Error ? error.message : String(error);
}

export function ErrorText({ error }: { error: unknown }) {
	const { t } = useTranslation();
	if (!error) return null;
	const msg = describeError(error, t);
	return (
		<p role="alert" className="flex items-start gap-1.5 text-err text-sm">
			<Icon name="alert" size={13} className="mt-[3px] shrink-0" />
			<span>{msg}</span>
		</p>
	);
}
