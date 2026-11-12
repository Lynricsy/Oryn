import { afterEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../api/client";
import { patchThread } from "./actions";
import { getThreadStore, indexStore, resetThreadStores } from "./stores";

/** 手动控制的应答（tsconfig 的 lib 不含 Promise.withResolvers）。 */
function deferred<T>() {
	let resolve: (v: T) => void = () => undefined;
	const promise = new Promise<T>((r) => {
		resolve = r;
	});
	return { promise, resolve };
}

const TID = "T-0193a1b2-0000-7000-8000-0000000000d1";

const base: Thread = {
	id: TID,
	title: "t",
	titleSource: "auto",
	state: "idle",
	stateChangedAt: 1,
	pinned: false,
	labels: [],
	unread: false,
	createdAt: 1,
	updatedAt: 1,
	leafEntryId: "old",
};

describe("patchThread", () => {
	afterEach(() => {
		vi.unstubAllGlobals();
		resetThreadStores();
	});

	it("迟到的 REST 应答只合并本次改动的字段，不回滚期间 WS 推来的状态与叶子（W6）", async () => {
		indexStore.getState().upsertThread(base);
		const { promise, resolve } = deferred<Response>();
		vi.stubGlobal(
			"fetch",
			vi.fn(() => promise),
		);
		const done = patchThread(TID, { pinned: true });
		// 请求在途时 WS 推来更新：线程开始工作、叶子前移
		const newer: Thread = {
			...base,
			state: "working",
			leafEntryId: "new",
			updatedAt: 5,
		};
		indexStore.getState().upsertThread(newer);
		getThreadStore(TID)
			.getState()
			.apply([{ type: "thread", thread: newer }]);
		// 服务端应答是请求时刻的快照（仍为 idle / old）
		resolve(Response.json({ ...base, pinned: true, updatedAt: 2 }));
		await done;
		const idx = indexStore.getState().threads[TID];
		expect(idx?.pinned).toBe(true);
		expect(idx?.state).toBe("working");
		expect(idx?.leafEntryId).toBe("new");
		const view = getThreadStore(TID).getState();
		expect(view.thread?.state).toBe("working");
		expect(view.leafEntryId).toBe("new");
	});

	it("清除可选字段（取消颜色）时同样生效", async () => {
		indexStore.getState().upsertThread({ ...base, color: "red" });
		const { color: _drop, ...noColor } = { ...base, color: "red" };
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => Response.json(noColor)),
		);
		await patchThread(TID, { color: "" });
		expect(indexStore.getState().threads[TID]?.color).toBeUndefined();
	});
});
