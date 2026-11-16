/**
 * 画布编辑器的图层面板（计划 M14 6.4）：宽 256，`role="tree"`。列出当前画布页的画框与标注（各一组，上层的在前）。
 * 单击选中并把镜头移过去，`Shift` 多选；双击画框行（或 `F2`）改名。键盘：上下移动、左右收起 / 展开分组、
 * `Enter` / `空格` 选中、`Home` / `End`。
 */
import {
	type KeyboardEvent,
	type MouseEvent,
	type ReactNode,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import {
	type CanvasDoc,
	type CanvasNote,
	NAME_CHARS,
} from "../../../lib/artifacts/canvas-doc";
import { Icon } from "../../ui/icons";
import { IconButton } from "../../ui/primitives";

export interface LayersPanelProps {
	doc: CanvasDoc;
	canvasKey: string;
	selected: readonly string[];
	/** 选中这些对象并把镜头移到 focus 上。 */
	onSelect(ids: string[], focus: string): void;
	/** 给画框改名；只读时不给（不能改名）。 */
	onRename?(pageId: string, name: string): void;
	onClose(): void;
	/** 窄窗口里覆盖在画布上。 */
	overlay: boolean;
}

type Group = "frames" | "notes";

interface Row {
	id: string;
	group: Group;
	label: string;
	detail?: string;
	locked: boolean;
	color?: string;
	isPage: boolean;
}

const EXCERPT = 40;

function excerpt(text: string): string {
	const line = text.trim().split("\n")[0] ?? "";
	const chars = [...line];
	return chars.length > EXCERPT ? `${chars.slice(0, EXCERPT).join("")}…` : line;
}

export function LayersPanel({
	doc,
	canvasKey,
	selected,
	onSelect,
	onRename,
	onClose,
	overlay,
}: LayersPanelProps): ReactNode {
	const { t } = useTranslation();
	const [collapsed, setCollapsed] = useState<Record<Group, boolean>>({
		frames: false,
		notes: false,
	});
	const [active, setActive] = useState<string | null>(null);
	const [renaming, setRenaming] = useState<string | null>(null);
	const tree = useRef<HTMLDivElement>(null);

	const noteLabel = (n: CanvasNote): string => {
		const kind =
			n.kind === "pen" && n.marker
				? t("artifacts.canvas.tools.marker")
				: t(`artifacts.canvas.tools.${n.kind}`);
		return (n.kind === "text" || n.kind === "sticky") && n.text.trim() !== ""
			? t("artifacts.canvas.layers.noteText", {
					kind,
					text: excerpt(n.text),
				})
			: kind;
	};

	const frames: Row[] = doc.pages
		.filter((p) => p.canvas === canvasKey)
		.toReversed()
		.map((p) => ({
			id: p.id,
			group: "frames",
			label: p.name,
			detail: `${p.width} × ${p.height}`,
			locked: p.locked,
			isPage: true,
		}));
	const notes: Row[] = doc.notes
		.filter((n) => n.canvas === canvasKey)
		.toReversed()
		.map((n) => ({
			id: n.id,
			group: "notes",
			label: noteLabel(n),
			locked: n.locked,
			color: `var(--note-${n.color})`,
			isPage: false,
		}));
	const groups: { key: Group; rows: Row[] }[] = [
		{ key: "frames", rows: frames },
		{ key: "notes", rows: notes },
	];
	// 键盘移动的顺序：分组行与展开分组里的行
	const visible = groups.flatMap((g) => [
		`group:${g.key}`,
		...(collapsed[g.key] ? [] : g.rows.map((r) => r.id)),
	]);
	const current = active && visible.includes(active) ? active : visible[0];
	const sel = new Set(selected);

	const focusRow = (key: string | undefined) => {
		if (!key) return;
		setActive(key);
		tree.current
			?.querySelector<HTMLElement>(`[data-key="${CSS.escape(key)}"]`)
			?.focus();
	};

	const pick = (id: string, additive: boolean) => {
		const ids = additive
			? sel.has(id)
				? selected.filter((x) => x !== id)
				: [...selected, id]
			: [id];
		setActive(id);
		onSelect(ids, id);
	};

	const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
		if (renaming || !current) return;
		const i = visible.indexOf(current);
		const group = current.startsWith("group:")
			? (current.slice(6) as Group)
			: null;
		switch (e.key) {
			case "ArrowDown":
				focusRow(visible[i + 1]);
				break;
			case "ArrowUp":
				focusRow(visible[i - 1]);
				break;
			case "Home":
				focusRow(visible[0]);
				break;
			case "End":
				focusRow(visible[visible.length - 1]);
				break;
			case "ArrowRight":
				if (group) setCollapsed((c) => ({ ...c, [group]: false }));
				break;
			case "ArrowLeft":
				if (group) setCollapsed((c) => ({ ...c, [group]: true }));
				else {
					const owner = frames.some((r) => r.id === current)
						? "frames"
						: "notes";
					focusRow(`group:${owner}`);
				}
				break;
			case "Enter":
			case " ":
				if (group) setCollapsed((c) => ({ ...c, [group]: !c[group] }));
				else pick(current, e.shiftKey);
				break;
			case "F2":
				if (onRename && frames.some((r) => r.id === current))
					setRenaming(current);
				break;
			default:
				return;
		}
		e.preventDefault();
	};

	const empty = frames.length === 0 && notes.length === 0;

	return (
		<aside
			data-testid="canvas-layers"
			aria-label={t("artifacts.canvas.layers.title")}
			className={`flex w-64 shrink-0 flex-col bg-panel ${overlay ? "absolute top-2 bottom-16 left-2 z-20 rounded-lg border border-border shadow-lg" : "border-border border-r"}`}
		>
			<div className="flex h-10 shrink-0 items-center gap-2 border-border border-b pr-1.5 pl-3">
				<h2 className="min-w-0 flex-1 truncate font-medium text-sm">
					{t("artifacts.canvas.layers.title")}
				</h2>
				<IconButton
					icon="close"
					box="sm"
					size={14}
					label={t("artifacts.canvas.layers.close")}
					onClick={onClose}
				/>
			</div>
			{empty ? (
				<p className="px-3 py-4 text-faint text-sm">
					{t("artifacts.canvas.layers.empty")}
				</p>
			) : (
				<div
					ref={tree}
					role="tree"
					aria-label={t("artifacts.canvas.layers.title")}
					aria-multiselectable="true"
					className="min-h-0 flex-1 overflow-y-auto p-1"
				>
					{groups.map((g) => {
						const key = `group:${g.key}`;
						const open = !collapsed[g.key];
						return (
							<div key={g.key} role="none">
								<div
									role="treeitem"
									aria-level={1}
									aria-expanded={open}
									aria-selected={false}
									aria-label={t(`artifacts.canvas.layers.${g.key}`)}
									data-key={key}
									tabIndex={current === key ? 0 : -1}
									onFocus={() => setActive(key)}
									onKeyDown={onKeyDown}
									onClick={() =>
										setCollapsed((c) => ({ ...c, [g.key]: !c[g.key] }))
									}
									className="flex h-7 w-full cursor-default select-none items-center gap-1 rounded-md px-1.5 font-medium text-faint text-xs outline-offset-[-2px] hover:bg-hover"
								>
									<Icon
										name={open ? "chevronDown" : "chevronRight"}
										size={12}
									/>
									<span className="flex-1">
										{t(`artifacts.canvas.layers.${g.key}`)}
									</span>
									<span className="tabular-nums">{g.rows.length}</span>
								</div>
								{open && (
									// biome-ignore lint/a11y/useSemanticElements: 树的子项分组（ARIA tree 的 group），不是表单字段集
									<div role="group">
										{g.rows.map((r) =>
											renaming === r.id && onRename ? (
												<RenameRow
													key={r.id}
													name={r.label}
													onDone={(name) => {
														setRenaming(null);
														if (name !== null) onRename(r.id, name);
														focusRow(r.id);
													}}
												/>
											) : (
												<LayerRow
													key={r.id}
													row={r}
													selected={sel.has(r.id)}
													focusable={current === r.id}
													onFocus={() => setActive(r.id)}
													onKeyDown={onKeyDown}
													onClick={(e) => pick(r.id, e.shiftKey)}
													onDoubleClick={
														r.isPage && onRename
															? () => setRenaming(r.id)
															: undefined
													}
												/>
											),
										)}
									</div>
								)}
							</div>
						);
					})}
				</div>
			)}
		</aside>
	);
}

function LayerRow({
	row,
	selected,
	focusable,
	onFocus,
	onKeyDown,
	onClick,
	onDoubleClick,
}: {
	row: Row;
	selected: boolean;
	focusable: boolean;
	onFocus(): void;
	onKeyDown(e: KeyboardEvent<HTMLDivElement>): void;
	onClick(e: MouseEvent): void;
	onDoubleClick?(): void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div
			role="treeitem"
			aria-level={2}
			aria-selected={selected}
			data-key={row.id}
			data-layer={row.id}
			tabIndex={focusable ? 0 : -1}
			onFocus={onFocus}
			onKeyDown={onKeyDown}
			onClick={onClick}
			onDoubleClick={onDoubleClick}
			className={`flex h-7 cursor-default select-none items-center gap-2 rounded-md pr-1.5 pl-5 text-sm outline-offset-[-2px] ${selected ? "bg-active text-fg" : "text-muted hover:bg-hover hover:text-fg"}`}
		>
			{row.isPage ? (
				<Icon name="frameCorners" size={13} className="shrink-0 text-faint" />
			) : (
				<span
					aria-hidden
					className="h-2.5 w-2.5 shrink-0 rounded-full"
					style={{ background: row.color }}
				/>
			)}
			<span className="min-w-0 flex-1 truncate">{row.label}</span>
			{row.detail && (
				<span className="shrink-0 text-faint text-xs tabular-nums">
					{row.detail}
				</span>
			)}
			{row.locked && (
				<Icon
					name="lock"
					size={12}
					className="shrink-0 text-faint"
					aria-label={t("artifacts.canvas.layers.locked")}
				/>
			)}
		</div>
	);
}

/** 就地改名：`Enter` 或失焦提交，`Esc` 取消（回 null）。 */
function RenameRow({
	name,
	onDone,
}: {
	name: string;
	onDone(name: string | null): void;
}): ReactNode {
	const { t } = useTranslation();
	const done = useRef(false);
	const finish = (value: string | null) => {
		if (done.current) return;
		done.current = true;
		const next = value?.trim() ?? "";
		onDone(value === null || next === "" || next === name ? null : next);
	};
	return (
		<div role="none" className="py-0.5 pl-5">
			<input
				// biome-ignore lint/a11y/noAutofocus: 双击就是为了改名
				autoFocus
				aria-label={t("artifacts.canvas.layers.rename")}
				defaultValue={name}
				maxLength={NAME_CHARS}
				onFocus={(e) => e.currentTarget.select()}
				onBlur={(e) => finish(e.currentTarget.value)}
				onKeyDown={(e) => {
					e.stopPropagation();
					if (e.key === "Enter") finish(e.currentTarget.value);
					else if (e.key === "Escape") finish(null);
				}}
				className="h-6 w-full rounded border border-accent bg-inset px-1.5 text-sm text-fg outline-none"
			/>
		</div>
	);
}
