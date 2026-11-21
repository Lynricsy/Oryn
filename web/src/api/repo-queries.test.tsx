/**
 * 项目仓库查询与 WS 对账（M24）：`useRealtimeRepoSync` 收到带项目 id 的通知时只让该项目的仓库查询失效；
 * 收到 null（WS 重新 open）时让全部项目的仓库查询失效；托管项目另让项目列表失效（Webhook 最近投递在项目里）；
 * 卸载后取消订阅。另测 `repoRetry` 与 `repoBlobUrl`。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it } from "vitest";
import { ApiError } from "./client";
import {
	repoBlobUrl,
	repoKeys,
	repoRetry,
	useRealtimeRepoSync,
} from "./repo-queries";

/** 只实现 `onProjectRepoChanged` 的假客户端：记下监听者，由测试手动触发。 */
function fakeClient() {
	const listeners = new Set<(projectId: string | null) => void>();
	return {
		listeners,
		onProjectRepoChanged(cb: (projectId: string | null) => void) {
			listeners.add(cb);
			return () => {
				listeners.delete(cb);
			};
		},
		fire(projectId: string | null) {
			for (const cb of listeners) cb(projectId);
		},
	};
}

function setup() {
	const qc = new QueryClient();
	const x = [
		repoKeys.status("prj_x"),
		repoKeys.commits("prj_x", "main"),
		repoKeys.tree("prj_x", undefined, "", true),
	];
	const y = [repoKeys.status("prj_y"), repoKeys.refs("prj_y", "branch")];
	for (const k of [...x, ...y]) qc.setQueryData(k, { ok: true });
	qc.setQueryData(["projects"], { projects: [] });
	const client = fakeClient();
	const wrapper = ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={qc}>{children}</QueryClientProvider>
	);
	const hook = renderHook(() => useRealtimeRepoSync(client), { wrapper });
	const invalidated = (k: readonly unknown[]) =>
		qc.getQueryState(k)?.isInvalidated;
	return { qc, x, y, client, hook, invalidated };
}

describe("useRealtimeRepoSync", () => {
	it("带项目 id：只让该项目的仓库查询失效", () => {
		const { x, y, client, hook, invalidated } = setup();
		expect(client.listeners.size).toBe(1);
		client.fire("prj_x");
		expect(invalidated(["projectRepo", "prj_x", "status"])).toBe(true);
		for (const k of x) expect(invalidated(k)).toBe(true);
		for (const k of y) expect(invalidated(k)).toBe(false);
		expect(invalidated(["projects"])).toBe(false);
		hook.unmount();
		expect(client.listeners.size).toBe(0);
	});

	it("null（WS 重新连上）：全部项目的仓库查询失效，别的查询不动", () => {
		const { x, y, client, invalidated } = setup();
		client.fire(null);
		for (const k of [...x, ...y]) expect(invalidated(k)).toBe(true);
		expect(invalidated(["projects"])).toBe(false);
	});

	it("托管项目：另让项目列表失效（Webhook 最近投递）", () => {
		const { qc, client, invalidated } = setup();
		qc.setQueryData(["projects"], {
			projects: [
				{ id: "prj_x", name: "x", hosted: true },
				{ id: "prj_y", name: "y" },
			],
		});
		client.fire("prj_y");
		expect(invalidated(["projects"])).toBe(false);
		client.fire("prj_x");
		expect(invalidated(["projects"])).toBe(true);
	});
});

describe("repoRetry", () => {
	it("确定性错误不重试，其余至多重试 2 次", () => {
		for (const s of [400, 404, 409, 413, 503])
			expect(repoRetry(0, new ApiError(s, "x", ""))).toBe(false);
		expect(repoRetry(0, new ApiError(500, "internal", ""))).toBe(true);
		expect(repoRetry(1, new TypeError("fetch failed"))).toBe(true);
		expect(repoRetry(2, new TypeError("fetch failed"))).toBe(false);
	});
});

describe("repoBlobUrl", () => {
	it("编码 ref 与 path；ref 为空时省略", () => {
		expect(repoBlobUrl("prj_a", "feature/x", "docs/a b.md")).toBe(
			"/api/v1/projects/prj_a/repo/blob?ref=feature%2Fx&path=docs%2Fa+b.md",
		);
		expect(repoBlobUrl("prj_a", "", "README.md")).toBe(
			"/api/v1/projects/prj_a/repo/blob?path=README.md",
		);
	});

	it("download 时追加 download=1", () => {
		expect(repoBlobUrl("prj_a", "main", "a.bin", { download: true })).toBe(
			"/api/v1/projects/prj_a/repo/blob?ref=main&path=a.bin&download=1",
		);
	});
});
