import { describe, expect, it } from "vitest";
import type { Entry } from "../../api/client";
import type { TranscriptItem } from "../../state/transcript";
import { findRevealHit, registerRevealer, revealEntry } from "./reveal";

function entry(seq: number, message: Record<string, unknown>): Entry {
	return {
		seq,
		file: "s.jsonl",
		entryId: `e${seq}`,
		type: "message",
		payload: { type: "message", id: `e${seq}`, message },
	};
}

const call = {
	role: "assistant",
	content: [{ type: "toolCall", id: "call_1", name: "bash", arguments: {} }],
};
const entries = [
	entry(1, { role: "user", content: "hi" }),
	entry(2, call),
	entry(3, { role: "toolResult", toolCallId: "call_1", content: [] }),
	entry(4, { role: "assistant", content: [] }),
];
const items: TranscriptItem[] = [
	{
		kind: "message",
		key: "e1",
		seq: 1,
		entryId: "e1",
		message: { role: "user" },
		live: false,
	},
	{
		kind: "message",
		key: "e2",
		seq: 2,
		entryId: "e2",
		message: call,
		live: false,
	},
	{
		kind: "message",
		key: "e4",
		seq: 4,
		entryId: "e4",
		message: { role: "assistant" },
		live: false,
	},
];

describe("findRevealHit", () => {
	it("消息条目按 entryId 找到所在行", () => {
		expect(findRevealHit(items, entries, { entryId: "e4", seq: 4 })).toEqual({
			index: 2,
		});
	});

	it("工具输出定位到承载该工具卡的助手消息，并带上 toolCallId", () => {
		expect(findRevealHit(items, entries, { entryId: "e3", seq: 3 })).toEqual({
			index: 1,
			toolCallId: "call_1",
		});
	});

	it("entryId 对不上时退回 seq；不在转录行里（被回退的分支 / 未加载）为 null", () => {
		expect(findRevealHit(items, entries, { entryId: "x", seq: 1 })).toEqual({
			index: 0,
		});
		expect(findRevealHit(items, entries, { entryId: "e9", seq: 9 })).toBeNull();
		expect(
			findRevealHit(items.slice(0, 1), entries, { entryId: "e4", seq: 4 }),
		).toBeNull();
	});
});

describe("revealEntry", () => {
	it("没有登记的转录时返回 false；注销只删自己", () => {
		expect(revealEntry("T-x", { entryId: "e1", seq: 1 })).toBe(false);
		const a = () => true;
		const offA = registerRevealer("T-x", a);
		const offB = registerRevealer("T-x", () => false);
		offA();
		expect(revealEntry("T-x", { entryId: "e1", seq: 1 })).toBe(false);
		offB();
		expect(revealEntry("T-x", { entryId: "e1", seq: 1 })).toBe(false);
	});
});
