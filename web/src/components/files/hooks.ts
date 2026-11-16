/** 文件 / 改动面板共用的小型 hook：容器宽度与 agent 活动触发的防抖刷新。 */
import { type RefObject, useEffect, useRef, useState } from "react";
import { ApiError } from "../../api/client";
import { getThreadStore } from "../../state/stores";
import { terminalHub } from "../../state/terminals";

/** 元素当前宽度（ResizeObserver）；不支持时返回 `fallback`。 */
export function useElementWidth(
	ref: RefObject<HTMLElement | null>,
	fallback = 0,
): number {
	const [width, setWidth] = useState(fallback);
	useEffect(() => {
		const el = ref.current;
		if (!el || typeof ResizeObserver === "undefined") return;
		const ro = new ResizeObserver((entries) => {
			const w = entries[0]?.contentRect.width;
			if (w !== undefined) setWidth(w);
		});
		ro.observe(el);
		setWidth(el.getBoundingClientRect().width || fallback);
		return () => ro.disconnect();
	}, [ref, fallback]);
	return width;
}

/** agent 活动后的刷新防抖（计划：`tool_execution_end` / `agent_end` / `terminal_exit` 后 1s）。 */
export const ACTIVITY_DEBOUNCE_MS = 1000;

/**
 * 线程里的工作区可能变化时（工具结束、agent 回合结束、条目新增，可选终端退出）
 * 以 1s 防抖调用 `onActivity`。`threadId` 为空时不订阅。
 */
export function useWorkspaceActivity(
	threadId: string | undefined,
	onActivity: () => void,
	opts: { terminal?: boolean } = {},
): void {
	const cb = useRef(onActivity);
	cb.current = onActivity;
	const terminal = opts.terminal ?? false;
	useEffect(() => {
		if (!threadId) return;
		let timer: ReturnType<typeof setTimeout> | undefined;
		const bump = () => {
			clearTimeout(timer);
			timer = setTimeout(() => cb.current(), ACTIVITY_DEBOUNCE_MS);
		};
		const unsub = getThreadStore(threadId).subscribe((s, prev) => {
			if (prev.running && !s.running) return bump();
			if (s.entries.length !== prev.entries.length) return bump();
			for (const id of s.liveToolOrder) {
				if (s.liveTools[id]?.ended && !prev.liveTools[id]?.ended) return bump();
			}
		});
		const unsubTerm = terminal ? terminalHub.onExit(threadId, bump) : undefined;
		return () => {
			clearTimeout(timer);
			unsub();
			unsubTerm?.();
		};
	}, [threadId, terminal]);
}

/** `orb_unavailable`（唤醒超时）后重试前的等待。 */
export const WAKE_RETRY_MS = 3000;
/** `offline_indexing` 未带 `Retry-After` 时的重试间隔。 */
export const INDEXING_RETRY_MS = 2000;
/** `offline_indexing` 的累计等待上限：provider 首次为停机的 orb 建文件索引，大仓库也应在此之内完成。 */
export const INDEXING_WAIT_MAX_MS = 3 * 60_000;

/** 请求重试期间在等什么：唤醒 orb，或 provider 读取停机 orb 的文件索引。 */
export type RetryWait = "waking" | "indexing";

/**
 * 文件请求的等待重试：
 * - 唤醒超时（503 `orb_unavailable`）：提示「正在唤醒」，3s 后重试一次；
 * - 停机预览的索引构建中（503 `offline_indexing`）：提示「正在读取文件索引」，
 *   按 `Retry-After`（缺省 2s）反复重试，累计等待超过 `indexingMaxMs` 仍未就绪则抛出最后一次的错误。
 * `onWait` 在开始等待时收到原因，结束（成功或放弃）时收到 `null`。
 */
export async function withWakeRetry<T>(
	fn: () => Promise<T>,
	onWait: (wait: RetryWait | null) => void,
	{
		wakeDelayMs = WAKE_RETRY_MS,
		indexingMaxMs = INDEXING_WAIT_MAX_MS,
	}: { wakeDelayMs?: number; indexingMaxMs?: number } = {},
): Promise<T> {
	let woke = false;
	let indexingWaited = 0;
	let waiting = false;
	try {
		for (;;) {
			try {
				return await fn();
			} catch (e) {
				if (!(e instanceof ApiError)) throw e;
				let delay: number;
				if (e.code === "orb_unavailable" && !woke) {
					woke = true;
					delay = wakeDelayMs;
					onWait("waking");
				} else if (e.code === "offline_indexing") {
					delay = e.retryAfter ? e.retryAfter * 1000 : INDEXING_RETRY_MS;
					if (indexingWaited + delay > indexingMaxMs) throw e;
					indexingWaited += delay;
					onWait("indexing");
				} else throw e;
				waiting = true;
				await new Promise((r) => setTimeout(r, delay));
			}
		}
	} finally {
		if (waiting) onWait(null);
	}
}

/** `orbd_outdated`：旧版本 orbd 不支持文件 / git 接口。 */
export function isOrbdOutdated(e: unknown): boolean {
	return e instanceof ApiError && e.code === "orbd_outdated";
}
