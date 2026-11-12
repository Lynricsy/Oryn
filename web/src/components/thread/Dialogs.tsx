/** 线程内的模态对话框：会话树、fork、报告、模型、子 agent、btw、goal、标签、改名、计划提案、调试、setup 日志。 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import type { ReportKind } from "../../lib/slash";
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
import { getThreadStore, useThread } from "../../state/stores";
import { uiStore, useUi } from "../../state/ui";
import { isRecord } from "../../tool-render/util";
import { Markdown } from "../markdown/Markdown";
import {
	TranscriptContext,
	type TranscriptContextValue,
} from "../transcript/context";
import { MessageView } from "../transcript/MessageView";
import { Icon } from "../ui/icons";
import {
	Button,
	Dialog,
	ErrorText,
	Field,
	Select,
	Spinner,
	TextArea,
	TextInput,
	Toggle,
} from "../ui/primitives";
import { StructuredView } from "../ui/StructuredView";
import { SetupLogDialog } from "./EnvBanner";

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
					className="max-w-36"
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
			<ul className="flex flex-col">
				{visible.map(({ node, depth }) => (
					<li key={node.entryId}>
						<div
							className={`flex items-center gap-2 rounded px-2 py-1 text-sm ${selected === node.entryId ? "bg-hover" : ""}`}
							style={{ paddingLeft: `${0.5 + Math.min(depth, 8) * 1}rem` }}
						>
							<button
								type="button"
								className="flex min-w-0 flex-1 items-center gap-2 text-left"
								onClick={() => setSelected(node.entryId)}
							>
								<span
									className={`h-2 w-2 shrink-0 rounded-full ${node.onActiveBranch ? "bg-accent" : "bg-border"}`}
									title={node.onActiveBranch ? t("tree.active") : undefined}
								/>
								<span className="shrink-0 text-faint text-xs">
									{t(`tree.role.${node.role}`)}
								</span>
								<span className="min-w-0 flex-1 truncate">
									{node.textPreview || "…"}
								</span>
								{node.isLeaf && (
									<span className="text-faint text-xs">{t("tree.leaf")}</span>
								)}
								{node.label && (
									<span className="rounded bg-accent/15 px-1 text-accent text-xs">
										{node.label}
									</span>
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
									className="text-faint hover:text-fg"
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
				{(["share", "copy"] as const).map((v) => (
					<label
						key={v}
						className={`flex cursor-pointer gap-3 rounded-lg border p-3 ${env === v ? "border-accent bg-hover" : "border-border"}`}
					>
						<input
							type="radio"
							name="env"
							checked={env === v}
							onChange={() => setEnv(v)}
							className="mt-1"
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
				))}
			</div>
			<ErrorText error={error} />
		</Dialog>
	);
}

// ---------------------------------------------------------------- 报告

function ContextReport({ data }: { data: ResponseData<"get_context_report"> }) {
	const { t } = useTranslation();
	const b = data.breakdown;
	if (!b) return <StructuredView value={data} />;
	const rows: [string, number][] = [
		[t("report.ctx.systemPrompt"), b.systemPromptTokens],
		[t("report.ctx.systemTools"), b.systemToolsTokens],
		[t("report.ctx.systemContext"), b.systemContextTokens],
		[t("report.ctx.instructions"), b.instructionFilesTokens],
		[t("report.ctx.skills"), b.skillsTokens],
		[t("report.ctx.messages"), b.messagesTokens],
	];
	return (
		<div className="flex flex-col gap-2 text-sm">
			<div className="text-muted">
				{data.model} · {b.usedTokens.toLocaleString()} /{" "}
				{b.contextWindow.toLocaleString()}
			</div>
			{rows.map(([label, n]) => (
				<div key={label}>
					<div className="flex justify-between text-xs">
						<span>{label}</span>
						<span className="font-mono text-muted">{n.toLocaleString()}</span>
					</div>
					<div className="mt-0.5 h-1.5 rounded bg-inset">
						<div
							className="h-full rounded bg-accent"
							style={{
								width: `${Math.min(100, (n / Math.max(1, b.contextWindow)) * 100)}%`,
							}}
						/>
					</div>
				</div>
			))}
		</div>
	);
}

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
	const q = useRpc(threadId, { type: report });
	return (
		<Dialog open wide onClose={onClose} title={t(`report.${report}`)}>
			<ErrorText error={q.error} />
			{q.isPending && <Spinner />}
			{q.data !== undefined &&
				(report === "get_context_report" ? (
					<ContextReport data={q.data as ResponseData<"get_context_report">} />
				) : (
					<StructuredView value={q.data} />
				))}
		</Dialog>
	);
}

// ---------------------------------------------------------------- 模型

function ModelDialog({
	threadId,
	onClose,
}: {
	threadId: string;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const state = useThread(threadId, (s) => s.state);
	const models = useRpc(threadId, { type: "get_available_models" });
	const [q, setQ] = useState("");
	const [error, setError] = useState<unknown>(null);
	const list = (models.data?.models ?? []).filter((m) =>
		`${m.provider}/${m.id} ${m.name}`.toLowerCase().includes(q.toLowerCase()),
	);
	const run = async (frame: OmpCommand) => {
		setError(null);
		try {
			await realtime.cmd(threadId, frame);
			await refreshState(threadId);
		} catch (e) {
			setError(e);
		}
	};
	const levels = state?.availableThinkingLevels ?? [];
	return (
		<Dialog open wide onClose={onClose} title={t("model.title")}>
			<div className="mb-3 flex flex-wrap items-end gap-3">
				<Field label={t("model.thinking")}>
					<Select
						value={state?.thinkingConfigured ?? state?.thinkingLevel ?? ""}
						onChange={(e) =>
							void run({
								type: "set_thinking_level",
								level: e.target.value as "auto",
							})
						}
					>
						{["auto", ...levels].map((l) => (
							<option key={l} value={l}>
								{l}
							</option>
						))}
					</Select>
				</Field>
				<Toggle
					checked={state?.fastModeEnabled ?? false}
					onChange={(v) => void run({ type: "set_fast_mode", enabled: v })}
					label={t("model.fast")}
				/>
				<Toggle
					checked={state?.slowModeEnabled ?? false}
					disabled={!state?.slowModeSupported}
					onChange={(v) => void run({ type: "set_slow_mode", enabled: v })}
					label={t("model.slow")}
				/>
			</div>
			<TextInput
				placeholder={t("model.search")}
				value={q}
				onChange={(e) => setQ(e.target.value)}
				className="mb-2"
			/>
			<ErrorText error={error ?? models.error} />
			{models.isPending && <Spinner />}
			<ul className="flex flex-col">
				{list.map((m) => {
					const current =
						state?.model?.provider === m.provider && state?.model?.id === m.id;
					return (
						<li key={`${m.provider}/${m.id}`}>
							<button
								type="button"
								className={`flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm hover:bg-hover ${current ? "text-accent" : ""}`}
								onClick={() =>
									void run({
										type: "set_model",
										provider: m.provider,
										modelId: m.id,
									}).then(onClose)
								}
							>
								<span className="min-w-0 flex-1 truncate">
									{m.name || m.id}
								</span>
								<span className="font-mono text-faint text-xs">{`${m.provider}/${m.id}`}</span>
								{current && <Icon name="check" size={14} />}
							</button>
						</li>
					);
				})}
			</ul>
		</Dialog>
	);
}

// ---------------------------------------------------------------- 子 agent

function SubagentDialog({
	threadId,
	id,
	onClose,
}: {
	threadId: string;
	id: string;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const sub = useThread(threadId, (s) =>
		Object.values(s.subagents).find((x) => x.id === id),
	);
	const msgs = useQuery({
		queryKey: ["rpc", threadId, "subagent-messages", id],
		refetchInterval: sub && sub.status === "started" ? 2000 : false,
		retry: false,
		queryFn: () =>
			realtime.cmd(threadId, { type: "get_subagent_messages", subagentId: id }),
	});
	const [steer, setSteer] = useState("");
	const [error, setError] = useState<unknown>(null);
	const messages = (msgs.data?.messages ?? []) as unknown as JsonRecord[];
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
	return (
		<Dialog
			open
			wide
			onClose={onClose}
			title={`${sub?.agent ?? t("subagent.title")} · ${sub?.description ?? id}`}
			footer={
				<>
					<Button
						size="sm"
						onClick={() =>
							void act({ type: "cancel_subagent", subagentId: id })
						}
					>
						{t("subagent.cancel")}
					</Button>
					<Button
						size="sm"
						variant="danger"
						onClick={() => void act({ type: "abort_subagent", agentId: id })}
					>
						{t("subagent.abort")}
					</Button>
					<Button
						size="sm"
						onClick={() => void act({ type: "revive_subagent", agentId: id })}
					>
						{t("subagent.revive")}
					</Button>
				</>
			}
		>
			<ErrorText error={error ?? msgs.error} />
			{msgs.isPending && <Spinner />}
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
					if (!steer.trim()) return;
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
					onChange={(e) => setSteer(e.target.value)}
				/>
				<Button type="submit" disabled={!steer.trim()}>
					{t("common.send")}
				</Button>
			</form>
		</Dialog>
	);
}

// ---------------------------------------------------------------- btw

function BtwDialog({
	threadId,
	onClose,
}: {
	threadId: string;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const btw = useThread(threadId, (s) => s.btw);
	const order = useThread(threadId, (s) => s.btwOrder);
	const [q, setQ] = useState("");
	const [error, setError] = useState<unknown>(null);
	useQuery({
		queryKey: ["rpc", threadId, "btw-history"],
		retry: false,
		queryFn: async () => {
			const data = await realtime.cmd(threadId, { type: "get_btw_history" });
			const store = getThreadStore(threadId);
			store.getState().apply(
				data.records.map((record) => ({
					type: "frame" as const,
					frame: { type: "btw_record", record },
				})),
			);
			return data.records.length;
		},
	});
	return (
		<Dialog open wide onClose={onClose} title={t("btw.title")}>
			<p className="mb-2 text-muted text-xs">{t("btw.hint")}</p>
			<div className="flex flex-col gap-3">
				{order.map((id) => {
					const r = btw[id];
					if (!r) return null;
					return (
						<div key={id} className="rounded-lg border border-border p-3">
							<div className="mb-1 flex items-center gap-2 text-sm">
								<span className="min-w-0 flex-1 font-medium">{r.question}</span>
								<span className="text-faint text-xs">
									{t(`btw.status.${r.status}`)}
								</span>
								{r.status === "running" && (
									<Button
										size="sm"
										variant="ghost"
										onClick={() =>
											void realtime.cmd(threadId, {
												type: "btw_cancel",
												recordId: id,
											})
										}
									>
										{t("common.cancel")}
									</Button>
								)}
							</div>
							<Markdown text={r.answer} streaming={r.status === "running"} />
							{r.error && <p className="text-err text-xs">{r.error}</p>}
						</div>
					);
				})}
			</div>
			<ErrorText error={error} />
			<form
				className="mt-3 flex gap-2"
				onSubmit={(e) => {
					e.preventDefault();
					if (!q.trim()) return;
					setError(null);
					realtime
						.cmd(threadId, { type: "btw", question: q.trim() })
						.then(() => setQ(""), setError);
				}}
			>
				<TextInput
					autoFocus
					placeholder={t("btw.placeholder")}
					value={q}
					onChange={(e) => setQ(e.target.value)}
				/>
				<Button type="submit" variant="primary" disabled={!q.trim()}>
					{t("btw.ask")}
				</Button>
			</form>
		</Dialog>
	);
}

// ---------------------------------------------------------------- goal

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
	const [error, setError] = useState<unknown>(null);
	const qc = useQueryClient();
	const act = async (frame: OmpCommand) => {
		setError(null);
		try {
			await realtime.cmd(threadId, frame);
			await qc.invalidateQueries({ queryKey: ["rpc", threadId] });
			await refreshState(threadId);
		} catch (e) {
			setError(e);
		}
	};
	const g = goal.data;
	return (
		<Dialog open onClose={onClose} title={t("goal.title")}>
			{g?.enabled ? (
				<div className="mb-3 rounded-lg border border-border p-3 text-sm">
					<div className="font-medium">{g.objective}</div>
					<div className="mt-1 text-muted text-xs">
						{t(`goal.status.${g.status ?? "active"}`)}
						{g.tokenBudget
							? ` · ${(g.tokensUsed ?? 0).toLocaleString()} / ${g.tokenBudget.toLocaleString()}`
							: ""}
					</div>
					<div className="mt-2 flex gap-2">
						{g.status === "paused" ? (
							<Button
								size="sm"
								onClick={() => void act({ type: "set_goal", action: "resume" })}
							>
								{t("goal.resume")}
							</Button>
						) : (
							<Button
								size="sm"
								onClick={() => void act({ type: "set_goal", action: "pause" })}
							>
								{t("goal.pause")}
							</Button>
						)}
						<Button
							size="sm"
							variant="danger"
							onClick={() => void act({ type: "set_goal", action: "drop" })}
						>
							{t("goal.drop")}
						</Button>
					</div>
				</div>
			) : (
				<p className="mb-3 text-muted text-sm">{t("goal.none")}</p>
			)}
			<div className="flex flex-col gap-2">
				<Field label={t("goal.objective")}>
					<TextArea
						rows={3}
						value={objective}
						onChange={(e) => setObjective(e.target.value)}
					/>
				</Field>
				<Field label={t("goal.budget")}>
					<TextInput
						inputMode="numeric"
						value={budget}
						onChange={(e) => setBudget(e.target.value.replace(/\D/g, ""))}
					/>
				</Field>
				<div className="flex flex-wrap gap-2">
					<Button
						variant="primary"
						disabled={!objective.trim()}
						onClick={() =>
							void act({
								type: "set_goal",
								objective: objective.trim(),
								...(budget ? { tokenBudget: Number(budget) } : {}),
							})
						}
					>
						{t("goal.set")}
					</Button>
					<Button
						onClick={() =>
							void act(
								objective.trim()
									? { type: "guided_goal", initial: objective.trim() }
									: { type: "guided_goal" },
							)
						}
					>
						{t("goal.guided")}
					</Button>
				</div>
			</div>
			<ErrorText error={error ?? goal.error} />
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
					<pre
						key={d.key}
						className="overflow-auto rounded bg-inset p-2 font-mono text-[11px]"
					>
						{`${new Date(d.ts).toLocaleTimeString()} ${d.source}\n${JSON.stringify(d.frame, null, 2)}`}
					</pre>
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
	const [hidden, setHidden] = useState(false);
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
		return (
			<div className="mx-auto mb-2 flex w-full max-w-3xl items-center gap-2 rounded-lg border border-accent/50 bg-panel px-3 py-2 text-sm">
				<Icon name="list" size={14} className="text-accent" />
				<span className="min-w-0 flex-1 truncate">
					{t("plan.pending", { title: proposal.title })}
				</span>
				<Button size="sm" onClick={() => setHidden(false)}>
					{t("plan.review")}
				</Button>
			</div>
		);
	}
	return (
		<Dialog
			open
			wide
			onClose={() => setHidden(true)}
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
								variant={o === "execute" ? "primary" : "secondary"}
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
			<div className="mb-2 font-mono text-faint text-xs">
				{proposal.planFilePath}
			</div>
			<Markdown text={proposal.planContent} />
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
			return <ModelDialog threadId={threadId} onClose={close} />;
		case "subagent":
			return (
				<SubagentDialog threadId={threadId} id={dialog.id} onClose={close} />
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
