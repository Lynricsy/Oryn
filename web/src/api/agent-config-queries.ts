/**
 * 设置 → Agent 各子页（技能、命令、扩展…）对 omp-home 文件接口的查询与写入。
 *
 * 文件接口不支持 `If-Match`：带 `base`（编辑起点的原文，`null` 表示应当不存在）写入时，先重读当前内容比对，
 * 不一致按 412 `modified` 抛出，并刷新缓存让界面显示最新内容；残余的竞争窗口是一次往返。
 * `config.yml` 的结构化改动（启停等）走 `useOmpConfig().mutate`：同一个 QueryClient 下串行执行，
 * 连点两个开关时后一次不会因为前一次刚写入而误报冲突。
 */
import {
	type QueryClient,
	skipToken,
	type UseQueryResult,
	useQueries,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useState } from "react";
import type { Document } from "yaml";
import { readConfigDoc, writeConfigDoc } from "../lib/omp-config";
import {
	ApiError,
	api,
	encodePathParam,
	rawFetch,
	type Schemas,
	unwrap,
} from "./client";
import { identityKeys } from "./identity-queries";

export type AgentFile = Schemas["AgentConfigFile"];
export type AgentWriteResult = Schemas["AgentConfigWriteResult"];

/** omp 主配置：`disabledExtensions` 等启停键写在这里。 */
export const OMP_CONFIG_PATH = "config.yml";

export const agentConfigKeys = {
	all: ["agent-config"] as const,
	files: ["agent-config", "files"] as const,
	text: (path: string) => ["agent-config", "text", path] as const,
};

const fileUrl = (path: string) =>
	`/api/v1/agent-config/files/${encodePathParam(path)}`;

export async function fetchAgentFiles(): Promise<AgentFile[]> {
	return (await unwrap(api.GET("/api/v1/agent-config/files"))).files;
}

/** 读文件原文；不存在时为 null。 */
export async function fetchAgentText(path: string): Promise<string | null> {
	try {
		return await (await rawFetch(fileUrl(path))).text();
	} catch (e) {
		if (e instanceof ApiError && e.status === 404) return null;
		throw e;
	}
}

/** 写入的冲突错误（编辑期间文件被别处改过或删掉）。 */
export function isModifiedError(e: unknown): boolean {
	return e instanceof ApiError && e.status === 412;
}

/**
 * 写入文本。给了 `base` 时先比对当前内容（`null` = 期望文件不存在），不一致抛 412 `modified`。
 */
export async function writeAgentText(
	path: string,
	text: string,
	base?: string | null,
): Promise<AgentWriteResult> {
	if (base !== undefined && (await fetchAgentText(path)) !== base)
		throw new ApiError(412, "modified", path);
	const res = await rawFetch(fileUrl(path), {
		method: "PUT",
		headers: { "Content-Type": "application/octet-stream" },
		body: text,
	});
	return (await res.json()) as AgentWriteResult;
}

export async function deleteAgentFile(path: string): Promise<void> {
	await rawFetch(fileUrl(path), { method: "DELETE" });
}

/**
 * 文件变动后的缓存同步：列表重取；config.yml 决定角色列表（身份与头像），一并重取；
 * 服务端随后下发：记下写入并重取生效状态，跟踪下发进度。
 */
function afterChange(qc: QueryClient, paths: readonly string[]): void {
	void qc.invalidateQueries({ queryKey: agentConfigKeys.files });
	if (paths.includes(OMP_CONFIG_PATH))
		void qc.invalidateQueries({ queryKey: identityKeys.roles });
	noteAgentConfigWrite(qc);
}

export function useAgentFiles(): UseQueryResult<AgentFile[]> {
	return useQuery({
		queryKey: agentConfigKeys.files,
		queryFn: fetchAgentFiles,
	});
}

export function useAgentText(
	path: string,
	enabled = true,
): UseQueryResult<string | null> {
	return useQuery({
		queryKey: agentConfigKeys.text(path),
		queryFn: () => fetchAgentText(path),
		enabled,
	});
}

/** 批量读原文：键为路径，值为原文（不存在为 null）；还没读到的不在表里。 */
export function useAgentTexts(paths: readonly string[]): {
	texts: ReadonlyMap<string, string | null>;
	error: unknown;
} {
	return useQueries({
		queries: paths.map((path) => ({
			queryKey: agentConfigKeys.text(path),
			queryFn: () => fetchAgentText(path),
		})),
		combine: (results) => {
			const texts = new Map<string, string | null>();
			results.forEach((r, i) => {
				const path = paths[i];
				if (path !== undefined && r.data !== undefined) texts.set(path, r.data);
			});
			return { texts, error: results.find((r) => r.error)?.error ?? null };
		},
	});
}

export interface AgentFileActions {
	/** 写入并更新缓存；`base` 见 `writeAgentText`。冲突时刷新该文件后重新抛出。 */
	write(
		path: string,
		text: string,
		base?: string | null,
	): Promise<AgentWriteResult>;
	/** 依次删除（目录式条目传其下全部文件）。 */
	remove(paths: readonly string[]): Promise<void>;
}

export function useAgentFileActions(): AgentFileActions {
	const qc = useQueryClient();
	return useMemo(
		() => ({
			async write(path, text, base) {
				try {
					const result = await writeAgentText(path, text, base);
					qc.setQueryData(agentConfigKeys.text(path), text);
					afterChange(qc, [path]);
					return result;
				} catch (e) {
					if (isModifiedError(e)) {
						void qc.invalidateQueries({ queryKey: agentConfigKeys.text(path) });
						void qc.invalidateQueries({ queryKey: agentConfigKeys.files });
					}
					throw e;
				}
			},
			async remove(paths) {
				try {
					for (const p of paths) await deleteAgentFile(p);
				} finally {
					for (const p of paths) qc.setQueryData(agentConfigKeys.text(p), null);
					afterChange(qc, paths);
				}
			},
		}),
		[qc],
	);
}

/** `disabledExtensions` 里的键；文档缺失、不合法或不是字符串列表时为空。 */
export function disabledIds(doc: Document | undefined): ReadonlySet<string> {
	if (!doc || doc.errors.length > 0) return new Set();
	const list: unknown = doc.toJS()?.disabledExtensions;
	if (!Array.isArray(list)) return new Set();
	return new Set(list.filter((v): v is string => typeof v === "string"));
}

/** config.yml 不是合法 YAML：结构化改动会拒绝写入（以免把坏文件再序列化一遍）。 */
export class ConfigInvalidError extends Error {
	constructor(message: string) {
		super(message);
		this.name = "ConfigInvalidError";
	}
}

/** 每个 QueryClient 一条串行队列。 */
const queues = new WeakMap<QueryClient, Promise<unknown>>();

export interface OmpConfig {
	/** 解析后的 config.yml；文件不存在时为空文档，读取中为 undefined */
	doc: Document | undefined;
	/** `disabledExtensions` 的键 */
	disabled: ReadonlySet<string>;
	isLoading: boolean;
	/** 读取失败 */
	error: unknown;
	/**
	 * 在最新的 config.yml 上执行 `edit`（返回 false 表示未改动，不写入），带冲突检查写回。
	 * 失败（含 412 冲突、文件不合法）时记进 `mutateError` 并返回 false；冲突时缓存已换成最新内容。
	 */
	mutate(edit: (doc: Document) => boolean | undefined): Promise<boolean>;
	pending: boolean;
	mutateError: unknown;
}

export function useOmpConfig(): OmpConfig {
	const qc = useQueryClient();
	const actions = useAgentFileActions();
	const q = useAgentText(OMP_CONFIG_PATH);
	const doc = useMemo(
		() => (q.data === undefined ? undefined : readConfigDoc(q.data ?? "")),
		[q.data],
	);
	const disabled = useMemo(() => disabledIds(doc), [doc]);
	const [pending, setPending] = useState(0);
	const [mutateError, setMutateError] = useState<unknown>(null);

	const mutate = useCallback(
		(edit: (doc: Document) => boolean | undefined) => {
			const run = async (): Promise<boolean> => {
				const key = agentConfigKeys.text(OMP_CONFIG_PATH);
				let base = qc.getQueryData<string | null>(key);
				if (base === undefined)
					base = await qc.fetchQuery({
						queryKey: key,
						queryFn: () => fetchAgentText(OMP_CONFIG_PATH),
					});
				const next = readConfigDoc(base ?? "");
				if (next.errors.length > 0)
					throw new ConfigInvalidError(next.errors[0]?.message ?? "");
				if (edit(next) === false) return false;
				await actions.write(OMP_CONFIG_PATH, writeConfigDoc(next), base);
				return true;
			};
			setPending((n) => n + 1);
			setMutateError(null);
			const prev = queues.get(qc) ?? Promise.resolve();
			const job = prev.then(run, run);
			queues.set(
				qc,
				job.catch(() => undefined),
			);
			return job
				.then(
					(changed) => changed,
					(e: unknown) => {
						setMutateError(e);
						return false;
					},
				)
				.finally(() => setPending((n) => n - 1));
		},
		[qc, actions],
	);

	return {
		doc,
		disabled,
		isLoading: q.isLoading,
		error: q.error,
		mutate,
		pending: pending > 0,
		mutateError,
	};
}

// ---------------------------------------------------------------- 生效状态（R29、R30）

export type ApplyStatus = Schemas["AgentConfigApplyStatus"];
export type ApplyStatusThread = Schemas["AgentConfigRestartThread"];

export const applyStatusKeys = {
	status: ["agent-config", "apply-status"] as const,
	/** 本页会话内最近一次写入与它的下发完成时间；不在 `agentConfigKeys.all` 下，免得被整体失效清掉。 */
	push: ["agent-config-apply", "push"] as const,
};

/** 下发进行中的轮询间隔（服务端防抖 1 秒后下发）。 */
const APPLY_PUSH_POLL_INTERVAL = 1000;
/** 还有会话等重启或等静止后重新加载时的轮询间隔。 */
const APPLY_POLL_INTERVAL = 5000;

/** 本页会话内最近一次写入（`writtenAt`）与观察到它下发完成的时间（`doneAt`，都是本机时钟）。 */
type PushMark = { writtenAt: number; doneAt?: number };

/**
 * omp-home 写入或删除成功之后调用（`useAgentFileActions` 已自动调用；「高级」的文件浏览器自己调用）。
 * 服务端在应答写入之前就登记了待下发，所以随即重取的生效状态里 `push.pending` 为真，直到这次写入下发完毕。
 */
export function noteAgentConfigWrite(qc: QueryClient): void {
	qc.setQueryData<PushMark>(applyStatusKeys.push, { writtenAt: Date.now() });
	void qc.invalidateQueries({ queryKey: applyStatusKeys.status });
}

export async function fetchApplyStatus(): Promise<ApplyStatus> {
	return unwrap(api.GET("/api/v1/agent-config/apply-status"));
}

/** 生效状态；下发进行中每秒轮询，有会话等重启或等重新加载时每 5 秒轮询。 */
export function useApplyStatus(): UseQueryResult<ApplyStatus> {
	return useQuery({
		queryKey: applyStatusKeys.status,
		queryFn: fetchApplyStatus,
		refetchInterval: (q) => {
			const d = q.state.data;
			if (d?.push.pending) return APPLY_PUSH_POLL_INTERVAL;
			return d && (d.threads.length > 0 || d.pendingReloads > 0)
				? APPLY_POLL_INTERVAL
				: false;
		},
	});
}

/** 本页会话内最近一次写入的下发进度：没写入过、下发中、已下发（附观察到完成的时间）。 */
export type PushProgress =
	| { phase: "idle" }
	| { phase: "pushing" }
	| { phase: "done"; doneAt: number };

/**
 * 由最近一次写入与生效状态推出下发进度：写入之后取到的状态里 `push.pending` 为假即下发完毕
 * （服务端在会话级分类落定之后才清掉 pending，此时 `threads` / `pendingReloads` 已含这次写入的结果）。
 */
export function usePushProgress(
	status: UseQueryResult<ApplyStatus>,
): PushProgress {
	const qc = useQueryClient();
	const mark = useQuery<PushMark>({
		queryKey: applyStatusKeys.push,
		queryFn: skipToken,
		staleTime: Number.POSITIVE_INFINITY,
		gcTime: Number.POSITIVE_INFINITY,
	}).data;
	const settled =
		mark !== undefined &&
		status.data !== undefined &&
		status.dataUpdatedAt >= mark.writtenAt &&
		!status.data.push.pending;
	useEffect(() => {
		if (!settled || mark.doneAt !== undefined) return;
		qc.setQueryData<PushMark>(applyStatusKeys.push, (prev) =>
			prev?.writtenAt === mark.writtenAt
				? { ...prev, doneAt: Date.now() }
				: prev,
		);
	}, [settled, mark, qc]);
	if (mark === undefined) return { phase: "idle" };
	if (mark.doneAt !== undefined) return { phase: "done", doneAt: mark.doneAt };
	return { phase: "pushing" };
}

/**
 * 重启需重启才能生效的会话：各会话静止后停掉 omp，下一条消息带着原会话拉起。
 * 返回的线程先在缓存里标为已安排，再重取生效状态。
 */
export async function restartSessions(qc: QueryClient): Promise<string[]> {
	const { threadIds } = await unwrap(
		api.POST("/api/v1/agent-config/restart-sessions"),
	);
	const scheduled = new Set(threadIds);
	qc.setQueryData<ApplyStatus>(applyStatusKeys.status, (prev) =>
		prev
			? {
					...prev,
					threads: prev.threads.map((t) =>
						scheduled.has(t.threadId) ? { ...t, scheduled: true } : t,
					),
				}
			: prev,
	);
	void qc.invalidateQueries({ queryKey: applyStatusKeys.status });
	return threadIds;
}
