/**
 * 画布编辑器的属性面板（计划 M14 6.4）：右侧宽 300（窄窗口里覆盖在画布上），两个标签「属性 / 调节」。
 *
 * - 单选画框：名字；尺寸预设（桌面、笔记本、平板、手机、自定义）；宽、高、X、Y。锁定的画框不能改位置与尺寸。
 * - 单选标注：颜色、线宽（画笔与形状）、字号（文字）、「已处理」（便签；agent 写的便签处理后显示「{线程} 已处理」）。
 * - 「排列」：对齐（≥ 2 个画框）、等距（≥ 3 个）、复制、锁定 / 解锁、移到最前 / 最后、删除。
 * 「调节」标签：单选画框时显示调用方给的调节面板（6.5 的 `TweaksPanel`）；没给时是空态。标签可由调用方受控
 * （画框名称行的「调节」按钮要直接切到这一页）。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type CanvasDoc,
	type CanvasNote,
	type CanvasOp,
	type CanvasPage,
	MAX_COORD,
	NAME_CHARS,
	PAGE_MAX,
	PAGE_MIN,
	TEXT_SIZE,
} from "../../../lib/artifacts/canvas-doc";
import type { IconName } from "../../ui/icons";
import {
	Field,
	IconButton,
	Select,
	Toggle,
	tabClass,
} from "../../ui/primitives";
import {
	ALIGN_EDGES,
	ALIGN_MIN,
	type AlignEdge,
	alignOps,
	allLocked,
	DISTRIBUTE_MIN,
	distributeOps,
	FRAME_PRESETS,
	type FramePreset,
	lockOps,
	orderOps,
	presetOf,
	presetOps,
	selectedPages,
	sizeOps,
} from "./arrange";
import { deleteOps, duplicateOps, findItem } from "./selection";
import { ColorChoices, WidthChoices } from "./Toolbar";

export interface PropsPanelProps {
	doc: CanvasDoc;
	/** 当前画布页上选中的对象。 */
	selected: readonly string[];
	/** artifact 的相关线程（「{线程} 已处理」用）。 */
	threads: readonly { threadId: string; title: string }[];
	commit(ops: CanvasOp[]): boolean;
	/** 复制、删除之后改选中。 */
	onSelect(ids: string[]): void;
	/** 窄窗口里覆盖在画布上。 */
	overlay: boolean;
	/** 受控的当前标签；不给时面板自己记。 */
	tab?: PropsTab;
	onTab?(tab: PropsTab): void;
	/** 单选画框时「调节」标签的内容。 */
	tweaks?(page: CanvasPage): ReactNode;
}

export type PropsTab = "props" | "tweaks";

const ALIGN_ICON: Record<AlignEdge, IconName> = {
	left: "alignLeft",
	centerH: "alignCenterH",
	right: "alignRight",
	top: "alignTop",
	centerV: "alignCenterV",
	bottom: "alignBottom",
};

function round2(v: number): number {
	return Math.round(v * 100) / 100;
}

export function PropsPanel({
	doc,
	selected,
	threads,
	commit,
	onSelect,
	overlay,
	tab: tabProp,
	onTab,
	tweaks,
}: PropsPanelProps): ReactNode {
	const { t } = useTranslation();
	const [ownTab, setOwnTab] = useState<PropsTab>("props");
	const tab = tabProp ?? ownTab;
	const setTab = onTab ?? setOwnTab;
	const [only] = selected;
	const single = selected.length === 1 && only ? findItem(doc, only) : null;
	const pages = selectedPages(doc, selected);
	const locked = allLocked(doc, selected);

	const arrange: {
		key: string;
		icon: IconName;
		label: string;
		ops(): CanvasOp[];
		disabled?: boolean;
	}[] = [
		...ALIGN_EDGES.map((edge) => ({
			key: `align-${edge}`,
			icon: ALIGN_ICON[edge],
			label: t(`artifacts.canvas.props.align.${edge}`),
			disabled: pages.length < ALIGN_MIN,
			ops: () => alignOps(doc, selected, edge),
		})),
		{
			key: "distribute-x",
			icon: "distributeH",
			label: t("artifacts.canvas.props.distributeH"),
			disabled: pages.length < DISTRIBUTE_MIN,
			ops: () => distributeOps(doc, selected, "x"),
		},
		{
			key: "distribute-y",
			icon: "distributeV",
			label: t("artifacts.canvas.props.distributeV"),
			disabled: pages.length < DISTRIBUTE_MIN,
			ops: () => distributeOps(doc, selected, "y"),
		},
	];

	return (
		<aside
			data-testid="canvas-props"
			aria-label={t("artifacts.canvas.props.title")}
			className={`flex w-[300px] shrink-0 flex-col bg-panel ${overlay ? "absolute top-2 right-2 bottom-16 z-20 rounded-lg border border-border shadow-lg" : "border-border border-l"}`}
		>
			<div
				role="tablist"
				aria-label={t("artifacts.canvas.props.title")}
				className="flex h-10 shrink-0 items-center gap-1 border-border border-b px-2"
			>
				{(["props", "tweaks"] as const).map((k) => (
					<button
						key={k}
						type="button"
						role="tab"
						aria-selected={tab === k}
						data-testid={`canvas-props-tab-${k}`}
						className={`${tabClass(tab === k)} ${tab === k ? "bg-hover" : ""}`}
						onClick={() => setTab(k)}
					>
						{t(`artifacts.canvas.props.tabs.${k}`)}
					</button>
				))}
			</div>
			<div role="tabpanel" className="min-h-0 flex-1 overflow-y-auto">
				{tab === "tweaks" ? (
					single?.type === "page" && tweaks ? (
						tweaks(single.value)
					) : (
						<p className="px-3 py-4 text-faint text-sm">
							{single?.type === "page"
								? t("artifacts.canvas.props.tweaksEmpty")
								: t("artifacts.canvas.props.tweaksPick")}
						</p>
					)
				) : (
					<>
						{single?.type === "page" ? (
							<FrameSection page={single.value} commit={commit} />
						) : single?.type === "note" ? (
							<NoteSection
								note={single.value}
								threads={threads}
								commit={commit}
							/>
						) : (
							<p className="px-3 pt-3 text-muted text-sm">
								{t("artifacts.canvas.props.selected", {
									count: selected.length,
								})}
							</p>
						)}
						<section className="flex flex-col gap-2 px-3 py-3">
							<h3 className="font-medium text-muted text-xs">
								{t("artifacts.canvas.props.arrange")}
							</h3>
							<div className="grid grid-cols-8 gap-0.5">
								{arrange.map((a) => (
									<IconButton
										key={a.key}
										icon={a.icon}
										box="sm"
										size={15}
										label={a.label}
										disabled={a.disabled}
										data-testid={`canvas-${a.key}`}
										onClick={() => commit(a.ops())}
									/>
								))}
							</div>
							<div className="flex flex-wrap gap-0.5">
								<IconButton
									icon="copy"
									box="sm"
									size={15}
									label={t("artifacts.canvas.props.duplicate")}
									data-testid="canvas-duplicate"
									onClick={() => {
										const dup = duplicateOps(doc, selected);
										if (commit(dup.ops)) onSelect(dup.ids);
									}}
								/>
								<IconButton
									icon={locked ? "lockOpen" : "lock"}
									box="sm"
									size={15}
									label={t(
										locked
											? "artifacts.canvas.props.unlock"
											: "artifacts.canvas.props.lock",
									)}
									data-testid="canvas-lock"
									onClick={() => commit(lockOps(doc, selected, !locked))}
								/>
								<IconButton
									icon="bringFront"
									box="sm"
									size={15}
									label={t("artifacts.canvas.props.bringFront")}
									onClick={() => commit(orderOps(doc, selected, "front"))}
								/>
								<IconButton
									icon="sendBack"
									box="sm"
									size={15}
									label={t("artifacts.canvas.props.sendBack")}
									onClick={() => commit(orderOps(doc, selected, "back"))}
								/>
								<IconButton
									icon="trash"
									box="sm"
									size={15}
									label={t("artifacts.canvas.props.delete")}
									className="hover:text-err"
									onClick={() => {
										if (commit(deleteOps(selected))) onSelect([]);
									}}
								/>
							</div>
						</section>
					</>
				)}
			</div>
		</aside>
	);
}

/**
 * 数字框：失焦或 `Enter` 提交，`Esc` 还原。值从外面变了（别处改了、撤销了）时按 key 重置。
 */
function NumberField({
	label,
	value,
	min,
	max,
	disabled,
	testId,
	onCommit,
}: {
	label: string;
	value: number;
	min: number;
	max: number;
	disabled?: boolean;
	testId?: string;
	onCommit(v: number): void;
}): ReactNode {
	return (
		<Field label={label}>
			<input
				key={value}
				type="number"
				inputMode="decimal"
				defaultValue={value}
				min={min}
				max={max}
				disabled={disabled}
				data-testid={testId}
				onBlur={(e) => {
					const v = Number(e.currentTarget.value);
					if (e.currentTarget.value.trim() === "" || !Number.isFinite(v)) {
						e.currentTarget.value = String(value);
						return;
					}
					const next = Math.min(max, Math.max(min, v));
					if (next !== value) onCommit(next);
					else e.currentTarget.value = String(value);
				}}
				onKeyDown={(e) => {
					if (e.key === "Enter") e.currentTarget.blur();
					else if (e.key === "Escape") {
						e.currentTarget.value = String(value);
						e.currentTarget.blur();
					}
				}}
				className="h-8 w-full rounded-md border border-border bg-inset px-2 text-sm text-fg tabular-nums focus-visible:border-accent focus-visible:outline-none disabled:opacity-50"
			/>
		</Field>
	);
}

function FrameSection({
	page,
	commit,
}: {
	page: CanvasPage;
	commit(ops: CanvasOp[]): boolean;
}): ReactNode {
	const { t } = useTranslation();
	const set = (key: string, value: unknown) =>
		commit([{ op: "set", id: page.id, key, value }]);
	const preset = presetOf(page);
	return (
		<section className="flex flex-col gap-3 border-border border-b px-3 py-3">
			<Field label={t("artifacts.canvas.props.name")}>
				<input
					key={page.name}
					defaultValue={page.name}
					maxLength={NAME_CHARS}
					data-testid="canvas-frame-name"
					onBlur={(e) => {
						const name = e.currentTarget.value.trim();
						if (name !== "" && name !== page.name) set("name", name);
						else e.currentTarget.value = page.name;
					}}
					onKeyDown={(e) => {
						if (e.key === "Enter") e.currentTarget.blur();
						else if (e.key === "Escape") {
							e.currentTarget.value = page.name;
							e.currentTarget.blur();
						}
					}}
					className="h-8 w-full rounded-md border border-border bg-inset px-2 text-sm text-fg focus-visible:border-accent focus-visible:outline-none"
				/>
			</Field>
			<Field label={t("artifacts.canvas.props.size")}>
				<Select
					value={preset}
					disabled={page.locked}
					data-testid="canvas-frame-preset"
					onChange={(e) =>
						commit(presetOps(page, e.target.value as FramePreset))
					}
				>
					{FRAME_PRESETS.map((p) => (
						<option key={p.key} value={p.key}>
							{t(`artifacts.canvas.props.presets.${p.key}`, {
								width: p.width,
								height: p.height,
							})}
						</option>
					))}
					<option value="custom" disabled={preset !== "custom"}>
						{t("artifacts.canvas.props.presets.custom")}
					</option>
				</Select>
			</Field>
			<div className="grid grid-cols-2 gap-2">
				<NumberField
					label={t("artifacts.canvas.props.width")}
					value={page.width}
					min={PAGE_MIN}
					max={PAGE_MAX}
					disabled={page.locked}
					testId="canvas-frame-width"
					onCommit={(v) => commit(sizeOps(page, v, page.height))}
				/>
				<NumberField
					label={t("artifacts.canvas.props.height")}
					value={page.height}
					min={PAGE_MIN}
					max={PAGE_MAX}
					disabled={page.locked}
					testId="canvas-frame-height"
					onCommit={(v) => commit(sizeOps(page, page.width, v))}
				/>
				<NumberField
					label="X"
					value={page.x}
					min={-MAX_COORD}
					max={MAX_COORD}
					disabled={page.locked}
					testId="canvas-frame-x"
					onCommit={(v) => set("x", round2(v))}
				/>
				<NumberField
					label="Y"
					value={page.y}
					min={-MAX_COORD}
					max={MAX_COORD}
					disabled={page.locked}
					testId="canvas-frame-y"
					onCommit={(v) => set("y", round2(v))}
				/>
			</div>
			{page.locked && (
				<p className="text-faint text-xs">
					{t("artifacts.canvas.props.lockedHint")}
				</p>
			)}
		</section>
	);
}

function NoteSection({
	note,
	threads,
	commit,
}: {
	note: CanvasNote;
	threads: readonly { threadId: string; title: string }[];
	commit(ops: CanvasOp[]): boolean;
}): ReactNode {
	const { t } = useTranslation();
	const set = (key: string, value: unknown) =>
		commit([{ op: "set", id: note.id, key, value }]);
	const by =
		note.by === null
			? null
			: (threads.find((x) => x.threadId === note.by)?.title ??
				t("artifacts.canvas.props.agent"));
	return (
		<section className="flex flex-col gap-3 border-border border-b px-3 py-3">
			<h3 className="font-medium text-sm">
				{note.kind === "pen" && note.marker
					? t("artifacts.canvas.tools.marker")
					: t(`artifacts.canvas.tools.${note.kind}`)}
			</h3>
			<Labeled label={t("artifacts.canvas.style.color")}>
				<ColorChoices value={note.color} onChange={(c) => set("color", c)} />
			</Labeled>
			{note.kind !== "text" && note.kind !== "sticky" && (
				<Labeled label={t("artifacts.canvas.style.width")}>
					<WidthChoices value={note.width} onChange={(w) => set("width", w)} />
				</Labeled>
			)}
			{note.kind === "text" && (
				<NumberField
					label={t("artifacts.canvas.props.fontSize")}
					value={note.size}
					min={TEXT_SIZE.min}
					max={TEXT_SIZE.max}
					testId="canvas-note-size"
					onCommit={(v) => set("size", round2(v))}
				/>
			)}
			{note.kind === "sticky" && (
				<div className="flex flex-col gap-1">
					<Toggle
						checked={note.done}
						label={t("artifacts.canvas.props.done")}
						onChange={(v) => set("done", v)}
					/>
					{note.done && by !== null && (
						<p className="text-faint text-xs">
							{t("artifacts.canvas.props.doneBy", { agent: by })}
						</p>
					)}
				</div>
			)}
		</section>
	);
}

/** 带小标题的一组按钮（不用 `<label>`：点标题会点到组里第一个按钮）。 */
function Labeled({
	label,
	children,
}: {
	label: string;
	children: ReactNode;
}): ReactNode {
	return (
		<div className="flex flex-col gap-1">
			<span className="font-medium text-muted text-xs">{label}</span>
			{children}
		</div>
	);
}
