import { act } from "@testing-library/react";
import {
	afterEach,
	beforeEach,
	describe,
	expect,
	it,
	type MockInstance,
	vi,
} from "vitest";
import type { Entry } from "../api/client";
import type { SelectionContext } from "../lib/context-menu/types";
import { composeSideQuestion } from "../lib/side-ask";
import type { RpcBtwHistoryRecord } from "../omp/types";
import { realtime } from "../realtime/socket";
import {
	closeSideAsk,
	locateEntry,
	runSideAsk,
	sideAskBusy,
	sideAskStore,
} from "./side-ask";
import { getThreadStore } from "./stores";

const T = "t-side-ask";

const SEL: SelectionContext = {
	threadId: T,
	text: "选中的话",
	before: "",
	after: "",
	source: { kind: "other" },
	anchor: { left: 0, top: 0, right: 0, bottom: 0 },
};

function record(
	id: string,
	question: string,
	status: RpcBtwHistoryRecord["status"],
): RpcBtwHistoryRecord {
	return {
		id,
		leafId: null,
		question,
		answer: "",
		status,
		createdAt: 1,
		updatedAt: 1,
	};
}

function putRecord(r: RpcBtwHistoryRecord) {
	getThreadStore(T)
		.getState()
		.apply([{ type: "frame", frame: { type: "btw_record", record: r } }]);
}

let cmd: MockInstance<typeof realtime.cmd>;
const sent = () => cmd.mock.calls.map((c) => c[1].type);

beforeEach(() => {
	getThreadStore(T).getState().patchLocal({ btw: {}, btwOrder: [] });
	cmd = vi.spyOn(realtime, "cmd").mockImplementation((async (
		_t: string,
		frame: { type: string; question?: string },
	) => {
		if (frame.type === "btw")
			return { record: record("new", frame.question ?? "", "running") };
		return { cancelled: true };
	}) as never);
});

afterEach(() => {
	act(() => closeSideAsk({ keepRunning: true }));
	vi.restoreAllMocks();
});

describe("runSideAsk", () => {
	it("在答的是信封问题：先取消它再发新问题", async () => {
		putRecord(
			record(
				"old",
				composeSideQuestion({ kind: "explain" }, SEL, "zh-CN"),
				"running",
			),
		);
		expect(sideAskBusy(T)).toBe(false);
		await runSideAsk({ kind: "explain" }, SEL);
		expect(sent()).toEqual(["btw_cancel", "btw"]);
		expect(sideAskStore.getState().card?.recordId).toBe("new");
	});

	it("在答的是用户手动问的：不发 btw，sideAskBusy 为真", async () => {
		putRecord(record("manual", "这是什么", "running"));
		expect(sideAskBusy(T)).toBe(true);
		await runSideAsk({ kind: "explain" }, SEL);
		expect(sent()).toEqual([]);
		expect(sideAskStore.getState().card).toBeNull();
	});

	it("关闭时记录仍在答则取消；keepRunning 时不取消", async () => {
		await runSideAsk({ kind: "explain" }, SEL);
		// Core 回推的运行中记录（问题就是刚发出的信封）
		putRecord(
			record(
				"new",
				composeSideQuestion({ kind: "explain" }, SEL, "zh-CN"),
				"running",
			),
		);
		closeSideAsk({ keepRunning: true });
		expect(sent()).toEqual(["btw"]);

		await runSideAsk({ kind: "explain" }, SEL);
		closeSideAsk();
		expect(sent().at(-1)).toBe("btw_cancel");
		expect(cmd.mock.calls.at(-1)?.[1]).toEqual({
			type: "btw_cancel",
			recordId: "new",
		});
	});

	it("取消旧问题的应答还没到就关掉：不再开卡、不发 btw", async () => {
		putRecord(
			record(
				"old",
				composeSideQuestion({ kind: "explain" }, SEL, "zh-CN"),
				"running",
			),
		);
		let reply: (v: unknown) => void = () => {};
		cmd.mockImplementation(((_t: string, frame: { type: string }) =>
			frame.type === "btw_cancel"
				? new Promise((r) => {
						reply = r;
					})
				: Promise.resolve({ record: record("new", "q", "running") })) as never);
		const run = runSideAsk({ kind: "explain" }, SEL);
		await Promise.resolve();
		closeSideAsk();
		reply({ cancelled: true });
		await run;
		expect(sent()).toEqual(["btw_cancel"]);
		expect(sideAskStore.getState().card).toBeNull();
	});

	it("应答晚于关闭：对新记录发 btw_cancel", async () => {
		let reply: (v: unknown) => void = () => {};
		cmd.mockImplementation(((_t: string, frame: { type: string }) =>
			frame.type === "btw"
				? new Promise((r) => {
						reply = r;
					})
				: Promise.resolve({ cancelled: true })) as never);
		const run = runSideAsk({ kind: "explain" }, SEL);
		await Promise.resolve();
		closeSideAsk();
		reply({ record: record("late", "q", "running") });
		await run;
		expect(cmd.mock.calls.at(-1)?.[1]).toEqual({
			type: "btw_cancel",
			recordId: "late",
		});
		expect(sideAskStore.getState().card).toBeNull();
	});
});

describe("locateEntry", () => {
	function chain(rows: [string, Record<string, unknown>][]): Entry[] {
		return rows.map(([type, payload], i) => ({
			seq: i + 1,
			file: "s.jsonl",
			entryId: `e${i + 1}`,
			parentId: i > 0 ? `e${i}` : undefined,
			type,
			payload: { type, ...payload },
		}));
	}
	const user = (text: string): [string, Record<string, unknown>] => [
		"message",
		{ message: { role: "user", content: text } },
	];
	const assistant = (text: string): [string, Record<string, unknown>] => [
		"message",
		{ message: { role: "assistant", content: [{ type: "text", text }] } },
	];

	it("数之后的用户回合；早于压缩保留段的不在上下文里", () => {
		const entries = chain([
			user("a"),
			assistant("b"),
			user("c"),
			["compaction", { summary: "s", firstKeptEntryId: "e3" }],
			user("d"),
			assistant("e"),
		]);
		getThreadStore(T).getState().patchLocal({ entries, leafEntryId: "e6" });
		expect(locateEntry(T, "e2")).toEqual({ turnsAgo: 2, inContext: false });
		expect(locateEntry(T, "e6")).toEqual({ turnsAgo: 0, inContext: true });
		expect(locateEntry(T, undefined)).toEqual({ turnsAgo: 0, inContext: true });
	});
});
