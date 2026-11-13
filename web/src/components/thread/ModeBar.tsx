/**
 * 模式开关（计划 / goal / vibe / loop / prewalk / 暂停全部 agent）、模式状态（压缩、重试、扩展状态、命令错误）
 * 与扩展小部件；都嵌在线程输入坞的玻璃托盘里。
 */
import { type ReactNode, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { OmpCommand } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { runModeCommand } from "../../state/actions";
import { getThreadStore, useThread } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { Badge } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";
import { Spinner } from "../ui/primitives";

/** 模式命令的最近错误（按线程）：ModeStrip 写、ModeStatus 读。 */
const modeErrorStore = createStore<{
	errors: Record<string, string | undefined>;
	set: (threadId: string, error: string | undefined) => void;
}>((set) => ({
	errors: {},
	set: (threadId, error) =>
		set((s) => ({ errors: { ...s.errors, [threadId]: error } })),
}));

function ModeToggle({
	on,
	icon,
	label,
	title,
	onClick,
}: {
	on: boolean;
	icon: IconName;
	label: string;
	title?: string;
	onClick: () => void;
}) {
	return (
		<button
			type="button"
			aria-pressed={on}
			aria-label={label}
			title={title ?? label}
			onClick={onClick}
			className={`inline-flex h-[30px] shrink-0 items-center justify-center rounded-md transition-colors duration-150 ${
				on
					? "gap-1.5 bg-accent-soft pr-2.5 pl-2 font-medium text-accent-ink text-xs ring-1 ring-accent/25 ring-inset"
					: "w-[30px] text-faint hover:bg-hover hover:text-fg"
			}`}
		>
			<Icon name={icon} size={on ? 14 : 15} />
			{on && <span className="whitespace-nowrap">{label}</span>}
		</button>
	);
}

/** 六个模式开关：关时只显示图标，开时显示图标 + 文案。 */
export function ModeStrip({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const state = useThread(threadId, (s) => s.state);
	const ready = useThread(threadId, (s) => s.agentStatus === "ready");
	const vibe = useThread(threadId, (s) => s.vibeEnabled);
	const loop = useThread(threadId, (s) => s.loopMode);

	useEffect(() => {
		if (!ready) return;
		const store = getThreadStore(threadId);
		realtime.cmd(threadId, { type: "get_vibe_mode" }).then(
			(d) => store.getState().patchLocal({ vibeEnabled: d.enabled }),
			() => undefined,
		);
		realtime.cmd(threadId, { type: "get_loop_mode" }).then(
			(d) => store.getState().patchLocal({ loopMode: d }),
			() => undefined,
		);
	}, [ready, threadId]);

	const run = async (frame: OmpCommand) => {
		const setError = modeErrorStore.getState().set;
		setError(threadId, undefined);
		try {
			await runModeCommand(threadId, frame);
		} catch (e) {
			setError(threadId, e instanceof Error ? e.message : String(e));
		}
	};

	const goal = state?.goal;
	return (
		<div className="focus-inset flex min-w-0 shrink items-center gap-0.5 overflow-x-auto">
			<ModeToggle
				on={state?.planModeEnabled ?? false}
				icon="list"
				label={t("mode.plan")}
				onClick={() =>
					void run({
						type: "set_plan_mode",
						enabled: !(state?.planModeEnabled ?? false),
					})
				}
			/>
			<ModeToggle
				on={Boolean(goal)}
				icon="target"
				label={
					goal
						? t("mode.goalOn", { status: goal.goal?.status ?? "" })
						: t("mode.goal")
				}
				onClick={() => uiStore.getState().open({ kind: "goal" })}
			/>
			<ModeToggle
				on={vibe ?? false}
				icon="bolt"
				label={t("mode.vibe")}
				onClick={() =>
					void run({ type: "set_vibe_mode", enabled: !(vibe ?? false) })
				}
			/>
			<ModeToggle
				on={loop?.enabled ?? false}
				icon="play"
				label={
					loop?.enabled
						? t("mode.loopOn", { state: t(`mode.loopState.${loop.state}`) })
						: t("mode.loop")
				}
				title={loop?.prompt || undefined}
				onClick={() =>
					void run({
						type: "set_loop_mode",
						enabled: !(loop?.enabled ?? false),
					})
				}
			/>
			<ModeToggle
				on={state?.prewalkArmed ?? false}
				icon="clock"
				label={t("mode.prewalk")}
				onClick={() =>
					void run({
						type: "set_prewalk",
						enabled: !(state?.prewalkArmed ?? false),
					})
				}
			/>
			<ModeToggle
				on={state?.agentsPaused ?? false}
				icon="pause"
				label={t("mode.pauseAgents")}
				onClick={() =>
					void run({
						type: "set_agents_paused",
						enabled: !(state?.agentsPaused ?? false),
					})
				}
			/>
		</div>
	);
}

/** 压缩中、重试中、扩展状态与模式命令错误；什么都没有时不渲染。 */
export function ModeStatus({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const compaction = useThread(threadId, (s) => s.compaction);
	const retry = useThread(threadId, (s) => s.retry);
	const statuses = useThread(threadId, (s) => s.statuses);
	const error = useStore(modeErrorStore, (s) => s.errors[threadId]);
	const entries = Object.entries(statuses);
	if (!compaction && !retry && entries.length === 0 && !error) return null;
	return (
		<span className="flex min-w-0 flex-wrap items-center gap-1.5">
			{compaction && (
				<span className="inline-flex shrink-0 items-center gap-1 text-muted">
					<Spinner size={10} />
					{t("mode.compacting")}
				</span>
			)}
			{retry && (
				<Badge tone="warn" title={retry.errorMessage}>
					{t("mode.retrying", {
						attempt: retry.attempt,
						max: retry.maxAttempts,
					})}
				</Badge>
			)}
			{entries.map(([k, v]) => (
				<Badge key={k} title={k}>
					{v}
				</Badge>
			))}
			{error && (
				<span className="min-w-0 truncate text-err" title={error}>
					{error}
				</span>
			)}
		</span>
	);
}

function Widgets({
	threadId,
	placement,
}: {
	threadId: string;
	placement: "aboveEditor" | "belowEditor";
}): ReactNode {
	const widgets = useThread(threadId, (s) => s.widgets);
	const list = Object.entries(widgets).filter(
		([, w]) => w.placement === placement,
	);
	if (list.length === 0) return null;
	return (
		<div>
			{list.map(([key, w]) => (
				<pre
					key={key}
					className="overflow-x-auto px-3 py-1.5 font-mono text-2xs text-muted"
				>
					{w.lines.join("\n")}
				</pre>
			))}
		</div>
	);
}

/** 输入框上方的扩展小部件（托盘里的一节）。 */
export function AboveEditorWidgets({
	threadId,
}: {
	threadId: string;
}): ReactNode {
	return <Widgets threadId={threadId} placement="aboveEditor" />;
}

/** 输入框下方的扩展小部件（托盘里的一节）。 */
export function BelowEditorWidgets({
	threadId,
}: {
	threadId: string;
}): ReactNode {
	return <Widgets threadId={threadId} placement="belowEditor" />;
}
