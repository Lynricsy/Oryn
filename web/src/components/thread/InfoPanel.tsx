/**
 * 右侧信息面板：orb 状态、规格与 CPU / 内存迷你曲线（`GET orbs/{id}/metrics`）、环境（最近 setup / resume）、受管服务（M4）、
 * 上下文占用（主动请求上下文报告，分消息 / 工具定义 / 系统提示词三段）、todo（可编辑，`set_todos`）、子 agent 列表，
 * 以及报告 / btw 入口。外观以 D 稿（`.dev/design/explore/d.html` 的右栏）为准。
 */
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useOrbMetrics } from "../../api/queries";
import { formatMemoryMB, formatPercent, formatTokens } from "../../lib/format";
import { keyed } from "../../lib/keys";
import type { ResponseData, TodoItem, TodoPhase } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { getThreadStore, useThread } from "../../state/stores";
import { uiStore, useUi } from "../../state/ui";
import {
	Card,
	ListRow,
	Meter,
	Stat,
	StatusDot,
	type Tone,
	thresholdTone,
} from "../ui/data";
import { Icon, type IconName } from "../ui/icons";
import { OrbBead } from "../ui/OrbBead";
import { IconButton, Sparkline } from "../ui/primitives";
import { ContextBar, ContextLegend, useContextParts } from "./ContextBreakdown";
import { EnvRuns } from "./EnvBanner";
import { ServicesSection } from "./ServicesSection";
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

/** 空的小节不单独占一张卡：只留卡头一行（图标 + 标题 + 右侧小字与可选的动作）。外观与 `Card` 的卡头一致。 */
function EmptyCard({
	title,
	icon,
	children,
	action,
}: {
	title: string;
	icon: IconName;
	children: ReactNode;
	action?: ReactNode;
}) {
	return (
		<section className="shrink-0 rounded-lg border border-border bg-panel">
			<header className="flex h-9 items-center gap-2 px-3 font-medium text-muted text-xs">
				<Icon name={icon} size={14} aria-hidden className="text-faint" />
				<h3 className="shrink-0">{title}</h3>
				<p className="min-w-0 flex-1 truncate text-right font-normal text-2xs text-faint">
					{children}
				</p>
				{action}
			</header>
		</section>
	);
}

/** 规格胶囊：细描边 + 内嵌底，图标 + 值（D 稿 `.spec`）；`strong` 时值用正文色加粗。 */
function Spec({
	icon,
	strong,
	children,
}: {
	icon: IconName;
	strong?: boolean;
	children: ReactNode;
}) {
	return (
		<span
			className={`inline-flex h-6 items-center gap-1.5 whitespace-nowrap rounded-sm border border-border bg-inset pr-2 pl-[7px] text-2xs ${strong ? "font-semibold text-fg" : "text-muted"}`}
		>
			<Icon name={icon} size={12} aria-hidden className="text-faint" />
			{children}
		</span>
	);
}

/** 迷你曲线：CPU 用强调色、内存用成功色（D 稿），越过阈值改染警告 / 错误色。 */
function chartTone(tone: Tone, base: string): string {
	if (tone === "warn") return "text-warn";
	if (tone === "err") return "text-err";
	return base;
}

/** 内嵌井里的一行读数：图标 + 标签、数值、迷你曲线（颜色由 `className` 的文字色决定）。细描边 + 内嵌底（D 稿 `.well`）。 */
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
		<div className="grid h-9 grid-cols-[62px_60px_minmax(0,1fr)] items-center rounded-md border border-border bg-inset px-2.5">
			<span className="flex items-center gap-[5px] font-medium text-2xs text-muted">
				<Icon name={icon} size={13} aria-hidden className="text-faint" />
				{label}
			</span>
			<span className="font-semibold text-sm tabular-nums tracking-tight">
				{value}
			</span>
			<span className={className}>{chart}</span>
		</div>
	);
}

/** 已运行时长：只取最大单位（47 分钟 / 3 小时 / 2 天），单位文字交给 Intl 按界面语言给出。 */
function formatUptime(ms: number, lang: string): string {
	const min = Math.max(1, Math.floor(ms / 60_000));
	const [n, unit] =
		min < 60
			? [min, "minute"]
			: min < 1440
				? [Math.floor(min / 60), "hour"]
				: [Math.floor(min / 1440), "day"];
	return new Intl.NumberFormat(lang, {
		style: "unit",
		unit,
		unitDisplay: "long",
	}).format(n);
}

/** 卡片收起状态（`uiStore.collapsedCards`，所有线程共用并存 localStorage）→ `Card` 的可收起属性。 */
function useCardFold(key: string) {
	const collapsed = useUi((s) => !!s.collapsedCards[key]);
	return {
		collapsible: true,
		collapsed,
		onToggle: () => uiStore.getState().toggleCard(key),
	};
}

function OrbSection({ threadId }: { threadId: string }) {
	const { t, i18n } = useTranslation();
	const orb = useOrbOf(threadId);
	const working = useThread(threadId, (s) => s.thread?.state === "working");
	const metrics = useOrbMetrics(orb?.state === "deleted" ? undefined : orb?.id);
	const fold = useCardFold("orb");
	if (!orb) {
		return (
			<Card
				title={t("panel.orb")}
				icon="box"
				{...fold}
				summary={<span className="truncate">{t("common.none")}</span>}
			>
				<EmptyLine icon="box">{t("panel.noOrb")}</EmptyLine>
			</Card>
		);
	}
	const samples = metrics.data?.samples ?? [];
	const last = samples[samples.length - 1];
	const cpuTone = last ? thresholdTone(last.cpuPct) : "neutral";
	const memLimit = last?.memLimitMB ?? 0;
	const memTone =
		last && memLimit > 0
			? thresholdTone((last.memUsedMB / memLimit) * 100)
			: "neutral";
	const uptime =
		orb.state === "ready" && orb.stateChangedAt
			? formatUptime(Date.now() - orb.stateChangedAt, i18n.language)
			: undefined;
	return (
		<Card
			title={t("panel.orb")}
			icon="box"
			{...fold}
			summary={
				// 收起时：状态珠 + 状态文字、CPU、内存（环境小节随 orb 卡一起收起）
				<span className="flex min-w-0 items-center gap-3">
					<span className="inline-flex min-w-0 items-center gap-1.5">
						<OrbBead state={orb.state} active={working} size={12} />
						<span className="truncate text-fg">
							{t(`orbState.${orb.state}`)}
						</span>
					</span>
					{last && (
						<>
							<Stat icon="cpu" label={t("panel.cpu")}>
								{formatPercent(last.cpuPct)}
							</Stat>
							<Stat icon="memory" label={t("panel.memory")}>
								{formatMemoryMB(last.memUsedMB)}
							</Stat>
						</>
					)}
				</span>
			}
			action={
				orb.state !== "deleted" && (
					<IconButton
						icon="terminal"
						size={13}
						box="xs"
						className="-mr-1.5"
						label={t("toolRender.terminal.viewInPanel")}
						onClick={() => uiStore.getState().setPanelTab("terminal")}
					/>
				)
			}
		>
			<div className="flex items-center gap-2.5">
				<OrbBead state={orb.state} active={working} size={20} />
				<div className="min-w-0">
					<div className="font-semibold text-sm leading-[18px]">
						{t(`orbState.${orb.state}`)}
					</div>
					{uptime && (
						<div className="text-2xs text-faint leading-4">
							{t("panel.uptime", { d: uptime })}
						</div>
					)}
				</div>
				<span className="ml-auto self-start font-mono text-2xs text-faint leading-[18px]">
					{orb.id.slice(0, 12)}
				</span>
			</div>
			<div className="mt-[11px] mb-2.5 flex flex-wrap gap-[5px]">
				<Spec icon="box" strong>
					{orb.type}
				</Spec>
				<Spec icon="cpu" strong>
					{t("panel.cores", { n: orb.cpus })}
				</Spec>
				<Spec icon="memory" strong>
					{formatMemoryMB(orb.memoryMB)}
				</Spec>
				{orb.providerName && <Spec icon="hardDrives">{orb.providerName}</Spec>}
			</div>
			<div className="flex flex-col gap-1.5">
				<MetricWell
					icon="cpu"
					label={t("panel.cpu")}
					value={last ? formatPercent(last.cpuPct) : "—"}
					className={chartTone(cpuTone, "text-accent")}
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
					className={chartTone(memTone, "text-ok")}
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
			<ServicesSection threadId={threadId} orb={orb} />
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
	// 没有待办时卡片只占一行；点「+」才展开出输入框并聚焦，空着离开就收回一行
	const [adding, setAdding] = useState(false);
	const inputRef = useRef<HTMLInputElement>(null);
	useEffect(() => {
		if (adding) inputRef.current?.focus();
	}, [adding]);
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
	const fold = useCardFold("todos");
	if (tasks.length === 0 && !adding)
		return (
			<EmptyCard
				title={t("panel.todos")}
				icon="list"
				action={
					<IconButton
						icon="plus"
						size={13}
						box="xs"
						className="-mr-1.5"
						label={t("panel.addTodo")}
						onClick={() => {
							if (fold.collapsed) fold.onToggle();
							setAdding(true);
						}}
					/>
				}
			>
				{t("panel.noTodos")}
			</EmptyCard>
		);
	return (
		<Card
			title={t("panel.todos")}
			icon="list"
			// 进度（2/4 + 小进度条）本来就在卡头动作位，收起时原位保留，不再另给摘要
			{...fold}
			action={
				total > 0 ? (
					<span className="flex items-center gap-2 text-2xs text-faint tabular-nums">
						{done}/{total}
						<Meter
							value={done}
							max={total}
							tone="ok"
							label={t("panel.todos")}
							className="h-1! w-11 inset-ring-1 inset-ring-border/70"
						/>
					</span>
				) : (
					tasks.length === 0 && (
						// 空时紧凑：说明放进卡头一行小字，正文只剩「添加待办」输入框
						<span className="font-normal text-2xs text-faint">
							{t("panel.noTodos")}
						</span>
					)
				)
			}
		>
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
				className={`flex gap-1 ${tasks.length > 0 ? "mt-1.5" : ""}`}
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
				{/* 卡片里的输入框只用内嵌底色，不再画第三层边框；焦点环由全局 :focus-visible 给 */}
				<input
					ref={inputRef}
					className="h-7 w-full rounded-md bg-inset px-2.5 text-fg text-xs placeholder:text-faint"
					placeholder={t("panel.addTodo")}
					value={draft}
					onBlur={() => {
						if (!draft.trim()) setAdding(false);
					}}
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
	const fold = useCardFold("subagents");
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
	const running = rows.filter(
		(s) => s.status === "started" || s.status === "running",
	).length;
	// 没有子 agent 时不占位，等有了再出现
	if (rows.length === 0) return null;
	return (
		<Card
			title={t("panel.subagents")}
			icon="robot"
			{...fold}
			summary={
				running > 0 ? (
					<span className="inline-flex items-center gap-1.5">
						<StatusDot tone="accent" pulse />
						{t("panel.subagentsRunning", { n: running })}
					</span>
				) : (
					t("panel.subagentsTotal", { n: rows.length })
				)
			}
		>
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
								title={<span className="truncate font-medium">{s.agent}</span>}
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
		</Card>
	);
}

const CONTEXT_FRAME = { type: "get_context_report" } as const;
/** 上下文分项随 token 变化重取的最短间隔。 */
const CONTEXT_REFRESH_MS = 15_000;
type ContextReport = ResponseData<"get_context_report">;
/**
 * 每份报告请求发出时的 `contextUsage.tokens`：随报告数据（缓存里的同一对象）一起记，面板重新挂载时也能判断
 * 缓存报告是否对得上当前上下文。报告对话框（`useRpc`）取到的报告不在表里，按「对不上」处理。
 */
const reportTokens = new WeakMap<ContextReport, number | undefined>();

/**
 * 上下文报告（`get_context_report`，omp 本地计算，不调 LLM）：面板打开时请求一次；之后报告对应的 token 数
 * 与当前 `contextUsage.tokens` 不一致就节流重取（距上次取到 ≥15 秒，期间的变化合并成一次）——包括关面板期间
 * 上下文变了、重开时缓存报告仍在保鲜期内的情况。与报告对话框共用同一个查询 key（`Dialogs.tsx` 的 `useRpc`）。
 */
function useContextBreakdown(threadId: string) {
	const ready = useThread(threadId, (s) => s.agentStatus === "ready");
	const tokens = useThread(threadId, (s) => s.state?.contextUsage?.tokens);
	const report = useQuery({
		queryKey: ["rpc", threadId, CONTEXT_FRAME],
		enabled: ready,
		retry: false,
		staleTime: CONTEXT_REFRESH_MS,
		refetchOnWindowFocus: false,
		// 快照按数据对象记：结构共享会把内容相同的新报告换回旧对象（带着旧快照），这里关掉
		structuralSharing: false,
		queryFn: async () => {
			const sentAt =
				getThreadStore(threadId).getState().state?.contextUsage?.tokens;
			const data = (await realtime.cmd(
				threadId,
				CONTEXT_FRAME,
			)) as ContextReport;
			reportTokens.set(data, sentAt);
			return data;
		},
	});
	const { data, dataUpdatedAt, errorUpdatedAt, isFetching, refetch } = report;
	useEffect(() => {
		// 请求在途时等它落地再比较，避免重复发
		if (!ready || !data || isFetching) return;
		if (reportTokens.has(data) && reportTokens.get(data) === tokens) return;
		// 节流从上次请求落地（成功或失败）算起，失败后不会立刻重发
		const wait =
			Math.max(dataUpdatedAt, errorUpdatedAt) + CONTEXT_REFRESH_MS - Date.now();
		const timer = window.setTimeout(() => void refetch(), Math.max(0, wait));
		return () => window.clearTimeout(timer);
	}, [ready, tokens, data, dataUpdatedAt, errorUpdatedAt, isFetching, refetch]);
	return data?.breakdown;
}

/** 占用偏高时百分比数字用阈值色（与量表外圈描边同一信号）。 */
const PERCENT_TONE: Partial<Record<Tone, string>> = {
	warn: "text-warn",
	err: "text-err",
};

function ContextSection({ threadId }: { threadId: string }) {
	const { t } = useTranslation();
	const ctx = useThread(threadId, (s) => s.state?.contextUsage);
	const breakdown = useContextBreakdown(threadId);
	const parts = useContextParts(threadId, breakdown);
	const tone = ctx ? thresholdTone(ctx.percent) : "neutral";
	const fold = useCardFold("context");
	return (
		<Card
			title={t("panel.context")}
			icon="database"
			{...fold}
			summary={
				ctx && (
					<span className="inline-flex items-center gap-2">
						<span className={PERCENT_TONE[tone] ?? "text-fg"}>
							{formatPercent(ctx.percent)}
						</span>
						{parts && breakdown ? (
							<ContextBar
								parts={parts}
								contextWindow={breakdown.contextWindow}
								tone={tone}
								label={t("panel.context")}
								compact
								className="w-12 shrink-0"
							/>
						) : (
							<Meter
								value={ctx.percent}
								tone={tone}
								label={t("panel.context")}
								className="w-12 shrink-0"
							/>
						)}
					</span>
				)
			}
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
					<div className="mb-[9px] flex items-baseline gap-2">
						<span
							className={`font-semibold text-xl tabular-nums leading-6 tracking-tight ${PERCENT_TONE[tone] ?? ""}`}
						>
							{formatPercent(ctx.percent)}
						</span>
						<span className="ml-auto text-faint text-xs tabular-nums">
							<span className="font-medium text-muted">
								{formatTokens(ctx.tokens)}
							</span>{" "}
							/ {formatTokens(ctx.contextWindow)}
						</span>
					</div>
					{parts && breakdown ? (
						<>
							<ContextBar
								parts={parts}
								contextWindow={breakdown.contextWindow}
								tone={tone}
								label={t("panel.context")}
							/>
							<ContextLegend
								parts={parts}
								contextWindow={breakdown.contextWindow}
							/>
						</>
					) : (
						// 报告还没取到：先用单段量表
						<Meter value={ctx.percent} tone={tone} label={t("panel.context")} />
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
	const fold = useCardFold("tools");
	// 工具卡是固定的入口列表，条数不随会话变，收起时不给摘要
	return (
		<Card title={t("panel.tools")} icon="wrench" {...fold}>
			<ul className="-mx-2 -mt-px -mb-2 flex flex-col">
				{TOOL_ROWS.map(({ icon, report }) => (
					<li key={icon}>
						{/* D 稿 `.lrow`：34px 行高，左侧 24px 细描边图标方块，行尾淡色箭头 */}
						<button
							type="button"
							className="flex h-[34px] w-full items-center gap-2.5 rounded-md pr-2 pl-1.5 text-left text-fg text-sm transition-colors duration-150 hover:bg-hover"
							onClick={() =>
								uiStore
									.getState()
									.open(report ? { kind: "report", report } : { kind: "btw" })
							}
						>
							<span className="grid h-6 w-6 shrink-0 place-items-center rounded-sm border border-border bg-inset text-muted">
								<Icon name={icon} size={13} aria-hidden />
							</span>
							<span className="min-w-0 flex-1 truncate">
								{report ? t(`report.${report}`) : t("btw.title")}
							</span>
							<Icon
								name="chevronRight"
								size={12}
								aria-hidden
								className="shrink-0 text-ghost"
							/>
						</button>
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
