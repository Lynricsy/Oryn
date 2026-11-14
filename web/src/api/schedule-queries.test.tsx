/**
 * 定时查询与 WS 对账（M9 审查 W6）：`useRealtimeScheduleSync` 收到通知（`schedules.changed` 或 WS 重新 open）时让
 * `["schedules"]` 前缀的查询全部失效，别的查询不动；卸载后取消订阅。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it } from "vitest";
import { scheduleKeys, useRealtimeScheduleSync } from "./schedule-queries";

/** 只实现 `onSchedulesChanged` 的假客户端：记下监听者，由测试手动触发。 */
function fakeClient() {
	const listeners = new Set<() => void>();
	return {
		listeners,
		onSchedulesChanged(cb: () => void) {
			listeners.add(cb);
			return () => {
				listeners.delete(cb);
			};
		},
		fire() {
			for (const cb of listeners) cb();
		},
	};
}

describe("useRealtimeScheduleSync", () => {
	it("通知到达：定时相关查询（列表 / 单个 / 线程内 / 运行记录）全部失效，其它查询不动", () => {
		const qc = new QueryClient();
		const keys = [
			scheduleKeys.list({}),
			scheduleKeys.list({ space: "spc_1" }),
			scheduleKeys.one("sch_1"),
			scheduleKeys.thread("T-1"),
			scheduleKeys.runs("sch_1"),
		];
		for (const k of keys) qc.setQueryData(k, { ok: true });
		qc.setQueryData(["threads"], { threads: [] });
		const client = fakeClient();
		const wrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={qc}>{children}</QueryClientProvider>
		);
		const { unmount } = renderHook(() => useRealtimeScheduleSync(client), {
			wrapper,
		});
		expect(client.listeners.size).toBe(1);
		for (const k of keys)
			expect(qc.getQueryState(k)?.isInvalidated).toBe(false);
		client.fire();
		for (const k of keys) expect(qc.getQueryState(k)?.isInvalidated).toBe(true);
		expect(qc.getQueryState(["threads"])?.isInvalidated).toBe(false);
		unmount();
		expect(client.listeners.size).toBe(0);
	});
});
