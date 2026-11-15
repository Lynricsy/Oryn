/** `task` — spawn subagents: batch shape, streamed progress, per-agent results. */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { agentStateLabel } from "../../components/transcript/AgentAvatar";
import {
	AgentStatusAvatar,
	type AgentVisualState,
} from "../../components/ui/AgentStatusAvatar";
import { agentDefaultGlyph } from "../../lib/avatar-defaults";
import { formatDuration, formatTokens } from "../../lib/format";
import { modelBrand, shortModelName } from "../../lib/model-brand";
import {
	AgentLink,
	Badge,
	Badges,
	Note,
	Output,
	ResultText,
	Row,
} from "../parts";
import type { ToolRenderer, ToolRenderHost, ToolRenderProps } from "../types";
import {
	detailsRecord,
	isRecord,
	normalizeWs,
	num,
	str,
	truncate,
} from "../util";

const MISSING_YIELD_PREFIX =
	"SYSTEM WARNING: Subagent exited without calling yield tool";

/** One spawned unit of work, normalized across the batch (`tasks[]`) and flat arg shapes. */
interface TaskItemView {
	name: string | null;
	agent: string | null;
	/** Single-prompt instructions; null for a stepped spawn. */
	task: string | null;
	/** Ordered stages sent one prompt at a time (`task.steps`); empty for a `task` spawn. */
	steps: string[];
	isolated: boolean;
}

/** Read one item; args stream in token by token, so non-string steps (still partial) are skipped. */
function taskItem(entry: Record<string, unknown>): TaskItemView {
	return {
		name: str(entry.name),
		agent: str(entry.agent),
		task: str(entry.task),
		steps: Array.isArray(entry.steps)
			? entry.steps.filter((step) => typeof step === "string")
			: [],
		isolated: entry.isolated === true,
	};
}

function taskItems(args: Record<string, unknown>): TaskItemView[] {
	if (Array.isArray(args.tasks))
		return args.tasks.filter(isRecord).map(taskItem);
	const flat = taskItem(args);
	return flat.name || flat.task || flat.steps.length > 0 ? [flat] : [];
}

/** First non-blank line of the work: the `task`, else the first step. */
function taskBrief(item: TaskItemView): string | null {
	const text = item.task ?? item.steps[0] ?? "";
	const line = text.split("\n").find((l) => l.trim() !== "");
	return line ? normalizeWs(line) : null;
}

/** "Anna.Bob" nesting → "Anna>Bob" breadcrumb (mirrors the TUI's formatTaskId). */
function taskIdLabel(id: string): string {
	return id.includes(".") ? id.split(".").join(">") : id;
}

/** Outcome of one agent, mirroring the TUI's aborted / merge-failed / done / failed split. */
function resultStatus(res: Record<string, unknown>): {
	label: string;
	tone: "ok" | "err" | "warn";
} {
	if (res.aborted === true) return { label: "aborted", tone: "err" };
	if (num(res.exitCode) === 0) {
		return str(res.error)
			? { label: "merge failed", tone: "warn" }
			: { label: "done", tone: "ok" };
	}
	return { label: "failed", tone: "err" };
}

/** 结果卡的头像状态：merge failed 时子 agent 本身已跑完（exitCode 0），头像记完成，合并失败由旁边的徽标说明。 */
function resultVisualState(res: Record<string, unknown>): AgentVisualState {
	const { label } = resultStatus(res);
	if (label === "aborted") return "aborted";
	if (label === "failed") return "failed";
	return "done";
}

/** 进度帧的 `status`（Core 的 SubagentStatus）→ 头像状态；未知的写法按运行中。 */
const PROGRESS_VISUAL: Record<string, AgentVisualState> = {
	pending: "pending",
	running: "running",
	completed: "done",
	failed: "failed",
	aborted: "aborted",
};

/** 头像尺寸：与卡片里的徽标同高。 */
const AVATAR_SIZE = 14;

/**
 * agent 头像（M13 R12）+ 其后的内容（agent 名徽标或子 agent id）。头像由宿主渲染（站内读 agent 头像设置，
 * 分享页读公开身份）；宿主不提供时只用该 agent 的默认字形。参数卡没有运行状态，用 `unknown`。
 */
function AgentTag({
	agent,
	state,
	host,
	children,
}: {
	agent: string;
	state: AgentVisualState;
	host?: ToolRenderHost;
	children: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const label = agentStateLabel(t, state);
	const title = label ? `${agent} · ${label}` : agent;
	const Avatar = host?.AgentAvatar;
	return (
		<span className="tv-agent">
			{Avatar ? (
				<Avatar agent={agent} state={state} title={title} size={AVATAR_SIZE} />
			) : (
				<AgentStatusAvatar
					face={undefined}
					fallbackGlyph={agentDefaultGlyph(agent)}
					state={state}
					size={AVATAR_SIZE}
					title={title}
				/>
			)}
			{children}
		</span>
	);
}

function Summary({ args, host }: ToolRenderProps): ReactNode {
	const tasks = taskItems(args);
	// One badge when every spawn runs the same agent; mixed batches badge each row in the body.
	const agents = new Set(tasks.map((t) => t.agent));
	const agent = agents.size === 1 ? (tasks[0]?.agent ?? null) : null;
	const first = tasks.length > 0 ? tasks[0] : null;
	const label = first ? (first.name ?? taskBrief(first)) : null;
	return (
		<>
			{agent && (
				<AgentTag agent={agent} state="unknown" host={host}>
					<Badge tone="accent">{agent}</Badge>
				</AgentTag>
			)}
			{label && <span className="tv-muted">{truncate(label, 72)}</span>}
			{tasks.length > 1 && <Badge>{tasks.length} tasks</Badge>}
		</>
	);
}

/** Final snapshot for one agent: status row, output preview, error/abort notes. */
function AgentResult({
	res,
	host,
}: {
	res: Record<string, unknown>;
	host?: ToolRenderHost;
}): ReactNode {
	const { label, tone } = resultStatus(res);
	const id = str(res.id) ?? "agent";
	const agent = str(res.agent);
	const description = str(res.description);
	const stats: ReactNode[] = [];
	const tokens = num(res.tokens);
	if (tokens) stats.push(`${formatTokens(tokens)} tok`);
	const requests = num(res.requests);
	if (requests) stats.push(`${requests} req`);
	const durationMs = num(res.durationMs);
	if (durationMs != null) stats.push(formatDuration(durationMs));
	const model = str(res.resolvedModel);
	// 与转录、输入坞一致：写短名，完整 id 进悬停提示
	if (model)
		stats.push(
			<span title={model}>
				<Badge>
					{shortModelName(model.split("/").pop() ?? model, modelBrand(model))}
				</Badge>
			</span>,
		);

	// The runtime prepends a one-line warning when a subagent never called
	// yield; lift it out of the output preview like the TUI does.
	let output = str(res.output) ?? "";
	let warning: string | null = null;
	const nl = output.indexOf("\n");
	const firstLine = (nl === -1 ? output : output.slice(0, nl)).trim();
	if (firstLine.startsWith(MISSING_YIELD_PREFIX)) {
		warning = firstLine;
		output = nl === -1 ? "" : output.slice(nl + 1).replace(/^\s*\n+/, "");
	}
	const error = str(res.error);
	const aborted = res.aborted === true;
	const abortReason = str(res.abortReason);
	const patchPath = str(res.patchPath);
	const branchName = str(res.branchName);
	const link = (
		<AgentLink id={id} host={host}>
			{taskIdLabel(id)}
		</AgentLink>
	);
	return (
		<>
			<Row
				k={
					agent ? (
						<AgentTag agent={agent} state={resultVisualState(res)} host={host}>
							{link}
						</AgentTag>
					) : (
						link
					)
				}
			>
				<Badge tone={tone}>{label}</Badge>{" "}
				{res.truncated === true && <Badge tone="warn">truncated</Badge>}{" "}
				{description && <span>{truncate(normalizeWs(description), 96)}</span>}{" "}
				<Badges items={stats} />
			</Row>
			{warning && <Note tone="warn">{warning}</Note>}
			{aborted && abortReason && <Note tone="err">{abortReason}</Note>}
			{output.trim() !== "" && (
				<Output text={output} maxLines={6} error={tone === "err"} />
			)}
			{error && !aborted && error !== abortReason && (
				<Note tone={tone === "warn" ? "warn" : "err"}>{error}</Note>
			)}
			{patchPath && <div className="tv-faint">patch: {patchPath}</div>}
			{!patchPath && branchName && (
				<div className="tv-faint">branch: {branchName}</div>
			)}
		</>
	);
}

/** Live (still-running) snapshot for one agent. */
function AgentProgressRow({
	p,
	host,
}: {
	p: Record<string, unknown>;
	host?: ToolRenderHost;
}): ReactNode {
	const status = str(p.status) ?? "running";
	const tone =
		status === "completed"
			? ("ok" as const)
			: status === "failed" || status === "aborted"
				? ("err" as const)
				: status === "running"
					? ("accent" as const)
					: undefined;
	const id = str(p.id) ?? "agent";
	const agent = str(p.agent);
	const description = str(p.description);
	const intent = str(p.lastIntent) ?? str(p.currentTool);
	const bits: string[] = [];
	const toolCount = num(p.toolCount);
	if (toolCount) bits.push(`${toolCount} tools`);
	const tokens = num(p.tokens);
	if (tokens) bits.push(`${formatTokens(tokens)} tok`);
	const durationMs = num(p.durationMs);
	if (durationMs) bits.push(formatDuration(durationMs));
	const link = (
		<AgentLink id={id} host={host}>
			{taskIdLabel(id)}
		</AgentLink>
	);
	return (
		<Row
			k={
				agent ? (
					<AgentTag
						agent={agent}
						state={PROGRESS_VISUAL[status] ?? "running"}
						host={host}
					>
						{link}
					</AgentTag>
				) : (
					link
				)
			}
		>
			<Badge tone={tone}>{status}</Badge>{" "}
			{description && <span>{truncate(normalizeWs(description), 96)}</span>}{" "}
			{intent && (
				<span className="tv-muted">{truncate(normalizeWs(intent), 64)}</span>
			)}{" "}
			<Badges items={bits} />
		</Row>
	);
}

function Body({ args, result, partial, host }: ToolRenderProps): ReactNode {
	// 运行中没有最终结果时，用结构化部分结果（details.progress）展示各子 agent 的进度
	const current = result ?? partial;
	const context = str(args.context);
	const tasks = taskItems(args);
	const details = detailsRecord(current);
	const results =
		details && Array.isArray(details.results)
			? details.results.filter(isRecord)
			: [];
	const progress =
		details && Array.isArray(details.progress)
			? details.progress.filter(isRecord)
			: [];
	const showProgress = results.length === 0 && progress.length > 0;

	// Run footer: outcome counts + total wall time (mirrors the TUI's bracket line).
	let footer: ReactNode = null;
	if (results.length > 0) {
		let ok = 0;
		let mergeFailed = 0;
		let aborted = 0;
		let failed = 0;
		for (const res of results) {
			const { label } = resultStatus(res);
			if (label === "done") ok++;
			else if (label === "merge failed") mergeFailed++;
			else if (label === "aborted") aborted++;
			else failed++;
		}
		const total = details ? num(details.totalDurationMs) : null;
		footer = (
			<Row>
				{ok > 0 && <Badge tone="ok">{ok} succeeded</Badge>}{" "}
				{mergeFailed > 0 && (
					<Badge tone="warn">{mergeFailed} merge failed</Badge>
				)}{" "}
				{failed > 0 && <Badge tone="err">{failed} failed</Badge>}{" "}
				{aborted > 0 && <Badge tone="err">{aborted} aborted</Badge>}{" "}
				{total != null && (
					<span className="tv-faint">{formatDuration(total)}</span>
				)}
			</Row>
		);
	}

	// Finished agents first, by runtime ascending — same order as the TUI.
	const ordered = [...results].sort(
		(a, b) =>
			(num(a.durationMs) ?? 0) - (num(b.durationMs) ?? 0) ||
			(num(a.index) ?? 0) - (num(b.index) ?? 0),
	);

	return (
		<>
			{context && <Output text={context} maxLines={4} title="context" />}
			{tasks.length > 0 && (
				<div className="tv-list">
					{tasks.map((t, i) => (
						<div key={t.name ?? i}>
							<Row
								k={
									t.name ? (
										<AgentLink id={t.name} host={host}>
											{taskIdLabel(t.name)}
										</AgentLink>
									) : (
										<Badge tone="accent">{`#${i + 1}`}</Badge>
									)
								}
							>
								{t.agent && (
									<AgentTag agent={t.agent} state="unknown" host={host}>
										<Badge>{t.agent}</Badge>
									</AgentTag>
								)}{" "}
								{t.steps.length > 0 && (
									<Badge>{`${t.steps.length} ${t.steps.length === 1 ? "step" : "steps"}`}</Badge>
								)}{" "}
								{t.isolated && <Badge>isolated</Badge>}
							</Row>
							{t.task && <Output text={t.task} maxLines={6} title="task" />}
							{t.steps.map((step, n) => (
								<Output
									key={n}
									text={step}
									maxLines={4}
									title={`step ${n + 1}/${t.steps.length}`}
								/>
							))}
						</div>
					))}
				</div>
			)}
			{ordered.length > 0 && (
				<div className="tv-list">
					{ordered.map((res, i) => (
						<AgentResult key={str(res.id) ?? i} res={res} host={host} />
					))}
					{footer}
				</div>
			)}
			{showProgress && (
				<div className="tv-list">
					{progress.map((p, i) => (
						<AgentProgressRow key={str(p.id) ?? i} p={p} host={host} />
					))}
				</div>
			)}
			{ordered.length === 0 && !showProgress && (
				<ResultText result={current} maxLines={12} />
			)}
		</>
	);
}

export const taskRenderer: ToolRenderer = { Summary, Body, livePartial: true };
