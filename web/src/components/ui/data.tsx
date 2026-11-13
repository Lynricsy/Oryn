/** 数据呈现元件：徽标、读数、量表、圆环、状态点、卡片、键值、列表行、提示条、头像、纸面、品牌标。 */
import { type CSSProperties, type ReactNode, useId } from "react";
import { Icon, type IconName } from "./icons";

export type Tone = "neutral" | "accent" | "ok" | "warn" | "err" | "frost";

const BADGE_TONE: Record<Tone, string> = {
	neutral: "bg-inset text-muted",
	accent: "bg-accent-soft text-accent",
	ok: "bg-ok/12 text-ok",
	warn: "bg-warn/15 text-warn",
	err: "bg-err/10 text-err",
	frost: "bg-frost/12 text-frost",
};

/** 小徽标：类型、计数、状态标签。 */
export function Badge({
	tone = "neutral",
	icon,
	children,
	title,
}: {
	tone?: Tone;
	icon?: IconName;
	children: ReactNode;
	title?: string;
}): ReactNode {
	return (
		<span
			title={title}
			className={`inline-flex h-[18px] max-w-full shrink-0 items-center gap-1 whitespace-nowrap rounded px-1.5 font-medium text-2xs ${BADGE_TONE[tone]}`}
		>
			{icon && <Icon name={icon} size={11} aria-hidden className="shrink-0" />}
			<span className="min-w-0 truncate">{children}</span>
		</span>
	);
}

/** 图标 + 数值的读数；标签只给读屏与悬停提示。 */
export function Stat({
	icon,
	label,
	children,
}: {
	icon: IconName;
	label: string;
	children: ReactNode;
}): ReactNode {
	return (
		<span title={label} className="inline-flex items-center gap-1 tabular-nums">
			<Icon name={icon} size={12} aria-hidden className="shrink-0" />
			<span className="sr-only">{label}</span>
			<span>{children}</span>
		</span>
	);
}

const FILL_TONE: Record<Tone, string> = {
	neutral: "bg-fg/70",
	accent: "bg-accent",
	ok: "bg-ok",
	warn: "bg-warn",
	err: "bg-err",
	frost: "bg-frost",
};

const TEXT_TONE: Record<Tone, string> = {
	neutral: "text-fg/70",
	accent: "text-accent",
	ok: "text-ok",
	warn: "text-warn",
	err: "text-err",
	frost: "text-frost",
};

/** 百分比超过 85 为错误色、超过 65 为警告色，否则中性。 */
export function thresholdTone(percent: number): Tone {
	if (percent > 85) return "err";
	if (percent > 65) return "warn";
	return "neutral";
}

function clampRatio(value: number, max: number): number {
	if (!Number.isFinite(value) || !(max > 0)) return 0;
	return Math.min(1, Math.max(0, value / max));
}

/** 水平量表。 */
export function Meter({
	value,
	max = 100,
	tone = "neutral",
	label,
	className = "",
}: {
	value: number;
	max?: number;
	tone?: Tone;
	label: string;
	className?: string;
}): ReactNode {
	const ratio = clampRatio(value, max);
	return (
		// biome-ignore lint/a11y/useSemanticElements: 原生 <meter> 无法跨浏览器统一外观
		<div
			role="meter"
			aria-label={label}
			aria-valuenow={Math.round(Number.isFinite(value) ? value : 0)}
			aria-valuemin={0}
			aria-valuemax={max}
			className={`h-1.5 overflow-hidden rounded-full bg-inset ${className}`}
		>
			<div
				className={`h-full rounded-full transition-[width] duration-300 ease-out ${FILL_TONE[tone]}`}
				style={{ width: `${ratio * 100}%` }}
			/>
		</div>
	);
}

/** 圆环进度（0..100），起点 12 点钟方向。 */
export function Ring({
	value,
	size = 20,
	tone = "neutral",
	label,
}: {
	value: number;
	size?: number;
	tone?: Tone;
	label: string;
}): ReactNode {
	const r = 7.5;
	const c = 2 * Math.PI * r;
	const ratio = clampRatio(value, 100);
	return (
		<svg
			role="img"
			aria-label={label}
			viewBox="0 0 20 20"
			width={size}
			height={size}
			className={`shrink-0 -rotate-90 ${TEXT_TONE[tone]}`}
		>
			<circle
				cx={10}
				cy={10}
				r={r}
				fill="none"
				stroke="var(--line-strong)"
				strokeWidth={2.5}
			/>
			<circle
				cx={10}
				cy={10}
				r={r}
				fill="none"
				stroke="currentColor"
				strokeWidth={2.5}
				strokeLinecap="round"
				strokeDasharray={`${c * ratio} ${c}`}
			/>
		</svg>
	);
}

/** 7px 状态点；pulse 时外圈扩散。 */
export function StatusDot({
	tone,
	pulse,
	label,
}: {
	tone: Tone;
	pulse?: boolean;
	label?: string;
}): ReactNode {
	const color = tone === "neutral" ? "bg-ghost" : FILL_TONE[tone];
	const dot = (
		<>
			{pulse && (
				<span
					className={`absolute inset-0 animate-ping rounded-full opacity-40 ${color}`}
				/>
			)}
			<span className={`relative h-[7px] w-[7px] rounded-full ${color}`} />
		</>
	);
	const cls = "relative inline-flex h-[7px] w-[7px] shrink-0";
	return label ? (
		<span role="img" aria-label={label} className={cls}>
			{dot}
		</span>
	) : (
		<span aria-hidden className={cls}>
			{dot}
		</span>
	);
}

/**
 * 纸面内的分组卡片。`collapsible` 时整条标题行可点（标题是 button，`::after` 铺满整行），不显示箭头；
 * 正文用 grid 行高 1fr ↔ 0fr 过渡收起，同时渐隐上移，`summary` 在标题行右侧淡入。收起后正文 `inert`。
 * `action` 叠在 `::after` 之上，独立可点。不传 `collapsible` 时与普通卡片完全一样。
 */
export function Card({
	title,
	icon,
	action,
	children,
	className = "",
	collapsible = false,
	collapsed = false,
	onToggle,
	summary,
}: {
	title?: ReactNode;
	icon?: IconName;
	action?: ReactNode;
	children: ReactNode;
	className?: string;
	collapsible?: boolean;
	collapsed?: boolean;
	onToggle?: () => void;
	/** 收起时显示在标题行右侧的关键信息 */
	summary?: ReactNode;
}): ReactNode {
	const bodyId = useId();
	const hasHead = !!(title || action);
	if (!collapsible) {
		return (
			<section
				className={`rounded-lg border border-border bg-panel ${className}`}
			>
				{hasHead && (
					<header className="flex h-9 items-center gap-2 px-3 font-medium text-muted text-xs">
						{icon && (
							<Icon name={icon} size={14} aria-hidden className="text-faint" />
						)}
						<h3 className="min-w-0 flex-1 truncate">{title}</h3>
						{action}
					</header>
				)}
				<div className={`px-3 pb-3 ${hasHead ? "" : "pt-3"}`}>{children}</div>
			</section>
		);
	}
	// overflow-hidden（裁圆角与收起动画）会让 flex 子项的自动最小高度变成 0：在纵向 flex 容器里不加 shrink-0
	// 就会被压扁裁掉内容，而不是让外层滚动
	return (
		<section
			className={`shrink-0 overflow-hidden rounded-lg border border-border bg-panel ${className}`}
		>
			<header className="relative flex h-9 items-center gap-2 px-3 font-medium text-muted text-xs transition-colors duration-150 hover:bg-hover has-[>h3>button:focus-visible]:outline-2 has-[>h3>button:focus-visible]:outline-[var(--ring)] has-[>h3>button:focus-visible]:-outline-offset-2">
				{icon && (
					<Icon name={icon} size={14} aria-hidden className="text-faint" />
				)}
				<h3 className="min-w-0 shrink truncate">
					<button
						type="button"
						aria-expanded={!collapsed}
						aria-controls={bodyId}
						onClick={onToggle}
						className="cursor-pointer outline-none after:absolute after:inset-0 after:content-['']"
					>
						{title}
					</button>
				</h3>
				{/* 摘要：收起时淡入，展开时淡出并退出无障碍树；点在摘要上同样落到整行的 ::after */}
				<span
					aria-hidden={!collapsed}
					className={`flex min-w-0 flex-1 items-center justify-end overflow-hidden whitespace-nowrap font-normal text-xs tabular-nums transition-[opacity,visibility] duration-[240ms] ease-[var(--ease)] ${collapsed ? "visible opacity-100" : "invisible opacity-0"}`}
				>
					{summary}
				</span>
				{action && (
					<span className="relative z-10 flex shrink-0">{action}</span>
				)}
			</header>
			<div
				id={bodyId}
				inert={collapsed}
				className={`grid transition-[grid-template-rows] duration-[240ms] ease-[var(--ease)] ${collapsed ? "grid-rows-[0fr]" : "grid-rows-[1fr]"}`}
			>
				<div className="min-h-0 overflow-hidden">
					<div
						className={`px-3 pb-3 transition-[opacity,translate] duration-[240ms] ease-[var(--ease)] ${collapsed ? "-translate-y-1 opacity-0" : "translate-y-0 opacity-100"}`}
					>
						{children}
					</div>
				</div>
			</div>
		</section>
	);
}

/** 键值表。 */
export function KeyValue({
	rows,
}: {
	rows: { key: string; label: ReactNode; value: ReactNode }[];
}): ReactNode {
	return (
		<dl className="grid grid-cols-[minmax(6rem,max-content)_1fr] gap-x-4 gap-y-1.5 text-sm">
			{rows.map((r) => (
				<div key={r.key} className="contents">
					<dt className="text-muted">{r.label}</dt>
					<dd className="m-0 min-w-0 break-words">{r.value}</dd>
				</div>
			))}
		</dl>
	);
}

/** 列表行：前置图标/状态 + 标题与元信息 + 尾部操作；有 onClick 时整行是按钮。 */
export function ListRow({
	leading,
	title,
	meta,
	trailing,
	onClick,
}: {
	leading?: ReactNode;
	title: ReactNode;
	meta?: ReactNode;
	trailing?: ReactNode;
	onClick?: () => void;
}): ReactNode {
	const body = (
		<>
			{leading && (
				<span className="inline-flex shrink-0 items-center">{leading}</span>
			)}
			<span className="flex min-w-0 flex-1 flex-col gap-0.5">
				<span className="flex min-w-0 items-center gap-1.5 text-sm">
					{title}
				</span>
				{meta && (
					<span className="flex flex-wrap items-center gap-1.5 text-faint text-xs">
						{meta}
					</span>
				)}
			</span>
			{trailing && (
				<span className="inline-flex shrink-0 items-center gap-1">
					{trailing}
				</span>
			)}
		</>
	);
	const cls = "flex min-h-10 items-center gap-3 rounded-md px-2 py-1.5";
	if (onClick)
		return (
			<button
				type="button"
				onClick={onClick}
				className={`${cls} w-full text-left transition-colors duration-150 hover:bg-hover`}
			>
				{body}
			</button>
		);
	return <div className={cls}>{body}</div>;
}

const CALLOUT_TONE: Record<Tone, string> = {
	neutral: "border-border bg-raised text-muted",
	accent: "border-accent/30 bg-accent-soft text-accent",
	ok: "border-ok/30 bg-ok/5 text-ok",
	warn: "border-warn/40 bg-warn/8 text-warn",
	err: "border-err/30 bg-err/5 text-err",
	frost: "border-frost/30 bg-frost/5 text-frost",
};

const CALLOUT_ICON: Record<Tone, IconName> = {
	neutral: "info",
	accent: "info",
	ok: "checkCircle",
	warn: "alert",
	err: "alert",
	frost: "info",
};

/** 提示条：说明、警告、错误。 */
export function Callout({
	tone,
	icon,
	title,
	children,
	actions,
	role,
}: {
	tone: Tone;
	icon?: IconName;
	title?: ReactNode;
	children?: ReactNode;
	actions?: ReactNode;
	role?: "alert" | "status";
}): ReactNode {
	return (
		<div
			role={role}
			className={`flex items-start gap-2 rounded-md border px-3 py-2 text-xs ${CALLOUT_TONE[tone]}`}
		>
			<Icon
				name={icon ?? CALLOUT_ICON[tone]}
				size={14}
				aria-hidden
				className="mt-px shrink-0"
			/>
			<div className="min-w-0 flex-1">
				{title && <div className="font-medium">{title}</div>}
				{children && (
					<div className={title ? "mt-0.5 opacity-90" : ""}>{children}</div>
				)}
			</div>
			{actions && (
				<div className="flex shrink-0 items-center gap-1">{actions}</div>
			)}
		</div>
	);
}

/** 名字的第一个字素（大写）；空名返回「?」。 */
export function firstGrapheme(name: string): string {
	const s = name.trim();
	if (!s) return "?";
	if (typeof Intl !== "undefined" && "Segmenter" in Intl) {
		const seg = new Intl.Segmenter(undefined, { granularity: "grapheme" });
		for (const { segment } of seg.segment(s)) return segment.toUpperCase();
	}
	return (s[0] ?? "?").toUpperCase();
}

/** 名字 → 色相（0..359），项目徽标、标题图标、头像共用。 */
export function nameHue(name: string): number {
	let sum = 0;
	for (const ch of name) sum += ch.codePointAt(0) ?? 0;
	return sum % 360;
}

function hueStyle(hue: number, size: number): CSSProperties {
	return { "--h": hue, width: size, height: size } as CSSProperties;
}

/** 名字首字的方形头像；色相缺省由名字算出。配色在 index.css 的 `.avatar`。 */
export function Avatar({
	name,
	hue,
	size = 16,
}: {
	name: string;
	hue?: number;
	size?: number;
}): ReactNode {
	return (
		<span
			aria-hidden
			className="avatar inline-flex shrink-0 items-center justify-center rounded-xs font-semibold text-[9px]"
			style={hueStyle(hue ?? nameHue(name), size)}
		>
			{firstGrapheme(name)}
		</span>
	);
}

/** 项目徽标：圆角方块，有图标字形（Nerd/emoji）用图标，否则用名字首字。 */
export function ProjectBadge({
	name,
	icon,
	size = 18,
}: {
	name: string;
	icon?: string | null;
	size?: number;
}): ReactNode {
	return (
		<span
			aria-hidden
			className={`avatar inline-flex shrink-0 items-center justify-center font-semibold leading-none ${size >= 28 ? "rounded-sm" : "rounded-xs"}`}
			style={{
				...hueStyle(nameHue(name), size),
				fontSize: Math.round(size * (icon ? 0.66 : 0.56)),
			}}
		>
			{icon || firstGrapheme(name)}
		</span>
	);
}

/** omp 标题卡图标：与项目徽标同形的着色方块；色相取标题文字，emoji 保持原色。 */
export function TitleGlyph({
	icon,
	seed,
	size = 16,
}: {
	icon: string;
	/** 计算色相用的文字（通常是标题） */
	seed: string;
	size?: number;
}): ReactNode {
	return (
		<span
			aria-hidden
			className="avatar inline-flex shrink-0 items-center justify-center rounded-xs leading-none"
			style={{
				...hueStyle(nameHue(seed), size),
				fontSize: Math.round(size * 0.68),
			}}
		>
			{icon}
		</span>
	);
}

/** 纸面：expanded 布局下带圆角描边阴影，否则全出血。 */
export function Sheet({
	inset,
	className = "",
	children,
}: {
	inset: boolean;
	className?: string;
	children: ReactNode;
}): ReactNode {
	return (
		<div className={`${inset ? "sheet" : "bg-panel"} ${className}`}>
			{children}
		</div>
	);
}

/**
 * 品牌标「O|」：左半个 orb + 一根插入符。`blink` 时插入符像文本光标一样硬切闪烁
 * （`.caret-blink`，见 index.css；减少动效时常亮）。
 */
export function BrandMark({
	size = 16,
	blink = false,
}: {
	size?: number;
	blink?: boolean;
}): ReactNode {
	return (
		<span aria-hidden className="inline-flex shrink-0 text-ink">
			<svg
				viewBox="0 0 32 32"
				width={size}
				height={size}
				fill="currentColor"
				aria-hidden
			>
				<path d="M20.1 4.52A12 12 0 1 0 20.1 27.48Z" />
				<path
					data-brand-caret=""
					className={blink ? "caret-blink" : undefined}
					d="M24.5 4H25.9A1.1 1.1 0 0 1 27 5.1V26.9A1.1 1.1 0 0 1 25.9 28H24.5A1.1 1.1 0 0 1 23.4 26.9V5.1A1.1 1.1 0 0 1 24.5 4Z"
				/>
			</svg>
		</span>
	);
}
