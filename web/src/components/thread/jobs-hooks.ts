/**
 * 后台任务的 React 入口：`get_jobs` 快照的查询与轮询、合成后的任务列表（`state/jobs.ts`）。
 *
 * 快照与 `/jobs` 报告对话框共用一个查询 key；有运行中的任务时每 2 秒轮询，转录里有任务启动或结束时立刻重取。
 * 合成结果按线程缓存一份：同一帧里多张工具卡、面板与对话框读到的是同一个对象，不各算一遍。
 */
import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { realtime } from "../../realtime/socket";
import {
	type JobsSnapshot,
	type JobView,
	jobsSignature,
	mergeJobs,
	type ThreadJobs,
} from "../../state/jobs";
import { agentQueryable, getThreadStore, useThread } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { JOBS_POLL_MS, jobsRunning } from "./reports/JobsReport";

const JOBS_FRAME = { type: "get_jobs" } as const;

/** 每份快照请求发出时转录里的任务指纹：快照取到时转录已经见过的任务，快照没列出才能判为已结束。 */
const snapshotSigs = new WeakMap<JobsSnapshot, string>();

/** `get_jobs` 快照：agent 可查询时取；有运行中的任务时轮询；转录里的任务集合变化时重取。 */
export function useJobsSnapshot(threadId: string): {
	data: JobsSnapshot | undefined;
	current: boolean;
} {
	const ready = useThread(threadId, agentQueryable);
	const sig = useThread(threadId, (s) => jobsSignature(s.entries));
	const q = useQuery({
		queryKey: ["rpc", threadId, JOBS_FRAME],
		enabled: ready,
		retry: false,
		refetchOnWindowFocus: false,
		// 指纹按数据对象记：结构共享会把内容相同的新快照换回旧对象（带着旧指纹）
		structuralSharing: false,
		refetchInterval: (query) =>
			jobsRunning(query.state.data as JobsSnapshot | undefined)
				? JOBS_POLL_MS
				: false,
		queryFn: async () => {
			const at = jobsSignature(getThreadStore(threadId).getState().entries);
			const data = (await realtime.cmd(threadId, JOBS_FRAME)) as JobsSnapshot;
			snapshotSigs.set(data, at);
			return data;
		},
	});
	const { refetch } = q;
	const seen = useRef(sig);
	useEffect(() => {
		if (seen.current === sig) return;
		seen.current = sig;
		// 多个订阅者同时看到变化：并入在途请求，不互相取消
		if (ready) void refetch({ cancelRefetch: false });
	}, [sig, ready, refetch]);
	return {
		data: q.data,
		current: q.data !== undefined && snapshotSigs.get(q.data) === sig,
	};
}

interface MergeSlot {
	entries: unknown;
	liveTools: unknown;
	snapshot: unknown;
	current: boolean;
	out: ThreadJobs;
}

const mergeSlots = new Map<string, MergeSlot>();

/** 线程合成后的后台任务。 */
export function useThreadJobs(threadId: string): ThreadJobs {
	const entries = useThread(threadId, (s) => s.entries);
	const liveTools = useThread(threadId, (s) => s.liveTools);
	const { data, current } = useJobsSnapshot(threadId);
	const slot = mergeSlots.get(threadId);
	if (
		slot &&
		slot.entries === entries &&
		slot.liveTools === liveTools &&
		slot.snapshot === data &&
		slot.current === current
	)
		return slot.out;
	const out = mergeJobs(entries, liveTools, data, current);
	mergeSlots.set(threadId, {
		entries,
		liveTools,
		snapshot: data,
		current,
		out,
	});
	return out;
}

/** 按对话框参数找任务：指定 key 时取那一次，否则取同 id 最近的一次。 */
export function findJob(
	jobs: ThreadJobs,
	id: string,
	key?: string,
): JobView | undefined {
	if (key) {
		const hit = jobs.byKey.get(key);
		if (hit) return hit;
	}
	return jobs.jobs.findLast((j) => j.id === id);
}

/** 打开后台任务详情对话框。 */
export function openJob(id: string, key?: string) {
	uiStore
		.getState()
		.open(key && key !== id ? { kind: "job", id, key } : { kind: "job", id });
}
