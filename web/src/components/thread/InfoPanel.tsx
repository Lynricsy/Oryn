/**
 * 右侧信息面板，纸面上只有一层底色（版式件见 `info-parts.tsx`）：
 * - 浅色块：orb（徽记、状态、故障提示行、规格）、CPU / 内存（`GET orbs/{id}/metrics` 的面积曲线）、
 *   上下文占用（主动请求上下文报告，分段条 + 图例）；
 * - 纸面上的分组：todo（可编辑，`set_todos`）、子 agent 树（含已入库、agent 离线时仍可看的子 agent 会话）；
 * - 入口行：服务（M4）、环境（最近 setup / resume）、浏览器 Profile（M8）、能力（只读）、SSH 主机（M18），
 *   以及报告 / btw 入口。
 */
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useBoxOfOrb } from "../../api/box-queries";
import type { Orb, Schemas } from "../../api/client";
import {
	useOrbMetrics,
	useRunnerOfOrb,
	useServiceAction,
} from "../../api/queries";
import {
	formatOrbCpus,
	formatOrbMemory,
	formatPercent,
	formatTokens,
	formatUptime,
} from "../../lib/format";
import { keyed } from "../../lib/keys";
import { contentTabs } from "../../lib/orb-features";
import type { ResponseData, TodoItem, TodoPhase } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { wakeThread } from "../../state/actions";
import { agentQueryable, getThreadStore, useThread } from "../../state/stores";
import {
	type SubagentNode,
	subagentVisualState,
} from "../../state/subagent-tree";
import { uiStore, useUi } from "../../state/ui";
import { BoxStartLink } from "../boxes/BoxStartLink";
import { boxThreadLabel } from "../boxes/box-model";
import { DesktopProfileRow } from "../desktop/profile-status";
import { AgentAvatar, agentStateLabel } from "../transcript/AgentAvatar";
import { AgentStatusRing } from "../ui/AgentStatusAvatar";
import { ListRow, Meter, type Tone, thresholdTone } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";
import { DockerBadge } from "../ui/OrbBadges";
import { OrbEmblem } from "../ui/OrbEmblem";
import { Button, ErrorText, IconButton, Spinner } from "../ui/primitives";
import { CapabilitiesRow } from "./CapabilitiesSection";
import { ContextBar, ContextLegend, useContextParts } from "./ContextBreakdown";
import { openSubagent, useSubagentTree } from "./Dialogs";
import { EnvRow } from "./EnvBanner";
import {
	BigValue,
	InfoBlock,
	InfoGroup,
	InfoLink,
	OrbMetricTiles,
	Spec,
} from "./info-parts";
import { JobsSection } from "./JobsSection";
import { SandboxSection } from "./SandboxSection";
import {
	faultyServices,
	ServiceLogDialog,
	ServicesRow,
} from "./ServicesSection";
import { SshSection } from "./SshSection";
import { useOrbOf } from "./ThreadHeader";

// 稳定的空数组，避免 zustand 选择器每次返回新引用
const NO_PHASES: readonly TodoPhase[] = [];

type OrbService = Schemas["OrbService"];
type SetupRun = NonNullable<Orb["lastSetup"]>;

/** 块内的紧凑空态：图标 + 一行说明。 */
function EmptyLine({
	icon,
	children,
}: {
	icon: IconName;
	children: ReactNode;
}) {
	return (
		<p className="flex items-center gap-2 text-faint text-xs">
			<Icon name={icon} size={14} aria-hidden className="shrink-0" />
			{children}
		</p>
	);
}

/** 分组收起状态（`uiStore.collapsedCards`，所有线程共用并存 localStorage）。 */
function useCardFold(key: string) {
	const collapsed = useUi((s) => !!s.collapsedCards[key]);
	return {
		collapsed,
		onToggle: () => uiStore.getState().toggleCard(key),
	};
}

/** orb 停机时状态行右侧的唤醒按钮（打开线程页不会自动唤醒：冷启动代价高）。 */
function WakeButton({ threadId }: { threadId: string }) {
	const { t } = useTranslation();
	const [pending, setPending] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const wake = async () => {
		setPending(true);
		setError(null);
		try {
			await wakeThread(threadId);
		} catch (e) {
			setError(e);
		} finally {
			setPending(false);
		}
	};
	return (
		<div className="ml-auto flex min-w-0 flex-col items-end gap-1 text-right">
			<Button size="sm" disabled={pending} onClick={() => void wake()}>
				{pending ? <Spinner size={12} /> : null}
				{t("thread.orbWake")}
			</Button>
			{error !== null && <ErrorText error={error} />}
		</div>
	);
}

const FAULT_ACTION =
	"inline-flex h-7 items-center gap-1.5 whitespace-nowrap rounded-full px-2 font-semibold transition-colors duration-150 disabled:opacity-40";

/** orb 块里的提示行：状态点 + 一句话，右侧是轻量的动作（不加底色，悬停才出浅底）。重启过程中点换成转圈，恢复后变绿。 */
function FaultLine({
	mark = "err",
	message,
	title,
	children,
}: {
	mark?: "err" | "ok" | "pending";
	message: ReactNode;
	title?: string;
	children?: ReactNode;
}) {
	return (
		<div className="mt-2.5 flex h-7 items-center gap-2 text-sm">
			{mark === "pending" ? (
				<span className="inline-flex w-3 shrink-0 justify-center text-faint">
					<Spinner size={12} />
				</span>
			) : (
				<span
					aria-hidden
					className={`mx-0.5 h-2 w-2 shrink-0 rounded-full ${mark === "ok" ? "bg-ok" : "bg-err"}`}
				/>
			)}
			{/* 重启的进度与结果由读屏播报 */}
			<span role="status" title={title} className="min-w-0 flex-1 truncate">
				{message}
			</span>
			{children && (
				<span className="-mr-2 flex shrink-0 items-center">{children}</span>
			)}
		</div>
	);
}

/** 重启后状态保持 `running` 这么久才算恢复：没有就绪探测的服务一拉起就是 running，短命的会在这段时间里退出、进退避。 */
export const RESTART_STABLE_MS = 5_000;
/** 「已恢复」显示多久后收起。 */
export const RESTART_RECOVERED_MS = 3_000;
/** 重启中最多等多久：orbd 60s 未就绪会标 unhealthy，再等不到结局（多半是实时连接断了）就按未恢复处理。 */
export const RESTART_GIVE_UP_MS = 90_000;

/**
 * 一次从提示行发起的重启，`step`：
 * - `sending` 请求在途；`waiting` 已被接受，但 orb 里还是重启前的旧状态（失败的旧状态不能当成结果）；
 * - `armed` 已看到重启后的非失败状态，从此再失败才算重启失败；
 * - `recovered` 稳定 running；`failed` 重启请求失败、重启后又失败、或等不到结局。
 * orbd 的结局：稳定 running、failed（连续短命）、unhealthy（60s 未就绪）；launch 失败直接回 stopped。
 */
interface RestartWatch {
	name: string;
	step: "sending" | "waiting" | "armed" | "recovered" | "failed";
}

/** `setWatch` 的更新函数：换到下一步；已收起（null）时不动。 */
const stepTo =
	(next: RestartWatch["step"]) =>
	(w: RestartWatch | null): RestartWatch | null =>
		w && { ...w, step: next };

const RESTART_FAILED: Partial<Record<OrbService["status"], true>> = {
	failed: true,
	unhealthy: true,
	stopped: true,
};

/**
 * orb 块的故障提示：失败的服务给出重启与日志（详情在「服务」入口行），重启过程中显示「重启中…」，
 * 结束后短暂显示「已恢复」或停在「仍然失败」；没有服务故障时退到 setup / resume 失败。
 * 自己持有重启状态，服务恢复后（已不在故障列表里）也要留在原位把结果说完。
 */
function FaultNotice({
	orb,
	faulty,
	failedRun,
}: {
	orb: Orb;
	faulty: readonly OrbService[];
	failedRun: SetupRun | undefined;
}) {
	const { t } = useTranslation();
	const action = useServiceAction(orb.id);
	const [watch, setWatch] = useState<RestartWatch | null>(null);
	const [log, setLog] = useState<string | null>(null);
	const name = watch?.name ?? faulty[0]?.name;
	const s = orb.services?.find((x) => x.name === name);
	const status = s?.status;
	const step = watch?.step;
	const pending = step === "sending" || step === "waiting" || step === "armed";
	useEffect(() => {
		if (step === "waiting" && status && !RESTART_FAILED[status])
			setWatch(stepTo("armed"));
		if (step !== "armed") return;
		if (status && RESTART_FAILED[status]) {
			setWatch(stepTo("failed"));
			return;
		}
		if (status !== "running") return;
		const timer = window.setTimeout(
			() => setWatch(stepTo("recovered")),
			RESTART_STABLE_MS,
		);
		return () => window.clearTimeout(timer);
	}, [step, status]);
	useEffect(() => {
		if (!pending) return;
		const timer = window.setTimeout(
			() => setWatch(stepTo("failed")),
			RESTART_GIVE_UP_MS,
		);
		return () => window.clearTimeout(timer);
	}, [pending]);
	useEffect(() => {
		// 「已恢复」说完就收起；停在「仍然失败」时服务自己好了也收起
		if (!(step === "recovered" || (step === "failed" && status === "running")))
			return;
		const timer = window.setTimeout(
			() => setWatch(null),
			step === "recovered" ? RESTART_RECOVERED_MS : 0,
		);
		return () => window.clearTimeout(timer);
	}, [step, status]);
	// 日志对话框跟着服务走，不跟提示行：重启中打开的日志在「已恢复」收起后仍开着；服务被移除才关
	const logOpen = log !== null && orb.services?.some((x) => x.name === log);
	useEffect(() => {
		if (log !== null && !logOpen) setLog(null);
	}, [log, logOpen]);
	const dialog = logOpen && (
		<ServiceLogDialog orbId={orb.id} name={log} onClose={() => setLog(null)} />
	);
	// 两种返回都把对话框放在第一格，位置不变，提示行收起时它不会重挂（日志不重新加载）
	if (!s || !name) {
		return (
			<>
				{dialog}
				{failedRun ? <EnvFault orbId={orb.id} run={failedRun} /> : null}
			</>
		);
	}
	const restart = () => {
		setWatch({ name, step: "sending" });
		action.mutate(
			{ name, action: "restart" },
			{
				onSuccess: (svc) =>
					setWatch(stepTo(RESTART_FAILED[svc.status] ? "failed" : "waiting")),
				onError: () => setWatch(stepTo("failed")),
			},
		);
	};
	// 两个按钮带 key：重启中只剩日志钮，不让 React 把重启钮的节点（连同焦点与配色过渡）挪给它
	const logButton = (
		<button
			key="log"
			type="button"
			onClick={() => setLog(name)}
			className={`${FAULT_ACTION} text-muted text-sm hover:bg-hover hover:text-fg`}
		>
			{t("env.log")}
		</button>
	);
	const restartButton = (
		<button
			key="restart"
			type="button"
			onClick={restart}
			className={`${FAULT_ACTION} text-accent-ink text-sm hover:bg-accent-soft`}
		>
			<Icon name="restart" size={15} weight="bold" aria-hidden />
			{t("panel.restart")}
		</button>
	);
	const who = <b className="font-semibold">{name}</b>;
	return (
		<>
			{dialog}
			{pending ? (
				<FaultLine
					mark="pending"
					message={
						<>
							{who} {t("panel.restarting")}
						</>
					}
				>
					{logButton}
				</FaultLine>
			) : step === "recovered" ? (
				<FaultLine
					mark="ok"
					message={
						<>
							{who} <span className="text-ok-ink">{t("panel.recovered")}</span>
						</>
					}
				/>
			) : step === "failed" ? (
				<FaultLine
					title={s.error}
					message={
						<>
							{who}{" "}
							<span className="font-medium text-err">
								{t("panel.stillFailing")}
							</span>
							{/* 结局是 failed 时「仍然失败」已经说了；未就绪、已停止要补一句 */}
							{s.status !== "failed" &&
								` · ${t(`services.status.${s.status}`)}`}
						</>
					}
				>
					{restartButton}
					{logButton}
				</FaultLine>
			) : (
				<FaultLine
					title={s.error}
					message={
						<>
							{who} {t(`services.status.${s.status}`)}
							{s.restarts > 0 &&
								` · ${t("services.restarts", { count: s.restarts })}`}
						</>
					}
				>
					{restartButton}
					{logButton}
				</FaultLine>
			)}
			<div className="empty:hidden">
				<ErrorText error={action.error} />
			</div>
		</>
	);
}

/** setup / resume 失败：日志就在提示行里。 */
function EnvFault({ orbId, run }: { orbId: string; run: SetupRun }) {
	const { t } = useTranslation();
	return (
		<FaultLine
			message={
				<>
					<b className="font-semibold">{t(`env.phase.${run.phase}`)}</b>{" "}
					{t("env.failed")}
				</>
			}
		>
			<button
				type="button"
				onClick={() =>
					uiStore.getState().open({ kind: "setupLog", orbId, run })
				}
				className={`${FAULT_ACTION} text-muted text-sm hover:bg-hover hover:text-fg`}
			>
				{t("env.log")}
			</button>
		</FaultLine>
	);
}

/** orb 规格一行：类型、核数、内存、提供方，GitHub 账号靠右；runner 换成 runner 名、主机与工作目录。 */
function OrbSpecs({ threadId, orb }: { threadId: string; orb: Orb }) {
	const { t } = useTranslation();
	const runnerInfo = useRunnerOfOrb(orb);
	const cwd = useThread(threadId, (s) => s.thread?.cwd);
	const cls =
		"mt-3.5 flex flex-wrap items-center gap-x-3.5 gap-y-1 text-muted text-xs leading-[18px]";
	const box = useBoxOfOrb(orb.box ? orb.id : undefined);
	const boxText = boxThreadLabel(orb, { cwd }, box ? [box] : undefined, t);
	// box 线程（M23）：先写 `<box> · <app 或 机器>`，其余同普通 orb；box 列表没到时与普通 orb 相同
	if (boxText)
		return (
			<div data-testid="box-specs" className={cls}>
				<Spec icon="hardDrives" className="font-medium text-fg">
					{boxText}
				</Spec>
				<Spec icon="box">{orb.type}</Spec>
				<Spec icon="cpu">{formatOrbCpus(orb.cpus)}</Spec>
				<Spec icon="memory">{formatOrbMemory(orb.memoryMB)}</Spec>
			</div>
		);
	if (orb.kind === "runner")
		return (
			<div data-testid="runner-specs" className={cls}>
				<Spec icon="hardDrives" className="font-medium text-fg">
					{orb.providerName ?? t("runner.label")}
				</Spec>
				{runnerInfo?.hostname && (
					<Spec icon="monitor">{runnerInfo.hostname}</Spec>
				)}
				{cwd && (
					<Spec icon="folder" className="max-w-full font-mono">
						<span title={cwd} className="min-w-0 truncate">
							{cwd}
						</span>
					</Spec>
				)}
			</div>
		);
	return (
		<div className={cls}>
			<Spec icon="box">{orb.type}</Spec>
			<Spec icon="cpu">{formatOrbCpus(orb.cpus)}</Spec>
			<Spec icon="memory">{formatOrbMemory(orb.memoryMB)}</Spec>
			{orb.providerName && <Spec icon="hardDrives">{orb.providerName}</Spec>}
			{orb.nestedDocker && <DockerBadge />}
			{orb.githubLogin && (
				<Spec icon="github" className="text-faint">
					@{orb.githubLogin}
				</Spec>
			)}
		</div>
	);
}

function OrbSection({ threadId }: { threadId: string }) {
	const { t, i18n } = useTranslation();
	const orb = useOrbOf(threadId);
	const working = useThread(threadId, (s) => s.thread?.state === "working");
	const running = useThread(threadId, (s) => s.running);
	const metrics = useOrbMetrics(orb?.state === "deleted" ? undefined : orb?.id);
	const box = useBoxOfOrb(orb?.box ? orb.id : undefined);
	if (!orb) {
		return (
			<InfoBlock>
				<EmptyLine icon="box">{t("panel.noOrb")}</EmptyLine>
			</InfoBlock>
		);
	}
	const runner = orb.kind === "runner";
	const samples = metrics.data?.samples ?? [];
	const uptime =
		orb.state === "ready" && orb.stateChangedAt
			? formatUptime(Date.now() - orb.stateChangedAt, i18n.language)
			: undefined;
	// 故障只在 orb 就绪时作数：失败的服务、失败的 setup / resume（与页头的 orb 状态同一判断）
	const services = faultyServices(orb);
	const failedRun =
		orb.state === "ready"
			? [orb.lastSetup, orb.lastResume].find((r) => r && !r.ok)
			: undefined;
	const faults = services.length + (failedRun ? 1 : 0);
	const stateLabel = t(`orbState.${orb.state}`);
	const faultLabel = faults > 0 ? t("panel.faults", { count: faults }) : "";
	const terminal =
		orb.state !== "deleted" && contentTabs(orb).includes("terminal");
	// runner 停机 = 离线，只能等它自己重连，唤醒必然失败（runner_offline）；box 的启停只由 Boxes 页决定
	const stopped = orb.state === "stopped" || orb.state === "stopping";
	const wake = !runner && !orb.box && !running && stopped;
	return (
		<>
			<InfoBlock className="pb-4">
				{/* orb 编号只留在悬停提示里供排查：它是线程的执行环境，用户不需要认它 */}
				<div className="flex items-center gap-3" title={orb.id}>
					<OrbEmblem
						state={orb.state}
						active={working}
						cpu={samples.map((s) => s.cpuPct)}
						fault={faults > 0}
						label={faultLabel ? `${stateLabel} · ${faultLabel}` : stateLabel}
						className="-my-0.5 -ml-0.5"
					/>
					<div className="min-w-0 flex-1">
						<div className="truncate font-semibold text-2xl leading-[30px] tracking-tight">
							{stateLabel}
						</div>
						<div className="truncate text-faint text-sm leading-[18px]">
							{runner ? t("runner.label") : t("panel.orb")}
							{uptime && ` · ${t("panel.uptime", { d: uptime })}`}
							{/* 只有一项故障时，下面紧挨着的故障提示行（带重启 / 日志）已经说清楚了，副标题不再重复；
							    多项时提示行只列第一项，计数仍有信息量 */}
							{faults > 1 && (
								<span className="font-medium text-err"> · {faultLabel}</span>
							)}
						</div>
					</div>
					{orb.box && stopped && box ? (
						<BoxStartLink boxId={box.id} />
					) : wake ? (
						<WakeButton threadId={threadId} />
					) : (
						terminal && (
							<IconButton
								icon="terminal"
								size={16}
								box="sm"
								className="-mt-1 -mr-1.5 self-start text-faint"
								label={t("toolRender.terminal.viewInPanel")}
								onClick={() => uiStore.getState().setPanelTab("terminal")}
							/>
						)
					)}
				</div>
				{/* 按 orb 而不是按服务挂载：重启的服务恢复后离开故障列表，提示行仍要把结果说完 */}
				<FaultNotice
					key={orb.id}
					orb={orb}
					faulty={services}
					failedRun={failedRun}
				/>
				<OrbSpecs threadId={threadId} orb={orb} />
			</InfoBlock>
			{orb.state !== "deleted" && <OrbMetricTiles samples={samples} />}
		</>
	);
}

/** orb 相关的入口行：服务、环境（runner 不跑 setup / resume）、浏览器 Profile。 */
function OrbRows({ threadId }: { threadId: string }) {
	const orb = useOrbOf(threadId);
	if (!orb) return null;
	return (
		<>
			<ServicesRow threadId={threadId} orb={orb} />
			{orb.kind !== "runner" && <EnvRow orb={orb} />}
			<DesktopProfileRow orb={orb} />
		</>
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
	pending: "text-fg",
	in_progress: "font-semibold text-fg",
	completed: "text-muted",
	abandoned: "text-faint line-through",
};

function TodoSection({ threadId }: { threadId: string }) {
	const { t } = useTranslation();
	const phases = useThread(threadId, (s) => s.state?.todoPhases) ?? NO_PHASES;
	const [draft, setDraft] = useState("");
	const [error, setError] = useState<string | null>(null);
	// 末尾平时是一行「添加待办…」，点了才换成输入框并聚焦，空着离开就换回来
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
	// 只有一个阶段时阶段名并进标题行，不再单独占一行
	const single = phases.length === 1 ? phases[0]?.name || undefined : undefined;
	return (
		<InfoGroup
			title={t("panel.todos")}
			icon="list"
			sub={single}
			collapsed={fold.collapsed}
			onToggle={fold.onToggle}
			right={
				total > 0 && (
					<>
						{done}/{total}
						<Meter
							value={done}
							max={total}
							tone="ok"
							label={t("panel.todos")}
							className="h-1! w-10"
						/>
					</>
				)
			}
		>
			{keyed(phases, "p").map(({ key: pk, item: phase }, pi) => (
				<div key={pk} className="mb-0.5">
					{!single && phase.name && (
						<div className="px-3.5 pt-1 pb-0.5 font-medium text-faint text-xs">
							{phase.name}
						</div>
					)}
					<ul className="flex flex-col gap-px">
						{keyed(phase.tasks, "t").map(({ key: tk, item: task }, ti) => {
							const mark = STATUS_ICON[task.status];
							const now = task.status === "in_progress";
							return (
								<li
									key={tk}
									// 右侧给删除钮留位；触屏上删除钮常显、点击区外扩 8px，再多留 12px，免得点到「进行中」或正文时误删
									className={`group relative flex items-start gap-2.5 rounded-md py-[7px] pr-8 pl-3.5 text-sm transition-colors duration-150 pointer-coarse:pr-11 ${now ? "bg-accent-soft" : "hover:bg-raised"}`}
								>
									<button
										type="button"
										className={`mt-px shrink-0 rounded-full ${mark.className}`}
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
											size={18}
											weight={mark.fill ? "fill" : "regular"}
										/>
									</button>
									<span
										className={`min-w-0 flex-1 break-words ${STATUS_TEXT[task.status]}`}
									>
										{task.content}
									</span>
									{now && (
										// 有悬停的设备上右缘与标题行的进度对齐、悬停时让位给删除钮；触屏上删除钮常显，标签排在它左边
										<span className="pointer-fine:-mr-[18px] mt-px shrink-0 font-semibold text-accent-ink text-xs transition-opacity duration-150 pointer-fine:group-hover:opacity-0 pointer-fine:group-has-[:focus-visible]:opacity-0">
											{t("todo.status.in_progress")}
										</span>
									)}
									{/* 隐藏时不接收点击（透明按钮叠在「进行中」上会被误点）；触屏没有悬停，常显并放大点击区 */}
									<button
										type="button"
										className="absolute top-[11px] right-3.5 text-faint transition-opacity duration-150 after:absolute after:-inset-2 after:content-[''] hover:text-err pointer-fine:pointer-events-none pointer-fine:opacity-0 pointer-fine:group-hover:pointer-events-auto pointer-fine:group-hover:opacity-100 pointer-fine:focus-visible:pointer-events-auto pointer-fine:focus-visible:opacity-100"
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
			{adding ? (
				<form
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
					{/* 纸面上的输入框只用一层浅底，不画边框；焦点环由全局 :focus-visible 给 */}
					<input
						ref={inputRef}
						className="h-[34px] w-full rounded-md bg-raised px-3.5 text-fg text-sm placeholder:text-faint"
						placeholder={t("panel.addTodo")}
						value={draft}
						onBlur={() => {
							if (!draft.trim()) setAdding(false);
						}}
						onChange={(e) => setDraft(e.target.value)}
					/>
				</form>
			) : (
				<button
					type="button"
					onClick={() => setAdding(true)}
					className="flex h-[34px] w-full items-center gap-2.5 rounded-md px-3.5 text-left text-faint text-sm transition-colors duration-150 hover:bg-raised"
				>
					<Icon name="plus" size={18} aria-hidden className="text-ghost" />
					{t("panel.addTodo")}
				</button>
			)}
			{error && <p className="mt-1 px-3.5 text-err text-xs">{error}</p>}
		</InfoGroup>
	);
}

/** 子 agent 树每深一层的缩进（px）。 */
const SUBAGENT_INDENT = 24;
/** 行首头像的边长，与含外圈留白（四边各 3px）的外框边长。 */
const AVATAR = 18;
const AVATAR_BOX = AVATAR + 6;
/** 行首头像中线到行左缘的距离：ListRow 左内边距 8px + 头像外框的一半。 */
const DOT_X = 8 + AVATAR_BOX / 2;
/** L 形引导线横向的终点：停在头像外框左侧 2px。 */
const GUIDE_END = DOT_X - AVATAR_BOX / 2 - 2;
/** 引导线（与左侧线程列表的 fork 引导线同款：1.5px、border-strong、L 形圆角）。 */
const GUIDE = "pointer-events-none absolute z-[1] border-border-strong";

/**
 * 子 agent 分组：实时、快照与已入库存档合并成的派发树（`useSubagentTree`）。与左侧线程列表的 fork 层级同一种画法：
 * 下级从上级头像的中线引出 L 形引导线连到自己的头像，非最后一个下级时竖线继续向下；标题行右侧的计数含所有层级。
 * 每行以 agent 头像开头，头像外圈与角标表示状态（M13 R10），标题行的运行汇总用同一种运行光环。
 */
function SubagentSection({ threadId }: { threadId: string }) {
	const { t } = useTranslation();
	const { nodes } = useSubagentTree(threadId);
	const fold = useCardFold("subagents");
	const running = nodes.filter(
		(n) => subagentVisualState(n.row) === "running",
	).length;
	// 先序序列 → 上级 key 到下级的映射；上级不在序列里（缺失 / 断环）的挂在根上
	const keys = new Set(nodes.map((n) => n.row.key));
	const kids = new Map<string | undefined, SubagentNode[]>();
	for (const n of nodes) {
		const p = n.parent && keys.has(n.parent) ? n.parent : undefined;
		const list = kids.get(p) ?? [];
		list.push(n);
		kids.set(p, list);
	}
	// 没有子 agent 时不占位，等有了再出现
	if (nodes.length === 0) return null;
	return (
		<InfoGroup
			title={t("panel.subagents")}
			icon="fork"
			collapsed={fold.collapsed}
			onToggle={fold.onToggle}
			right={
				running > 0 ? (
					<span className="inline-flex items-center gap-1.5">
						<AgentStatusRing state="running" size={12} />
						{t("panel.subagentsRunning", { n: running })}
					</span>
				) : (
					t("panel.subagentsTotal", { n: nodes.length })
				)
			}
		>
			{/* ListRow 自带 8px 内边距，外加 6px 与分组标题的 14px 对齐 */}
			<ul className="flex flex-col gap-px px-1.5">
				{(kids.get(undefined) ?? []).map((n) => (
					<SubagentBranch key={n.row.key} node={n} kids={kids} root last />
				))}
			</ul>
		</InfoGroup>
	);
}

/** 树的一个节点：本行 + 下级。`last` 为同级最后一个（竖线不再向下延续）。 */
function SubagentBranch({
	node,
	kids,
	root = false,
	last,
}: {
	node: SubagentNode;
	kids: ReadonlyMap<string | undefined, SubagentNode[]>;
	root?: boolean;
	last: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const { row } = node;
	const children = kids.get(row.key) ?? [];
	const state = subagentVisualState(row);
	// 引导线挂在上级头像的中线上：本节点坐标系里是 DOT_X - 缩进
	const parentX = DOT_X - SUBAGENT_INDENT - 0.75;
	return (
		<li
			className="relative"
			style={root ? undefined : { marginLeft: SUBAGENT_INDENT }}
		>
			<div className="relative">
				{!root &&
					(last ? (
						// 最后一个下级：└ 从上方下到本行中线，圆角拐向本行头像
						<span
							aria-hidden
							data-guide="elbow"
							className={`${GUIDE} -top-px bottom-1/2 rounded-bl-[6px] border-b-[1.5px] border-l-[1.5px]`}
							style={{ left: parentX, width: GUIDE_END - parentX }}
						/>
					) : (
						// 后面还有同级：├ 竖线贯穿本行接到下一个同级，中线处横向分出一支连到本行头像
						<>
							<span
								aria-hidden
								className={`${GUIDE} -top-px -bottom-px border-l-[1.5px]`}
								style={{ left: parentX }}
							/>
							<span
								aria-hidden
								data-guide="elbow"
								className={`${GUIDE} bottom-1/2 h-0 border-b-[1.5px]`}
								style={{ left: parentX, width: GUIDE_END - parentX }}
							/>
						</>
					))}
				{children.length > 0 && (
					// 有下级时从本行头像下方引出竖线，接到第一个下级的引导线
					<span
						aria-hidden
						className={`${GUIDE} bottom-0 border-l-[1.5px]`}
						style={{
							left: DOT_X - 0.75,
							top: `calc(50% + ${AVATAR_BOX / 2 + 1}px)`,
						}}
					/>
				)}
				<ListRow
					leading={
						<AgentAvatar
							agent={row.agent}
							state={state}
							size={AVATAR}
							title={agentStateLabel(t, state, row.archiveReason) ?? row.agent}
						/>
					}
					title={<span className="truncate font-medium">{row.agent}</span>}
					meta={
						row.description && (
							<span className="min-w-0 truncate">{row.description}</span>
						)
					}
					onClick={() => openSubagent(row)}
				/>
			</div>
			{children.length > 0 && (
				<div className="relative">
					{/* 本节点不是同级最后一个：竖线贯穿它的整棵子树，接到下一个同级 */}
					{!root && !last && (
						<span
							aria-hidden
							className={`${GUIDE} top-0 bottom-0 border-l-[1.5px]`}
							style={{ left: parentX }}
						/>
					)}
					<ul className="flex flex-col gap-px pt-px">
						{children.map((c, i) => (
							<SubagentBranch
								key={c.row.key}
								node={c}
								kids={kids}
								last={i === children.length - 1}
							/>
						))}
					</ul>
				</div>
			)}
		</li>
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
	const ready = useThread(threadId, agentQueryable);
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
	return (
		<InfoBlock
			title={t("panel.context")}
			icon="database"
			// token 读数放标题行右端，正好落在下面分段条终点（窗口上限）的正上方；百分比与分段条并排，
			// 不再各占一行、右半边留空
			action={
				<span className="flex items-center gap-1.5">
					{ctx && (
						<span className="text-faint text-xs tabular-nums">
							<span className="font-semibold text-muted">
								{formatTokens(ctx.tokens)}
							</span>{" "}
							/ {formatTokens(ctx.contextWindow)}
						</span>
					)}
					<IconButton
						icon="info"
						size={14}
						box="sm"
						className="text-faint"
						label={t("report.get_context_report")}
						onClick={() =>
							uiStore
								.getState()
								.open({ kind: "report", report: "get_context_report" })
						}
					/>
				</span>
			}
		>
			{ctx ? (
				<>
					<div className="mt-1.5 flex items-center gap-3">
						<BigValue
							value={formatPercent(ctx.percent)}
							className={`shrink-0 text-3xl leading-9 ${PERCENT_TONE[tone] ?? ""}`}
						/>
						<div className="min-w-0 flex-1">
							{parts && breakdown ? (
								<ContextBar
									parts={parts}
									contextWindow={breakdown.contextWindow}
									tone={tone}
									label={t("panel.context")}
									// 块底已是一层浅色，轨道用悬停色而不是更深的内嵌色（深色下内嵌色比块底还暗）
									className="h-2! bg-hover!"
								/>
							) : (
								// 报告还没取到：先用单段量表
								<Meter
									value={ctx.percent}
									tone={tone}
									label={t("panel.context")}
									className="h-2! bg-hover!"
								/>
							)}
						</div>
					</div>
					{parts && breakdown && (
						<ContextLegend
							parts={parts}
							contextWindow={breakdown.contextWindow}
						/>
					)}
				</>
			) : (
				<div className="mt-2">
					<EmptyLine icon="database">{t("common.none")}</EmptyLine>
				</div>
			)}
		</InfoBlock>
	);
}

const TOOL_ROWS: readonly {
	icon: IconName;
	report?: "get_usage" | "get_session_stats" | "get_active_tools";
}[] = [
	{ icon: "message" },
	{ icon: "coin", report: "get_usage" },
	{ icon: "chart", report: "get_session_stats" },
	{ icon: "bolt", report: "get_active_tools" },
];

export function InfoPanel({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	return (
		<aside
			className="flex h-full w-full flex-col gap-5 overflow-y-auto bg-panel px-3 pt-3 pb-6"
			aria-label={t("nav.infoPanel")}
		>
			<div className="flex flex-col gap-2">
				<OrbSection threadId={threadId} />
				<ContextSection threadId={threadId} />
			</div>
			<TodoSection threadId={threadId} />
			<SubagentSection threadId={threadId} />
			<JobsSection threadId={threadId} />
			<SandboxSection threadId={threadId} />
			<div className="flex flex-col empty:hidden">
				<OrbRows threadId={threadId} />
				<CapabilitiesRow threadId={threadId} />
				<SshSection threadId={threadId} />
			</div>
			<div className="-mt-2 flex flex-col">
				{TOOL_ROWS.map(({ icon, report }) => (
					<InfoLink
						key={icon}
						icon={icon}
						title={report ? t(`report.${report}`) : t("btw.title")}
						onClick={() =>
							uiStore
								.getState()
								.open(report ? { kind: "report", report } : { kind: "btw" })
						}
					/>
				))}
			</div>
		</aside>
	);
}
