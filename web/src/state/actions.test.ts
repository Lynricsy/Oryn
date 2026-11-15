import { afterEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../api/client";
import { describeSlashResult, patchThread, slashPromptFrame } from "./actions";
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

describe("describeSlashResult", () => {
	it("按应答生成给人看的提示键", () => {
		expect(
			describeSlashResult(
				{ type: "omfg", complaint: "x" },
				{ state: "saved", savedPath: "/r/no-test-edits.md" },
			),
		).toEqual({
			level: "info",
			key: "slash.result.omfgSaved",
			params: { path: "/r/no-test-edits.md" },
		});
		expect(
			describeSlashResult(
				{ type: "omfg", complaint: "x" },
				{ state: "rejected" },
			),
		).toMatchObject({ level: "warning", key: "slash.result.omfgRejected" });
		expect(
			describeSlashResult({ type: "handoff" }, { savedPath: "/h.md" }),
		).toMatchObject({
			key: "slash.result.handoffSaved",
			params: { path: "/h.md" },
		});
		expect(
			describeSlashResult(
				{ type: "set_agents_paused", enabled: false },
				{ paused: false, heldMs: 90_000 },
			),
		).toEqual({
			level: "info",
			key: "slash.result.resumedAfter",
			params: { duration: "1m 30s" },
		});
		expect(
			describeSlashResult({ type: "set_goal", tokenBudget: null }, {}),
		).toMatchObject({ key: "slash.result.goalBudgetCleared" });
		expect(
			describeSlashResult({ type: "set_goal", tokenBudget: 50000 }, {}),
		).toMatchObject({ key: "slash.result.goalBudgetSet" });
		expect(describeSlashResult({ type: "fresh" }, {})).toMatchObject({
			key: "slash.result.fresh",
		});
		expect(
			describeSlashResult({ type: "guided_goal" }, { started: true }),
		).toMatchObject({
			key: "slash.result.guidedGoal",
		});
	});

	it("结果在别处可见的命令不重复提示（模式开关、目标、压缩卡片）", () => {
		expect(
			describeSlashResult({ type: "set_plan_mode", enabled: true }, {}),
		).toBeNull();
		expect(
			describeSlashResult({ type: "set_goal", objective: "发布" }, {}),
		).toBeNull();
		expect(
			describeSlashResult({ type: "compact" }, { tokensBefore: 1 }),
		).toBeNull();
	});
});

describe("slashPromptFrame", () => {
	it("斜杠文本一律发 prompt：忙时引导，排队键时 followUp", () => {
		expect(slashPromptFrame("steer", "/todo")).toEqual({
			type: "prompt",
			message: "/todo",
			streamingBehavior: "steer",
		});
		expect(slashPromptFrame("abort_and_prompt", "/todo")).toMatchObject({
			type: "prompt",
			streamingBehavior: "steer",
		});
		expect(slashPromptFrame("follow_up", "/skill:x", [], ["att_1"])).toEqual({
			type: "prompt",
			message: "/skill:x",
			streamingBehavior: "followUp",
			orynAttachments: ["att_1"],
		});
	});
});
