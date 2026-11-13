/**
 * 右侧信息面板：orb 状态、规格与 CPU / 内存迷你曲线（`GET orbs/{id}/metrics`）、环境（最近 setup / resume）、
 * 上下文占用（已有上下文报告时显示分项）、todo（可编辑，`set_todos`）、子 agent 列表，以及报告 / btw 入口。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useOrbMetrics } from "../../api/queries";
import { formatMemoryMB, formatPercent, formatTokens } from "../../lib/format";
import { keyed } from "../../lib/keys";
import type { TodoItem, TodoPhase } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { getThreadStore, useThread } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { Card, ListRow, Meter, StatusDot, thresholdTone } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";
import { OrbBead } from "../ui/OrbBead";
import { IconButton, Sparkline, TextInput } from "../ui/primitives";
import { useRpc } from "./Dialogs";
import { EnvRuns } from "./EnvBanner";
import { useOrbOf } from "./ThreadHeader";

// 稳定的空数组，避免 zustand 选择器每次返回新引用
const NO_PHASES: readonly TodoPhase[] = [];

/** 卡片内的紧凑空态：圆底图标 + 一行说明。 */
function EmptyLine({
	icon,
	children,
}: {
	icon: IconName;
	children: ReactNode;
}) {
	return (
		<p className="flex items-center gap-2 text-faint text-xs">
			<span className="grid h-[22px] w-[22px] shrink-0 place-items-center rounded-full bg-inset">
				<Icon name={icon} size={12} aria-hidden />
			</span>
			{children}
		</p>
	);
}

/** 规格胶囊：图标 + 值。 */
function Spec({ icon, children }: { icon: IconName; children: ReactNode }) {
	return (
		<span className="inline-flex h-6 items-center gap-1.5 rounded-sm border border-border bg-inset px-2 text-2xs text-muted">
			<Icon name={icon} size={12} aria-hidden className="text-faint" />
			<span className="font-medium text-fg">{children}</span>
		</span>
	);
}

/** 内嵌井里的一行读数：图标 + 标签、数值、着色迷你曲线（颜色由 `className` 的文字色决定）。 */
function MetricWell({
	icon,
	label,
	value,
	chart,
	className,
}: {
	icon: IconName;
	label: string;
	value: string;
	chart: ReactNode;
	className: string;
}) {
	return (
		<div className="grid h-10 grid-cols-[4.25rem_4.5rem_minmax(0,1fr)] items-center rounded-md border border-border bg-inset px-2.5">
			<span className="flex items-center gap-1.5 font-medium text-2xs text-muted">
				<Icon name={icon} size={13} aria-hidden className="text-faint" />
				{label}
			</span>
			<span className="font-semibold text-base tabular-nums tracking-tight">
				{value}
			</span>
			<span className={className}>{chart}</span>
		</div>
	);
}

function OrbSection({ threadId }: { threadId: string }) {
	const { t } = useTranslation();
	const orb = useOrbOf(threadId);
	const working = useThread(threadId, (s) => s.thread?.state === "working");
	const metrics = useOrbMetrics(orb?.state === "deleted" ? undefined : orb?.id);
	if (!orb) {
		return (
			<Card title={t("panel.orb")} icon="box">
				<EmptyLine icon="box">{t("panel.noOrb")}</EmptyLine>
			</Card>
		);
	}
	const samples = metrics.data?.samples ?? [];
	const last = samples[samples.length - 1];
	return (
		<Card title={t("panel.orb")} icon="box">
			<div className="flex items-center gap-2.5">
				<OrbBead state={orb.state} active={working} size={18} />
				<span className="font-medium text-sm">
					{t(`orbState.${orb.state}`)}
				</span>
				<span className="ml-auto font-mono text-2xs text-faint">
					{orb.id.slice(0, 12)}
				</span>
			</div>
			<div className="mt-3 mb-2.5 flex flex-wrap gap-1.5">
				<Spec icon="box">{orb.type}</Spec>
				<Spec icon="cpu">{t("panel.cores", { n: orb.cpus })}</Spec>
				<Spec icon="memory">{formatMemoryMB(orb.memoryMB)}</Spec>
				{orb.providerName && <Spec icon="hardDrives">{orb.providerName}</Spec>}
			</div>
			<div className="flex flex-col gap-1.5">
				<MetricWell
					icon="cpu"
					label={t("panel.cpu")}
					value={last ? formatPercent(last.cpuPct) : "—"}
					className="text-accent"
					chart={
						<Sparkline
							values={samples.map((s) => s.cpuPct)}
							max={100}
							label={t("panel.cpuChart")}
						/>
					}
				/>
				<MetricWell
					icon="memory"
					label={t("panel.memory")}
					value={last ? formatMemoryMB(last.memUsedMB) : "—"}
					className="text-ok"
					chart={
						<Sparkline
							values={samples.map((s) => s.memUsedMB)}
							max={last?.memLimitMB || undefined}
							label={t("panel.memChart")}
						/>
					}
				/>
			</div>
			<div className="mt-3">
				<h4 className="mb-1 font-medium text-2xs text-faint">
					{t("env.title")}
				</h4>
				<EnvRuns orbId={orb.id} runs={[orb.lastSetup, orb.lastResume]} />
			</div>
		</Card>
	);
}

const NEXT_STATUS: Record<TodoItem["status"], TodoItem["status"]> = {
	pending: "in_progress",
	in_progress: "completed",
	completed: "pending",
	abandoned: "pending",
};

const STATUS_ICON: Record<
	TodoItem["status"],
	{ icon: IconName; fill?: boolean; className: string }
> = {
	pending: { icon: "circle", className: "text-ghost" },
	in_progress: { icon: "circleHalf", className: "text-accent" },
	completed: { icon: "checkCircle", fill: true, className: "text-ok" },
	abandoned: { icon: "minusCircle", className: "text-faint" },
};

const STATUS_TEXT: Record<TodoItem["status"], string> = {
	pending: "text-muted",
	in_progress: "font-medium text-fg",
	completed: "text-faint",
	abandoned: "text-faint line-through",
};

function TodoSection({ threadId }: { threadId: string }) {
	const { t } = useTranslation();
	const phases = useThread(threadId, (s) => s.state?.todoPhases) ?? NO_PHASES;
	const [draft, setDraft] = useState("");
	const [error, setError] = useState<string | null>(null);
	const save = async (next: TodoPhase[]) => {
		setError(null);
		try {
			const res = await realtime.cmd(threadId, {
				type: "set_todos",
				phases: next,
			});
			const store = getThreadStore(threadId);
			const st = store.getState().state;
			if (st)
				store
					.getState()
					.patchLocal({ state: { ...st, todoPhases: res.todoPhases } });
		} catch (e) {
			setError(e instanceof Error ? e.message : String(e));
		}
	};
	const update = (pi: number, ti: number, patch: Partial<TodoItem> | null) =>
		phases.map((p, i) =>
			i !== pi
				? p
				: {
						...p,
						tasks:
							patch === null
								? p.tasks.filter((_, j) => j !== ti)
								: p.tasks.map((task, j) =>
										j === ti ? { ...task, ...patch } : task,
									),
					},
		);
	// 进度不计已放弃的条目
	const tasks = phases.flatMap((p) => p.tasks);
	const total = tasks.filter((x) => x.status !== "abandoned").length;
	const done = tasks.filter((x) => x.status === "completed").length;
	return (
		<Card
			title={t("panel.todos")}
			icon="list"
			action={
				total > 0 && (
					<span className="flex items-center gap-2 text-2xs text-faint tabular-nums">
						{done}/{total}
						<Meter
							value={done}
							max={total}
							tone="ok"
							label={t("panel.todos")}
							className="w-11"
						/>
					</span>
				)
			}
		>
			{phases.length === 0 && (
				<EmptyLine icon="list">{t("panel.noTodos")}</EmptyLine>
			)}
			{keyed(phases, "p").map(({ key: pk, item: phase }, pi) => (
				<div key={pk} className="-mx-1.5 mb-1.5">
					{phase.name && (
						<div className="mb-0.5 px-1.5 font-medium text-2xs text-faint">
							{phase.name}
						</div>
					)}
					<ul className="flex flex-col gap-px">
						{keyed(phase.tasks, "t").map(({ key: tk, item: task }, ti) => {
							const mark = STATUS_ICON[task.status];
							return (
								<li
									key={tk}
									className={`group flex items-start gap-2 rounded-sm px-1.5 py-1 text-sm transition-colors duration-150 ${task.status === "in_progress" ? "bg-accent-soft/60" : "hover:bg-hover/60"}`}
								>
									<button
										type="button"
										className={`mt-0.5 shrink-0 rounded-full ${mark.className}`}
										aria-label={t(`todo.status.${task.status}`)}
										title={t(`todo.status.${task.status}`)}
										onClick={() =>
											void save(
												update(pi, ti, { status: NEXT_STATUS[task.status] }),
											)
										}
									>
										<Icon
											name={mark.icon}
											size={15}
											weight={mark.fill ? "fill" : "regular"}
										/>
									</button>
									<span
										className={`min-w-0 flex-1 break-words ${STATUS_TEXT[task.status]}`}
									>
										{task.content}
									</span>
									<button
										type="button"
										className="mt-0.5 shrink-0 text-faint opacity-0 transition-opacity duration-150 hover:text-err focus-visible:opacity-100 group-hover:opacity-100"
										aria-label={t("common.delete")}
										onClick={() => void save(update(pi, ti, null))}
									>
										<Icon name="close" size={12} />
									</button>
								</li>
							);
						})}
					</ul>
				</div>
			))}
			<form
				className="mt-1.5 flex gap-1"
				onSubmit={(e) => {
					e.preventDefault();
					if (!draft.trim()) return;
					const base = phases.length > 0 ? phases : [{ name: "", tasks: [] }];
					const last = base.length - 1;
					void save(
						base.map((p, i) =>
							i === last
								? {
										...p,
										tasks: [
											...p.tasks,
											{ content: draft.trim(), status: "pending" as const },
										],
									}
								: p,
						),
					).then(() => setDraft(""));
				}}
			>
				<TextInput
					className="h-7 py-0 text-xs"
					placeholder={t("panel.addTodo")}
					value={draft}
					onChange={(e) => setDraft(e.target.value)}
				/>
			</form>
			{error && <p className="mt-1 text-err text-xs">{error}</p>}
		</Card>
	);
}

function SubagentSection({ threadId }: { threadId: string }) {
	const { t } = useTranslation();
	const subs = useThread(threadId, (s) => s.subagents);
	const ready = useThread(threadId, (s) => s.agentStatus === "ready");
	const [snapshots, setSnapshots] = useState<
		{ id: string; agent: string; status: string; description?: string }[]
	>([]);
	useEffect(() => {
		if (!ready) return;
		realtime.cmd(threadId, { type: "get_subagents" }).then(
			(d) => setSnapshots(d.subagents),
			() => undefined,
		);
	}, [ready, threadId]);
	const live = Object.values(subs).filter((s) => s.id);
	const ids = new Set(live.map((s) => s.id));
	const rows = [
		...live.map((s) => ({
			id: s.id ?? "",
			agent: s.agent ?? "",
			status: s.status ?? "",
			description: s.description ?? s.task,
		})),
		...snapshots.filter((s) => !ids.has(s.id)),
	];
	return (
		<Card title={t("panel.subagents")} icon="robot">
			{rows.length === 0 && (
				<EmptyLine icon="robot">{t("panel.noSubagents")}</EmptyLine>
			)}
			{rows.length > 0 && (
				<ul className="-mx-1.5 flex flex-col gap-px">
					{rows.map((s) => {
						const running = s.status === "started" || s.status === "running";
						return (
							<li key={s.id}>
								<ListRow
									leading={
										<StatusDot
											tone={
												running
													? "accent"
													: s.status === "failed"
														? "err"
														: "neutral"
											}
											pulse={running}
										/>
									}
									title={
										<span className="truncate font-medium">{s.agent}</span>
									}
									meta={
										s.description && (
											<span className="min-w-0 truncate">{s.description}</span>
										)
									}
									onClick={() =>
										uiStore.getState().open({ kind: "subagent", id: s.id })
									}
								/>
							</li>
						);
					})}
				</ul>
			)}
		</Card>
	);
}

/** 上下文报告的分项（只读取已有缓存，不主动请求）：消息、工具定义、其余合计为系统提示词。 */
function useContextSegments(threadId: string) {
	const { t } = useTranslation();
	const report = useRpc(threadId, { type: "get_context_report" }, false);
	const b = report.data?.breakdown;
	if (!b || !(b.usedTokens > 0)) return null;
	return [
		{
			key: "messages",
			label: t("report.ctx.messages"),
			tokens: b.messagesTokens,
			className: "bg-fg/80",
		},
		{
			key: "tools",
			label: t("report.ctx.systemTools"),
			tokens: b.systemToolsTokens,
			className: "bg-fg/45",
		},
		{
			key: "system",
			label: t("report.ctx.systemPrompt"),
			tokens:
				b.systemPromptTokens +
				b.systemContextTokens +
				b.instructionFilesTokens +
				b.skillsTokens,
			className: "bg-fg/25",
		},
	].map((s) => ({ ...s, share: s.tokens / b.usedTokens }));
}

function ContextSection({ threadId }: { threadId: string }) {
	const { t } = useTranslation();
	const ctx = useThread(threadId, (s) => s.state?.contextUsage);
	const segments = useContextSegments(threadId);
	const percent = ctx ? Math.min(100, Math.max(0, ctx.percent)) : 0;
	return (
		<Card
			title={t("panel.context")}
			icon="database"
			action={
				<IconButton
					icon="info"
					size={13}
					box="xs"
					className="-mr-1.5"
					label={t("report.get_context_report")}
					onClick={() =>
						uiStore
							.getState()
							.open({ kind: "report", report: "get_context_report" })
					}
				/>
			}
		>
			{ctx ? (
				<>
					<div className="mb-2 flex items-baseline gap-2">
						<span className="font-semibold text-lg tabular-nums tracking-tight">
							{formatPercent(ctx.percent)}
						</span>
						<span className="ml-auto text-faint text-xs tabular-nums">
							<span className="font-medium text-muted">
								{formatTokens(ctx.tokens)}
							</span>{" "}
							/ {formatTokens(ctx.contextWindow)}
						</span>
					</div>
					{/* 占用偏高时改用带阈值色的单段量表，分段的灰阶表达不了警示 */}
					{segments && thresholdTone(ctx.percent) === "neutral" ? (
						<>
							{/* biome-ignore lint/a11y/useSemanticElements: 原生 <meter> 无法画分段 */}
							<div
								role="meter"
								aria-label={t("panel.context")}
								aria-valuenow={Math.round(ctx.percent)}
								aria-valuemin={0}
								aria-valuemax={100}
								className="flex h-1.5 gap-0.5 overflow-hidden rounded-full bg-inset"
							>
								{segments.map((s) => (
									<span
										key={s.key}
										className={`h-full ${s.className}`}
										style={{ width: `${s.share * percent}%` }}
									/>
								))}
							</div>
							<div className="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-2xs text-faint">
								{segments.map((s) => (
									<span
										key={s.key}
										className="inline-flex items-center gap-1.5 whitespace-nowrap"
									>
										<i
											aria-hidden
											className={`h-[7px] w-[7px] rounded-[2px] ${s.className}`}
										/>
										{s.label}
										<b className="font-medium text-muted tabular-nums">
											{formatTokens(s.tokens)}
										</b>
									</span>
								))}
							</div>
						</>
					) : (
						<Meter
							value={ctx.percent}
							tone={thresholdTone(ctx.percent)}
							label={t("panel.context")}
						/>
					)}
				</>
			) : (
				<EmptyLine icon="database">{t("common.none")}</EmptyLine>
			)}
		</Card>
	);
}

const TOOL_ROWS: readonly {
	icon: IconName;
	report?: "get_usage" | "get_session_stats" | "get_jobs" | "get_active_tools";
}[] = [
	{ icon: "message" },
	{ icon: "coin", report: "get_usage" },
	{ icon: "list", report: "get_session_stats" },
	{ icon: "clock", report: "get_jobs" },
	{ icon: "wrench", report: "get_active_tools" },
];

function ToolsSection() {
	const { t } = useTranslation();
	return (
		<Card title={t("panel.tools")} icon="wrench">
			<ul className="-mx-1.5 flex flex-col gap-px">
				{TOOL_ROWS.map(({ icon, report }) => (
					<li key={icon}>
						<ListRow
							leading={
								<span className="grid h-6 w-6 place-items-center rounded-sm border border-border bg-inset text-muted">
									<Icon name={icon} size={13} aria-hidden />
								</span>
							}
							title={report ? t(`report.${report}`) : t("btw.title")}
							trailing={
								<Icon
									name="chevronRight"
									size={12}
									aria-hidden
									className="text-ghost"
								/>
							}
							onClick={() =>
								uiStore
									.getState()
									.open(report ? { kind: "report", report } : { kind: "btw" })
							}
						/>
					</li>
				))}
			</ul>
		</Card>
	);
}

export function InfoPanel({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	return (
		<aside
			className="flex h-full w-full flex-col gap-2 overflow-y-auto bg-raised p-2"
			aria-label={t("nav.infoPanel")}
		>
			<OrbSection threadId={threadId} />
			<ContextSection threadId={threadId} />
			<TodoSection threadId={threadId} />
			<SubagentSection threadId={threadId} />
			<ToolsSection />
		</aside>
	);
}
