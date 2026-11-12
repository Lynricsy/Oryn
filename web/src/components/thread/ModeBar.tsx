/**
 * 模式条：计划 / goal / vibe / loop / prewalk / 暂停全部 agent 的开关，以及压缩、重试、扩展状态与小部件。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { OmpCommand, SessionState } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { getThreadStore, useThread } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { Icon, type IconName } from "../ui/icons";
import { Spinner } from "../ui/primitives";

function Chip({
	on,
	icon,
	label,
	onClick,
	title,
}: {
	on: boolean;
	icon: IconName;
	label: ReactNode;
	onClick: () => void;
	title?: string;
}) {
	return (
		<button
			type="button"
			aria-pressed={on}
			title={title}
			onClick={onClick}
			className={`inline-flex h-6 shrink-0 items-center gap-1 rounded-full border px-2 text-[11px] transition-colors ${on ? "border-accent/60 bg-accent/10 text-accent" : "border-border text-muted hover:text-fg"}`}
		>
			<Icon name={icon} size={11} />
			{label}
		</button>
	);
}

export function ModeBar({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const state = useThread(threadId, (s) => s.state);
	const ready = useThread(threadId, (s) => s.agentStatus === "ready");
	const vibe = useThread(threadId, (s) => s.vibeEnabled);
	const loop = useThread(threadId, (s) => s.loopMode);
	const compaction = useThread(threadId, (s) => s.compaction);
	const retry = useThread(threadId, (s) => s.retry);
	const statuses = useThread(threadId, (s) => s.statuses);
	const widgets = useThread(threadId, (s) => s.widgets);
	const [error, setError] = useState<string | null>(null);

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
		setError(null);
		try {
			const data = await realtime.cmd(threadId, frame);
			const store = getThreadStore(threadId);
			if (
				frame.type === "set_vibe_mode" &&
				data &&
				typeof data === "object" &&
				"enabled" in data
			) {
				store.getState().patchLocal({ vibeEnabled: data.enabled === true });
			}
			const st = (await realtime.cmd(threadId, {
				type: "get_state",
			})) as SessionState;
			store.getState().apply([{ type: "state", state: st }]);
		} catch (e) {
			setError(e instanceof Error ? e.message : String(e));
		}
	};

	const goal = state?.goal;
	const above = Object.entries(widgets).filter(
		([, w]) => w.placement === "aboveEditor",
	);
	return (
		<div className="mx-auto mb-1.5 w-full max-w-3xl">
			{above.map(([key, w]) => (
				<pre
					key={key}
					className="mb-1 overflow-x-auto rounded-md bg-inset px-2 py-1 font-mono text-[11px] text-muted"
				>
					{w.lines.join("\n")}
				</pre>
			))}
			<div className="flex items-center gap-1.5 overflow-x-auto pb-0.5">
				<Chip
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
				<Chip
					on={Boolean(goal)}
					icon="target"
					label={
						goal
							? t("mode.goalOn", { status: goal.goal?.status ?? "" })
							: t("mode.goal")
					}
					onClick={() => uiStore.getState().open({ kind: "goal" })}
				/>
				<Chip
					on={vibe ?? false}
					icon="bolt"
					label={t("mode.vibe")}
					onClick={() =>
						void run({ type: "set_vibe_mode", enabled: !(vibe ?? false) })
					}
				/>
				<Chip
					on={loop?.enabled ?? false}
					icon="play"
					label={
						loop?.enabled
							? t("mode.loopOn", { state: t(`mode.loopState.${loop.state}`) })
							: t("mode.loop")
					}
					title={loop?.prompt}
					onClick={() =>
						void run({
							type: "set_loop_mode",
							enabled: !(loop?.enabled ?? false),
						})
					}
				/>
				<Chip
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
				<Chip
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
				{compaction && (
					<span className="inline-flex shrink-0 items-center gap-1 text-[11px] text-muted">
						<Spinner size={10} />
						{t("mode.compacting")}
					</span>
				)}
				{retry && (
					<span
						className="inline-flex shrink-0 items-center gap-1 text-[11px] text-warn"
						title={retry.errorMessage}
					>
						<Spinner size={10} />
						{t("mode.retrying", {
							attempt: retry.attempt,
							max: retry.maxAttempts,
						})}
					</span>
				)}
				{Object.entries(statuses).map(([k, v]) => (
					<span
						key={k}
						className="shrink-0 rounded bg-inset px-1.5 text-[11px] text-muted"
					>
						{v}
					</span>
				))}
			</div>
			{error && <p className="text-err text-xs">{error}</p>}
		</div>
	);
}

export function BelowEditorWidgets({
	threadId,
}: {
	threadId: string;
}): ReactNode {
	const widgets = useThread(threadId, (s) => s.widgets);
	const below = Object.entries(widgets).filter(
		([, w]) => w.placement === "belowEditor",
	);
	if (below.length === 0) return null;
	return (
		<div className="mx-auto mt-1 w-full max-w-3xl">
			{below.map(([key, w]) => (
				<pre
					key={key}
					className="overflow-x-auto rounded-md bg-inset px-2 py-1 font-mono text-[11px] text-muted"
				>
					{w.lines.join("\n")}
				</pre>
			))}
		</div>
	);
}
