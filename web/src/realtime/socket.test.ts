/**
 * RealtimeClient 与 WS 契约 §4 的回归测试：假 WebSocket + 假票据接口，逐条驱动服务端消息。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Entry, Thread } from "../api/client";
import { makeOrb } from "../mock/fixtures";
import { notificationsStore } from "../state/notifications";
import {
	connectionStore,
	getThreadStore,
	resetThreadStores,
} from "../state/stores";
import { terminalHub } from "../state/terminals";
import { RealtimeClient } from "./socket";

const TID = "T-0193a1b2-0000-7000-8000-0000000000c1";

type Json = Record<string, unknown>;

class FakeWS {
	static readonly CONNECTING = 0;
	static readonly OPEN = 1;
	static readonly CLOSING = 2;
	static readonly CLOSED = 3;
	static instances: FakeWS[] = [];
	readyState = FakeWS.CONNECTING;
	sent: Json[] = [];
	onopen: (() => void) | null = null;
	onmessage: ((ev: { data: string }) => void) | null = null;
	onclose: (() => void) | null = null;
	readonly url: string;
	constructor(url: string) {
		this.url = url;
		FakeWS.instances.push(this);
	}
	send(data: string) {
		this.sent.push(JSON.parse(data) as Json);
	}
	/** 浏览器语义：close 事件异步到达 */
	close() {
		this.readyState = FakeWS.CLOSED;
		setTimeout(() => this.onclose?.(), 0);
	}
	accept() {
		this.readyState = FakeWS.OPEN;
		this.onopen?.();
	}
	recv(msg: Json) {
		this.onmessage?.({ data: JSON.stringify(msg) });
	}
	/** 服务端断开 */
	drop() {
		this.readyState = FakeWS.CLOSED;
		this.onclose?.();
	}
	subs(): Json[] {
		return this.sent.filter((m) => m.type === "sub");
	}
	cmds(type: string): Json[] {
		return this.sent.filter(
			(m) => m.type === "cmd" && (m.frame as Json).type === type,
		);
	}
}

const thread: Thread = {
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
};

function entry(seq: number): Entry {
	return {
		seq,
		file: "s.jsonl",
		entryId: `e${seq}`,
		type: "message",
		payload: { type: "message", id: `e${seq}` },
	};
}

function snapshot(p: Json = {}, agent: Json = {}): Json {
	return {
		type: "thread.snapshot",
		threadId: TID,
		thread,
		agent: {
			status: "starting",
			pendingUi: [],
			subagents: [],
			live: [],
			...agent,
		},
		lastSeq: 0,
		transcriptRev: 1,
		fullReplay: false,
		...p,
	};
}

async function connected(client: RealtimeClient): Promise<FakeWS> {
	const before = FakeWS.instances.length;
	client.connect();
	await vi.waitFor(() => expect(FakeWS.instances.length).toBe(before + 1));
	const ws = FakeWS.instances[before] as FakeWS;
	ws.accept();
	return ws;
}

describe("RealtimeClient", () => {
	let client: RealtimeClient;
	beforeEach(() => {
		FakeWS.instances = [];
		resetThreadStores();
		connectionStore.setState({ status: "idle", attempts: 0, errors: [] });
		vi.stubGlobal("WebSocket", FakeWS);
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => Response.json({ ticket: "tk", expiresIn: 30 })),
		);
		// 退避重连、命令副作用去抖与假 socket 的异步 close 都走假时钟
		vi.useFakeTimers({
			toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"],
		});
		client = new RealtimeClient();
	});
	afterEach(() => {
		client.close();
		vi.useRealTimers();
		vi.unstubAllGlobals();
		vi.restoreAllMocks();
	});

	it("补发中途断线：重连游标只到已收到的最大 seq，不取服务端补发目标（W2）", async () => {
		const ws = await connected(client);
		client.subscribe(TID);
		ws.recv(snapshot({ lastSeq: 600 }));
		// 旧服务端语义：每批都报快照的最终 seq
		ws.recv({
			type: "entries",
			threadId: TID,
			entries: Array.from({ length: 200 }, (_, i) => entry(i + 1)),
			lastSeq: 600,
		});
		client.flush();
		expect(getThreadStore(TID).getState().entries).toHaveLength(200);
		ws.drop();
		await vi.advanceTimersByTimeAsync(500);
		await vi.waitFor(() => expect(FakeWS.instances.length).toBe(2));
		const ws2 = FakeWS.instances[1] as FakeWS;
		ws2.accept();
		expect(ws2.subs()).toEqual([
			{ type: "sub", threadId: TID, afterSeq: 200, transcriptRev: 1 },
		]);
	});

	it("fullReplay 快照先丢弃缓存条目", async () => {
		const ws = await connected(client);
		client.subscribe(TID);
		ws.recv(snapshot({ lastSeq: 3 }));
		ws.recv({
			type: "entries",
			threadId: TID,
			entries: [entry(1), entry(2), entry(3)],
			lastSeq: 3,
		});
		ws.recv(snapshot({ lastSeq: 5, transcriptRev: 2, fullReplay: true }));
		client.flush();
		const st = getThreadStore(TID).getState();
		expect(st.entries).toEqual([]);
		expect(st.lastSeq).toBe(0);
		expect(st.transcriptRev).toBe(2);
	});

	it("transcript.reset：清空条目并立即以 afterSeq=0 + 新 rev 重新订阅", async () => {
		const ws = await connected(client);
		client.subscribe(TID);
		ws.recv(snapshot({ lastSeq: 2 }));
		ws.recv({
			type: "entries",
			threadId: TID,
			entries: [entry(1), entry(2)],
			lastSeq: 2,
		});
		client.flush();
		ws.recv({ type: "transcript.reset", threadId: TID, transcriptRev: 7 });
		const st = getThreadStore(TID).getState();
		expect(st.entries).toEqual([]);
		expect(st.transcriptRev).toBe(7);
		expect(ws.subs().at(-1)).toEqual({
			type: "sub",
			threadId: TID,
			afterSeq: 0,
			transcriptRev: 7,
		});
	});

	it("plan.resolved 清除本端的计划提案", async () => {
		const ws = await connected(client);
		client.subscribe(TID);
		ws.recv(
			snapshot(
				{},
				{
					planProposal: {
						type: "plan_proposal",
						title: "p",
						plan: "x",
						suggestedFileName: "p.md",
						options: [],
					},
				},
			),
		);
		client.flush();
		expect(getThreadStore(TID).getState().planProposal).not.toBeNull();
		ws.recv({ type: "plan.resolved", threadId: TID });
		client.flush();
		expect(getThreadStore(TID).getState().planProposal).toBeNull();
	});

	it("close() 同步清理：ping 计时器停掉、在途命令立即拒绝（W8）", async () => {
		const setI = vi.spyOn(globalThis, "setInterval");
		const clearI = vi.spyOn(globalThis, "clearInterval");
		const ws = await connected(client);
		const ping = setI.mock.results.at(-1)?.value;
		expect(ping).toBeDefined();
		const pending = client.cmd(TID, { type: "get_state" });
		client.close();
		await expect(pending).rejects.toMatchObject({ code: "disconnected" });
		expect(clearI).toHaveBeenCalledWith(ping);
		// 迟到的原生 close 事件已摘除，不会触发重连
		await vi.advanceTimersByTimeAsync(20_000);
		expect(ws.onclose).toBeNull();
		expect(FakeWS.instances).toHaveLength(1);
		expect(connectionStore.getState().status).toBe("idle");
	});

	it("快照恢复已有队列时拉取可编辑条目（W11）", async () => {
		const ws = await connected(client);
		client.subscribe(TID);
		ws.recv(
			snapshot(
				{},
				{
					status: "ready",
					state: { isStreaming: false, isSettled: true },
					queue: {
						type: "queue_update",
						steering: [],
						followUp: ["later"],
					},
				},
			),
		);
		vi.advanceTimersByTime(250);
		expect(ws.cmds("get_queue")).toHaveLength(1);
	});

	it("live.reset 后主动刷新 state（W3）", async () => {
		const ws = await connected(client);
		client.subscribe(TID);
		ws.recv({ type: "live.reset", threadId: TID });
		vi.advanceTimersByTime(250);
		expect(ws.cmds("get_state")).toHaveLength(1);
	});

	it("ui.response 被拒：错误进全局提示并重订阅取回待答请求（W14）", async () => {
		const ws = await connected(client);
		client.subscribe(TID);
		const subsBefore = ws.subs().length;
		ws.recv({
			type: "error",
			code: "invalid_frame",
			message: "bad",
			threadId: TID,
		});
		expect(connectionStore.getState().errors).toMatchObject([
			{ code: "invalid_frame", threadId: TID },
		]);
		expect(ws.subs()).toHaveLength(subsBefore + 1);
		const key = connectionStore.getState().errors[0]?.key as number;
		client.dismissError(key);
		expect(connectionStore.getState().errors).toEqual([]);
	});

	it("终端：四种终端帧交给 terminalHub 不进 reducer；term.watch 未订阅时只记下，重连后在 sub 之后重发", async () => {
		const onFrame = vi
			.spyOn(terminalHub, "onFrame")
			.mockImplementation(() => {});
		const onLagged = vi
			.spyOn(terminalHub, "onLagged")
			.mockImplementation(() => {});
		const onResized = vi
			.spyOn(terminalHub, "onResized")
			.mockImplementation(() => {});
		const ws = await connected(client);
		client.termWatch(TID, true);
		expect(ws.sent.filter((m) => m.type === "term.watch")).toEqual([]);
		client.subscribe(TID);
		client.termWatch(TID, true);
		expect(ws.sent.at(-1)).toEqual({
			type: "term.watch",
			threadId: TID,
			on: true,
		});
		const out = {
			type: "terminal_output",
			terminalId: "t1",
			data: "x",
			seq: 1,
		};
		ws.recv({ type: "frame", threadId: TID, frame: out });
		client.flush();
		expect(onFrame).toHaveBeenCalledWith(TID, out);
		expect(getThreadStore(TID).getState().debug).toEqual([]);
		ws.recv({ type: "term.lagged", threadId: TID });
		ws.recv({
			type: "term.resized",
			threadId: TID,
			terminalId: "t1",
			cols: 90,
			rows: 20,
		});
		expect(onLagged).toHaveBeenCalledWith(TID);
		expect(onResized).toHaveBeenCalledWith(TID, "t1", 90, 20);
		ws.drop();
		await vi.advanceTimersByTimeAsync(500);
		await vi.waitFor(() => expect(FakeWS.instances.length).toBe(2));
		const ws2 = FakeWS.instances[1] as FakeWS;
		ws2.accept();
		const types = ws2.sent.map((m) => m.type);
		expect(types.indexOf("term.watch")).toBeGreaterThan(types.indexOf("sub"));
		client.termWatch(TID, false);
		expect(ws2.sent.at(-1)).toEqual({
			type: "term.watch",
			threadId: TID,
			on: false,
		});
	});

	describe("focus 随标签页可见性（M5）", () => {
		let visibility: DocumentVisibilityState;
		beforeEach(() => {
			visibility = "visible";
			vi.spyOn(document, "visibilityState", "get").mockImplementation(
				() => visibility,
			);
		});
		const setVisibility = (v: DocumentVisibilityState) => {
			visibility = v;
			document.dispatchEvent(new Event("visibilitychange"));
		};
		const focuses = (ws: FakeWS) =>
			ws.sent.filter((m) => m.type === "focus").map((m) => m.threadId);

		it("隐藏时发 focus null，恢复可见时重发想看的线程；连接建立时按可见性发", async () => {
			const ws = await connected(client);
			client.focus(TID);
			setVisibility("hidden");
			// 重复的同值事件不重复发
			setVisibility("hidden");
			setVisibility("visible");
			expect(focuses(ws)).toEqual([null, TID, null, TID]);

			// 后台标签页断线重连：onopen 按可见性发 null；可见后才 focus
			setVisibility("hidden");
			ws.drop();
			await vi.advanceTimersByTimeAsync(500);
			await vi.waitFor(() => expect(FakeWS.instances.length).toBe(2));
			const ws2 = FakeWS.instances[1] as FakeWS;
			ws2.accept();
			expect(focuses(ws2)).toEqual([null]);
			setVisibility("visible");
			expect(focuses(ws2)).toEqual([null, TID]);
		});

		it("隐藏期间离开线程页：恢复可见时 focus null 不重发", async () => {
			const ws = await connected(client);
			client.focus(TID);
			setVisibility("hidden");
			client.focus(null);
			setVisibility("visible");
			expect(focuses(ws)).toEqual([null, TID, null]);
		});
	});

	it("notification / notifications.read 交给通知 store（M5）", async () => {
		const store = notificationsStore.getState();
		const onNotification = vi
			.spyOn(store, "onNotification")
			.mockImplementation(() => {});
		const onRead = vi.spyOn(store, "onRead").mockImplementation(() => {});
		const ws = await connected(client);
		const n = {
			id: "ntf_1",
			kind: "done",
			threadId: TID,
			title: "t",
			body: "",
			url: `/t/${TID}`,
			createdAt: 1,
			deliveries: [],
		};
		ws.recv({ type: "notification", notification: n });
		ws.recv({ type: "notifications.read", threadId: TID });
		ws.recv({ type: "notifications.read" });
		expect(onNotification).toHaveBeenCalledWith(n);
		expect(onRead.mock.calls).toEqual([[TID], [undefined]]);
	});

	it("orb 从挂起回到 ready：重取 state 与命令列表（挂起期间的只读查询被拒、不会重放）", async () => {
		const ws = await connected(client);
		client.subscribe(TID);
		const frozen = makeOrb("orb-x", [TID], "frozen");
		ws.recv(snapshot({ orb: frozen }, { status: "ready" }));
		vi.advanceTimersByTime(250);
		const before = {
			state: ws.cmds("get_state").length,
			commands: ws.cmds("get_available_commands").length,
		};
		ws.recv({ type: "orb", orb: makeOrb("orb-x", [TID], "waking") });
		vi.advanceTimersByTime(250);
		expect(ws.cmds("get_state")).toHaveLength(before.state);
		ws.recv({ type: "orb", orb: makeOrb("orb-x", [TID], "ready") });
		vi.advanceTimersByTime(250);
		expect(ws.cmds("get_state")).toHaveLength(before.state + 1);
		expect(ws.cmds("get_available_commands")).toHaveLength(before.commands + 1);
		// 已就绪时的 orb 更新（如采样、服务状态）不重取
		ws.recv({ type: "orb", orb: makeOrb("orb-x", [TID], "ready") });
		vi.advanceTimersByTime(250);
		expect(ws.cmds("get_state")).toHaveLength(before.state + 1);
	});
});
