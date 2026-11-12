/**
 * 右侧信息面板：orb 状态与 CPU / 内存迷你曲线（`GET orbs/{id}/metrics`）、todo（可编辑，`set_todos`）、
 * 子 agent 列表、上下文占用，以及报告 / btw 入口。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useOrbMetrics } from "../../api/queries";
import { keyed } from "../../lib/keys";
import type { TodoItem, TodoPhase } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { getThreadStore, useThread } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { Icon } from "../ui/icons";
import { Button, IconButton, Sparkline, TextInput } from "../ui/primitives";
import { useOrbOf } from "./ThreadHeader";

// 稳定的空数组，避免 zustand 选择器每次返回新引用
const NO_PHASES: readonly TodoPhase[] = [];

function Section({
	title,
	children,
	action,
}: {
	title: string;
	children: ReactNode;
	action?: ReactNode;
}) {
	return (
		<section className="border-border border-b px-4 py-3">
			<div className="mb-2 flex items-center justify-between">
				<h3 className="font-medium text-faint text-xs uppercase tracking-wide">
					{title}
				</h3>
				{action}
			</div>
			{children}
		</section>
	);
}

function OrbSection({ threadId }: { threadId: string }) {
	const { t } = useTranslation();
	const orb = useOrbOf(threadId);
	const metrics = useOrbMetrics(orb?.state === "deleted" ? undefined : orb?.id);
	if (!orb) {
		return (
			<Section title={t("panel.orb")}>
				<p className="text-faint text-sm">{t("panel.noOrb")}</p>
			</Section>
		);
	}
	const samples = metrics.data?.samples ?? [];
	const last = samples[samples.length - 1];
	return (
		<Section title={t("panel.orb")}>
			<div className="flex items-center gap-2 text-sm">
				<span
					className={`h-2 w-2 rounded-full ${orb.state === "ready" ? "bg-ok" : orb.state === "failed" ? "bg-err" : orb.state === "frozen" || orb.state === "stopped" ? "bg-faint" : "bg-warn"}`}
				/>
				<span>{t(`orbState.${orb.state}`)}</span>
				<span className="ml-auto font-mono text-faint text-xs">
					{orb.id.slice(0, 12)}
				</span>
			</div>
			<div className="mt-1 text-muted text-xs">
				{t("panel.orbSpec", {
					type: orb.type,
					cpus: orb.cpus,
					mem: Math.round(orb.memoryMB / 1024),
				})}
				{orb.providerName ? ` · ${orb.providerName}` : ""}
			</div>
			<div className="mt-3 grid grid-cols-2 gap-3">
				<div className="text-accent">
					<div className="flex justify-between text-[11px] text-muted">
						<span>CPU</span>
						<span className="font-mono">
							{last ? `${last.cpuPct.toFixed(0)}%` : "—"}
						</span>
					</div>
					<Sparkline
						values={samples.map((s) => s.cpuPct)}
						max={100}
						label={t("panel.cpuChart")}
					/>
				</div>
				<div className="text-ok">
					<div className="flex justify-between text-[11px] text-muted">
						<span>{t("panel.memory")}</span>
						<span className="font-mono">
							{last ? `${(last.memUsedMB / 1024).toFixed(1)}G` : "—"}
						</span>
					</div>
					<Sparkline
						values={samples.map((s) => s.memUsedMB)}
						max={last?.memLimitMB || undefined}
						label={t("panel.memChart")}
					/>
				</div>
			</div>
		</Section>
	);
}

const NEXT_STATUS: Record<TodoItem["status"], TodoItem["status"]> = {
	pending: "in_progress",
	in_progress: "completed",
	completed: "pending",
	abandoned: "pending",
};

const STATUS_MARK: Record<TodoItem["status"], string> = {
	pending: "○",
	in_progress: "◐",
	completed: "●",
	abandoned: "⊘",
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
	return (
		<Section title={t("panel.todos")}>
			{phases.length === 0 && (
				<p className="text-faint text-sm">{t("panel.noTodos")}</p>
			)}
			{keyed(phases, "p").map(({ key: pk, item: phase }, pi) => (
				<div key={pk} className="mb-2">
					{phase.name && (
						<div className="mb-1 text-muted text-xs">{phase.name}</div>
					)}
					<ul className="flex flex-col gap-0.5">
						{keyed(phase.tasks, "t").map(({ key: tk, item: task }, ti) => (
							<li key={tk} className="group flex items-start gap-1.5 text-sm">
								<button
									type="button"
									className={`mt-0.5 w-4 shrink-0 text-center ${task.status === "completed" ? "text-ok" : task.status === "in_progress" ? "text-accent" : "text-faint"}`}
									aria-label={t(`todo.status.${task.status}`)}
									title={t(`todo.status.${task.status}`)}
									onClick={() =>
										void save(
											update(pi, ti, { status: NEXT_STATUS[task.status] }),
										)
									}
								>
									{STATUS_MARK[task.status]}
								</button>
								<span
									className={`min-w-0 flex-1 ${task.status === "completed" || task.status === "abandoned" ? "text-faint line-through" : ""}`}
								>
									{task.content}
								</span>
								<button
									type="button"
									className="hidden text-faint hover:text-err group-hover:block"
									aria-label={t("common.delete")}
									onClick={() => void save(update(pi, ti, null))}
								>
									<Icon name="close" size={12} />
								</button>
							</li>
						))}
					</ul>
				</div>
			))}
			<form
				className="mt-1 flex gap-1"
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
		</Section>
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
		<Section title={t("panel.subagents")}>
			{rows.length === 0 && (
				<p className="text-faint text-sm">{t("panel.noSubagents")}</p>
			)}
			<ul className="flex flex-col gap-1">
				{rows.map((s) => (
					<li key={s.id}>
						<button
							type="button"
							className="flex w-full items-center gap-2 rounded px-1.5 py-1 text-left text-sm hover:bg-hover"
							onClick={() =>
								uiStore.getState().open({ kind: "subagent", id: s.id })
							}
						>
							<span
								className={`h-1.5 w-1.5 shrink-0 rounded-full ${s.status === "started" || s.status === "running" ? "bg-accent" : s.status === "failed" ? "bg-err" : "bg-faint"}`}
							/>
							<span className="shrink-0 font-medium">{s.agent}</span>
							<span className="min-w-0 flex-1 truncate text-muted text-xs">
								{s.description}
							</span>
						</button>
					</li>
				))}
			</ul>
		</Section>
	);
}

function ContextSection({ threadId }: { threadId: string }) {
	const { t } = useTranslation();
	const ctx = useThread(threadId, (s) => s.state?.contextUsage);
	return (
		<Section
			title={t("panel.context")}
			action={
				<IconButton
					icon="info"
					size={13}
					className="h-6 w-6"
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
					<div className="flex justify-between text-xs">
						<span>{t("status.context", { pct: Math.round(ctx.percent) })}</span>
						<span className="font-mono text-muted">
							{ctx.tokens.toLocaleString()} /{" "}
							{ctx.contextWindow.toLocaleString()}
						</span>
					</div>
					<div className="mt-1 h-1.5 rounded bg-inset">
						<div
							className={`h-full rounded ${ctx.percent > 85 ? "bg-err" : ctx.percent > 65 ? "bg-warn" : "bg-accent"}`}
							style={{ width: `${Math.min(100, ctx.percent)}%` }}
						/>
					</div>
				</>
			) : (
				<p className="text-faint text-sm">—</p>
			)}
		</Section>
	);
}

export function InfoPanel({
	threadId,
	onClose,
}: {
	threadId: string;
	onClose?: () => void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<aside
			className="flex h-full w-full flex-col overflow-y-auto bg-panel"
			aria-label={t("nav.infoPanel")}
		>
			{onClose && (
				<div className="flex items-center justify-between border-border border-b px-3 py-2">
					<span className="font-medium text-sm">{t("nav.infoPanel")}</span>
					<IconButton
						icon="close"
						label={t("common.close")}
						onClick={onClose}
					/>
				</div>
			)}
			<OrbSection threadId={threadId} />
			<ContextSection threadId={threadId} />
			<TodoSection threadId={threadId} />
			<SubagentSection threadId={threadId} />
			<section className="flex flex-wrap gap-1.5 px-4 py-3">
				<Button
					size="sm"
					icon="message"
					onClick={() => uiStore.getState().open({ kind: "btw" })}
				>
					{t("btw.title")}
				</Button>
				{(
					[
						"get_usage",
						"get_session_stats",
						"get_jobs",
						"get_active_tools",
					] as const
				).map((r) => (
					<Button
						key={r}
						size="sm"
						onClick={() =>
							uiStore.getState().open({ kind: "report", report: r })
						}
					>
						{t(`report.${r}`)}
					</Button>
				))}
			</section>
		</aside>
	);
}
