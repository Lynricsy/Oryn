/**
 * 画布编辑器的工具条（计划 M14 6.4）：底部居中的浮动 dock（`role="toolbar"`）。
 *
 * 从左到右：「页面」下拉（各画布页与画框数；新建、改名、复制、删除——最后一页不能删）、八个工具、图层开关、
 * 缩放控件（`−`、百分比菜单、`+`）。画笔与形状工具选中时，dock 上方多一条浮层：子选项（画笔 / 荧光笔、四种形状）、
 * 三档粗细与七种颜色。只读时只剩移动、抓手、评论、页面切换与缩放。
 */
import { type FormEvent, type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type CanvasDoc,
	type CanvasOp,
	NAME_CHARS,
	NOTE_COLORS,
	type NoteColor,
	type ShapeKind,
} from "../../../lib/artifacts/canvas-doc";
import { showToast } from "../../../state/toasts";
import { Icon, type IconName } from "../../ui/icons";
import {
	Button,
	Dialog,
	IconButton,
	Menu,
	type MenuItem,
	TextInput,
} from "../../ui/primitives";
import {
	addSheetOps,
	canDeleteSheet,
	copySheetOps,
	deleteSheetOps,
	renameSheetOps,
} from "./arrange";
import { TOOL_KEYS, type Tool, type ZoomTarget } from "./keys";

export type PenKind = "pen" | "marker";
/** 画笔、荧光笔、形状各记一套颜色与粗细。 */
export type DrawFamily = PenKind | "shape";
export interface DrawStyle {
	color: NoteColor;
	width: number;
}
export type DrawStyles = Record<DrawFamily, DrawStyle>;

/** 画笔、荧光笔与形状的三档粗细。 */
export const STROKE_TIERS = [2, 3, 6] as const;

export const DEFAULT_STYLES: DrawStyles = {
	pen: { color: "ink", width: 3 },
	marker: { color: "yellow", width: 6 },
	shape: { color: "ink", width: 3 },
};

export const SHAPES: readonly ShapeKind[] = [
	"rect",
	"ellipse",
	"line",
	"arrow",
];

const TOOL_ICON: Record<Exclude<Tool, "pen" | "shape">, IconName> = {
	move: "cursor",
	hand: "hand",
	frame: "frameCorners",
	text: "textT",
	sticky: "note",
	comment: "message",
};
export const SHAPE_ICON: Record<ShapeKind, IconName> = {
	rect: "rectangle",
	ellipse: "circle",
	line: "lineSegment",
	arrow: "arrowUpRight",
};
const PEN_ICON: Record<PenKind, IconName> = {
	pen: "penNib",
	marker: "highlighter",
};

/** 七种颜色的色块（单选组）。颜色取 `.canvas-editor` 上的 `--note-<颜色>`。 */
export function ColorChoices({
	value,
	onChange,
	disabled,
}: {
	value: NoteColor;
	onChange(color: NoteColor): void;
	disabled?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div
			role="radiogroup"
			aria-label={t("artifacts.canvas.style.color")}
			className="flex items-center gap-0.5"
		>
			{NOTE_COLORS.map((c) => (
				<Choice
					key={c}
					checked={c === value}
					label={t(`artifacts.canvas.style.colors.${c}`)}
					disabled={disabled}
					onSelect={() => onChange(c)}
				>
					<span
						className={`h-4 w-4 rounded-full ${c === value ? "outline-2 outline-[var(--ring)] outline-offset-1" : ""}`}
						style={{ background: `var(--note-${c})` }}
					/>
				</Choice>
			))}
		</div>
	);
}

/** 三档粗细（单选组）。 */
export function WidthChoices({
	value,
	onChange,
	disabled,
}: {
	value: number;
	onChange(width: number): void;
	disabled?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div
			role="radiogroup"
			aria-label={t("artifacts.canvas.style.width")}
			className="flex items-center gap-0.5"
		>
			{STROKE_TIERS.map((w) => (
				<Choice
					key={w}
					checked={w === value}
					label={t("artifacts.canvas.style.widthPx", { n: w })}
					disabled={disabled}
					onSelect={() => onChange(w)}
				>
					<span
						className="rounded-full bg-current"
						style={{ width: 14, height: w }}
					/>
				</Choice>
			))}
		</div>
	);
}

/** 单选组里的一项：方形分段按钮（原生 radio 画不出色块与图标，语义由 role / aria-checked 提供）。 */
function Choice({
	checked,
	label,
	disabled,
	onSelect,
	children,
}: {
	checked: boolean;
	label: string;
	disabled?: boolean;
	onSelect(): void;
	children: ReactNode;
}): ReactNode {
	return (
		// biome-ignore lint/a11y/useSemanticElements: 色块与图标的分段选择，原生 radio 画不出来
		<button
			type="button"
			role="radio"
			aria-checked={checked}
			aria-label={label}
			title={label}
			disabled={disabled}
			onClick={onSelect}
			className={`inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-muted hover:bg-hover hover:text-fg disabled:opacity-40 ${checked ? "bg-active text-fg" : ""}`}
		>
			{children}
		</button>
	);
}

export interface ToolbarProps {
	doc: CanvasDoc;
	canvasKey: string;
	readOnly: boolean;
	commentable: boolean;
	tool: Tool;
	penKind: PenKind;
	shape: ShapeKind;
	styles: DrawStyles;
	onTool(tool: Tool): void;
	onPenKind(kind: PenKind): void;
	onShape(shape: ShapeKind): void;
	onStyle(family: DrawFamily, patch: Partial<DrawStyle>): void;
	/** 切换画布页。 */
	onCanvas(id: string): void;
	/** 提交操作（画布页的增删改）；只读时不调。 */
	commit(ops: CanvasOp[]): boolean;
	zoom: number;
	hasSelection: boolean;
	onZoom(to: ZoomTarget): void;
	/** 图层开关；不给时没有这个按钮（右面板里）。 */
	layers?: { open: boolean; onToggle(): void };
}

export function Toolbar({
	doc,
	canvasKey,
	readOnly,
	commentable,
	tool,
	penKind,
	shape,
	styles,
	onTool,
	onPenKind,
	onShape,
	onStyle,
	onCanvas,
	commit,
	zoom,
	hasSelection,
	onZoom,
	layers,
}: ToolbarProps): ReactNode {
	const { t } = useTranslation();
	const editable = !readOnly;
	const tools: { tool: Tool; icon: IconName; label: string }[] = [
		{
			tool: "move",
			icon: TOOL_ICON.move,
			label: t("artifacts.canvas.tools.move"),
		},
		{
			tool: "hand",
			icon: TOOL_ICON.hand,
			label: t("artifacts.canvas.tools.hand"),
		},
		...(editable
			? [
					{
						tool: "frame" as const,
						icon: TOOL_ICON.frame,
						label: t("artifacts.canvas.tools.frame"),
					},
					{
						tool: "text" as const,
						icon: TOOL_ICON.text,
						label: t("artifacts.canvas.tools.text"),
					},
					{
						tool: "sticky" as const,
						icon: TOOL_ICON.sticky,
						label: t("artifacts.canvas.tools.sticky"),
					},
					{
						tool: "pen" as const,
						icon: PEN_ICON[penKind],
						label: t(`artifacts.canvas.tools.${penKind}`),
					},
					{
						tool: "shape" as const,
						icon: SHAPE_ICON[shape],
						label: t(`artifacts.canvas.tools.${shape}`),
					},
				]
			: []),
		...(commentable
			? [
					{
						tool: "comment" as const,
						icon: TOOL_ICON.comment,
						label: t("artifacts.canvas.tools.comment"),
					},
				]
			: []),
	];
	const family: DrawFamily | null =
		!editable || (tool !== "pen" && tool !== "shape")
			? null
			: tool === "pen"
				? penKind
				: "shape";
	const zoomItems: MenuItem[] = [
		{
			key: "fit",
			label: t("artifacts.canvas.zoomFit"),
			hint: "⇧1",
			onSelect: () => onZoom("fit"),
		},
		{
			key: "selection",
			label: t("artifacts.canvas.zoomSelection"),
			hint: "⇧2",
			disabled: !hasSelection,
			onSelect: () => onZoom("selection"),
		},
		{
			key: "actual",
			label: t("artifacts.canvas.zoomActual"),
			hint: "⇧0",
			onSelect: () => onZoom("actual"),
		},
		{
			key: "in",
			label: t("artifacts.canvas.zoomIn"),
			hint: "+",
			onSelect: () => onZoom("in"),
		},
		{
			key: "out",
			label: t("artifacts.canvas.zoomOut"),
			hint: "−",
			onSelect: () => onZoom("out"),
		},
	];

	return (
		<div className="pointer-events-none absolute inset-x-0 bottom-2 z-10 flex flex-col items-center gap-1.5 px-2">
			{family && (
				<div
					data-testid="canvas-style-bar"
					className="pointer-events-auto flex max-w-full items-center gap-0.5 overflow-x-auto rounded-xl border border-border bg-panel p-1 shadow-md"
				>
					<div
						role="radiogroup"
						aria-label={t("artifacts.canvas.style.kind")}
						className="flex items-center gap-0.5"
					>
						{tool === "pen"
							? (["pen", "marker"] as const).map((k) => (
									<Choice
										key={k}
										checked={penKind === k}
										label={t(`artifacts.canvas.tools.${k}`)}
										onSelect={() => onPenKind(k)}
									>
										<Icon name={PEN_ICON[k]} size={15} />
									</Choice>
								))
							: SHAPES.map((k) => (
									<Choice
										key={k}
										checked={shape === k}
										label={t(`artifacts.canvas.tools.${k}`)}
										onSelect={() => onShape(k)}
									>
										<Icon name={SHAPE_ICON[k]} size={15} />
									</Choice>
								))}
					</div>
					<span className="mx-1 h-5 w-px shrink-0 bg-border" />
					<WidthChoices
						value={styles[family].width}
						onChange={(width) => onStyle(family, { width })}
					/>
					<span className="mx-1 h-5 w-px shrink-0 bg-border" />
					<ColorChoices
						value={styles[family].color}
						onChange={(color) => onStyle(family, { color })}
					/>
				</div>
			)}
			<div
				role="toolbar"
				aria-label={t("artifacts.canvas.toolbar")}
				className="pointer-events-auto flex max-w-full items-center gap-0.5 rounded-xl border border-border bg-panel p-1 shadow-md"
			>
				<SheetMenu
					doc={doc}
					canvasKey={canvasKey}
					editable={editable}
					onCanvas={onCanvas}
					commit={commit}
				/>
				<span className="mx-1 h-5 w-px shrink-0 bg-border" />
				<div className="flex min-w-0 items-center gap-0.5 overflow-x-auto">
					{tools.map((b) => (
						<IconButton
							key={b.tool}
							icon={b.icon}
							box="sm"
							size={15}
							active={tool === b.tool}
							data-testid={`canvas-tool-${b.tool}`}
							label={t("artifacts.canvas.toolKey", {
								name: b.label,
								key: TOOL_KEYS[b.tool],
							})}
							onClick={() => onTool(b.tool)}
						/>
					))}
				</div>
				{layers && (
					<>
						<span className="mx-1 h-5 w-px shrink-0 bg-border" />
						<IconButton
							icon="stack"
							box="sm"
							size={15}
							active={layers.open}
							data-testid="canvas-layers-toggle"
							label={t("artifacts.canvas.layers.toggle")}
							onClick={layers.onToggle}
						/>
					</>
				)}
				<span className="mx-1 h-5 w-px shrink-0 bg-border" />
				<IconButton
					icon="zoomOut"
					box="sm"
					size={15}
					label={t("artifacts.canvas.zoomOut")}
					onClick={() => onZoom("out")}
				/>
				<Menu
					side="top"
					align="right"
					className="shrink-0"
					items={zoomItems}
					trigger={(p) => (
						<button
							type="button"
							{...p}
							aria-haspopup="menu"
							aria-label={t("artifacts.canvas.zoomMenu", {
								percent: Math.round(zoom * 100),
							})}
							data-testid="canvas-zoom-menu"
							className="h-7 w-12 shrink-0 rounded-md text-muted text-xs tabular-nums hover:bg-hover hover:text-fg"
						>
							{Math.round(zoom * 100)}%
						</button>
					)}
				/>
				<IconButton
					icon="zoomIn"
					box="sm"
					size={15}
					label={t("artifacts.canvas.zoomIn")}
					onClick={() => onZoom("in")}
				/>
			</div>
		</div>
	);
}

/** 「页面」下拉：列出各画布页（带画框数）；可编辑时还有新建、改名、复制、删除。手机的视图条（CompactBar）也用它。 */
export function SheetMenu({
	doc,
	canvasKey,
	editable,
	onCanvas,
	commit,
}: {
	doc: CanvasDoc;
	canvasKey: string;
	editable: boolean;
	onCanvas(id: string): void;
	commit(ops: CanvasOp[]): boolean;
}): ReactNode {
	const { t } = useTranslation();
	const [renaming, setRenaming] = useState(false);
	const [deleting, setDeleting] = useState(false);
	const sheet = doc.canvases.find((c) => c.id === canvasKey);
	const frames = (id: string) =>
		doc.pages.filter((p) => p.canvas === id).length;
	const notes = doc.notes.filter((n) => n.canvas === canvasKey).length;
	const deletable = canDeleteSheet(doc, canvasKey);

	const remove = () => {
		const ops = deleteSheetOps(doc, canvasKey);
		if (!ops) return;
		const index = doc.canvases.findIndex((c) => c.id === canvasKey);
		const next = doc.canvases[index + 1] ?? doc.canvases[index - 1];
		if (commit(ops) && next) onCanvas(next.id);
	};

	const items: MenuItem[] = doc.canvases.map((c) => ({
		key: `sheet:${c.id}`,
		label: c.name,
		checked: c.id === canvasKey,
		hint: t("artifacts.canvas.sheets.frames", { count: frames(c.id) }),
		onSelect: () => onCanvas(c.id),
	}));
	if (editable && sheet)
		items.push(
			{
				key: "add",
				label: t("artifacts.canvas.sheets.add"),
				icon: "plus",
				onSelect: () => {
					const { ops, id } = addSheetOps(
						t("artifacts.canvas.sheets.newName", {
							n: doc.canvases.length + 1,
						}),
					);
					if (commit(ops)) onCanvas(id);
				},
			},
			{
				key: "rename",
				label: t("artifacts.canvas.sheets.rename"),
				icon: "edit",
				onSelect: () => setRenaming(true),
			},
			{
				key: "copy",
				label: t("artifacts.canvas.sheets.copy"),
				icon: "copy",
				onSelect: () => {
					const copy = copySheetOps(
						doc,
						canvasKey,
						t("artifacts.canvas.sheets.copyName", { name: sheet.name }),
					);
					if (copy && commit(copy.ops)) onCanvas(copy.id);
					else
						showToast({
							tone: "err",
							text: t("artifacts.canvas.sheets.copyFailed"),
						});
				},
			},
			{
				key: "delete",
				label: t("artifacts.canvas.sheets.delete"),
				icon: "trash",
				danger: true,
				disabled: !deletable,
				title: deletable ? undefined : t("artifacts.canvas.sheets.lastOne"),
				onSelect: () => {
					if (frames(canvasKey) + notes > 0) setDeleting(true);
					else remove();
				},
			},
		);

	return (
		<>
			<Menu
				side="top"
				align="left"
				className="shrink-0"
				items={items}
				trigger={(p) => (
					<button
						type="button"
						{...p}
						aria-haspopup="menu"
						aria-label={t("artifacts.canvas.sheets.menu", {
							name: sheet?.name ?? "",
						})}
						data-testid="canvas-sheet-menu"
						className="inline-flex h-7 max-w-36 shrink-0 items-center gap-1 rounded-md px-2 text-fg text-xs hover:bg-hover"
					>
						<span className="min-w-0 truncate">{sheet?.name}</span>
						<Icon
							name="chevronDown"
							size={12}
							className="shrink-0 text-faint"
						/>
					</button>
				)}
			/>
			{renaming && sheet && (
				<RenameSheetDialog
					name={sheet.name}
					onClose={() => setRenaming(false)}
					onSave={(name) => {
						commit(renameSheetOps(doc, canvasKey, name));
						setRenaming(false);
					}}
				/>
			)}
			{deleting && sheet && (
				<Dialog
					open
					onClose={() => setDeleting(false)}
					title={t("artifacts.canvas.sheets.deleteTitle", { name: sheet.name })}
					footer={
						<>
							<Button onClick={() => setDeleting(false)}>
								{t("artifacts.canvas.sheets.cancel")}
							</Button>
							<Button
								variant="danger"
								data-testid="canvas-sheet-delete-confirm"
								onClick={() => {
									setDeleting(false);
									remove();
								}}
							>
								{t("artifacts.canvas.sheets.deleteConfirm")}
							</Button>
						</>
					}
				>
					<p className="text-muted text-sm">
						{t("artifacts.canvas.sheets.deleteBody", {
							frames: frames(canvasKey),
							notes,
						})}
					</p>
				</Dialog>
			)}
		</>
	);
}

function RenameSheetDialog({
	name,
	onClose,
	onSave,
}: {
	name: string;
	onClose(): void;
	onSave(name: string): void;
}): ReactNode {
	const { t } = useTranslation();
	const [value, setValue] = useState(name);
	const submit = (e: FormEvent) => {
		e.preventDefault();
		if (value.trim() !== "") onSave(value);
	};
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("artifacts.canvas.sheets.renameTitle")}
			footer={
				<>
					<Button onClick={onClose}>
						{t("artifacts.canvas.sheets.cancel")}
					</Button>
					<Button
						variant="primary"
						type="submit"
						form="canvas-sheet-rename"
						disabled={value.trim() === ""}
					>
						{t("artifacts.canvas.sheets.save")}
					</Button>
				</>
			}
		>
			<form id="canvas-sheet-rename" onSubmit={submit}>
				<TextInput
					autoFocus
					aria-label={t("artifacts.canvas.sheets.nameLabel")}
					maxLength={NAME_CHARS}
					value={value}
					onChange={(e) => setValue(e.target.value)}
				/>
			</form>
		</Dialog>
	);
}
