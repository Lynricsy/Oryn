/** 通用界面元件：按钮、快捷键胶囊、对话框、菜单、表单控件、迷你曲线、状态图标。 */

import type { TFunction } from "i18next";
import {
	type ButtonHTMLAttributes,
	type InputHTMLAttributes,
	type ReactNode,
	type SelectHTMLAttributes,
	type TextareaHTMLAttributes,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { ApiError, type ThreadState } from "../../api/client";
import { focusablesIn, isTopModal, pushModal, trapTab } from "../../lib/modal";
import { Icon, type IconName } from "./icons";

type Variant = "primary" | "secondary" | "ghost" | "danger";

const VARIANT: Record<Variant, string> = {
	primary: "bg-accent text-accent-fg hover:opacity-90",
	secondary: "border border-border bg-raised text-fg hover:bg-hover",
	ghost: "text-muted hover:bg-hover hover:text-fg",
	danger: "border border-err/40 text-err hover:bg-err/10",
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
		size === "sm" ? "h-7 px-2 text-xs gap-1" : "h-8 px-3 text-sm gap-1.5";
	return (
		<button
			type="button"
			className={`inline-flex select-none items-center justify-center rounded-md font-medium transition-colors disabled:pointer-events-none disabled:opacity-50 ${sz} ${VARIANT[variant]} ${className}`}
			{...rest}
		>
			{icon && <Icon name={icon} size={size === "sm" ? 13 : 15} />}
			{children}
		</button>
	);
}

export interface IconButtonProps
	extends ButtonHTMLAttributes<HTMLButtonElement> {
	icon: IconName;
	label: string;
	active?: boolean;
	size?: number;
}

export function IconButton({
	icon,
	label,
	active,
	size = 16,
	className = "",
	...rest
}: IconButtonProps) {
	return (
		<button
			type="button"
			aria-label={label}
			title={label}
			aria-pressed={active}
			className={`inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-muted transition-colors hover:bg-hover hover:text-fg disabled:opacity-40 ${active ? "bg-hover text-fg" : ""} ${className}`}
			{...rest}
		>
			<Icon name={icon} size={size} />
		</button>
	);
}

export function Kbd({ children }: { children: ReactNode }) {
	return (
		<kbd className="rounded border border-border bg-inset px-1 font-mono text-[10px] text-faint leading-4">
			{children}
		</kbd>
	);
}

export function Spinner({ size = 14 }: { size?: number }) {
	return (
		<span
			role="status"
			className="inline-block animate-spin rounded-full border-2 border-current border-t-transparent"
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
	useLayoutEffect(() => {
		const host = hostRef.current;
		const panel = panelRef.current;
		if (!open || !host || !panel) return;
		const previous =
			document.activeElement instanceof HTMLElement
				? document.activeElement
				: null;
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
				className="absolute inset-0 bg-black/40"
				onClick={onClose}
			/>
			<div
				ref={panelRef}
				role="dialog"
				aria-modal="true"
				tabIndex={-1}
				aria-label={typeof title === "string" ? title : undefined}
				className={`relative flex max-h-[85dvh] w-full flex-col rounded-t-xl border border-border bg-panel shadow-xl outline-none sm:rounded-xl ${wide ? "sm:max-w-3xl" : "sm:max-w-lg"} pb-safe`}
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
	danger?: boolean;
	disabled?: boolean;
	onSelect?: () => void;
	children?: MenuItem[];
	hint?: ReactNode;
}

/** 下拉菜单（支持一级子菜单）。 */
export function Menu({
	trigger,
	items,
	align = "right",
}: {
	trigger: (props: {
		onClick: () => void;
		"aria-expanded": boolean;
	}) => ReactNode;
	items: MenuItem[];
	align?: "left" | "right";
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
		<div ref={ref} className="relative">
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
					className={`absolute z-40 mt-1 min-w-52 max-w-72 rounded-lg border border-border bg-panel p-1 shadow-lg ${align === "right" ? "right-0" : "left-0"}`}
				>
					{sub && (
						<button
							type="button"
							className="flex w-full items-center gap-2 rounded px-2 py-1.5 text-faint text-xs hover:bg-hover"
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
							role="menuitem"
							disabled={item.disabled}
							className={`flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm hover:bg-hover disabled:opacity-40 ${item.danger ? "text-err" : "text-fg"}`}
							onClick={() => {
								if (item.children) {
									setSub(item.key);
									return;
								}
								setOpen(false);
								item.onSelect?.();
							}}
						>
							{item.icon && (
								<Icon
									name={item.icon}
									size={14}
									className="shrink-0 text-muted"
								/>
							)}
							<span className="min-w-0 flex-1 truncate">{item.label}</span>
							{item.hint && (
								<span className="text-faint text-xs">{item.hint}</span>
							)}
							{item.children && (
								<Icon name="chevronRight" size={13} className="text-faint" />
							)}
						</button>
					))}
				</div>
			)}
		</div>
	);
}

const INPUT =
	"w-full rounded-md border border-border bg-inset px-2.5 py-1.5 text-sm text-fg placeholder:text-faint focus:border-accent focus:outline-none";

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

export function Select(props: SelectHTMLAttributes<HTMLSelectElement>) {
	return <select {...props} className={`${INPUT} ${props.className ?? ""}`} />;
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
			<span className="relative h-4 w-7 rounded-full bg-border transition-colors after:absolute after:top-0.5 after:left-0.5 after:h-3 after:w-3 after:rounded-full after:bg-panel after:transition-transform peer-checked:bg-accent peer-checked:after:translate-x-3 peer-disabled:opacity-50" />
			<span>{label}</span>
		</label>
	);
}

/** SVG 迷你曲线；values 为 0..max。 */
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
	const w = 120;
	const h = 28;
	const top = max ?? Math.max(1, ...values);
	const points = values.map((v, i) => {
		const x = values.length > 1 ? (i / (values.length - 1)) * w : w;
		const y = h - (Math.min(v, top) / top) * (h - 2) - 1;
		return `${x.toFixed(1)},${y.toFixed(1)}`;
	});
	return (
		<svg
			viewBox={`0 0 ${w} ${h}`}
			className={`h-7 w-full ${className}`}
			role="img"
			aria-label={label}
		>
			{points.length > 1 && (
				<>
					<polyline
						points={`0,${h} ${points.join(" ")} ${w},${h}`}
						fill="currentColor"
						opacity={0.12}
						stroke="none"
					/>
					<polyline
						points={points.join(" ")}
						fill="none"
						stroke="currentColor"
						strokeWidth={1.5}
					/>
				</>
			)}
		</svg>
	);
}

const STATE_STYLE: Record<
	ThreadState,
	{ icon: IconName | null; className: string }
> = {
	provisioning: { icon: "box", className: "text-faint" },
	awaiting_input: { icon: "message", className: "text-warn" },
	working: { icon: null, className: "text-accent" },
	error: { icon: "alert", className: "text-err" },
	idle: { icon: "check", className: "text-faint" },
};

export function ThreadStateIcon({
	state,
	size = 14,
}: {
	state: ThreadState;
	size?: number;
}) {
	const { t } = useTranslation();
	const style = STATE_STYLE[state];
	return (
		<span
			className={`inline-flex shrink-0 items-center ${style.className}`}
			title={t(`threadState.${state}`)}
		>
			{style.icon ? (
				<Icon name={style.icon} size={size} />
			) : (
				<Spinner size={size - 2} />
			)}
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
			<Icon name={icon} size={28} className="text-faint" />
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
		<p role="alert" className="text-err text-sm">
			{msg}
		</p>
	);
}
