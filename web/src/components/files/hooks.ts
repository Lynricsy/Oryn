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

/** 唤醒超时（503 `orb_unavailable`）时提示「正在唤醒」并在 3s 后重试一次。 */
export async function withWakeRetry<T>(
	fn: () => Promise<T>,
	onWaking: (waking: boolean) => void,
	delayMs = WAKE_RETRY_MS,
): Promise<T> {
	try {
		return await fn();
	} catch (e) {
		if (!(e instanceof ApiError) || e.code !== "orb_unavailable") throw e;
	}
	onWaking(true);
	try {
		await new Promise((r) => setTimeout(r, delayMs));
		return await fn();
	} finally {
		onWaking(false);
	}
}

/** `orbd_outdated`：旧版本 orbd 不支持文件 / git 接口。 */
export function isOrbdOutdated(e: unknown): boolean {
	return e instanceof ApiError && e.code === "orbd_outdated";
}
