/** 线程内的模态对话框：会话树、fork、报告、模型、子 agent、btw、goal、标签、改名、附件（含公开发布）、分享、计划提案、调试、setup 日志。 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { useShareOverview } from "../../api/share-queries";
import {
	attachmentContentUrl,
	deleteAttachment,
	listThreadAttachments,
} from "../../lib/attachments";
import {
	formatBytes,
	formatCount,
	formatDateTime,
	formatDuration,
} from "../../lib/format";
import { selectModel } from "../../lib/model-select";
import type { ReportKind } from "../../lib/slash";
import { useBreakpoint } from "../../lib/viewport";
import type {
	OmpCommand,
	ResponseData,
	RpcPlanProposalFrame,
	RpcSessionTreeNode,
	SessionState,
} from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { forkThread, patchThread, renameThread } from "../../state/actions";
import type { JsonRecord } from "../../state/message-builder";
import { agentQueryable, getThreadStore, useThread } from "../../state/stores";
import {
	subagentFileOf,
	subagentFiles,
	subagentLabel,
	subagentMessages,
} from "../../state/subagent-entries";
import { uiStore, useUi } from "../../state/ui";
import { isRecord } from "../../tool-render/util";
import { RoleList } from "../composer/RolePicker";
import { Markdown } from "../markdown/Markdown";
import { AttachmentPublishButton } from "../share/AttachmentPublish";
import { ShareDialog } from "../share/ShareDialog";
import {
	TranscriptContext,
	type TranscriptContextValue,
} from "../transcript/context";
import { MessageView } from "../transcript/MessageView";
import { CopyButton } from "../ui/CopyButton";
import { Badge, Callout, Meter, type Tone, thresholdTone } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";
import { ModelLogo } from "../ui/ModelLogo";
import {
	Button,
	Dialog,
	describeError,
	EmptyState,
	ErrorText,
	Field,
	Radio,
	Select,
	Spinner,
	TextArea,
	TextInput,
	Toggle,
} from "../ui/primitives";
import { StructuredView } from "../ui/StructuredView";
import { BtwDialog } from "./BtwDialog";
import { ContextBar, ContextTable, useContextParts } from "./ContextBreakdown";
import { SetupLogDialog } from "./EnvBanner";
import { JOBS_POLL_MS, JobsReport, jobsRunning } from "./reports/JobsReport";
import {
	ConfirmStrip,
	ReportCard,
	ReportError,
	ReportLoading,
	ReportSection,
} from "./reports/parts";
import { SessionReport } from "./reports/SessionReport";
import { ToolsReport } from "./reports/ToolsReport";
import { UsageReport } from "./reports/UsageReport";
import { useOrbOf } from "./ThreadHeader";

// 稳定的空数组，避免 zustand 选择器每次返回新引用
const NO_LABELS: readonly string[] = [];

/** 打开对话框时向 agent 拉一次数据。 */
export function useRpc<T extends OmpCommand>(
	threadId: string,
	frame: T,
	enabled = true,
) {
	return useQuery({
		queryKey: ["rpc", threadId, frame],
		enabled,
		retry: false,
		queryFn: () =>
			realtime.cmd(threadId, frame) as Promise<ResponseData<T["type"]>>,
	});
}

async function refreshState(threadId: string) {
	const state = (await realtime.cmd(threadId, {
		type: "get_state",
	})) as SessionState;
	getThreadStore(threadId)
		.getState()
		.apply([{ type: "state", state }]);
}

// ---------------------------------------------------------------- 会话树

interface TreeRow {
	node: RpcSessionTreeNode;
	depth: number;
}

function flattenTree(nodes: readonly RpcSessionTreeNode[]): TreeRow[] {
	const children = new Map<string | null, RpcSessionTreeNode[]>();
	for (const n of nodes) {
		const list = children.get(n.parentId) ?? [];
		list.push(n);
		children.set(n.parentId, list);
	}
	const ids = new Set(nodes.map((n) => n.entryId));
	const roots = nodes.filter(
		(n) => n.parentId === null || !ids.has(n.parentId),
	);
	const out: TreeRow[] = [];
	const walk = (n: RpcSessionTreeNode, depth: number) => {
		out.push({ node: n, depth });
		const kids = (children.get(n.entryId) ?? []).sort(
			(a, b) => a.timestamp - b.timestamp,
		);
		// 单子节点链不增加缩进，只有分叉处才缩进
		for (const k of kids) walk(k, kids.length > 1 ? depth + 1 : depth);
	};
	for (const r of roots.sort((a, b) => a.timestamp - b.timestamp)) walk(r, 0);
	return out;
}

function SessionTreeDialog({
	threadId,
	onClose,
}: {
	threadId: string;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const tree = useRpc(threadId, { type: "get_session_tree" });
	const [filter, setFilter] = useState<"all" | "user" | "labeled">("all");
	const [q, setQ] = useState("");
	const [selected, setSelected] = useState<string | null>(null);
	const [summarize, setSummarize] = useState(false);
	const [instructions, setInstructions] = useState("");
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const [labelDraft, setLabelDraft] = useState<string | null>(null);
	const rows = useMemo(() => flattenTree(tree.data?.tree ?? []), [tree.data]);
	const visible = rows.filter(({ node }) => {
		if (filter === "user" && node.role !== "user") return false;
		if (filter === "labeled" && !node.label) return false;
		if (
			q &&
			!`${node.textPreview} ${node.label ?? ""}`
				.toLowerCase()
				.includes(q.toLowerCase())
		)
			return false;
		return true;
	});
	const jump = async () => {
		if (!selected) return;
		setBusy(true);
		setError(null);
		try {
			const frame: OmpCommand = summarize
				? {
						type: "switch_leaf",
						entryId: selected,
						summarize: true,
						...(instructions ? { customInstructions: instructions } : {}),
					}
				: { type: "switch_leaf", entryId: selected };
			const res = await realtime.cmd(threadId, frame, 600_000);
			if (isRecord(res)) {
				if (typeof res.editorText === "string" && res.editorText) {
					const store = getThreadStore(threadId);
					store.getState().patchLocal({
						editorText: {
							text: res.editorText,
							prepend: false,
							nonce: (store.getState().editorText?.nonce ?? 0) + 1,
						},
					});
				}
				if (res.askReanswerCommitted === true)
					await realtime.cmd(threadId, { type: "resume_after_ask_reanswer" });
			}
			onClose();
		} catch (e) {
			setError(e);
		} finally {
			setBusy(false);
		}
	};
	const saveLabel = async (entryId: string, label: string) => {
		await realtime.cmd(
			threadId,
			label
				? { type: "set_entry_label", entryId, label }
				: { type: "set_entry_label", entryId },
		);
		setLabelDraft(null);
		void tree.refetch();
	};
	return (
		<Dialog
			open
			wide
			onClose={onClose}
			title={t("tree.title")}
			footer={
				<>
					{busy && summarize && (
						<Button
							variant="danger"
							onClick={() =>
								void realtime.cmd(threadId, { type: "abort_branch_summary" })
							}
						>
							{t("tree.abortSummary")}
						</Button>
					)}
					<Button onClick={onClose}>{t("common.cancel")}</Button>
					<Button
						variant="primary"
						disabled={!selected || busy}
						onClick={() => void jump()}
					>
						{busy ? <Spinner /> : t("tree.jump")}
					</Button>
				</>
			}
		>
			<div className="mb-2 flex flex-wrap items-center gap-2">
				<TextInput
					className="max-w-60"
					placeholder={t("tree.search")}
					value={q}
					onChange={(e) => setQ(e.target.value)}
				/>
				<Select
					wrapperClassName="max-w-36"
					value={filter}
					onChange={(e) => setFilter(e.target.value as typeof filter)}
				>
					<option value="all">{t("tree.filterAll")}</option>
					<option value="user">{t("tree.filterUser")}</option>
					<option value="labeled">{t("tree.filterLabeled")}</option>
				</Select>
				<Toggle
					checked={summarize}
					onChange={setSummarize}
					label={t("tree.summarize")}
				/>
			</div>
			{summarize && (
				<TextInput
					className="mb-2"
					placeholder={t("tree.summaryInstructions")}
					value={instructions}
					onChange={(e) => setInstructions(e.target.value)}
				/>
			)}
			<ErrorText error={error ?? tree.error} />
			{tree.isPending && <Spinner />}
			<ul className="flex flex-col gap-px">
				{visible.map(({ node, depth }) => (
					<li key={node.entryId}>
						<div
							className={`flex min-h-8 items-center gap-2 rounded-sm px-2 py-1 text-sm transition-colors duration-150 ${selected === node.entryId ? "bg-hover" : "hover:bg-hover/60"}`}
							style={{ paddingLeft: `${0.5 + Math.min(depth, 8) * 1}rem` }}
						>
							<button
								type="button"
								className="flex min-w-0 flex-1 items-center gap-2 text-left"
								onClick={() => setSelected(node.entryId)}
							>
								<span
									className={`h-2 w-2 shrink-0 rounded-full ${node.onActiveBranch ? "bg-accent" : "bg-border-strong"}`}
									title={node.onActiveBranch ? t("tree.active") : undefined}
								/>
								<span className="w-8 shrink-0 text-faint text-xs">
									{t(`tree.role.${node.role}`)}
								</span>
								<span className="min-w-0 flex-1 truncate">
									{node.textPreview || "…"}
								</span>
								{node.isLeaf && <Badge>{t("tree.leaf")}</Badge>}
								{node.label && (
									<Badge tone="accent" icon="pin">
										{node.label}
									</Badge>
								)}
							</button>
							{labelDraft !== null && selected === node.entryId ? (
								<form
									className="flex gap-1"
									onSubmit={(e) => {
										e.preventDefault();
										void saveLabel(node.entryId, labelDraft);
									}}
								>
									<TextInput
										autoFocus
										className="h-7 w-32"
										value={labelDraft}
										onChange={(e) => setLabelDraft(e.target.value)}
									/>
									<Button size="sm" type="submit">
										{t("common.save")}
									</Button>
								</form>
							) : (
								<button
									type="button"
									className="grid h-6 w-6 shrink-0 place-items-center rounded-sm text-faint transition-colors duration-150 hover:bg-hover hover:text-fg"
									aria-label={t("tree.editLabel")}
									onClick={() => {
										setSelected(node.entryId);
										setLabelDraft(node.label ?? "");
									}}
								>
									<Icon name="edit" size={13} />
								</button>
							)}
						</div>
					</li>
				))}
			</ul>
		</Dialog>
	);
}

// ---------------------------------------------------------------- fork

function ForkDialog({
	threadId,
	entryId,
	onClose,
}: {
	threadId: string;
	entryId?: string;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const [env, setEnv] = useState<"share" | "copy">("share");
	const runner = useOrbOf(threadId)?.kind === "runner";
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("fork.title")}
			footer={
				<>
					<Button onClick={onClose}>{t("common.cancel")}</Button>
					<Button
						variant="primary"
						disabled={busy}
						onClick={async () => {
							setBusy(true);
							try {
								const thread = await forkThread(threadId, env, entryId);
								onClose();
								navigate(`/t/${thread.id}`);
							} catch (e) {
								setError(e);
							} finally {
								setBusy(false);
							}
						}}
					>
						{busy ? <Spinner /> : t("fork.submit")}
					</Button>
				</>
			}
		>
			<p className="mb-3 text-muted text-sm">
				{entryId ? t("fork.fromEntry") : t("fork.fromLeaf")}
			</p>
			<div className="flex flex-col gap-2">
				{/* runner（M11b）不能连环境 fork（409 runner_unsupported）：只有共用 */}
				{(runner ? (["share"] as const) : (["share", "copy"] as const)).map(
					(v) => (
						// biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Radio 组件（内含原生 input）
						<label
							key={v}
							className={`flex cursor-pointer gap-3 rounded-lg border p-3 transition-colors duration-150 ${env === v ? "border-accent bg-accent-soft" : "border-border bg-panel/60 hover:bg-hover"}`}
						>
							<Radio
								name="env"
								checked={env === v}
								onChange={() => setEnv(v)}
								className="mt-0.5"
							/>
							<span>
								<span className="block font-medium text-sm">
									{t(`fork.${v}`)}
								</span>
								<span className="block text-muted text-xs">
									{t(`fork.${v}Hint`)}
								</span>
							</span>
						</label>
					),
				)}
			</div>
			<ErrorText error={error} />
		</Dialog>
	);
}

// ---------------------------------------------------------------- 报告

function ContextReport({
	threadId,
	data,
}: {
	threadId: string;
	data: ResponseData<"get_context_report">;
}) {
	const { t } = useTranslation();
	const b = data.breakdown;
	// 与信息面板的上下文卡同一套分段（ContextBreakdown.tsx）
	const parts = useContextParts(threadId, b);
	if (!b || !parts) return <StructuredView value={data} />;
	const percent = (b.usedTokens / Math.max(1, b.contextWindow)) * 100;
	const tone = thresholdTone(percent);
	return (
		<div className="flex flex-col gap-3 text-sm">
			{/* 总览：模型 + 已用 / 窗口 + 彩色分段量表 */}
			<div className="rounded-md border border-border bg-raised p-3">
				<div className="flex flex-wrap items-center gap-2">
					{data.model && (
						<Badge icon="sparkle" title={data.model}>
							{data.model}
						</Badge>
					)}
					<span className="flex-1" />
					<span
						className={`font-semibold text-lg tabular-nums leading-none ${tone === "err" ? "text-err" : tone === "warn" ? "text-warn" : ""}`}
					>
						{Math.round(percent)}%
					</span>
					<span className="text-faint text-xs tabular-nums">
						{formatCount(b.usedTokens)} / {formatCount(b.contextWindow)}
					</span>
				</div>
				<ContextBar
					className="mt-2.5"
					parts={parts}
					contextWindow={b.contextWindow}
					tone={tone}
					label={t("panel.context")}
				/>
			</div>
			<ContextTable parts={parts} contextWindow={b.contextWindow} />
		</div>
	);
}

/**
 * 报告对话框：各报告有专属渲染（上下文、用量、后台任务、工具、会话统计）；加载态带文案，读取失败可重试，
 * 底部「刷新」重读。后台任务有运行中的任务时每 2 秒自动刷新。
 */
function ReportDialog({
	threadId,
	report,
	onClose,
}: {
	threadId: string;
	report: ReportKind;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	// 与 useRpc 同一个 key（信息面板的上下文卡共用缓存）；多了后台任务的轮询
	const q = useQuery({
		queryKey: ["rpc", threadId, { type: report }],
		retry: false,
		refetchInterval: (query) =>
			report === "get_jobs" &&
			jobsRunning(query.state.data as ResponseData<"get_jobs"> | undefined)
				? JOBS_POLL_MS
				: false,
		queryFn: () => realtime.cmd(threadId, { type: report }),
	});
	const refetch = () => void q.refetch();
	const data = q.data;
	// 自动刷新时不再给手动刷新按钮（它会每 2 秒闪一次禁用态）
	const polling =
		report === "get_jobs" && jobsRunning(data as ResponseData<"get_jobs">);
	let body: ReactNode = null;
	if (q.isPending)
		body = <ReportLoading label={t(`report.loading.${report}`)} />;
	else if (q.isError)
		body = (
			<ReportError
				title={t(`report.failed.${report}`)}
				error={q.error}
				retrying={q.isFetching}
				onRetry={refetch}
			/>
		);
	else if (report === "get_context_report")
		body = (
			<ContextReport
				threadId={threadId}
				data={data as ResponseData<"get_context_report">}
			/>
		);
	else if (report === "get_usage")
		body = (
			<UsageReport
				threadId={threadId}
				data={data as ResponseData<"get_usage">}
			/>
		);
	else if (report === "get_jobs")
		body = (
			<JobsReport
				threadId={threadId}
				data={data as ResponseData<"get_jobs">}
				onChanged={refetch}
			/>
		);
	else if (report === "get_active_tools")
		body = <ToolsReport data={data as ResponseData<"get_active_tools">} />;
	else
		body = <SessionReport data={data as ResponseData<"get_session_stats">} />;
	return (
		<Dialog
			open
			wide
			onClose={onClose}
			title={t(`report.${report}`)}
			footer={
				q.isSuccess &&
				!polling && (
					<Button
						size="sm"
						variant="ghost"
						disabled={q.isFetching}
						onClick={refetch}
					>
						{q.isFetching ? (
							<Spinner size={12} />
						) : (
							<Icon name="refresh" size={13} />
						)}
						{t("report.refresh")}
					</Button>
				)
			}
		>
			{body}
		</Dialog>
	);
}

// ---------------------------------------------------------------- 模型

function ModelDialog({
	threadId,
	initialQuery,
	onClose,
}: {
	threadId: string;
	/** 打开时预填的筛选词（`/model <选择器>` 解析不出时带过来） */
	initialQuery?: string;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const state = useThread(threadId, (s) => s.state);
	// 手机上自动聚焦搜索框会弹出软键盘，把顶部的角色列表挤出视野
	const compact = useBreakpoint() === "compact";
	const models = useRpc(threadId, { type: "get_available_models" });
	const [q, setQ] = useState(initialQuery ?? "");
	const [error, setError] = useState<unknown>(null);
	const all = models.data?.models ?? [];
	const list = all.filter((m) =>
		`${m.provider}/${m.id} ${m.name}`.toLowerCase().includes(q.toLowerCase()),
	);
	// 模型列表里直接选 = 手动选择（来源记录 `manual`，回合头回落）；旧 Core 自动回退到 set_model
	const pick = async (m: { provider: string; id: string }) => {
		setError(null);
		try {
			await selectModel(threadId, m);
			await refreshState(threadId);
		} catch (e) {
			setError(e);
		}
	};
	return (
		<Dialog open wide onClose={onClose} title={t("model.title")}>
			<RoleList threadId={threadId} onPicked={onClose} />
			{/* 思考级别在输入坞的圆圈菜单里调，速度在页脚的速度读数里调；这里只选模型 */}
			<h3 className="mb-1 px-2 font-medium text-2xs text-faint">
				{t("model.allModels")}
			</h3>
			{/* 桌面打开即可输入筛选；手机上不自动聚焦，免得软键盘把角色列表挤出视野 */}
			<TextInput
				autoFocus={!compact}
				placeholder={t("model.search")}
				value={q}
				onChange={(e) => setQ(e.target.value)}
				className="mb-2"
			/>
			<ErrorText error={error ?? models.error} />
			{models.isPending && (
				<div className="flex items-center gap-2 px-2 py-3 text-muted text-sm">
					<Spinner />
					{t("model.loading")}
				</div>
			)}
			{models.isSuccess && list.length === 0 && (
				<p className="px-2 py-3 text-muted text-sm">
					{all.length === 0
						? t("model.noModels")
						: t("model.noMatch", { q: q.trim() })}
				</p>
			)}
			<ul className="flex flex-col gap-px">
				{list.map((m) => {
					const current =
						state?.model?.provider === m.provider && state?.model?.id === m.id;
					return (
						<li key={`${m.provider}/${m.id}`}>
							<button
								type="button"
								data-model={`${m.provider}/${m.id}`}
								className={`flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm transition-colors duration-150 hover:bg-hover ${current ? "bg-hover font-medium" : ""}`}
								onClick={() =>
									void pick({ provider: m.provider, id: m.id }).then(onClose)
								}
							>
								<ModelLogo
									model={m.id}
									provider={m.provider}
									className="text-muted"
								/>
								<span className="min-w-0 flex-1 truncate">
									{m.name || m.id}
								</span>
								<span className="min-w-0 truncate font-mono text-faint text-xs">{`${m.provider}/${m.id}`}</span>
								{/* 与角色列表同一种选中样式：灰底 + 行尾中性勾 */}
								{current && (
									<Icon
										name="check"
										size={13}
										weight="bold"
										className="shrink-0 text-fg"
									/>
								)}
							</button>
						</li>
					);
				})}
			</ul>
		</Dialog>
	);
}

// ---------------------------------------------------------------- 子 agent

/**
 * 子 agent 状态 → 徽标色调：运行中用中性（强调色只给「需要你」的东西，见 docs/design/web-guidelines.md §2），
 * 失败为错误、完成为成功；状态文字照常显示。
 */
export function subagentTone(status: string): Tone {
	if (status === "failed" || status === "error" || status === "aborted")
		return "err";
	if (status === "completed" || status === "done") return "ok";
	return "neutral";
}

// goal 的状态色调（未列出的状态用中性）；进行中不用强调色（强调色只给「需要你」的东西）
const GOAL_TONE: Record<string, Tone> = {
	active: "neutral",
	paused: "warn",
	"budget-limited": "warn",
	complete: "ok",
};

/**
 * 子 agent 对话框。控制面已入库的子 agent 会话（`<产物目录>/<id>.jsonl` 的条目）优先：有就直接展示、不再向 agent
 * 拉取，orb 挂起时也能看；没有才用 `get_subagent_messages`。agent 不可查询时控制按钮与引导输入禁用。
 * `file` 指定存档（列表里的离线行）；只给 id 时取该 id 当前主会话的存档。指定的是较早主会话的同名存档时，
 * 它与在线的同 id 子 agent 不是同一个：不取实时状态、不发控制命令。
 */
export function SubagentDialog({
	threadId,
	id,
	file,
	onClose,
}: {
	threadId: string;
	id: string;
	file?: string;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const entries = useThread(threadId, (s) => s.entries);
	const files = useMemo(() => subagentFiles(entries), [entries]);
	const saved = subagentFileOf(files, id, file);
	const current =
		file === undefined || subagentFileOf(files, id)?.file === file;
	const liveSub = useThread(threadId, (s) =>
		Object.values(s.subagents).find((x) => x.id === id),
	);
	const sub = current ? liveSub : undefined;
	const queryable = useThread(threadId, agentQueryable);
	const controllable = queryable && current;
	const savedFile = saved?.file;
	const local = useMemo(
		() => (savedFile ? subagentMessages(entries, savedFile) : []),
		[entries, savedFile],
	);
	const msgs = useQuery({
		queryKey: ["rpc", threadId, "subagent-messages", id],
		enabled: local.length === 0 && controllable,
		refetchInterval: sub && sub.status === "started" ? 2000 : false,
		retry: false,
		queryFn: () =>
			realtime.cmd(threadId, { type: "get_subagent_messages", subagentId: id }),
	});
	const [steer, setSteer] = useState("");
	const [error, setError] = useState<unknown>(null);
	const remote = msgs.data?.messages;
	const messages = useMemo(
		() =>
			local.length > 0 ? local : ((remote ?? []) as unknown as JsonRecord[]),
		[local, remote],
	);
	const ctx: TranscriptContextValue = useMemo(() => {
		const results = new Map<string, JsonRecord>();
		for (const m of messages)
			if (m.role === "toolResult" && typeof m.toolCallId === "string")
				results.set(m.toolCallId, m);
		return {
			threadId,
			toolResults: results,
			liveTools: {},
			subagents: {},
			openSubagent: (sid) =>
				uiStore.getState().open({ kind: "subagent", id: sid }),
			forkFrom: () => undefined,
			labelEntry: () => undefined,
		};
	}, [messages, threadId]);
	const act = async (frame: OmpCommand) => {
		setError(null);
		try {
			await realtime.cmd(threadId, frame);
		} catch (e) {
			setError(e);
		}
	};
	const agent = sub?.agent ?? saved?.agent;
	return (
		<Dialog
			open
			wide
			onClose={onClose}
			title={`${agent ?? t("subagent.title")} · ${subagentLabel(sub?.description, saved) ?? id}`}
			footer={
				<>
					<Button
						size="sm"
						disabled={!controllable}
						onClick={() =>
							void act({ type: "cancel_subagent", subagentId: id })
						}
					>
						{t("subagent.cancel")}
					</Button>
					<Button
						size="sm"
						variant="danger"
						disabled={!controllable}
						onClick={() => void act({ type: "abort_subagent", agentId: id })}
					>
						{t("subagent.abort")}
					</Button>
					<Button
						size="sm"
						disabled={!controllable}
						onClick={() => void act({ type: "revive_subagent", agentId: id })}
					>
						{t("subagent.revive")}
					</Button>
				</>
			}
		>
			{(!queryable || !current) && (
				<div className="mb-3">
					<Callout tone="neutral" role="status">
						{t(queryable ? "subagent.archived" : "subagent.offline")}
					</Callout>
				</div>
			)}
			{(sub || saved) && (
				<div className="mb-3 flex flex-wrap items-center gap-1.5">
					{agent && <Badge icon="robot">{agent}</Badge>}
					{sub?.status && (
						<Badge tone={subagentTone(sub.status)}>{sub.status}</Badge>
					)}
					<span className="min-w-0 truncate font-mono text-2xs text-faint">
						{id}
					</span>
				</div>
			)}
			<ErrorText error={error ?? msgs.error} />
			{msgs.isLoading && <Spinner />}
			<TranscriptContext.Provider value={ctx}>
				<div className="flex flex-col gap-2">
					{messages
						.filter((m) => m.role !== "toolResult")
						.map((m, i) => (
							// biome-ignore lint/suspicious/noArrayIndexKey: 子 agent 消息无稳定 id
							<MessageView key={i} message={m} live={false} />
						))}
				</div>
			</TranscriptContext.Provider>
			<form
				className="mt-3 flex gap-2"
				onSubmit={(e) => {
					e.preventDefault();
					if (!controllable || !steer.trim()) return;
					void act({
						type: "steer_subagent",
						subagentId: id,
						message: steer.trim(),
					}).then(() => setSteer(""));
				}}
			>
				<TextInput
					placeholder={t("subagent.steer")}
					value={steer}
					disabled={!controllable}
					onChange={(e) => setSteer(e.target.value)}
				/>
				<Button type="submit" disabled={!controllable || !steer.trim()}>
					{t("common.send")}
				</Button>
			</form>
		</Dialog>
	);
}

// ---------------------------------------------------------------- goal

type GoalStep =
	| "pause"
	| "resume"
	| "drop"
	| "budget"
	| "clearBudget"
	| "set"
	| "guided";

/** 目标对话框里一步操作失败：哪一步（标题）+ 为什么（错误原文），就近显示在那一步所在的分区里。 */
function GoalFailure({
	step,
	error,
}: {
	step: GoalStep;
	error: unknown;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<Callout tone="err" role="alert" title={t(`goal.failed.${step}`)}>
			<span className="break-words">{describeError(error, t)}</span>
		</Callout>
	);
}

/**
 * 目标对话框：当前目标（状态、用量、暂停 / 继续 / 丢弃）、预算（只调预算，`set_goal` 只带 `tokenBudget`）、
 * 设定新目标（已有进行中的目标时提示会替换；目标暂停时须先继续或丢弃）。丢弃前确认；失败就近显示在对应分区。
 */
function GoalDialog({
	threadId,
	onClose,
}: {
	threadId: string;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const goal = useRpc(threadId, { type: "get_goal" });
	const [objective, setObjective] = useState("");
	const [budget, setBudget] = useState("");
	const [nextBudget, setNextBudget] = useState("");
	const [confirmDrop, setConfirmDrop] = useState(false);
	const [busy, setBusy] = useState<GoalStep | null>(null);
	const [failure, setFailure] = useState<{
		step: GoalStep;
		error: unknown;
	} | null>(null);
	const qc = useQueryClient();
	const act = async (step: GoalStep, frame: OmpCommand): Promise<boolean> => {
		setFailure(null);
		setBusy(step);
		try {
			await realtime.cmd(threadId, frame);
			await qc.invalidateQueries({ queryKey: ["rpc", threadId] });
			await refreshState(threadId);
			return true;
		} catch (error) {
			setFailure({ step, error });
			return false;
		} finally {
			setBusy(null);
		}
	};
	const failed = (...steps: GoalStep[]) =>
		failure && steps.includes(failure.step) ? (
			<GoalFailure step={failure.step} error={failure.error} />
		) : null;
	const g = goal.data;
	// 暂停的目标 `enabled` 为 false 但仍有目标；已放弃的不算
	const hasGoal = !!g?.objective && g.status !== "dropped";
	const paused = hasGoal && g?.status === "paused";
	const replacing = hasGoal && !!g?.enabled && g.status !== "complete";
	const used = g?.tokensUsed ?? 0;
	const parsedBudget = Number(nextBudget);
	return (
		<Dialog open onClose={onClose} title={t("goal.title")}>
			{goal.isPending ? (
				<ReportLoading label={t("goal.loading")} />
			) : goal.isError ? (
				<ReportError
					title={t("goal.loadFailed")}
					error={goal.error}
					retrying={goal.isFetching}
					onRetry={() => void goal.refetch()}
				/>
			) : (
				<div className="flex flex-col gap-5">
					<ReportSection title={t("goal.current")}>
						{hasGoal && g ? (
							<ReportCard className="flex flex-col gap-3">
								<div className="flex items-start gap-2">
									<Icon
										name="target"
										size={15}
										aria-hidden
										className="mt-0.5 shrink-0 text-faint"
									/>
									<p
										data-goal-objective
										className="min-w-0 flex-1 whitespace-pre-wrap break-words font-medium text-sm"
									>
										{g.objective}
									</p>
									<Badge tone={GOAL_TONE[g.status ?? "active"] ?? "neutral"}>
										{t(`goal.status.${g.status ?? "active"}`)}
									</Badge>
								</div>
								<div className="flex flex-col gap-1.5">
									<div className="flex flex-wrap items-baseline gap-x-3 text-faint text-xs tabular-nums">
										<span>
											{g.tokenBudget
												? t("goal.usedOf", {
														used: formatCount(used),
														budget: formatCount(g.tokenBudget),
													})
												: t("goal.usedNoBudget", { used: formatCount(used) })}
										</span>
										<span className="flex-1" />
										{(g.timeUsedSeconds ?? 0) > 0 && (
											<span>
												{t("goal.timeUsed", {
													d: formatDuration((g.timeUsedSeconds ?? 0) * 1000),
												})}
											</span>
										)}
									</div>
									{g.tokenBudget ? (
										<Meter
											value={used}
											max={g.tokenBudget}
											tone={thresholdTone((used / g.tokenBudget) * 100)}
											label={t("goal.usedOf", {
												used: formatCount(used),
												budget: formatCount(g.tokenBudget),
											})}
										/>
									) : null}
								</div>
								{confirmDrop ? (
									<ConfirmStrip
										label={t("goal.dropConfirmTitle")}
										body={t("goal.dropConfirmBody")}
										cancelLabel={t("goal.keep")}
										confirmLabel={t("goal.dropConfirm")}
										danger
										busy={busy === "drop"}
										onCancel={() => setConfirmDrop(false)}
										onConfirm={() =>
											void act("drop", {
												type: "set_goal",
												action: "drop",
											}).then((ok) => ok && setConfirmDrop(false))
										}
									/>
								) : (
									<div className="flex flex-wrap gap-2">
										{paused ? (
											<Button
												size="sm"
												icon="play"
												disabled={busy !== null}
												onClick={() =>
													void act("resume", {
														type: "set_goal",
														action: "resume",
													})
												}
											>
												{t("goal.resume")}
											</Button>
										) : g.enabled ? (
											<Button
												size="sm"
												icon="pause"
												disabled={busy !== null}
												onClick={() =>
													void act("pause", {
														type: "set_goal",
														action: "pause",
													})
												}
											>
												{t("goal.pause")}
											</Button>
										) : null}
										<Button
											size="sm"
											variant="danger"
											disabled={busy !== null}
											onClick={() => {
												setFailure(null);
												setConfirmDrop(true);
											}}
										>
											{t("goal.drop")}
										</Button>
									</div>
								)}
								{failed("pause", "resume", "drop")}
							</ReportCard>
						) : (
							<p className="px-0.5 text-muted text-sm">{t("goal.none")}</p>
						)}
					</ReportSection>

					{replacing && g && (
						<ReportSection title={t("goal.budgetTitle")}>
							<form
								className="flex flex-col gap-2"
								onSubmit={(e) => {
									e.preventDefault();
									if (!(parsedBudget > 0)) return;
									void act("budget", {
										type: "set_goal",
										tokenBudget: parsedBudget,
									}).then((ok) => ok && setNextBudget(""));
								}}
							>
								<div className="flex flex-wrap items-center gap-2">
									<span className="relative min-w-0 flex-1 basis-40">
										<TextInput
											inputMode="numeric"
											aria-label={t("goal.newBudget")}
											placeholder={
												g.tokenBudget
													? formatCount(g.tokenBudget)
													: t("goal.budgetPlaceholder")
											}
											value={nextBudget}
											onChange={(e) =>
												setNextBudget(e.target.value.replace(/\D/g, ""))
											}
											className="pr-16"
										/>
										<span className="pointer-events-none absolute top-1/2 right-3 -translate-y-1/2 text-faint text-xs">
											{t("goal.tokensUnit")}
										</span>
									</span>
									<Button
										type="submit"
										size="sm"
										disabled={!(parsedBudget > 0) || busy !== null}
									>
										{t("goal.updateBudget")}
									</Button>
									{g.tokenBudget ? (
										<Button
											size="sm"
											variant="ghost"
											disabled={busy !== null}
											onClick={() =>
												void act("clearBudget", {
													type: "set_goal",
													tokenBudget: null,
												})
											}
										>
											{t("goal.clearBudget")}
										</Button>
									) : null}
								</div>
								<p className="px-0.5 text-faint text-xs">
									{parsedBudget > 0
										? t("goal.budgetPreview", { n: formatCount(parsedBudget) })
										: t("goal.budgetHint")}
								</p>
								{failed("budget", "clearBudget")}
							</form>
						</ReportSection>
					)}

					<ReportSection
						title={replacing ? t("goal.replaceTitle") : t("goal.newTitle")}
					>
						{/* 目标暂停时 Core 不接受新目标：只留一句说明，不摆一套禁用的表单 */}
						{paused ? (
							<Callout tone="neutral" role="status">
								{t("goal.pausedBlocks")}
							</Callout>
						) : (
							<div className="flex flex-col gap-3">
								<Field label={t("goal.objective")}>
									<TextArea
										rows={3}
										value={objective}
										onChange={(e) => setObjective(e.target.value)}
									/>
								</Field>
								<Field label={t("goal.budget")} hint={t("goal.budgetHintNew")}>
									<TextInput
										inputMode="numeric"
										value={budget}
										onChange={(e) =>
											setBudget(e.target.value.replace(/\D/g, ""))
										}
									/>
								</Field>
								{/* 开始输入新目标时才提醒会替换，平时只靠分区标题「替换目标」 */}
								{replacing && objective.trim() && (
									<Callout tone="warn" role="status">
										{t("goal.replaceWarning")}
									</Callout>
								)}
								<div className="flex flex-wrap gap-2">
									<Button
										variant="primary"
										disabled={!objective.trim() || busy !== null}
										onClick={() =>
											void act("set", {
												type: "set_goal",
												objective: objective.trim(),
												...(budget ? { tokenBudget: Number(budget) } : {}),
											}).then((ok) => {
												if (!ok) return;
												setObjective("");
												setBudget("");
											})
										}
									>
										{replacing ? t("goal.replace") : t("goal.set")}
									</Button>
									{/* 引导式设定只在没有目标时可用（Core 对进行中的目标会拒绝） */}
									{!replacing && (
										<Button
											disabled={busy !== null}
											onClick={() =>
												void act(
													"guided",
													objective.trim()
														? { type: "guided_goal", initial: objective.trim() }
														: { type: "guided_goal" },
												).then((ok) => ok && onClose())
											}
										>
											{t("goal.guided")}
										</Button>
									)}
								</div>
								{failed("set", "guided")}
							</div>
						)}
					</ReportSection>
				</div>
			)}
		</Dialog>
	);
}

// ---------------------------------------------------------------- 标签 / 改名 / 线程标签

function EntryLabelDialog({
	threadId,
	entryId,
	onClose,
}: {
	threadId: string;
	entryId: string;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const [label, setLabel] = useState("");
	const [error, setError] = useState<unknown>(null);
	return (
		<Dialog open onClose={onClose} title={t("label.title")}>
			<form
				className="flex gap-2"
				onSubmit={async (e) => {
					e.preventDefault();
					try {
						await realtime.cmd(
							threadId,
							label
								? { type: "set_label", entryId, label }
								: { type: "set_label", entryId },
						);
						onClose();
					} catch (err) {
						setError(err);
					}
				}}
			>
				<TextInput
					autoFocus
					value={label}
					placeholder={t("label.placeholder")}
					onChange={(e) => setLabel(e.target.value)}
				/>
				<Button type="submit" variant="primary">
					{t("common.save")}
				</Button>
			</form>
			<ErrorText error={error} />
		</Dialog>
	);
}

function RenameDialog({
	threadId,
	onClose,
}: {
	threadId: string;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const current = useThread(threadId, (s) => s.thread?.title ?? "");
	const [title, setTitle] = useState(current);
	const [error, setError] = useState<unknown>(null);
	return (
		<Dialog open onClose={onClose} title={t("thread.rename")}>
			<form
				className="flex gap-2"
				onSubmit={async (e) => {
					e.preventDefault();
					try {
						await renameThread(threadId, title.trim());
						onClose();
					} catch (err) {
						setError(err);
					}
				}}
			>
				<TextInput
					autoFocus
					value={title}
					onChange={(e) => setTitle(e.target.value)}
				/>
				<Button type="submit" variant="primary" disabled={!title.trim()}>
					{t("common.save")}
				</Button>
			</form>
			<ErrorText error={error} />
		</Dialog>
	);
}

function LabelsDialog({
	threadId,
	onClose,
}: {
	threadId: string;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const current = useThread(threadId, (s) => s.thread?.labels) ?? NO_LABELS;
	const [text, setText] = useState(current.join(", "));
	const [error, setError] = useState<unknown>(null);
	return (
		<Dialog open onClose={onClose} title={t("thread.labels")}>
			<form
				className="flex flex-col gap-2"
				onSubmit={async (e) => {
					e.preventDefault();
					const labels = text
						.split(",")
						.map((l) => l.trim())
						.filter(Boolean);
					try {
						await patchThread(threadId, { labels });
						onClose();
					} catch (err) {
						setError(err);
					}
				}}
			>
				<Field label={t("thread.labelsHint")}>
					<TextInput
						autoFocus
						value={text}
						onChange={(e) => setText(e.target.value)}
					/>
				</Field>
				<div className="flex justify-end">
					<Button type="submit" variant="primary">
						{t("common.save")}
					</Button>
				</div>
			</form>
			<ErrorText error={error} />
		</Dialog>
	);
}

// ---------------------------------------------------------------- 附件

/** 附件图标：按 MIME 大类。 */
function attachmentIcon(mime: string): IconName {
	if (mime.startsWith("image/")) return "fileImage";
	if (mime.startsWith("text/") || mime === "application/pdf") return "fileText";
	if (mime === "application/json") return "fileCode";
	return "file";
}

function AttachmentsDialog({
	threadId,
	onClose,
}: {
	threadId: string;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const key = ["attachments", threadId];
	const q = useQuery({
		queryKey: key,
		queryFn: () => listThreadAttachments(threadId),
	});
	const [error, setError] = useState<unknown>(null);
	const [deleting, setDeleting] = useState<string | null>(null);
	// 已发布附件的撤回要用发布记录的 id（附件列表只带 publicUrl）
	const overview = useShareOverview();
	const list = q.data ?? [];
	return (
		<Dialog open wide onClose={onClose} title={t("attachments.title")}>
			{q.isPending && <Spinner />}
			<ErrorText error={q.error ?? error} />
			{q.isSuccess && list.length === 0 && (
				<EmptyState icon="attach" title={t("attachments.empty")}>
					{t("attachments.hint")}
				</EmptyState>
			)}
			<ul className="flex flex-col divide-y divide-border">
				{list.map((a) => (
					<li
						key={a.id}
						className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2 text-sm"
					>
						<span className="grid h-8 w-8 shrink-0 place-items-center rounded-md border border-border bg-inset text-faint">
							<Icon name={attachmentIcon(a.mime)} size={15} aria-hidden />
						</span>
						<span className="min-w-0 flex-1">
							<span className="block truncate font-medium">{a.name}</span>
							<span className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-faint text-xs tabular-nums">
								<Badge>{formatBytes(a.size)}</Badge>
								<span>{formatDateTime(a.createdAt)}</span>
								{a.publicUrl && (
									<>
										<Badge icon="globe">{t("publish.published")}</Badge>
										<CopyButton
											text={a.publicUrl}
											ariaLabel={t("publish.copyUrl")}
										/>
									</>
								)}
							</span>
						</span>
						<span className="ml-auto flex shrink-0 items-center gap-1">
							<AttachmentPublishButton
								attachment={a}
								published={overview.data?.files.find(
									(f) => f.attachmentId === a.id,
								)}
								onError={setError}
							/>
							<a
								href={attachmentContentUrl(a.id)}
								download={a.name}
								className="inline-flex h-7 items-center gap-1 rounded-md px-2 text-muted text-xs transition-colors duration-150 hover:bg-hover hover:text-fg"
							>
								<Icon name="download" size={13} aria-hidden />
								{t("attachments.download")}
							</a>
							<Button
								size="sm"
								variant="danger"
								icon="trash"
								disabled={deleting === a.id}
								onClick={async () => {
									if (
										!window.confirm(
											t("attachments.deleteConfirm", { name: a.name }),
										)
									)
										return;
									setError(null);
									setDeleting(a.id);
									try {
										await deleteAttachment(a.id);
										await qc.invalidateQueries({ queryKey: key });
									} catch (e) {
										setError(e);
									} finally {
										setDeleting(null);
									}
								}}
							>
								{t("attachments.delete")}
							</Button>
						</span>
					</li>
				))}
			</ul>
			{list.length > 0 && (
				<p className="mt-3 text-faint text-xs">
					{t("attachments.hint")} {t("publish.hint")}
				</p>
			)}
		</Dialog>
	);
}

// ---------------------------------------------------------------- 调试

function DebugDialog({
	threadId,
	onClose,
}: {
	threadId: string;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const debug = useThread(threadId, (s) => s.debug);
	return (
		<Dialog open wide onClose={onClose} title={t("debug.title")}>
			<p className="mb-2 text-muted text-xs">{t("debug.hint")}</p>
			{debug.length === 0 && (
				<p className="text-faint text-sm">{t("debug.empty")}</p>
			)}
			<div className="flex flex-col gap-2">
				{[...debug].reverse().map((d) => (
					<section
						key={d.key}
						className="overflow-hidden rounded-lg border border-border bg-inset"
					>
						<header className="flex items-center gap-2 border-border border-b px-2.5 py-1.5 text-faint text-xs">
							<Icon name="clock" size={12} aria-hidden />
							<span className="tabular-nums">{formatDateTime(d.ts)}</span>
							<Badge>{d.source}</Badge>
						</header>
						<pre className="overflow-auto px-2.5 py-2 font-mono text-2xs leading-relaxed">
							{JSON.stringify(d.frame, null, 2)}
						</pre>
					</section>
				))}
			</div>
		</Dialog>
	);
}

// ---------------------------------------------------------------- 计划提案

type PlanChoice = "execute" | "compact" | "keep_context" | "save" | "refine";

export function PlanProposalDialog({
	threadId,
	proposal,
}: {
	threadId: string;
	proposal: RpcPlanProposalFrame;
}) {
	const { t } = useTranslation();
	const [choice, setChoice] = useState<PlanChoice | null>(null);
	const [feedback, setFeedback] = useState("");
	const [savePath, setSavePath] = useState(proposal.suggestedFileName);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	// 收起时记下当时的 `planReviewNonce`：`/plan-review`（uiStore.reviewPlan）递增它，胶囊即回到对话框
	const reviewNonce = useUi((s) => s.planReviewNonce);
	const [hiddenAt, setHiddenAt] = useState<number | null>(null);
	const hidden = hiddenAt === reviewNonce;
	const options: PlanChoice[] = [
		"execute",
		"compact",
		"keep_context",
		"save",
		"refine",
	];
	const submit = async (c: PlanChoice) => {
		setBusy(true);
		setError(null);
		try {
			let frame: OmpCommand;
			if (c === "refine")
				frame = { type: "plan_approval", approved: false, feedback };
			else if (c === "save")
				frame = {
					type: "plan_approval",
					approved: true,
					option: "save",
					savePath,
				};
			else frame = { type: "plan_approval", approved: true, option: c };
			await realtime.cmd(threadId, frame, 600_000);
			getThreadStore(threadId).getState().patchLocal({ planProposal: null });
		} catch (e) {
			setError(e);
		} finally {
			setBusy(false);
		}
	};
	if (hidden) {
		// 输入坞玻璃托盘里的一节：透明、无边框、无外边距
		return (
			<div className="flex items-center gap-2.5 px-3.5 py-2 text-sm">
				<Icon
					name="list"
					size={15}
					weight="bold"
					aria-hidden
					className="shrink-0 text-accent"
				/>
				<span className="min-w-0 flex-1 truncate">
					{t("plan.pending", { title: proposal.title })}
				</span>
				<Button size="sm" variant="accent" onClick={() => setHiddenAt(null)}>
					{t("plan.review")}
				</Button>
			</div>
		);
	}
	return (
		<Dialog
			open
			wide
			onClose={() => setHiddenAt(uiStore.getState().planReviewNonce)}
			title={t("plan.title", { title: proposal.title })}
			footer={
				<div className="flex w-full flex-col gap-2">
					{choice === "refine" && (
						<TextArea
							rows={3}
							autoFocus
							placeholder={t("plan.feedback")}
							value={feedback}
							onChange={(e) => setFeedback(e.target.value)}
						/>
					)}
					{choice === "save" && (
						<TextInput
							value={savePath}
							onChange={(e) => setSavePath(e.target.value)}
						/>
					)}
					<ErrorText error={error} />
					<div className="flex flex-wrap justify-end gap-2">
						{options.map((o) => (
							<Button
								key={o}
								variant={o === "execute" ? "accent" : "secondary"}
								disabled={
									busy || (choice === o && o === "refine" && !feedback.trim())
								}
								onClick={() => {
									if ((o === "refine" || o === "save") && choice !== o) {
										setChoice(o);
										return;
									}
									void submit(o);
								}}
							>
								{t(`plan.option.${o}`)}
							</Button>
						))}
					</div>
				</div>
			}
		>
			{/* 打开时 Dialog 把焦点给正文第一个可聚焦元素：给可滚动的计划正文本身，方向键即可滚动。
			    计划是 agent 推来的、焦点是程序移入的，此处不画焦点环（否则关闭钮 / 代码块复制钮会顶着粗环出现）；
			    Tab 之后的按钮仍有全局 :focus-visible 环 */}
			{/* biome-ignore lint/a11y/noNoninteractiveTabindex: 可滚动区域需要能用键盘聚焦以便方向键滚动 */}
			<div tabIndex={0} className="outline-none">
				<div className="mb-3 flex min-w-0 items-center gap-1.5 font-mono text-faint text-xs">
					<Icon name="fileText" size={13} aria-hidden className="shrink-0" />
					<span className="min-w-0 truncate" title={proposal.planFilePath}>
						{proposal.planFilePath}
					</span>
				</div>
				<Markdown text={proposal.planContent} />
			</div>
		</Dialog>
	);
}

/** 渲染当前打开的线程对话框。 */
export function ThreadDialogs({ threadId }: { threadId: string }): ReactNode {
	const dialog = useUi((s) => s.dialog);
	const close = useUi((s) => s.close);
	if (!dialog) return null;
	switch (dialog.kind) {
		case "tree":
			return <SessionTreeDialog threadId={threadId} onClose={close} />;
		case "fork":
			return (
				<ForkDialog
					threadId={threadId}
					entryId={dialog.entryId}
					onClose={close}
				/>
			);
		case "report":
			return (
				<ReportDialog
					threadId={threadId}
					report={dialog.report}
					onClose={close}
				/>
			);
		case "model":
			return (
				<ModelDialog
					threadId={threadId}
					initialQuery={dialog.query}
					onClose={close}
				/>
			);
		case "subagent":
			return (
				<SubagentDialog
					threadId={threadId}
					id={dialog.id}
					file={dialog.file}
					onClose={close}
				/>
			);
		case "btw":
			return <BtwDialog threadId={threadId} onClose={close} />;
		case "goal":
			return <GoalDialog threadId={threadId} onClose={close} />;
		case "label":
			return (
				<EntryLabelDialog
					threadId={threadId}
					entryId={dialog.entryId}
					onClose={close}
				/>
			);
		case "labels":
			return <LabelsDialog threadId={threadId} onClose={close} />;
		case "rename":
			return <RenameDialog threadId={threadId} onClose={close} />;
		case "debug":
			return <DebugDialog threadId={threadId} onClose={close} />;
		case "attachments":
			return <AttachmentsDialog threadId={threadId} onClose={close} />;
		case "share":
			return <ShareDialog threadId={threadId} onClose={close} />;
		case "setupLog":
			return (
				<SetupLogDialog
					threadId={threadId}
					orbId={dialog.orbId}
					run={dialog.run}
					onClose={close}
				/>
			);
	}
}
