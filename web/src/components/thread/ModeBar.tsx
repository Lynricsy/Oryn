/**
 * 模式开关（计划 / goal / vibe / loop / prewalk / 暂停全部 agent）、模式状态（压缩、重试、扩展状态、命令错误）
 * 与扩展小部件；都嵌在线程输入坞的玻璃托盘里。
 */
import {
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { OmpCommand } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { runModeCommand } from "../../state/actions";
import { agentQueryable, getThreadStore, useThread } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { Badge } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";
import { Menu, type MenuItem, Spinner } from "../ui/primitives";

/** 模式命令的最近错误（按线程）：ModeStrip 写、ModeStatus 读。 */
const modeErrorStore = createStore<{
	errors: Record<string, string | undefined>;
	set: (threadId: string, error: string | undefined) => void;
}>((set) => ({
	errors: {},
	set: (threadId, error) =>
		set((s) => ({ errors: { ...s.errors, [threadId]: error } })),
}));

/**
 * 托盘里的设置类命令（模式开关、思考级别、速度）：执行后刷新状态；失败写进 modeErrorStore，
 * 由 ModeStatus 在托盘里显示，下一次命令开始时清掉。返回是否成功，供连续几步的操作在失败处停下。
 */
export async function runTrayCommand(
	threadId: string,
	frame: OmpCommand,
): Promise<boolean> {
	const setError = modeErrorStore.getState().set;
	setError(threadId, undefined);
	try {
		await runModeCommand(threadId, frame);
		return true;
	} catch (e) {
		setError(threadId, e instanceof Error ? e.message : String(e));
		return false;
	}
}

interface ModeDef {
	key: string;
	on: boolean;
	icon: IconName;
	label: string;
	title?: string;
	onClick: () => void;
}

function toggleClass(on: boolean): string {
	return `inline-flex h-[30px] shrink-0 items-center justify-center rounded-md transition-colors duration-150 ${
		on
			? "gap-1.5 bg-accent-soft pr-2.5 pl-2 font-medium text-accent-ink text-xs ring-1 ring-accent/25 ring-inset"
			: "w-[30px] text-faint hover:bg-hover hover:text-fg"
	}`;
}

function ModeToggle({ mode }: { mode: ModeDef }) {
	return (
		<button
			type="button"
			aria-pressed={mode.on}
			aria-label={mode.label}
			title={mode.title ?? mode.label}
			onClick={mode.onClick}
			className={toggleClass(mode.on)}
		>
			<Icon name={mode.icon} size={mode.on ? 14 : 15} />
			{mode.on && <span className="whitespace-nowrap">{mode.label}</span>}
		</button>
	);
}

/** 「更多」按钮宽度与开关间距（px），与 `toggleClass`、`gap-0.5` 一致。 */
const MORE_W = 30;
const GAP = 2;

/** 前 k 个开关加上（有剩余时的）「更多」按钮能放进 `avail` 的最大 k。 */
function fitCount(widths: number[], avail: number): number {
	let used = 0;
	for (let k = 0; k < widths.length; k++) {
		const w = (widths[k] ?? 0) + (k ? GAP : 0);
		const more = k < widths.length - 1 ? GAP + MORE_W : 0;
		if (used + w + more > avail) return k;
		used += w;
	}
	return widths.length;
}

/**
 * 六个模式开关：关时只显示图标，开时显示图标 + 文案。
 * 放不下的开关按顺序收进末尾的「更多」菜单，任何宽度下都不出现半截图标。
 * 宽度量的是一排不可见的同尺寸替身（不进无障碍树），开关开合、容器缩放时重算。
 */
export function ModeStrip({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const state = useThread(threadId, (s) => s.state);
	const ready = useThread(threadId, agentQueryable);
	const vibe = useThread(threadId, (s) => s.vibeEnabled);
	const loop = useThread(threadId, (s) => s.loopMode);
	const outerRef = useRef<HTMLDivElement>(null);
	const measureRef = useRef<HTMLDivElement>(null);
	// 没有 ResizeObserver（jsdom）时全部显示
	const [fit, setFit] = useState(Number.POSITIVE_INFINITY);

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

	useLayoutEffect(() => {
		const outer = outerRef.current;
		const measure = measureRef.current;
		if (!outer || !measure || typeof ResizeObserver === "undefined") return;
		const compute = () => {
			const widths = [...measure.children].map(
				(c) => (c as HTMLElement).offsetWidth,
			);
			setFit(fitCount(widths, outer.clientWidth));
		};
		const ro = new ResizeObserver(compute);
		ro.observe(outer);
		ro.observe(measure);
		compute();
		return () => ro.disconnect();
	}, []);

	const run = (frame: OmpCommand) => runTrayCommand(threadId, frame);

	const goal = state?.goal;
	const modes: ModeDef[] = [
		{
			key: "plan",
			on: state?.planModeEnabled ?? false,
			icon: "list",
			label: t("mode.plan"),
			onClick: () =>
				void run({
					type: "set_plan_mode",
					enabled: !(state?.planModeEnabled ?? false),
				}),
		},
		{
			key: "goal",
			on: Boolean(goal),
			icon: "target",
			label: goal
				? t("mode.goalOn", { status: goal.goal?.status ?? "" })
				: t("mode.goal"),
			onClick: () => uiStore.getState().open({ kind: "goal" }),
		},
		{
			key: "vibe",
			on: vibe ?? false,
			icon: "bolt",
			label: t("mode.vibe"),
			onClick: () =>
				void run({ type: "set_vibe_mode", enabled: !(vibe ?? false) }),
		},
		{
			key: "loop",
			on: loop?.enabled ?? false,
			icon: "play",
			label: loop?.enabled
				? t("mode.loopOn", { state: t(`mode.loopState.${loop.state}`) })
				: t("mode.loop"),
			title: loop?.prompt || undefined,
			onClick: () =>
				void run({
					type: "set_loop_mode",
					enabled: !(loop?.enabled ?? false),
				}),
		},
		{
			key: "prewalk",
			on: state?.prewalkArmed ?? false,
			icon: "clock",
			label: t("mode.prewalk"),
			onClick: () =>
				void run({
					type: "set_prewalk",
					enabled: !(state?.prewalkArmed ?? false),
				}),
		},
		{
			key: "pause",
			on: state?.agentsPaused ?? false,
			icon: "pause",
			label: t("mode.pauseAgents"),
			onClick: () =>
				void run({
					type: "set_agents_paused",
					enabled: !(state?.agentsPaused ?? false),
				}),
		},
	];
	const shown = modes.slice(0, fit);
	const rest = modes.slice(fit);
	const restOn = rest.some((m) => m.on);
	const items: MenuItem[] = rest.map((m) => ({
		key: m.key,
		icon: m.icon,
		label: m.label,
		hint: m.on ? (
			<Icon name="check" size={13} weight="bold" className="text-accent" />
		) : undefined,
		onSelect: m.onClick,
	}));

	return (
		<div
			ref={outerRef}
			className="relative flex min-w-[30px] flex-1 items-center gap-0.5"
		>
			<div
				ref={measureRef}
				aria-hidden
				inert
				className="pointer-events-none invisible absolute top-0 left-0 flex gap-0.5"
			>
				{modes.map((m) => (
					<span key={m.key} className={toggleClass(m.on)}>
						<Icon name={m.icon} size={m.on ? 14 : 15} />
						{m.on && <span className="whitespace-nowrap">{m.label}</span>}
					</span>
				))}
			</div>
			{shown.map((m) => (
				<ModeToggle key={m.key} mode={m} />
			))}
			{rest.length > 0 && (
				<Menu
					side="top"
					align="left"
					items={items}
					trigger={(p) => (
						<button
							type="button"
							aria-label={t("mode.more")}
							title={t("mode.more")}
							className={`relative inline-flex h-[30px] w-[30px] shrink-0 items-center justify-center rounded-md transition-colors duration-150 hover:bg-hover hover:text-fg ${restOn ? "text-accent-ink" : "text-faint"}`}
							{...p}
						>
							<Icon name="more" size={15} />
							{restOn && (
								<span
									aria-hidden
									className="absolute top-1 right-1 h-1.5 w-1.5 rounded-full bg-accent"
								/>
							)}
						</button>
					)}
				/>
			)}
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
