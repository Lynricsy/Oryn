/** 通用界面元件：按钮、快捷键胶囊、对话框、菜单、表单控件（含复选框/单选）、迷你曲线、状态图标、标签页样式。 */

import type { TFunction } from "i18next";
import {
	type ButtonHTMLAttributes,
	type ComponentProps,
	type InputHTMLAttributes,
	type ReactNode,
	type Ref,
	type SelectHTMLAttributes,
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
			className={`inline-flex select-none items-center justify-center rounded-md font-medium transition-[background-color,box-shadow,scale,color] duration-150 ease-out active:scale-[0.98] disabled:pointer-events-none ${sz} ${VARIANT[variant]} ${DISABLED[variant]} ${className}`}
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
			className={`inline-flex ${BOX[box]} shrink-0 items-center justify-center rounded-md text-muted transition-[background-color,color,scale] duration-150 ease-out hover:bg-hover hover:text-fg active:scale-95 disabled:opacity-40 ${active ? "bg-active text-fg" : ""} ${className}`}
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
 * 模态对话框：Esc / 点遮罩关闭；compact 屏上为底部抽屉。
 * 渲染到 body 下；打开时把焦点移入对话框、Tab 在其中循环、背景 `inert`，关闭后焦点回到打开前的元素。
 */
export function Dialog({
	open,
	onClose,
	title,
	children,
	footer,
	wide,
}: {
	open: boolean;
	onClose: () => void;
	title: ReactNode;
	children: ReactNode;
	footer?: ReactNode;
	wide?: boolean;
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
			if (e.key === "Escape") onCloseRef.current();
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
			<button
				type="button"
				tabIndex={-1}
				aria-label={t("common.close")}
				className="absolute inset-0 bg-scrim backdrop-blur-[2px]"
				onClick={onClose}
			/>
			<div
				ref={panelRef}
				role="dialog"
				aria-modal="true"
				tabIndex={-1}
				aria-label={typeof title === "string" ? title : undefined}
				className={`pop-in relative flex max-h-[85dvh] w-full flex-col rounded-t-xl border border-border bg-panel shadow-xl outline-none sm:rounded-xl ${wide ? "sm:max-w-3xl" : "sm:max-w-lg"} pb-safe`}
			>
				<div className="flex items-center justify-between gap-2 border-border border-b px-4 py-3">
					<h2 className="truncate font-semibold text-sm">{title}</h2>
					<IconButton
						icon="close"
						label={t("common.close")}
						onClick={onClose}
					/>
				</div>
				<div ref={bodyRef} className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
					{children}
				</div>
				{footer && (
					<div className="flex flex-wrap justify-end gap-2 border-border border-t px-4 py-3">
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
			if (e.key === "Escape") setOpen(false);
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
			})}
			{open && (
				<div
					role="menu"
					className={`glass pop-in absolute z-40 min-w-52 max-w-72 rounded-lg p-1 ${side === "top" ? "bottom-full mb-1" : "mt-1"} ${align === "right" ? "right-0" : "left-0"}`}
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
							{...(item.checked === undefined
								? { role: "menuitem" }
								: { role: "menuitemradio", "aria-checked": item.checked })}
							disabled={item.disabled}
							className={`flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-hover disabled:opacity-40 ${item.danger ? "text-err" : "text-fg"} ${item.checked ? "font-medium" : ""}`}
							onClick={() => {
								if (item.children) {
									setSub(item.key);
									return;
								}
								setOpen(false);
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
							{item.hint && (
								<span className="text-faint text-xs">{item.hint}</span>
							)}
							{item.children && (
								<Icon name="chevronRight" size={13} className="text-faint" />
							)}
							{item.checked && (
								<Icon
									name="check"
									size={13}
									weight="bold"
									className="shrink-0 text-accent-ink"
								/>
							)}
						</button>
					))}
					{footer && !sub && (
						<div className="mt-1 border-border border-t pt-1">{footer}</div>
					)}
				</div>
			)}
		</div>
	);
}

/** 输入框聚焦：强调色描边 + 一圈淡光晕，替代全局的 2px 外描边（两者叠加会成双层环）。 */
const INPUT =
	"w-full rounded-md border border-border bg-inset px-2.5 py-1.5 text-sm text-fg placeholder:text-faint transition-[border-color,box-shadow] duration-150 focus-visible:border-accent focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-accent/20";

export function TextInput(props: InputHTMLAttributes<HTMLInputElement>) {
	return <input {...props} className={`${INPUT} ${props.className ?? ""}`} />;
}

export function TextArea(props: TextareaHTMLAttributes<HTMLTextAreaElement>) {
	return (
		<textarea
			{...props}
			className={`${INPUT} font-mono ${props.className ?? ""}`}
		/>
	);
}

/** 原生下拉（保留 combobox 语义），外观统一并加右侧箭头；`wrapperClassName` 控制外包宽度。 */
export function Select({
	wrapperClassName = "w-full",
	...props
}: SelectHTMLAttributes<HTMLSelectElement> & { wrapperClassName?: string }) {
	return (
		<span className={`relative inline-flex min-w-0 ${wrapperClassName}`}>
			<select
				{...props}
				className={`${INPUT} w-full appearance-none pr-8 ${props.className ?? ""}`}
			/>
			<Icon
				name="chevronDown"
				size={12}
				className="pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-faint"
			/>
		</span>
	);
}

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
		<label className="flex flex-col gap-1 text-sm">
			<span className="font-medium text-muted text-xs">{label}</span>
			{children}
			{hint && <span className="text-faint text-xs">{hint}</span>}
		</label>
	);
}

export function Toggle({
	checked,
	onChange,
	label,
	disabled,
}: {
	checked: boolean;
	onChange: (v: boolean) => void;
	label: ReactNode;
	disabled?: boolean;
}) {
	return (
		<label className="inline-flex cursor-pointer items-center gap-2 text-sm">
			<input
				type="checkbox"
				className="peer sr-only"
				checked={checked}
				disabled={disabled}
				onChange={(e) => onChange(e.target.checked)}
			/>
			<span className="relative h-[18px] w-8 shrink-0 rounded-full bg-border-strong transition-colors duration-150 after:absolute after:top-0.5 after:left-0.5 after:h-3.5 after:w-3.5 after:rounded-full after:bg-panel after:shadow-xs after:transition-transform after:duration-150 peer-checked:bg-accent peer-checked:after:translate-x-3.5 peer-focus-visible:outline-2 peer-focus-visible:outline-[var(--ring)] peer-focus-visible:outline-offset-2 peer-disabled:opacity-50" />
			<span>{label}</span>
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
				className="peer h-4 w-4 cursor-pointer appearance-none rounded border border-border-strong bg-panel transition-colors duration-150 checked:border-ink checked:bg-ink indeterminate:border-ink indeterminate:bg-ink disabled:cursor-default disabled:opacity-50"
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

/** 标签页按钮样式；不带滑块的场合选中再加 `bg-hover`。 */
export function tabClass(selected: boolean): string {
	return `relative z-10 inline-flex h-7 shrink-0 items-center gap-1.5 rounded-md px-2.5 text-xs font-medium transition-colors duration-150 ${selected ? "text-fg" : "text-muted hover:text-fg"}`;
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
	return (
		<span
			className="inline-flex shrink-0 items-center"
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

/** 错误的用户可读文案：已知错误码走 i18n（429 带上 Retry-After 秒数），其余用原始消息。 */
export function describeError(error: unknown, t: TFunction): string {
	const code = friendlyCode(error);
	if (
		code === "rate_limited" &&
		error instanceof ApiError &&
		error.retryAfter
	) {
		return t("errors.rate_limited_after", { s: error.retryAfter });
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
