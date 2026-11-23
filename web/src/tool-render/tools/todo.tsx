/**
 * `todo` — 分阶段的任务清单。收起时头部读数是整张清单的完成进度（进度格 + `完成/总数`），
 * 展开体先列这次调用的操作（完成了哪项、开始了哪项……），再给出操作后的看板：每个阶段带自己的进度，
 * 本次调用触及的任务行高亮。参数与结果形状见 oryn-core `tools/todo.ts`。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon, type IconName } from "../../components/ui/icons";
import { type CellTone, Note, ProgressCells } from "../parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "../types";
import {
	detailsRecord,
	isRecord,
	normalizeWs,
	resultTextOf,
	str,
	truncate,
} from "../util";

type TaskStatus = "pending" | "in_progress" | "completed" | "abandoned";

/** 与右栏待办卡同一套图标（颜色在 tool-render.css 的 `.tv-task--*`）。 */
const TASK_ICONS: Record<TaskStatus, { name: IconName; fill?: boolean }> = {
	completed: { name: "checkCircle", fill: true },
	in_progress: { name: "circleHalf" },
	abandoned: { name: "minusCircle" },
	pending: { name: "circle" },
};

const CELL_TONE: Record<TaskStatus, CellTone | null> = {
	completed: "ok",
	in_progress: "accent",
	pending: "idle",
	// 放弃的任务不计入进度
	abandoned: null,
};

type TodoOp = "init" | "start" | "done" | "drop" | "rm" | "append" | "view";

/** 操作的图标与色调（色调只在展开体里显示，头部保持灰度）。 */
const OP_STYLE: Record<TodoOp, { icon: IconName; tone: string }> = {
	init: { icon: "list", tone: "muted" },
	start: { icon: "play", tone: "accent" },
	done: { icon: "check", tone: "ok" },
	drop: { icon: "minusCircle", tone: "warn" },
	rm: { icon: "trash", tone: "err" },
	append: { icon: "plus", tone: "muted" },
	view: { icon: "eye", tone: "muted" },
};

function isTodoOp(op: string): op is TodoOp {
	return Object.hasOwn(OP_STYLE, op);
}

const ROMAN_PAIRS: ReadonlyArray<readonly [number, string]> = [
	[1000, "M"],
	[900, "CM"],
	[500, "D"],
	[400, "CD"],
	[100, "C"],
	[90, "XC"],
	[50, "L"],
	[40, "XL"],
	[10, "X"],
	[9, "IX"],
	[5, "V"],
	[4, "IV"],
	[1, "I"],
];

/** One-based roman numeral for phase headers (I, II, III, IV, …). */
function roman(n: number): string {
	if (n <= 0) return "";
	let out = "";
	let rem = n;
	for (const [value, sym] of ROMAN_PAIRS) {
		while (rem >= value) {
			out += sym;
			rem -= value;
		}
	}
	return out;
}

interface OpEntry {
	op: string;
	/** `task`（字符串或数组）指向的任务原文。 */
	tasks: string[];
	phase: string | null;
	/** `append` / 扁平 `init` 的新任务。 */
	items: string[];
	/** `init` 的阶段列表：阶段名与任务数。 */
	list: { phase: string; count: number; items: string[] }[];
}

function stringsOf(value: unknown): string[] {
	if (typeof value === "string") return value ? [value] : [];
	if (!Array.isArray(value)) return [];
	return value.filter((v): v is string => typeof v === "string" && v !== "");
}

/**
 * 调用参数 → 操作列表。现行契约是单个顶层 `{op,…}`；旧转录里还有批量的 `{ops:[…]}`。
 * 非对象的条目（流式参数还没到齐）丢弃。
 */
function toOps(args: ToolRenderProps["args"]): OpEntry[] {
	const raw: unknown[] = Array.isArray(args.ops)
		? args.ops
		: typeof args.op === "string"
			? [args]
			: [];
	const out: OpEntry[] = [];
	for (const entry of raw) {
		if (!isRecord(entry)) continue;
		const list: OpEntry["list"] = [];
		if (Array.isArray(entry.list)) {
			for (const p of entry.list) {
				if (!isRecord(p)) continue;
				const items = stringsOf(p.items);
				list.push({ phase: str(p.phase) ?? "", count: items.length, items });
			}
		}
		out.push({
			op: str(entry.op) ?? "update",
			tasks: stringsOf(entry.task),
			phase: str(entry.phase),
			items: stringsOf(entry.items),
			list,
		});
	}
	return out;
}

interface BoardTask {
	content: string;
	status: TaskStatus;
}

interface BoardPhase {
	name: string;
	tasks: BoardTask[];
}

function statusOf(raw: unknown): TaskStatus {
	return raw === "completed" || raw === "in_progress" || raw === "abandoned"
		? raw
		: "pending";
}

/** 结果（或运行中的部分结果）里的看板；没有可用的 `details.phases` 时为 null。 */
function boardOf(current: ToolResultLike | undefined): BoardPhase[] | null {
	const rec = detailsRecord(current);
	if (!rec || !Array.isArray(rec.phases)) return null;
	const phases: BoardPhase[] = [];
	for (const p of rec.phases) {
		if (!isRecord(p)) continue;
		const tasks: BoardTask[] = [];
		if (Array.isArray(p.tasks)) {
			for (const t of p.tasks) {
				if (!isRecord(t)) continue;
				tasks.push({
					content: str(t.content) ?? "",
					status: statusOf(t.status),
				});
			}
		}
		phases.push({ name: str(p.name) ?? "", tasks });
	}
	return phases;
}

/** 一组任务的进度：完成数与计入总数的任务数（放弃的不算）。 */
function tally(tasks: readonly BoardTask[]): { done: number; total: number } {
	let done = 0;
	let total = 0;
	for (const task of tasks) {
		if (task.status === "abandoned") continue;
		total++;
		if (task.status === "completed") done++;
	}
	return { done, total };
}

/** 这次调用触及的任务（按原文匹配）与整个被触及的阶段；`init` 是整张新清单，不高亮。 */
function touchedBy(ops: readonly OpEntry[]): {
	tasks: Set<string>;
	phases: Set<string>;
} {
	const tasks = new Set<string>();
	const phases = new Set<string>();
	for (const op of ops) {
		if (op.op === "init" || op.op === "view") continue;
		for (const t of op.tasks) tasks.add(normalizeWs(t));
		for (const t of op.items) tasks.add(normalizeWs(t));
		// 只给了阶段（整阶段完成 / 放弃）：该阶段的任务都算触及；`append` 的阶段只是落点
		if (op.phase && op.tasks.length === 0 && op.op !== "append")
			phases.add(op.phase);
	}
	return { tasks, phases };
}

function OpIcon({ op, size = 12 }: { op: string; size?: number }): ReactNode {
	return <Icon name={isTodoOp(op) ? OP_STYLE[op].icon : "edit"} size={size} />;
}

function useOpLabel(): (op: string) => string {
	const { t } = useTranslation();
	return (op) => (isTodoOp(op) ? t(`toolRender.todo.op.${op}`) : op);
}

/** 头部摘要：每个操作一枚「图标 + 对象」，对象是任务原文、阶段名或新清单的规模。 */
function Summary({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const label = useOpLabel();
	const ops = toOps(args);
	if (ops.length === 0)
		return <span className="tv-faint">{t("toolRender.todo.op.update")}</span>;
	return (
		<>
			{ops.map((op, i) => {
				const target =
					op.tasks[0] ??
					(op.op === "init" && op.list.length > 0
						? `${t("toolRender.todo.phases", { count: op.list.length })} · ${t(
								"toolRender.todo.tasks",
								{ count: op.list.reduce((n, p) => n + p.count, 0) },
							)}`
						: op.phase) ??
					(op.items.length > 0
						? t("toolRender.todo.tasks", { count: op.items.length })
						: null);
				const extra = op.tasks.length > 1 ? ` +${op.tasks.length - 1}` : "";
				return (
					<span key={i} className="tv-todo-sumop" title={label(op.op)}>
						<OpIcon op={op.op} />
						{target ? (
							<span className="tv-trunc tv-sans">
								{truncate(normalizeWs(target), 60)}
								{extra}
							</span>
						) : (
							<span className="tv-sans">{label(op.op)}</span>
						)}
					</span>
				);
			})}
		</>
	);
}

/** 头部读数：整张清单的完成进度。查看与修改都给，收起时一眼看到做到哪了。 */
function Meta({ result, partial }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const board = boardOf(result ?? partial);
	if (!board) return null;
	const all = board.flatMap((p) => p.tasks);
	const { done, total } = tally(all);
	if (total === 0) return null;
	const tones = all
		.map((task) => CELL_TONE[task.status])
		.filter((tone): tone is CellTone => tone !== null);
	return (
		<>
			<ProgressCells
				tones={tones}
				label={t("toolRender.todo.progress", { done, total })}
			/>
			<span className={done === total ? "tv-meta-ok" : undefined}>
				{done}/{total}
			</span>
		</>
	);
}

/** 输入：本次请求的各项操作及其对象（看板只给结果状态，看不出改了哪一项）。 */
function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const label = useOpLabel();
	const ops = toOps(args).filter((op) => op.op !== "view");
	if (ops.length === 0) return null;
	return (
		<div className="tv-todo-ops">
			{ops.map((op, i) => {
				const tone = isTodoOp(op.op) ? OP_STYLE[op.op].tone : "muted";
				const targets =
					op.tasks.length > 0
						? op.tasks
						: op.op === "init" && op.list.length > 0
							? op.list.map((p) =>
									t("toolRender.todo.phaseWithTasks", {
										phase: p.phase,
										count: p.count,
									}),
								)
							: op.items;
				return (
					<div key={i} className={`tv-todo-op tv-todo-op--${tone}`}>
						<span className="tv-todo-op-chip">
							<OpIcon op={op.op} size={11} />
							{label(op.op)}
						</span>
						<span className="tv-todo-op-body">
							{op.phase && (op.tasks.length === 0 || op.op === "append") && (
								<span className="tv-todo-op-phase">{op.phase}</span>
							)}
							{targets.map((target, k) => (
								<span key={k} className="tv-todo-op-target">
									{target}
								</span>
							))}
						</span>
					</div>
				);
			})}
		</div>
	);
}

function Board({
	phases,
	touched,
}: {
	phases: readonly BoardPhase[];
	touched: { tasks: Set<string>; phases: Set<string> };
}): ReactNode {
	const { t } = useTranslation();
	if (phases.every((p) => p.tasks.length === 0)) return null;
	return (
		<div className="tv-todo">
			{phases.map((phase, i) => {
				const { done, total } = tally(phase.tasks);
				const phaseTouched = touched.phases.has(phase.name);
				return (
					<div
						key={`${i}:${phase.name}`}
						className={`tv-todo-phase${total > 0 && done === total ? " tv-todo-phase--done" : ""}`}
					>
						<div className="tv-todo-phase-head">
							<span className="tv-todo-phase-num">{roman(i + 1)}</span>
							<span className="tv-todo-phase-name">{phase.name}</span>
							{total > 0 && (
								<span className="tv-todo-phase-count">
									{done}/{total}
								</span>
							)}
						</div>
						{phase.tasks.map((task, k) => {
							const hit =
								phaseTouched || touched.tasks.has(normalizeWs(task.content));
							const icon = TASK_ICONS[task.status];
							return (
								<div
									key={`${k}:${task.content}`}
									className={`tv-task tv-task--${task.status}${hit ? " tv-task--touched" : ""}`}
									title={hit ? t("toolRender.todo.touched") : undefined}
								>
									<span className="tv-task-icon" aria-hidden="true">
										<Icon
											name={icon.name}
											size={13}
											weight={icon.fill ? "fill" : "regular"}
										/>
									</span>
									<span className="tv-task-text">{task.content}</span>
								</div>
							);
						})}
					</div>
				);
			})}
		</div>
	);
}

function Result({ args, result, partial }: ToolRenderProps): ReactNode {
	// 运行中没有最终结果时，用结构化部分结果展示看板
	const current = result ?? partial;
	const board = boardOf(current);
	// 出错（任务名对不上等）时 Core 仍返回操作前后的看板：错误说明放在看板上面
	const errorText = current?.isError ? resultTextOf(current).trim() : "";
	if (!board)
		return errorText ? (
			<Note tone="err">{errorText}</Note>
		) : (
			<span className="tv-faint">{resultTextOf(current).trim()}</span>
		);
	return (
		<>
			{errorText && <Note tone="err">{errorText}</Note>}
			<Board phases={board} touched={touchedBy(toOps(args))} />
		</>
	);
}

export const todoRenderer: ToolRenderer = {
	Summary,
	Meta,
	Input,
	Result,
	livePartial: true,
};
