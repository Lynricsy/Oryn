/**
 * `task` — spawn subagents. 头部读数是「完成数 / 总数」进度格；展开体的输入段是派工单（共享上下文 + 每个子 agent
 * 的任务），输出段是子 agent 名册：运行中每行给阶段、完成百分比与当前动作，结束后给统计与可展开的结果预览。
 * 形状见 oryn-core `tui/src/tools/task.ts`（`TaskToolDetails`、`AgentProgress`、`SingleResult`）。
 */
import type { CSSProperties, ReactNode } from "react";
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
	type CellTone,
	jsonText,
	MarkdownBlock,
	Note,
	Output,
	ProgressCells,
	ResultText,
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
	/** Ordered stages sent one prompt at a time (older `task.steps`); empty for a `task` spawn. */
	steps: string[];
	solutionSpace: string | null;
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
		solutionSpace: str(entry.solutionSpace),
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

/** 一个子 agent 的结局：中止 / 合并失败 / 完成 / 失败，同 TUI 的划分。 */
type Outcome = "done" | "mergeFailed" | "aborted" | "failed";

function outcomeOf(res: Record<string, unknown>): Outcome {
	if (res.aborted === true) return "aborted";
	if (num(res.exitCode) === 0) return str(res.error) ? "mergeFailed" : "done";
	return "failed";
}

/** 结果的头像状态：merge failed 时子 agent 本身已跑完（exitCode 0），头像记完成，合并失败由状态标说明。 */
const OUTCOME_VISUAL: Record<Outcome, AgentVisualState> = {
	done: "done",
	mergeFailed: "done",
	aborted: "aborted",
	failed: "failed",
};

const OUTCOME_CELL: Record<Outcome, CellTone> = {
	done: "ok",
	mergeFailed: "warn",
	aborted: "warn",
	failed: "err",
};

/** 进度帧的 `status`（Core 的 SubagentStatus）→ 头像状态；未知的写法按运行中。 */
const PROGRESS_VISUAL: Record<string, AgentVisualState> = {
	pending: "pending",
	running: "running",
	completed: "done",
	failed: "failed",
	aborted: "aborted",
};

const VISUAL_CELL: Partial<Record<AgentVisualState, CellTone>> = {
	pending: "idle",
	running: "accent",
	done: "ok",
	failed: "err",
	aborted: "warn",
};

/** 头像尺寸：名册行里比徽标略大，头部与派工单里与徽标同高。 */
const AVATAR_ROW = 18;
const AVATAR_INLINE = 14;

/**
 * agent 头像（M13 R12）+ 其后的内容（agent 名徽标或子 agent id）。头像由宿主渲染（站内读 agent 头像设置，
 * 分享页读公开身份）；宿主不提供时只用该 agent 的默认字形。参数卡没有运行状态，用 `unknown`。
 */
function AgentTag({
	agent,
	state,
	host,
	size = AVATAR_INLINE,
	children,
}: {
	agent: string;
	state: AgentVisualState;
	host?: ToolRenderHost;
	size?: number;
	children: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const label = agentStateLabel(t, state);
	const title = label ? `${agent} · ${label}` : agent;
	const Avatar = host?.AgentAvatar;
	return (
		<span className="tv-agent">
			{Avatar ? (
				<Avatar agent={agent} state={state} title={title} size={size} />
			) : (
				<AgentStatusAvatar
					face={undefined}
					fallbackGlyph={agentDefaultGlyph(agent)}
					state={state}
					size={size}
					title={title}
				/>
			)}
			{children}
		</span>
	);
}

/** 模型短名（完整 id 进悬停提示），与转录、输入坞一致。 */
function ModelName({ model }: { model: string }): ReactNode {
	return (
		<span className="tv-subagent-model" title={model}>
			{shortModelName(model.split("/").pop() ?? model, modelBrand(model))}
		</span>
	);
}

/** 当前这次调用的结构化结果：结束后是 result，运行中是 partial。 */
function taskDetails(props: ToolRenderProps): {
	results: Record<string, unknown>[];
	progress: Record<string, unknown>[];
	totalMs: number | null;
} {
	const details = detailsRecord(props.result ?? props.partial);
	const list = (v: unknown) => (Array.isArray(v) ? v.filter(isRecord) : []);
	return {
		results: list(details?.results),
		progress: list(details?.progress),
		totalMs: num(details?.totalDurationMs),
	};
}

function Summary({ args, host }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const tasks = taskItems(args);
	// One badge when every spawn runs the same agent; mixed batches show each agent in the body.
	const agents = new Set(tasks.map((task) => task.agent));
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
			{tasks.length > 1 && (
				<Badge>{t("toolRender.task.more", { count: tasks.length - 1 })}</Badge>
			)}
		</>
	);
}

/**
 * 右侧读数：每个子 agent 一格（完成绿、运行中强调色、排队空格、失败红）+ 完成数 / 总数；结束后再给总用时。
 * 运行中还没有进度帧时按参数里的任务数全记排队。
 */
function Meta(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const { results, progress, totalMs } = taskDetails(props);
	let tones: CellTone[];
	if (results.length > 0) {
		tones = [...results]
			.sort(byIndex)
			.map((res) => OUTCOME_CELL[outcomeOf(res)]);
	} else if (progress.length > 0) {
		tones = progress.map(
			(p) =>
				VISUAL_CELL[PROGRESS_VISUAL[str(p.status) ?? ""] ?? "running"] ??
				"accent",
		);
	} else if (props.running) {
		tones = taskItems(props.args).map(() => "idle");
	} else {
		return null;
	}
	const total = tones.length;
	if (total === 0) return null;
	const done = tones.filter((tone) => tone === "ok").length;
	const bad = tones.some((tone) => tone === "err");
	const finished = results.length > 0;
	return (
		<>
			<ProgressCells
				tones={tones}
				label={t("toolRender.task.progress", { done, total })}
			/>
			<span
				className={
					finished
						? bad
							? "tv-meta-err"
							: done === total
								? "tv-meta-ok"
								: "tv-meta-warn"
						: undefined
				}
			>
				{done}/{total}
			</span>
			{finished && totalMs != null && totalMs > 0 && (
				<span>{formatDuration(totalMs)}</span>
			)}
		</>
	);
}

function byIndex(
	a: Record<string, unknown>,
	b: Record<string, unknown>,
): number {
	return (num(a.index) ?? 0) - (num(b.index) ?? 0);
}

/** 名册行的统计读数（用时 · token · 请求 · 工具数 · 模型），间隔点分隔。 */
function Stats({ items }: { items: ReactNode[] }): ReactNode {
	const shown = items.filter(
		(item) => item != null && item !== "" && item !== false,
	);
	if (shown.length === 0) return null;
	return (
		<span className="tv-subagent-stats">
			{shown.map((item, i) => (
				<span key={i}>{item}</span>
			))}
		</span>
	);
}

/** 名册的一行：头像 + id（可钻入子会话）+ 描述 + 状态标 + 统计；其下是该 agent 的详情。 */
function SubagentRow({
	id,
	agent,
	state,
	description,
	status,
	stats,
	host,
	children,
}: {
	id: string;
	agent: string | null;
	state: AgentVisualState;
	description: string | null;
	status: ReactNode;
	stats: ReactNode[];
	host?: ToolRenderHost;
	children?: ReactNode;
}): ReactNode {
	const link = (
		<AgentLink id={id} host={host}>
			{taskIdLabel(id)}
		</AgentLink>
	);
	return (
		<div className={`tv-subagent tv-subagent--${state}`} data-agent-id={id}>
			<div className="tv-subagent-head">
				<span className="tv-subagent-who">
					{agent ? (
						<AgentTag agent={agent} state={state} host={host} size={AVATAR_ROW}>
							{link}
						</AgentTag>
					) : (
						link
					)}
				</span>
				{description && (
					<span className="tv-subagent-desc">{normalizeWs(description)}</span>
				)}
				{status}
			</div>
			<Stats items={stats} />
			{children}
		</div>
	);
}

/** 子 agent 的最终输出：JSON（yield 的结构化数据）按 JSON 高亮，其余按 Markdown，超高时折叠。 */
function AgentOutput({
	text,
	error,
}: {
	text: string;
	error: boolean;
}): ReactNode {
	if (error) return <Output text={text} maxLines={6} error />;
	const json = jsonText(text);
	if (json !== null)
		return <Output text={json} lang="json" variant="code" maxLines={8} />;
	return (
		<div className="tv-subagent-out">
			<MarkdownBlock text={text} maxHeight={150} />
		</div>
	);
}

/** Final snapshot for one agent: status row, output preview, error/abort notes, isolation artifacts. */
function AgentResult({
	res,
	host,
}: {
	res: Record<string, unknown>;
	host?: ToolRenderHost;
}): ReactNode {
	const { t } = useTranslation();
	const outcome = outcomeOf(res);
	const id = str(res.id) ?? "agent";
	const tokens = num(res.tokens);
	const requests = num(res.requests);
	const durationMs = num(res.durationMs);
	const model = str(res.resolvedModel);

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
	const retryFailure = isRecord(res.retryFailure)
		? str(res.retryFailure.errorMessage)
		: null;
	const patchPath = str(res.patchPath);
	const branchName = str(res.branchName);
	const status =
		outcome === "done" ? null : (
			<Badge tone={outcome === "mergeFailed" ? "warn" : "err"}>
				{t(`toolRender.task.outcome.${outcome}`)}
			</Badge>
		);
	return (
		<SubagentRow
			id={id}
			agent={str(res.agent)}
			state={OUTCOME_VISUAL[outcome]}
			description={str(res.description)}
			status={
				<>
					{status}
					{res.truncated === true && (
						<Badge tone="warn">{t("toolRender.task.truncated")}</Badge>
					)}
				</>
			}
			stats={[
				durationMs != null && formatDuration(durationMs),
				tokens
					? t("toolRender.task.tokens", { n: formatTokens(tokens) })
					: null,
				requests ? t("toolRender.task.requests", { count: requests }) : null,
				model && <ModelName model={model} />,
			]}
			host={host}
		>
			{warning && <Note tone="warn">{warning}</Note>}
			{aborted && abortReason && <Note tone="err">{abortReason}</Note>}
			{retryFailure && retryFailure !== error && (
				<Note tone="err">{retryFailure}</Note>
			)}
			{output.trim() !== "" ? (
				<AgentOutput
					text={output}
					error={outcome === "failed" || outcome === "aborted"}
				/>
			) : (
				!error &&
				!abortReason && (
					<div className="tv-subagent-empty">
						{t("toolRender.task.noOutput")}
					</div>
				)
			)}
			{error && !aborted && error !== abortReason && (
				<Note tone={outcome === "mergeFailed" ? "warn" : "err"}>{error}</Note>
			)}
			{(patchPath || branchName) && (
				<div className="tv-subagent-artifact">
					<span className="tv-subagent-artifact-key">
						{patchPath
							? t("toolRender.task.patch")
							: t("toolRender.task.branch")}
					</span>
					<span className="tv-path">{patchPath ?? branchName}</span>
				</div>
			)}
		</SubagentRow>
	);
}

/** Live (still-running) snapshot for one agent: 阶段 + 完成百分比、当前动作、限流重试。 */
function AgentProgressRow({
	p,
	host,
}: {
	p: Record<string, unknown>;
	host?: ToolRenderHost;
}): ReactNode {
	const { t } = useTranslation();
	const status = str(p.status) ?? "running";
	const state = PROGRESS_VISUAL[status] ?? "running";
	const id = str(p.id) ?? "agent";
	const live = state === "running";
	const percentRaw = num(p.completionPercent);
	const percent =
		percentRaw === null
			? null
			: Math.max(0, Math.min(100, Math.round(percentRaw)));
	const stage = str(p.stage) ?? str(p.lastIntent);
	const tool = str(p.currentTool);
	const toolWhat = str(p.currentToolIntent) ?? str(p.currentToolArgs);
	const retry = isRecord(p.retryState) ? p.retryState : null;
	const toolCount = num(p.toolCount);
	const tokens = num(p.tokens);
	const durationMs = num(p.durationMs);
	const model = str(p.resolvedModel);
	const label = agentStateLabel(t, state);
	return (
		<SubagentRow
			id={id}
			agent={str(p.agent)}
			state={state}
			description={str(p.description)}
			status={
				state === "pending" || state === "failed" || state === "aborted" ? (
					<Badge tone={state === "pending" ? undefined : "err"}>{label}</Badge>
				) : null
			}
			stats={[
				durationMs ? formatDuration(durationMs) : null,
				toolCount ? t("toolRender.task.tools", { count: toolCount }) : null,
				tokens
					? t("toolRender.task.tokens", { n: formatTokens(tokens) })
					: null,
				model && <ModelName model={model} />,
			]}
			host={host}
		>
			{live && (stage || percent !== null) && (
				<div className="tv-subagent-stage">
					<span className="tv-subagent-stage-text">{stage ?? label}</span>
					{percent !== null && (
						<>
							<span
								className="tv-subagent-meter"
								role="progressbar"
								aria-valuenow={percent}
								aria-valuemin={0}
								aria-valuemax={100}
								aria-label={t("toolRender.task.percent", { n: percent })}
								style={{ "--tv-frac": percent / 100 } as CSSProperties}
							/>
							<span className="tv-subagent-pct">{percent}%</span>
						</>
					)}
				</div>
			)}
			{live && tool && (
				<div className="tv-subagent-now">
					<span className="tv-subagent-tool">{tool}</span>
					{toolWhat && (
						<span className="tv-subagent-now-what">
							{truncate(normalizeWs(toolWhat), 140)}
						</span>
					)}
				</div>
			)}
			{retry && (
				<Note tone="warn">
					{t("toolRender.task.retry", {
						attempt: num(retry.attempt) ?? 1,
						max: num(retry.maxAttempts) ?? "?",
					})}
					{str(retry.errorMessage) &&
						` · ${truncate(normalizeWs(str(retry.errorMessage) ?? ""), 120)}`}
				</Note>
			)}
		</SubagentRow>
	);
}

/** 一段 Markdown 加一个小标签（派工单里的共享上下文、任务正文、分步）。 */
function Labeled({
	label,
	children,
}: {
	label: ReactNode;
	children: ReactNode;
}): ReactNode {
	return (
		<div className="tv-dispatch-part">
			<div className="tv-dispatch-label">{label}</div>
			{children}
		</div>
	);
}

/** 输入：派工单。共享上下文在最上，每个子 agent 一张（序号、头像与名字、agent 类型、隔离、任务正文、解空间）。 */
function Input({ args, host }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const context = str(args.context)?.trim();
	const tasks = taskItems(args);
	if (!context && tasks.length === 0) return null;
	return (
		<div className="tv-dispatch">
			{context && (
				<Labeled label={t("toolRender.task.context")}>
					<MarkdownBlock text={context} maxHeight={120} />
				</Labeled>
			)}
			{tasks.map((task, i) => (
				<div key={task.name ?? i} className="tv-dispatch-item">
					<div className="tv-dispatch-head">
						<span className="tv-dispatch-num">{i + 1}</span>
						{task.name &&
							(task.agent ? (
								<AgentTag agent={task.agent} state="unknown" host={host}>
									<AgentLink id={task.name} host={host}>
										{taskIdLabel(task.name)}
									</AgentLink>
								</AgentTag>
							) : (
								<AgentLink id={task.name} host={host}>
									{taskIdLabel(task.name)}
								</AgentLink>
							))}
						{task.agent && (
							<span className="tv-dispatch-agent">
								{!task.name && (
									<AgentTag agent={task.agent} state="unknown" host={host}>
										{null}
									</AgentTag>
								)}
								{task.agent}
							</span>
						)}
						{task.isolated && <Badge>{t("toolRender.task.isolated")}</Badge>}
						{task.steps.length > 0 && (
							<Badge>
								{t("toolRender.task.steps", { count: task.steps.length })}
							</Badge>
						)}
					</div>
					{task.task && <MarkdownBlock text={task.task} maxHeight={110} />}
					{task.steps.map((step, n) => (
						<Labeled
							key={n}
							label={t("toolRender.task.step", {
								n: n + 1,
								total: task.steps.length,
							})}
						>
							<MarkdownBlock text={step} maxHeight={90} />
						</Labeled>
					))}
					{task.solutionSpace && (
						<div className="tv-dispatch-space">
							<span className="tv-dispatch-label">
								{t("toolRender.task.solutionSpace")}
							</span>
							<span>{task.solutionSpace}</span>
						</div>
					)}
				</div>
			))}
		</div>
	);
}

/** 名册底部的汇总：各结局计数、总 token、总用时。 */
function Footer({
	results,
	totalMs,
}: {
	results: Record<string, unknown>[];
	totalMs: number | null;
}): ReactNode {
	const { t } = useTranslation();
	const counts: Record<Outcome, number> = {
		done: 0,
		mergeFailed: 0,
		aborted: 0,
		failed: 0,
	};
	let tokens = 0;
	for (const res of results) {
		counts[outcomeOf(res)]++;
		tokens += num(res.tokens) ?? 0;
	}
	const parts: ReactNode[] = [];
	if (counts.done > 0)
		parts.push(
			<span className="tv-ok-text">
				{t("toolRender.task.sum.done", { count: counts.done })}
			</span>,
		);
	if (counts.mergeFailed > 0)
		parts.push(
			<span className="tv-warn-text">
				{t("toolRender.task.sum.mergeFailed", { count: counts.mergeFailed })}
			</span>,
		);
	if (counts.failed > 0)
		parts.push(
			<span className="tv-err-text">
				{t("toolRender.task.sum.failed", { count: counts.failed })}
			</span>,
		);
	if (counts.aborted > 0)
		parts.push(
			<span className="tv-err-text">
				{t("toolRender.task.sum.aborted", { count: counts.aborted })}
			</span>,
		);
	if (tokens > 0)
		parts.push(t("toolRender.task.tokens", { n: formatTokens(tokens) }));
	if (totalMs != null && totalMs > 0)
		parts.push(t("toolRender.task.wall", { d: formatDuration(totalMs) }));
	if (results.length < 2 || parts.length === 0) return null;
	return (
		<div className="tv-subagent-foot">
			{parts.map((part, i) => (
				<span key={i}>{part}</span>
			))}
		</div>
	);
}

/** 输出：各子 agent 的最终结果与汇总，运行中则是结构化进度；都没有时回落到结果文本。 */
function Result(props: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const { result, partial, running, host } = props;
	const { results, progress, totalMs } = taskDetails(props);
	if (results.length > 0) {
		// 按派工顺序（index）排：与运行中的进度行、输入段的派工单一一对应，结束时行不跳位
		const ordered = [...results].sort(byIndex);
		return (
			<>
				{result?.isError && <ResultText result={result} maxLines={4} />}
				<div className="tv-subagents">
					{ordered.map((res, i) => (
						<AgentResult key={str(res.id) ?? i} res={res} host={host} />
					))}
				</div>
				<Footer results={results} totalMs={totalMs} />
			</>
		);
	}
	if (progress.length > 0) {
		return (
			<div className="tv-subagents">
				{progress.map((p, i) => (
					<AgentProgressRow key={str(p.id) ?? i} p={p} host={host} />
				))}
			</div>
		);
	}
	if (running && !result) {
		return (
			<div className="tv-subagent-empty">{t("toolRender.task.starting")}</div>
		);
	}
	return <ResultText result={result ?? partial} maxLines={12} />;
}

export const taskRenderer: ToolRenderer = {
	Summary,
	Meta,
	Input,
	Result,
	livePartial: true,
};
