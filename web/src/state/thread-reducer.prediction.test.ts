/**
 * 下一条消息预测（`oryn_composer_prediction`）在线程状态里的生命周期：帧设置 / 撤回，
 * 新一轮（agent_start）作废，快照恢复，live.reset 清空。
 */
import { describe, expect, it } from "vitest";
import type { WsThreadSnapshot } from "../api/client";
import {
	applyFrame,
	applyLiveReset,
	applySnapshot,
	initialThreadState,
	type ThreadViewState,
} from "./thread-reducer";

const TID = "T-pred";

function predicted(choices: unknown): ThreadViewState {
	return applyFrame(initialThreadState(TID), {
		type: "oryn_composer_prediction",
		choices,
	});
}

function snapshot(composerPrediction?: string[]): WsThreadSnapshot {
	// agent 先单独构造：schema 加上 composerPrediction 之前也能编译
	const agent = {
		status: "ready" as const,
		pendingUi: [],
		subagents: [],
		live: [],
		...(composerPrediction ? { composerPrediction } : {}),
	};
	return {
		type: "thread.snapshot",
		threadId: TID,
		thread: {
			id: TID,
			title: "t",
			titleSource: "auto",
			state: "idle",
			stateChangedAt: 0,
			pinned: false,
			labels: [],
			unread: false,
			createdAt: 0,
			updatedAt: 0,
		},
		agent,
		lastSeq: 0,
		transcriptRev: 0,
		fullReplay: false,
	} as WsThreadSnapshot;
}

describe("输入框预测：线程状态", () => {
	it("初始没有预测", () => {
		expect(initialThreadState(TID).composerPrediction).toEqual([]);
	});

	it("帧带来完整候选列表，空列表撤回；不进调试面板", () => {
		let s = predicted(["run the tests", "run the tests again"]);
		expect(s.composerPrediction).toEqual([
			"run the tests",
			"run the tests again",
		]);
		expect(s.debug).toEqual([]);
		s = applyFrame(s, { type: "oryn_composer_prediction", choices: [] });
		expect(s.composerPrediction).toEqual([]);
	});

	it("丢弃非字符串与空串候选；choices 缺失视为撤回", () => {
		expect(predicted(["ok", "", 3, null]).composerPrediction).toEqual(["ok"]);
		expect(predicted(undefined).composerPrediction).toEqual([]);
	});

	it("agent_start 作废上一轮的预测", () => {
		const s = applyFrame(predicted(["next"]), { type: "agent_start" });
		expect(s.composerPrediction).toEqual([]);
		expect(s.running).toBe(true);
	});

	it("agent_end 不影响预测", () => {
		const s = applyFrame(predicted(["next"]), {
			type: "agent_end",
			isTerminal: true,
		});
		expect(s.composerPrediction).toEqual(["next"]);
	});

	it("快照恢复缓存的预测，缺失时清空", () => {
		const restored = applySnapshot(
			initialThreadState(TID),
			snapshot(["a", "ab"]),
		);
		expect(restored.composerPrediction).toEqual(["a", "ab"]);
		const cleared = applySnapshot(predicted(["stale"]), snapshot());
		expect(cleared.composerPrediction).toEqual([]);
	});

	it("live.reset 清空预测", () => {
		expect(applyLiveReset(predicted(["next"])).composerPrediction).toEqual([]);
	});
});
