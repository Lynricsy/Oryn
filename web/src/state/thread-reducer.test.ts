import { describe, expect, it } from "vitest";
import type { Entry, Thread } from "../api/client";
import { isThreadBusy } from "./actions";
import { parsePartialJson } from "./message-builder";
import { reduceThread, type ThreadStore } from "./stores";
import {
	applyEntries,
	applyFrame,
	applyLiveReset,
	applySnapshot,
	initialThreadState,
	type OmpFrame,
	SESSION_EVENT_TYPES,
	type ThreadViewState,
} from "./thread-reducer";
import { buildTranscript } from "./transcript";

const TID = "T-test";

function frames(s: ThreadViewState, list: OmpFrame[]): ThreadViewState {
	return list.reduce(applyFrame, s);
}

let seq = 0;
function msgEntry(
	id: string,
	message: Record<string, unknown>,
	parentId?: string,
): Entry {
	seq++;
	return {
		seq,
		file: "s.jsonl",
		entryId: id,
		parentId,
		type: "message",
		ts: typeof message.timestamp === "number" ? message.timestamp : 0,
		payload: { type: "message", id, parentId: parentId ?? null, message },
	};
}

const upd = (ev: Record<string, unknown>): OmpFrame => ({
	type: "message_update",
	messageId: "m1",
	message: { role: "assistant" },
	assistantMessageEvent: ev,
});

describe("帧 reducer：流式消息", () => {
	it("delta 投影重建思考、文本与工具调用参数", () => {
		const s = frames(initialThreadState(TID), [
			{ type: "agent_start" },
			{
				type: "message_start",
				messageId: "m1",
				message: { role: "assistant", content: [], timestamp: 1000 },
			},
			upd({
				type: "thinking_start",
				contentIndex: 0,
				block: { type: "thinking", thinking: "" },
			}),
			upd({ type: "thinking_delta", contentIndex: 0, delta: "想一" }),
			upd({ type: "thinking_delta", contentIndex: 0, delta: "想" }),
			upd({
				type: "text_start",
				contentIndex: 1,
				block: { type: "text", text: "" },
			}),
			upd({ type: "text_delta", contentIndex: 1, delta: "Hel" }),
			upd({ type: "text_delta", contentIndex: 1, delta: "lo" }),
			upd({
				type: "toolcall_start",
				contentIndex: 2,
				block: { type: "toolCall", id: "tc1", name: "bash", arguments: {} },
			}),
			upd({
				type: "toolcall_delta",
				contentIndex: 2,
				delta: '{"command":"una',
			}),
		]);
		expect(s.running).toBe(true);
		const content = s.liveMessage?.message.content as Record<string, unknown>[];
		expect(content[0]).toMatchObject({ type: "thinking", thinking: "想一想" });
		expect(content[1]).toMatchObject({ type: "text", text: "Hello" });
		// 残缺 JSON 也能先显示已有参数
		expect(content[2]).toMatchObject({
			type: "toolCall",
			id: "tc1",
			arguments: { command: "una" },
		});
		expect(s.liveMessage?.ended).toBe(false);

		const ended = applyFrame(s, {
			type: "message_end",
			messageId: "m1",
			message: {
				role: "assistant",
				content: [{ type: "text", text: "Hello" }],
				timestamp: 1000,
			},
		});
		expect(ended.liveMessage?.ended).toBe(true);
		expect(ended.liveMessage?.timestamp).toBe(1000);
	});

	it("在途消息被 (role, timestamp) 相同的条目覆盖后清除", () => {
		let s = frames(initialThreadState(TID), [
			{
				type: "message_start",
				messageId: "m1",
				message: { role: "assistant", content: [], timestamp: 2000 },
			},
			upd({ type: "text_delta", contentIndex: 0, delta: "hi" }),
		]);
		// 角色相同但时间戳不同：不覆盖
		s = applyEntries(s, {
			entries: [
				msgEntry("e0", { role: "assistant", content: [], timestamp: 1999 }),
			],
		});
		expect(s.liveMessage).not.toBeNull();
		s = applyEntries(s, {
			entries: [
				msgEntry("e1", {
					role: "assistant",
					content: [{ type: "text", text: "hi" }],
					timestamp: 2000,
				}),
			],
			leafEntryId: "e1",
		});
		expect(s.liveMessage).toBeNull();
		expect(s.leafEntryId).toBe("e1");
	});

	it("下一个 message_start 替换上一条在途消息", () => {
		const s = frames(initialThreadState(TID), [
			{
				type: "message_start",
				messageId: "a",
				message: { role: "user", content: "x", timestamp: 1 },
			},
			{
				type: "message_start",
				messageId: "b",
				message: { role: "assistant", content: [], timestamp: 2 },
			},
		]);
		expect(s.liveMessage?.messageId).toBe("b");
	});
});

describe("帧 reducer：工具生命周期", () => {
	it("start → update → stream → end，toolResult 条目按 toolCallId 清除", () => {
		let s = frames(initialThreadState(TID), [
			{
				type: "tool_execution_start",
				toolCallId: "tc1",
				toolName: "bash",
				args: { command: "ls" },
			},
			{
				type: "tool_execution_update",
				toolCallId: "tc1",
				toolName: "bash",
				partialResult: { content: [{ type: "text", text: "a" }] },
			},
			{
				type: "tool_stream_update",
				toolCallId: "tc1",
				toolName: "bash",
				update: { chunk: 1 },
			},
		]);
		expect(s.liveTools.tc1).toMatchObject({
			toolName: "bash",
			args: { command: "ls" },
			ended: false,
			streamUpdate: { chunk: 1 },
		});
		s = applyFrame(s, {
			type: "tool_execution_end",
			toolCallId: "tc1",
			toolName: "bash",
			result: { content: [] },
			isError: false,
		});
		expect(s.liveTools.tc1?.ended).toBe(true);
		s = applyEntries(s, {
			entries: [
				msgEntry("r1", {
					role: "toolResult",
					toolCallId: "tc1",
					toolName: "bash",
					content: [],
					isError: false,
					timestamp: 5,
				}),
			],
		});
		expect(s.liveTools.tc1).toBeUndefined();
		expect(s.liveToolOrder).toEqual([]);
	});

	it("父工具清除时一并清掉其子 agent", () => {
		let s = frames(initialThreadState(TID), [
			{ type: "tool_execution_start", toolCallId: "task1", toolName: "task" },
			{
				type: "subagent_lifecycle",
				payload: {
					id: "sa1",
					index: 0,
					agent: "explore",
					status: "started",
					parentToolCallId: "task1",
				},
			},
		]);
		expect(Object.keys(s.subagents)).toHaveLength(1);
		s = applyEntries(s, {
			entries: [
				msgEntry("r2", {
					role: "toolResult",
					toolCallId: "task1",
					toolName: "task",
					content: [],
					isError: false,
					timestamp: 6,
				}),
			],
		});
		expect(s.subagents).toEqual({});
	});

	it("嵌套派发：按 id 区分同 index 的子 agent，lifecycle 与 progress 都合并 parentSubagentId", () => {
		const s = frames(initialThreadState(TID), [
			{
				type: "subagent_lifecycle",
				payload: { id: "0-Scout", index: 0, agent: "scout", status: "started" },
			},
			// 子 agent 自己的 task 调用里同样从 0 编号
			{
				type: "subagent_lifecycle",
				payload: {
					id: "0-Scout.0-Worker",
					index: 0,
					agent: "worker",
					status: "started",
					parentSubagentId: "0-Scout",
				},
			},
			{
				type: "subagent_progress",
				payload: {
					index: 0,
					agent: "leaf",
					task: "t",
					parentSubagentId: "0-Scout.0-Worker",
					progress: { id: "0-Scout.0-Worker.0-Leaf", status: "running" },
				},
			},
			{
				type: "subagent_progress",
				payload: {
					index: 0,
					agent: "scout",
					task: "t",
					progress: { id: "0-Scout", status: "running" },
				},
			},
		]);
		expect(
			Object.values(s.subagents).map((x) => [
				x.id,
				x.agent,
				x.parentSubagentId,
			]),
		).toEqual([
			["0-Scout", "scout", undefined],
			["0-Scout.0-Worker", "worker", "0-Scout"],
			["0-Scout.0-Worker.0-Leaf", "leaf", "0-Scout.0-Worker"],
		]);
	});
});

describe("live.reset", () => {
	it("丢弃叠加层，直到下一个 message_start 之前忽略流式更新", () => {
		let s = frames(initialThreadState(TID), [
			{
				type: "message_start",
				messageId: "m1",
				message: { role: "assistant", content: [], timestamp: 1 },
			},
			upd({ type: "text_delta", contentIndex: 0, delta: "x" }),
			{ type: "tool_execution_start", toolCallId: "tc1", toolName: "read" },
		]);
		s = applyLiveReset(s);
		expect(s.liveMessage).toBeNull();
		expect(s.liveTools).toEqual({});
		s = applyFrame(s, upd({ type: "text_delta", contentIndex: 0, delta: "y" }));
		expect(s.liveMessage).toBeNull();
		s = applyFrame(s, {
			type: "message_start",
			messageId: "m2",
			message: { role: "assistant", content: [], timestamp: 9 },
		});
		expect(s.liveMessage?.messageId).toBe("m2");
		expect(s.liveSuppressed).toBe(false);
	});

	it("live.reset 作废工作标志、待答 UI、计划提案与子 agent（W3）", () => {
		let s = frames(initialThreadState(TID), [
			{ type: "agent_start" },
			{
				type: "extension_ui_request",
				id: "u1",
				method: "select",
				title: "approve?",
				options: ["yes", "no"],
			},
			{
				type: "plan_proposal",
				title: "p",
				plan: "x",
				suggestedFileName: "p.md",
				options: [],
			},
			{
				type: "subagent_lifecycle",
				payload: { index: 0, id: "a", status: "started" },
			},
		]);
		expect(s.running).toBe(true);
		expect(s.pendingUi).toHaveLength(1);
		s = applyLiveReset(s);
		s = reduceThread(s, {
			type: "thread",
			thread: { id: TID, state: "idle" } as unknown as Thread,
		});
		expect(s.running).toBe(false);
		expect(isThreadBusy(s as ThreadStore)).toBe(false);
		expect(s.pendingUi).toEqual([]);
		expect(s.planProposal).toBeNull();
		expect(s.subagents).toEqual({});
	});
});

describe("帧覆盖面", () => {
	it("33 种会话事件都被识别（不进调试面板）", () => {
		expect(SESSION_EVENT_TYPES).toHaveLength(33);
		let s = initialThreadState(TID);
		for (const type of SESSION_EVENT_TYPES)
			s = applyFrame(s, {
				type,
				toolCallId: "t",
				toolName: "x",
				message: { role: "assistant", timestamp: 1 },
			});
		expect(s.debug).toEqual([]);
	});

	it("未识别的帧进入调试面板而不是被丢弃", () => {
		const s = applyFrame(initialThreadState(TID), {
			type: "brand_new_frame",
			x: 1,
		});
		expect(s.debug).toHaveLength(1);
		expect(s.debug[0]?.frame).toEqual({ type: "brand_new_frame", x: 1 });
	});

	it("非会话帧：UI 请求、计划提案、队列、prompt_result", () => {
		const s = frames(initialThreadState(TID), [
			{
				type: "extension_ui_request",
				id: "u1",
				method: "select",
				title: "?",
				options: ["a"],
			},
			{
				type: "extension_ui_request",
				id: "u2",
				method: "setStatus",
				statusKey: "k",
				statusText: "busy",
			},
			{
				type: "plan_proposal",
				planFilePath: "/p.md",
				title: "P",
				suggestedFileName: "p.md",
				planContent: "x",
				options: [],
			},
			{
				type: "queue_update",
				steering: ["s"],
				followUp: ["f1", "f2"],
				liveSteered: 0,
			},
			{
				type: "prompt_result",
				agentInvoked: true,
				status: "error",
				error: { message: "boom", retryable: false },
				sessionSettled: true,
			},
		]);
		expect(s.pendingUi.map((r) => r.id)).toEqual(["u1"]);
		expect(s.statuses).toEqual({ k: "busy" });
		expect(s.planProposal?.title).toBe("P");
		expect(s.queue).toEqual({
			steering: ["s"],
			followUp: ["f1", "f2"],
			liveSteered: 0,
		});
		expect(s.notices.at(-1)).toMatchObject({
			kind: "prompt_error",
			level: "error",
			text: "boom",
		});
		const cancelled = applyFrame(s, {
			type: "extension_ui_request",
			id: "c1",
			method: "cancel",
			targetId: "u1",
		});
		expect(cancelled.pendingUi).toEqual([]);
	});

	it("xd:// 挂载变动提示不进转录，其它 notice 照常", () => {
		const s = frames(initialThreadState(TID), [
			{
				type: "notice",
				level: "info",
				message: "xd://: mounted list_threads, read_thread",
				source: "xdev",
			},
			{ type: "notice", level: "warning", message: "w", source: "oryn" },
		]);
		expect(s.notices.map((n) => n.text)).toEqual(["w"]);
	});
});

describe("thread.snapshot", () => {
	it("迟到订阅者从 snapshot.live 拿到在途消息的已有帧", () => {
		const s = applySnapshot(initialThreadState(TID), {
			type: "thread.snapshot",
			threadId: TID,
			thread: {
				id: TID,
				title: "t",
				titleSource: "auto",
				state: "working",
				stateChangedAt: 0,
				pinned: false,
				labels: [],
				unread: false,
				createdAt: 0,
				updatedAt: 0,
			},
			agent: {
				status: "ready",
				pendingUi: [
					{
						type: "extension_ui_request",
						id: "u1",
						method: "confirm",
						title: "ok?",
						message: "?",
					},
				],
				subagents: [],
				live: [
					{
						type: "message_start",
						messageId: "m1",
						message: { role: "assistant", content: [], timestamp: 7 },
					},
					{
						type: "message_update",
						messageId: "m1",
						message: { role: "assistant" },
						assistantMessageEvent: {
							type: "text_delta",
							contentIndex: 0,
							delta: "partial",
						},
					},
				],
			},
			lastSeq: 0,
			transcriptRev: 0,
			fullReplay: false,
		});
		expect(s.snapshotReceived).toBe(true);
		expect(s.liveMessage?.message.content).toEqual([
			{ type: "text", text: "partial" },
		]);
		expect(s.pendingUi.map((r) => r.id)).toEqual(["u1"]);
	});

	// M12.1 走查问题 3：/omfg 的规则预览（setWidget）与状态行（setStatus）在重新订阅后丢失
	it("从 snapshot.displayUi 恢复扩展的 statuses / widgets；live.reset 清空", () => {
		let s = frames(initialThreadState(TID), [
			{
				type: "extension_ui_request",
				id: "old",
				method: "setStatus",
				statusKey: "stale",
				statusText: "旧连接的状态",
			},
		]);
		s = applySnapshot(s, {
			type: "thread.snapshot",
			threadId: TID,
			thread: {
				id: TID,
				title: "t",
				titleSource: "auto",
				state: "awaiting_input",
				stateChangedAt: 0,
				pinned: false,
				labels: [],
				unread: false,
				createdAt: 0,
				updatedAt: 0,
			},
			agent: {
				status: "ready",
				pendingUi: [],
				displayUi: [
					{
						type: "extension_ui_request",
						id: "w1",
						method: "setWidget",
						widgetKey: "omfg",
						widgetLines: ["rule: no sleep"],
						widgetPlacement: "belowEditor",
					},
					{
						type: "extension_ui_request",
						id: "s1",
						method: "setStatus",
						statusKey: "omfg",
						statusText: "drafting",
					},
				],
				subagents: [],
				live: [],
			},
			lastSeq: 0,
			transcriptRev: 0,
			fullReplay: false,
		});
		expect(s.widgets).toEqual({
			omfg: { lines: ["rule: no sleep"], placement: "belowEditor" },
		});
		expect(s.statuses).toEqual({ omfg: "drafting" });
		expect(s.pendingUi).toEqual([]);

		s = applyLiveReset(s);
		expect(s.widgets).toEqual({});
		expect(s.statuses).toEqual({});
	});
});

describe("转录推导", () => {
	it("只渲染活动分支，叠加层里已被条目覆盖的部分不重复", () => {
		seq = 0;
		const e1 = msgEntry("u1", { role: "user", content: "hi", timestamp: 1 });
		const e2 = msgEntry(
			"a1",
			{
				role: "assistant",
				content: [{ type: "text", text: "branch A" }],
				timestamp: 2,
			},
			"u1",
		);
		const e3 = msgEntry(
			"a2",
			{
				role: "assistant",
				content: [{ type: "text", text: "branch B" }],
				timestamp: 3,
			},
			"u1",
		);
		let s = applyEntries(initialThreadState(TID), {
			entries: [e1, e2, e3],
			leafEntryId: "a1",
		});
		s = applyFrame(s, {
			type: "message_start",
			messageId: "x",
			message: { role: "assistant", content: [], timestamp: 2 },
		});
		const items = buildTranscript(s).items;
		const texts = items.flatMap((i) =>
			i.kind === "message" ? [JSON.stringify(i.message.content)] : [],
		);
		expect(texts).toHaveLength(2);
		expect(texts[1]).toContain("branch A");
		expect(texts.join()).not.toContain("branch B");
	});

	it("无叶子指针时，带 activeLeafId 的分支外条目（迟到的 model_usage）不改变活动分支", () => {
		const u1 = msgEntry("u1", { role: "user", content: "q1", timestamp: 1 });
		const a1 = msgEntry(
			"a1",
			{
				role: "assistant",
				content: [{ type: "text", text: "A1" }],
				timestamp: 2,
			},
			"u1",
		);
		const u2 = msgEntry(
			"u2",
			{ role: "user", content: "q2", timestamp: 3 },
			"a1",
		);
		const a2 = msgEntry(
			"a2",
			{
				role: "assistant",
				content: [{ type: "text", text: "A2" }],
				timestamp: 4,
			},
			"u2",
		);
		seq++;
		const late: Entry = {
			seq,
			file: "s.jsonl",
			entryId: "mu",
			parentId: "a1",
			type: "model_usage",
			payload: {
				type: "model_usage",
				id: "mu",
				parentId: "a1",
				activeLeafId: "a2",
			},
		};
		const s = applyEntries(initialThreadState(TID), {
			entries: [u1, a1, u2, a2, late],
		});
		const texts = buildTranscript(s).items.flatMap((i) =>
			i.kind === "message" ? [JSON.stringify(i.message.content)] : [],
		);
		expect(texts).toHaveLength(4);
		expect(texts[3]).toContain("A2");
	});
});

describe("线程摘要推送", () => {
	it("index.patch 带来的 leafEntryId 更新活动分支（switch_leaf 后多端同步）", () => {
		const base = applyEntries(initialThreadState(TID), {
			entries: [],
			leafEntryId: "old",
		});
		const thread = { id: TID, leafEntryId: "new" } as unknown as Thread;
		expect(reduceThread(base, { type: "thread", thread }).leafEntryId).toBe(
			"new",
		);
		const noLeaf = { id: TID } as unknown as Thread;
		expect(
			reduceThread(base, { type: "thread", thread: noLeaf }).leafEntryId,
		).toBe("old");
	});
});

describe("parsePartialJson", () => {
	it("补齐未闭合的字符串与括号", () => {
		expect(parsePartialJson('{"a":"b')).toEqual({ a: "b" });
		expect(parsePartialJson('{"a":[1,2')).toEqual({ a: [1, 2] });
		expect(parsePartialJson('{"a":')).toEqual({});
		expect(parsePartialJson("")).toBeUndefined();
	});
});

describe("btw 帧", () => {
	const topic = {
		id: "b1",
		leafId: null,
		question: "首轮",
		answer: "首轮回答",
		status: "complete" as const,
		createdAt: 100,
		updatedAt: 100,
	};

	it("无追问时 btw_delta 追加到首轮回答", () => {
		const s = frames(initialThreadState(TID), [
			{
				type: "btw_record",
				record: { ...topic, answer: "", status: "running" },
			},
			{ type: "btw_delta", recordId: "b1", delta: "你" },
			{ type: "btw_delta", recordId: "b1", delta: "好" },
		]);
		expect(s.btw.b1?.answer).toBe("你好");
	});

	it("追问运行中 btw_delta 只追加到 followUps 末轮，顶层与更早的追问不变", () => {
		const s = frames(initialThreadState(TID), [
			{
				type: "btw_record",
				record: {
					...topic,
					followUps: [
						{ ...topic, question: "追问一", answer: "答一" },
						{
							...topic,
							question: "追问二",
							answer: "部分",
							status: "running",
						},
					],
				},
			},
			{ type: "btw_delta", recordId: "b1", delta: "回答" },
		]);
		const r = s.btw.b1;
		expect(r?.answer).toBe("首轮回答");
		expect(r?.followUps?.map((f) => f.answer)).toEqual(["答一", "部分回答"]);
	});

	it("未知 topic 的 btw_delta 忽略", () => {
		const s0 = initialThreadState(TID);
		expect(
			applyFrame(s0, { type: "btw_delta", recordId: "nope", delta: "x" }),
		).toBe(s0);
	});

	it("btwOrder 按 createdAt 新的在前，与到达顺序无关；同一 topic 更新不改变位置", () => {
		const s = frames(initialThreadState(TID), [
			{ type: "btw_record", record: { ...topic, id: "new", createdAt: 3 } },
			{ type: "btw_record", record: { ...topic, id: "old", createdAt: 1 } },
			{ type: "btw_record", record: { ...topic, id: "mid", createdAt: 2 } },
			{
				type: "btw_record",
				record: { ...topic, id: "new", createdAt: 3, answer: "更新" },
			},
		]);
		expect(s.btwOrder).toEqual(["new", "mid", "old"]);
		expect(s.btw.new?.answer).toBe("更新");
	});
});
