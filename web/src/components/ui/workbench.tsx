/**
 * 工作台原生（D）控件：分组列表与行、开关、L2 表单字段、输入框、自绘下拉。
 *
 * 试点：目前只有「设置 › 通知」与它的弹窗在用；确认真实效果后替换 data.tsx 的 Card / ListRow 与
 * primitives.tsx 的 Toggle / Field / TextInput / TextArea / Select，届时删除旧件，不长期并存。
 *
 * - 分组：标题在外（12.5/600 次要灰），行在内（16px 圆角面板，发丝线从文字列起，见 index.css `.wb-list`）。
 * - 行：28px 中性图标方块 + 标题 / 说明 + 行尾；`Reveal` 包住的动作平时不占位，悬停或 Tab 进这一行时出现，
 *   `StatusCell` 是定宽状态列，所以出现的动作与状态文字长短无关，后面的 ⋯ 和开关也不动。
 * - 开关 / 勾选开态用墨色（深色里整体反色：白底黑钮）。
 * - 输入框：高 34、圆角 11；聚焦「提亮浮起」——底色变实、外缘加深、上浮 1px，不加主题色（令牌 `--field*`、`--sh-field*`）。
 * - 字段标题（L2）：标题 13/550，说明并到标题行右侧，只标「可选」；说明与「可选」经 aria-describedby 读给读屏。
 * - 下拉：粗指针（触屏）用原生 select；其余是 combobox + listbox，焦点留在触发钮上（aria-activedescendant）。
 */
import {
	type InputHTMLAttributes,
	type KeyboardEvent,
	type ReactNode,
	type TextareaHTMLAttributes,
	useEffect,
	useId,
	useRef,
	useState,
} from "react";
import { useCoarsePointer } from "../../lib/viewport";
import { StatusDot, type Tone } from "./data";
import { Floating } from "./Floating";
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
		<ul className="wb-list overflow-hidden rounded-[16px] bg-panel shadow-xs ring-1 ring-border">
			{children}
		</ul>
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

/** 列表行；`children` 渲染在行下方、与文字列对齐（就地结果、错误）。 */
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
			<div className="flex min-h-14 items-center gap-3 py-2 pr-3 pl-3.5">
				{icon && <Tile icon={icon} />}
				<div className="flex min-w-0 flex-1 flex-col gap-0.5">
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
					<div className="flex shrink-0 items-center gap-2">{trailing}</div>
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

// ---------------------------------------------------------------- 开关

/** 开关：原生 checkbox（role=switch）；开态墨色轨道 + 反色圆钮，深色里自动反色。 */
export function Switch({
	checked,
	onChange,
	label,
	disabled,
}: {
	checked: boolean;
	onChange: (v: boolean) => void;
	/** 读屏名称（行标题已在视觉上说明） */
	label: string;
	disabled?: boolean;
}): ReactNode {
	return (
		<label className="relative inline-flex shrink-0 cursor-pointer has-disabled:cursor-default">
			<input
				type="checkbox"
				role="switch"
				aria-checked={checked}
				aria-label={label}
				className="peer sr-only"
				checked={checked}
				disabled={disabled}
				onChange={(e) => onChange(e.target.checked)}
			/>
			<span
				aria-hidden
				className="relative h-5 w-9 rounded-full bg-border-strong transition-colors duration-150 after:absolute after:top-0.5 after:left-0.5 after:h-4 after:w-4 after:rounded-full after:bg-white after:shadow-xs after:transition-[translate,background-color] after:duration-150 peer-checked:bg-ink peer-checked:after:translate-x-4 peer-checked:after:bg-ink-fg peer-focus-visible:outline-2 peer-focus-visible:outline-[var(--ring)] peer-focus-visible:outline-offset-2 peer-disabled:opacity-50 motion-reduce:after:transition-none"
			/>
		</label>
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

/** 输入框外观（单行 / 多行 / 下拉触发钮共用）。 */
const FIELD =
	"w-full rounded-[11px] bg-[var(--field)] px-3 text-[13.5px] text-fg shadow-[var(--sh-field)] outline-none transition-[background-color,box-shadow,translate] duration-150 placeholder:text-faint focus:-translate-y-px focus:bg-[var(--field-focus)] focus:shadow-[var(--sh-field-focus)] motion-reduce:focus:translate-y-0 forced-colors:focus-visible:outline-2 forced-colors:focus-visible:outline-offset-1 forced-colors:focus-visible:outline-[Highlight]";

/** 单行输入。 */
export function TextField({
	className = "",
	...props
}: InputHTMLAttributes<HTMLInputElement>): ReactNode {
	return (
		<input
			{...props}
			className={`h-[34px] caret-[var(--brand)] ${FIELD} ${className}`}
		/>
	);
}

/** 多行输入；默认等宽字体（配置类内容）。 */
export function TextAreaField({
	className = "",
	...props
}: TextareaHTMLAttributes<HTMLTextAreaElement>): ReactNode {
	return (
		<textarea
			{...props}
			className={`py-2 font-mono caret-[var(--brand)] ${FIELD} ${className}`}
		/>
	);
}

export interface ComboOption<T extends string> {
	value: T;
	label: string;
	/** 选项右侧的次要说明（如英文名的中文） */
	hint?: string;
}

/**
 * 下拉选择。鼠标 / 键盘：combobox 触发钮 + 毛玻璃 listbox（`Floating`），焦点始终留在触发钮上；
 * 关闭时 ↓ ↑ Enter Space 打开，打开时 ↑ ↓ Home End 移动、首字母跳转、Enter / Space 选中，
 * Esc 关闭不改值（不冒泡到外层对话框），Tab 关闭并照常前进，点外面关闭。触屏用原生 select。
 */
export function ComboSelect<T extends string>({
	value,
	options,
	onChange,
	...control
}: FieldControlProps & {
	value: T;
	options: readonly ComboOption<T>[];
	onChange: (v: T) => void;
}): ReactNode {
	const coarse = useCoarsePointer();
	const [open, setOpen] = useState(false);
	const [active, setActive] = useState(0);
	const wrapRef = useRef<HTMLDivElement>(null);
	const listId = useId();
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
			<span className="relative flex">
				<select
					{...control}
					value={value}
					onChange={(e) => onChange(e.target.value as T)}
					className={`h-[34px] appearance-none pr-9 ${FIELD}`}
				>
					{options.map((o) => (
						<option key={o.value} value={o.value}>
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
		if (o) onChange(o.value);
		setOpen(false);
	};
	const move = (i: number) => setActive((i + options.length) % options.length);

	const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
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

	return (
		<div ref={wrapRef} className="relative">
			<button
				type="button"
				role="combobox"
				aria-haspopup="listbox"
				aria-expanded={open}
				aria-controls={listId}
				aria-activedescendant={open ? `${listId}-${active}` : undefined}
				{...control}
				aria-labelledby={`${control["aria-labelledby"]} ${control.id}`}
				onClick={() => (open ? setOpen(false) : show())}
				onKeyDown={onKeyDown}
				className={`flex h-[34px] cursor-pointer items-center justify-between gap-2 text-left aria-expanded:-translate-y-px aria-expanded:bg-[var(--field-focus)] aria-expanded:shadow-[var(--sh-field-focus)] ${FIELD}`}
			>
				<span className="min-w-0 truncate">{options[selected]?.label}</span>
				<Icon name="caretUpDown" size={13} className="shrink-0 text-faint" />
			</button>
			{open && (
				<Floating
					id={listId}
					role="listbox"
					aria-labelledby={control["aria-labelledby"]}
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
							onMouseMove={() => setActive(i)}
							onMouseDown={(e) => e.preventDefault()}
							onClick={() => choose(i)}
							className={`flex h-[30px] w-full cursor-default items-center gap-2 rounded-[9px] px-2.5 text-left text-[13.5px] text-fg ${i === active ? "bg-hover" : ""}`}
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
