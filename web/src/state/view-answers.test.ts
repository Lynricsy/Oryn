/**
 * 视图作答的状态（M33，docs/plan-m33.md 的 C4 / C5 / C10）：转录推导出的作答（回执与工具结果两个来源、同一块取最早）、
 * 输入坞去掉视图的阻塞请求、提交走阻塞或非阻塞通道、主人改为打字时撤销挂起的请求。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Entry } from "../api/client";
import { buildViewReceipt, type ViewAnswer } from "../lib/view/answer";
import { realtime } from "../realtime/socket";
import { dismissViewAwaits, submitViewAnswer } from "./actions";
import { getThreadStore, resetThreadStores } from "./stores";
import { dockPendingUi, type OmpFrame, viewAwaitOf } from "./thread-reducer";
import { buildTranscript } from "./transcript";
import { partitionViewInFlight, viewInFlightStore } from "./view-inflight";

vi.mock("../realtime/socket", () => ({
	realtime: {
		uiResponse: vi.fn(() => true),
		cmd: vi.fn(async () => ({})),
	},
}));

const TID = "T-0193a1b2-0000-7000-8000-0000000000v1";

let seq = 0;
function entry(message: Record<string, unknown>): Entry {
	seq++;
	return {
		seq,
		file: "a.jsonl",
		entryId: `e${seq}`,
		parentId: seq > 1 ? `e${seq - 1}` : undefined,
		type: "message",
		payload: { type: "message", id: `e${seq}`, message },
	};
}

const call = (id: string) =>
	entry({
		role: "assistant",
		content: [{ type: "toolCall", id, name: "render_ui", arguments: {} }],
	});
const userMsg = (content: unknown, extra: Record<string, unknown> = {}) =>
	entry({ role: "user", content, ...extra });
const result = (id: string, details: unknown) =>
	entry({
		role: "toolResult",
		toolCallId: id,
		toolName: "render_ui",
		content: [{ type: "text", text: "ok" }],
		details,
	});

function answersOf(entries: Entry[]) {
	return buildTranscript({
		entries,
		leafEntryId: undefined,
		liveMessage: null,
		liveTools: {},
		liveToolOrder: [],
		notices: [],
	}).viewAnswers;
}

const pick: ViewAnswer = { block: "0", kind: "action", choice: 0 };
const pick2: ViewAnswer = { block: "0", kind: "action", choice: 1 };

describe("viewAnswers", () => {
	beforeEach(() => {
		seq = 0;
	});

	it("来自主人消息里的回执（字符串与文本块两种 content）", () => {
		const form: ViewAnswer = { block: "1.0", kind: "form", values: { a: "x" } };
		const answers = answersOf([
			call("c1"),
			result("c1", { view: { outcome: "rendered" } }),
			userMsg(buildViewReceipt("c1", pick, "选 A")),
			userMsg([
				{
					type: "text",
					text: buildViewReceipt(
						"c1",
						form,
						'- A: x\n\n```json\n{"a":"x"}\n```',
					),
				},
			]),
		]);
		expect(answers.get("c1")?.get("0")).toEqual(pick);
		expect(answers.get("c1")?.get("1.0")).toEqual(form);
	});

	it("来自工具结果的 details.view.answer", () => {
		const answers = answersOf([
			call("c2"),
			result("c2", { view: { outcome: "answered", answer: pick } }),
		]);
		expect(answers.get("c2")?.get("0")).toEqual(pick);
	});

	it("同一块有多条时取最早的", () => {
		const answers = answersOf([
			call("c3"),
			result("c3", { view: { outcome: "answered", answer: pick } }),
			userMsg(buildViewReceipt("c3", pick2, "B")),
		]);
		expect(answers.get("c3")?.get("0")).toEqual(pick);
		const later = answersOf([
			call("c4"),
			result("c4", { view: { outcome: "rendered" } }),
			userMsg(buildViewReceipt("c4", pick2, "B")),
			userMsg(buildViewReceipt("c4", pick, "A")),
		]);
		expect(later.get("c4")?.get("0")).toEqual(pick2);
	});

	it("IRC 转述与合成消息里的回执不算", () => {
		const answers = answersOf([
			call("c5"),
			result("c5", { view: { outcome: "rendered" } }),
			userMsg(buildViewReceipt("c5", pick, "A"), { synthetic: true }),
			userMsg(buildViewReceipt("c5", pick, "A"), {
				parentIrc: { from: "x", body: "y" },
			}),
		]);
		expect(answers.has("c5")).toBe(false);
	});
});

const viewReq = (id: string, toolCallId: string) =>
	({
		type: "extension_ui_request",
		id,
		method: "input",
		title: "有一个视图等你操作",
		placeholder: `oryn-view:${toolCallId}`,
	}) as unknown as OmpFrame;
const plainReq = {
	type: "extension_ui_request",
	id: "u1",
	method: "confirm",
	title: "继续？",
	message: "",
} as unknown as OmpFrame;

function pushFrames(...frames: OmpFrame[]) {
	getThreadStore(TID)
		.getState()
		.apply(frames.map((frame) => ({ type: "frame", frame })));
}

describe("阻塞请求的选择器", () => {
	afterEach(() => resetThreadStores());

	it("dockPendingUi 去掉视图请求；没有视图请求时原样返回同一个数组", () => {
		pushFrames(plainReq);
		const s1 = getThreadStore(TID).getState();
		expect(dockPendingUi(s1)).toBe(s1.pendingUi);
		pushFrames(viewReq("v1", "c1"));
		const s2 = getThreadStore(TID).getState();
		expect(s2.pendingUi.map((r) => r.id)).toEqual(["u1", "v1"]);
		expect(dockPendingUi(s2).map((r) => r.id)).toEqual(["u1"]);
		// 同一个 pendingUi 重复取得到同一个数组（可作 store 选择器）
		expect(dockPendingUi(s2)).toBe(dockPendingUi(s2));
		expect(viewAwaitOf(s2, "c1")?.id).toBe("v1");
		expect(viewAwaitOf(s2, "c2")).toBeUndefined();
	});
});

describe("提交与撤销", () => {
	afterEach(() => {
		resetThreadStores();
		viewInFlightStore.setState({ pending: {} });
		vi.mocked(realtime.uiResponse).mockClear();
		vi.mocked(realtime.uiResponse).mockReturnValue(true);
		vi.mocked(realtime.cmd).mockReset();
	});

	it("挂起时作为工具结果应答，并在途锁定", async () => {
		pushFrames(viewReq("v1", "c1"));
		await submitViewAnswer(TID, "c1", pick, "选 A");
		expect(realtime.uiResponse).toHaveBeenCalledWith(TID, {
			type: "extension_ui_response",
			id: "v1",
			value: JSON.stringify({ v: 1, answer: pick, text: "选 A" }),
		});
		expect(realtime.cmd).not.toHaveBeenCalled();
		expect(getThreadStore(TID).getState().pendingUi).toEqual([]);
		expect(viewInFlightStore.getState().pending[`${TID}:c1`]).toEqual({
			"0": { answer: pick, awaited: true },
		});
	});

	it("没有挂起时作为下一条消息（回执）发出", async () => {
		vi.mocked(realtime.cmd).mockResolvedValue({});
		await submitViewAnswer(TID, "c2", pick, "选 A");
		expect(realtime.uiResponse).not.toHaveBeenCalled();
		const frame = vi.mocked(realtime.cmd).mock.calls[0]?.[1] as {
			type: string;
			message: string;
		};
		expect(frame.type).toBe("prompt");
		expect(frame.message).toBe(buildViewReceipt("c2", pick, "选 A"));
	});

	it("发送失败时清掉在途记录并抛出", async () => {
		vi.mocked(realtime.cmd).mockRejectedValue(new Error("offline"));
		await expect(submitViewAnswer(TID, "c3", pick, "A")).rejects.toThrow(
			"offline",
		);
		expect(viewInFlightStore.getState().pending[`${TID}:c3`]).toBeUndefined();
		pushFrames(viewReq("v3", "c3"));
		vi.mocked(realtime.uiResponse).mockReturnValue(false);
		await expect(submitViewAnswer(TID, "c3", pick, "A")).rejects.toThrow();
		expect(viewInFlightStore.getState().pending[`${TID}:c3`]).toBeUndefined();
	});

	it("主人改为打字：只撤销视图的挂起请求", () => {
		pushFrames(plainReq, viewReq("v1", "c1"), viewReq("v2", "c2"));
		dismissViewAwaits(TID);
		expect(vi.mocked(realtime.uiResponse).mock.calls).toEqual([
			[TID, { type: "extension_ui_response", id: "v1", cancelled: true }],
			[TID, { type: "extension_ui_response", id: "v2", cancelled: true }],
		]);
		expect(
			getThreadStore(TID)
				.getState()
				.pendingUi.map((r) => r.id),
		).toEqual(["u1"]);
	});
});

describe("在途作答的作废", () => {
	const a: ViewAnswer = { block: "0", kind: "action", choice: 0 };
	const b: ViewAnswer = { block: "1", kind: "action", choice: 1 };
	const blocks = {
		"0": { answer: a, awaited: false },
		"1": { answer: b, awaited: true },
	};

	it("转录还没有作答、调用没结束：都锁定", () => {
		expect(partitionViewInFlight(blocks, undefined, false)).toEqual({
			live: [
				["0", a],
				["1", b],
			],
			stale: [],
		});
	});

	it("转录接管的块作废（之后回退到回执之前，块随转录解锁）", () => {
		const known = new Map([["0", a]]);
		expect(partitionViewInFlight(blocks, known, false)).toEqual({
			live: [["1", b]],
			stale: ["0"],
		});
	});

	it("阻塞作答的调用已结束而结果里没有它：没送到，作废；非阻塞的不受影响", () => {
		expect(partitionViewInFlight(blocks, new Map(), true)).toEqual({
			live: [["0", a]],
			stale: ["1"],
		});
	});
});
