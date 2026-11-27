/**
 * Tool card chrome + per-tool dispatch. Works in the collab-web app and inside
 * the `<omp-tool-view>` web component embedded in HTML session exports.
 */
import type { ReactNode } from "react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Icon, type IconName } from "../components/ui/icons";
import { useExpand } from "../lib/use-expand";
import { Elapsed } from "./parts";
import {
	baseToolName,
	isComputerTool,
	ownLookup,
	resolveToolRenderer,
} from "./registry";
import { ARTIFACT_TOOLS, ARTIFACT_WRITE_TOOLS } from "./tools/artifact";
import { SANDBOX_TOOLS } from "./tools/sandbox";
import {
	isMcpToolName,
	mcpOrigin,
	unwrapXdevResult,
	XDEV_DEVICE_TOOL,
	XDEV_DOCS_TOOL,
	xdevCardOf,
} from "./tools/xdev";
import type {
	ToolJobMark,
	ToolRenderHost,
	ToolRenderProps,
	ToolResultLike,
} from "./types";
import { isRecord, replaceTabs, stripAnsi } from "./util";
import "./tool-render.css";

/** 工具图标：键是 `registry.ts` 的注册名，同一渲染器的别名共用图标；未列出的名字用 wrench。 */
const TOOL_ICON: Record<string, IconName> = {
	read: "fileText",
	write: "filePlus",
	edit: "edit",
	apply_patch: "edit",
	ast_edit: "code",
	ast_grep: "search",
	grep: "search",
	search: "search",
	glob: "folderOpen",
	find: "folderOpen",
	bash: "terminal",
	terminal: "terminal",
	eval: "code",
	js: "code",
	python: "code",
	notebook: "code",
	fetch: "globe",
	web_search: "globe",
	browser: "browser",
	puppeteer: "browser",
	task: "robot",
	todo: "list",
	ask: "message",
	goal: "target",
	github: "github",
	lsp: "code",
	debug: "bug",
	recall: "brain",
	reflect: "brain",
	retain: "brain",
	generate_image: "image",
	wait: "hourglass",
	yield: "checkCircle",
	resolve: "checkCircle",
	reject: "checkCircle",
	propose: "checkCircle",
	report_tool_issue: "alert",
	report_issue: "alert",
	checkpoint: "snapshot",
	rewind: "snapshot",
	memory_edit: "brain",
	tts: "megaphone",
	security_scan: "lock",
	ida: "cpu",
	desktop_record: "videoCamera",
	computer_record: "videoCamera",
	[XDEV_DOCS_TOOL]: "info",
	[XDEV_DEVICE_TOOL]: "device",
};

type ToolCat = "read" | "edit" | "exec" | "web" | "agent" | "other";

/** 工具类别：只决定悬停或展开时工具小图标的低彩度色调（`--cat-*`），不表达状态。 */
const TOOL_CAT: Record<string, ToolCat> = {
	read: "read",
	grep: "read",
	search: "read",
	ast_grep: "read",
	glob: "read",
	find: "read",
	lsp: "read",
	write: "edit",
	edit: "edit",
	apply_patch: "edit",
	ast_edit: "edit",
	bash: "exec",
	terminal: "exec",
	eval: "exec",
	js: "exec",
	python: "exec",
	notebook: "exec",
	debug: "exec",
	fetch: "web",
	web_search: "web",
	browser: "web",
	puppeteer: "web",
	github: "web",
	task: "agent",
	ask: "agent",
	goal: "agent",
	wait: "agent",
	recall: "agent",
	reflect: "agent",
	retain: "agent",
	yield: "agent",
	resolve: "agent",
	reject: "agent",
	propose: "agent",
	checkpoint: "agent",
	rewind: "agent",
	memory_edit: "agent",
	security_scan: "exec",
	ida: "exec",
	tts: "exec",
	[XDEV_DOCS_TOOL]: "read",
};

// Artifacts 工具（M14）一律用 shapes 图标；会写入的归 edit，其余归 read
for (const name of ARTIFACT_TOOLS) {
	TOOL_ICON[name] = "shapes";
	TOOL_CAT[name] = ARTIFACT_WRITE_TOOLS.includes(name) ? "edit" : "read";
}
// Agent 沙箱工具（M30）：在沙箱里跑命令、传文件，图标与类别同 bash
for (const name of SANDBOX_TOOLS) {
	TOOL_ICON[name] = "terminal";
	TOOL_CAT[name] = "exec";
}

/** omp 线路上工具参数里的意图字段名（原为 `@oh-my-pi/pi-wire` 的 INTENT_FIELD）。 */
const INTENT_FIELD = "i";

export interface ToolViewProps {
	name: string;
	args?: unknown;
	result?: ToolResultLike;
	/** Tool is still executing (live collab view). */
	running?: boolean;
	/** Model-provided intent (`i`): shown in the header instead of the summary, collapsed or expanded. */
	intent?: string;
	/** Streaming partial output tail while running. */
	partial?: string;
	/** 运行中的结构化部分结果；渲染器声明 `livePartial` 时由它自己展示，替代文本尾巴。 */
	partialResult?: ToolResultLike;
	/** 调用开始执行的时刻（epoch ms，实时视图才有）：运行中头部据此走表，渲染器也拿到它。 */
	startedAt?: number;
	/**
	 * 默认是否展开：用户还没手动开合时跟随这个值（宿主据此让当前一批工具调用展开、之后收起），
	 * 用户点过头部后以用户为准。
	 */
	defaultOpen?: boolean;
	/** 递增时清除用户手动开合态，重新跟随 `defaultOpen`（如 Rewind 模式 Ctrl+O 批量切换）。 */
	expandEpoch?: number;
	/** Host capabilities (sub-session drill-down, …). */
	host?: ToolRenderHost;
	/** 这次调用转入了后台：任务此刻的状态（宿主合成）。头部动态标记，展开体附最近输出与入口。 */
	job?: ToolJobMark;
	/** 卡片根元素的 ref（宿主在上面登记右键菜单）。 */
	rootRef?: (el: HTMLDivElement | null) => void;
}

function normalizeArgs(raw: unknown): {
	args: Record<string, unknown>;
	intent: string | undefined;
} {
	if (!isRecord(raw)) return { args: {}, intent: undefined };
	const intent =
		typeof raw[INTENT_FIELD] === "string"
			? (raw[INTENT_FIELD] as string).trim()
			: undefined;
	if (!(INTENT_FIELD in raw)) return { args: raw, intent };
	const args: Record<string, unknown> = {};
	for (const k in raw) {
		if (k !== INTENT_FIELD) args[k] = raw[k];
	}
	return { args, intent };
}

export function ToolView(props: ToolViewProps): ReactNode {
	const { t } = useTranslation();
	// 用户点过头部后以用户为准；否则跟随宿主给的 defaultOpen。expandEpoch 变化时清掉手动覆写。
	const [userOpen, setUserOpen] = useState<boolean | null>(null);
	const [prevEpoch, setPrevEpoch] = useState(props.expandEpoch);
	if (props.expandEpoch !== prevEpoch) {
		setPrevEpoch(props.expandEpoch);
		setUserOpen(null);
	}
	const open = userOpen ?? props.defaultOpen ?? false;
	// 展开体开合做高度过渡；收起动画期间仍挂着（aria-expanded 已经是收起）
	const body = useExpand<HTMLDivElement>(open);
	const { args: outerArgs, intent: argIntent } = normalizeArgs(
		props.name === "ask" && typeof props.args === "string"
			? { html: props.args }
			: props.args,
	);
	// `write` / `read xd://…`：换成真实工具（或设备文档卡）的名字与参数
	const xdev = xdevCardOf({
		name: props.name,
		args: outerArgs,
		result: props.result,
		partialResult: props.partialResult,
	});
	const intent = props.intent?.trim() || argIntent || xdev?.intent;
	const wireName = xdev?.tool ?? props.name;
	// 助手的电脑（M19）：`computer_<x>` 的卡片与 `<x>` 相同（渲染器、图标、类别），头部多一个「电脑」小标
	const computer = isComputerTool(wireName);
	const name = baseToolName(wireName);
	const unwrap = (r: ToolResultLike | undefined) =>
		xdev?.unwrap ? unwrapXdevResult(r) : r;
	const result = unwrap(props.result);
	const renderer = resolveToolRenderer(name);
	const renderProps: ToolRenderProps = {
		name,
		args: xdev?.args ?? outerArgs,
		result,
		running: props.running,
		partial: props.running && !result ? unwrap(props.partialResult) : undefined,
		startedAt: props.startedAt,
		host: props.host,
	};
	// MCP 工具（顶层或 `xd://mcp__…`）：头部名字写成 server / tool，完整工具名放进 title
	const mcp = mcpOrigin(
		name,
		(renderProps.result ?? renderProps.partial)?.details,
	);
	const label = xdev?.label ?? name;

	const job = props.job;
	const isError = props.result?.isError === true;
	// 调用已返回、任务还在后台跑：头部仍是「运行中」
	const status =
		props.running || job?.status === "running"
			? "run"
			: isError || job?.status === "failed"
				? "err"
				: props.result
					? "ok"
					: "pending";
	const partial =
		props.running &&
		!props.result &&
		props.partial &&
		!(renderer.livePartial === true && renderProps.partial)
			? stripAnsi(replaceTabs(props.partial))
			: "";
	// 这张卡在本次挂载里跑过：结束时结果图标弹出（历史卡片、重新挂载的卡片不播放）
	const [ran, setRan] = useState(false);
	if (status === "run" && !ran) setRan(true);
	const jobOutput = job?.output ? stripAnsi(replaceTabs(job.output)) : "";

	return (
		<div
			ref={props.rootRef}
			className={`tv-card${isError ? " tv-card--error" : ""}${status === "run" ? " tv-card--running" : ""}`}
		>
			<button
				type="button"
				className="tv-head"
				data-ctx-touch=""
				aria-expanded={open}
				onClick={() => setUserOpen(!open)}
				title={intent || undefined}
			>
				<span
					className={`tv-cat tv-cat--${ownLookup(TOOL_CAT, name) ?? "other"}`}
					aria-hidden="true"
				>
					<Icon
						name={
							ownLookup(TOOL_ICON, name) ??
							(isMcpToolName(name) ? "plugs" : "wrench")
						}
						size={13}
					/>
				</span>
				{mcp ? (
					<span className="tv-name" title={label}>
						<span className="tv-muted">{mcp.server} / </span>
						{mcp.tool}
					</span>
				) : (
					<span className="tv-name" title={computer ? wireName : undefined}>
						{label}
					</span>
				)}
				{computer && (
					<span className="tv-badge" data-testid="tool-computer-badge">
						{t("toolRender.computer")}
					</span>
				)}
				{/* 头部说「为什么调用」（意图），收起、展开都一样；没有意图才显示参数摘要。完整参数在展开体的输入段 */}
				<span className="tv-sum">
					{intent ? (
						<span className="tv-sum-intent">{intent}</span>
					) : (
						<renderer.Summary {...renderProps} />
					)}
				</span>
				{renderer.Meta && (
					<span className="tv-meta">
						<renderer.Meta {...renderProps} />
					</span>
				)}
				{job && (
					<span
						className={`tv-job tv-job--${job.status}`}
						title={t("toolRender.job.title", { id: job.id })}
					>
						<span className="tv-job-dot" aria-hidden="true" />
						{t(`toolRender.job.${job.status}`)}
					</span>
				)}
				{/* 调用本身在跑：已用时长每秒走动（CSS 延迟 1 秒才显现，瞬间完成的调用不闪一下） */}
				{props.running && props.startedAt !== undefined && (
					<Elapsed since={props.startedAt} className="tv-elapsed" />
				)}
				{status === "run" ? (
					<span
						role="status"
						className="tv-spin"
						aria-label={t("toolRender.running")}
					/>
				) : (
					<span
						className={`tv-status tv-status--${status}${ran ? " tv-status--settle" : ""}`}
						aria-hidden="true"
					>
						<Icon
							name={
								status === "ok"
									? "checkCircle"
									: status === "err"
										? "xCircle"
										: "circle"
							}
							size={14}
						/>
					</span>
				)}
				<span className="tv-chev" aria-hidden="true">
					<Icon name="chevronRight" size={12} />
				</span>
			</button>
			{body.shown && (
				<div ref={body.ref} className="tv-body">
					{renderer.Input && (
						<ToolSection kind="in" label={t("toolRender.input")}>
							<renderer.Input {...renderProps} />
						</ToolSection>
					)}
					{renderer.Result && (
						<ToolSection kind="out" label={t("toolRender.output")}>
							<renderer.Result {...renderProps} />
						</ToolSection>
					)}
					{job && (
						<section
							className="tv-sec tv-sec--job"
							aria-label={t("toolRender.job.title", { id: job.id })}
						>
							<div className="tv-jobhead">
								<span className="tv-jobhead-label">
									{t("toolRender.job.title", { id: job.id })}
								</span>
								{job.onOpen && (
									<button
										type="button"
										className="tv-joblink"
										onClick={job.onOpen}
									>
										{t("toolRender.job.open")}
										<Icon name="chevronRight" size={11} />
									</button>
								)}
							</div>
							{jobOutput && (
								<pre className="tv-jobout">
									{jobOutput.length > 4096
										? `…${jobOutput.slice(-4096)}`
										: jobOutput}
								</pre>
							)}
						</section>
					)}
				</div>
			)}
			{partial && (
				<pre className="tv-partial">
					{partial.length > 2048 ? `…${partial.slice(-2048)}` : partial}
				</pre>
			)}
		</div>
	);
}

/**
 * 展开体的一段（输入 / 输出）：不写可见标题，只靠底色与分隔线区分；`label` 只给读屏器。
 * 内容渲染为空时整段由 CSS 隐去（`.tv-sec:has(> .tv-sec-body:empty)`），渲染器不必预判自己是否有内容。
 */
function ToolSection({
	kind,
	label,
	children,
}: {
	kind: "in" | "out";
	label: string;
	children: ReactNode;
}): ReactNode {
	return (
		<section className={`tv-sec tv-sec--${kind}`} aria-label={label}>
			<div className="tv-sec-body">{children}</div>
		</section>
	);
}
