/**
 * 分享页数据源：快照分页喂入、实时消息映射、`transcript.reset` 从 0 重连、4001 / `share.revoked` 触发 onGone、
 * 普通断开退避重连、25s 心跳。假 WebSocket + 假定时器，不 sleep。
 */
import {
	afterEach,
	beforeEach,
	describe,
	expect,
	it,
	type Mock,
	vi,
} from "vitest";
import type { Entry } from "../api/client";
import { getThreadStore, resetThreadStores } from "../state/stores";
import { type PublicShare, ShareFeed, shareKey } from "./feed";

const SLUG = "0123456789abcdef0123456789abcdef";
type Json = Record<string, unknown>;

class FakeWS {
	static readonly CONNECTING = 0;
	static readonly OPEN = 1;
	static readonly CLOSING = 2;
	static readonly CLOSED = 3;
	static instances: FakeWS[] = [];
	readyState = FakeWS.CONNECTING;
	sent: Json[] = [];
	closed = false;
	onopen: (() => void) | null = null;
	onmessage: ((ev: { data: string }) => void) | null = null;
	onclose: ((ev: { code: number }) => void) | null = null;
	readonly url: string;
	constructor(url: string) {
		this.url = url;
		FakeWS.instances.push(this);
	}
	send(data: string) {
		this.sent.push(JSON.parse(data) as Json);
	}
	close() {
		this.closed = true;
		this.readyState = FakeWS.CLOSED;
	}
	accept() {
		this.readyState = FakeWS.OPEN;
		this.onopen?.();
	}
	recv(msg: Json) {
		this.onmessage?.({ data: JSON.stringify(msg) });
	}
	/** 服务端断开 */
	drop(code: number) {
		this.readyState = FakeWS.CLOSED;
		this.onclose?.({ code });
	}
}

function entry(seq: number): Entry {
	return {
		seq,
		file: "s.jsonl",
		entryId: `e${seq}`,
		parentId: seq > 1 ? `e${seq - 1}` : undefined,
		type: "message",
		payload: {
			type: "message",
			id: `e${seq}`,
			message: { role: "user", content: [{ type: "text", text: `m${seq}` }] },
		},
	};
}

const SNAPSHOT: PublicShare = {
	mode: "snapshot",
	title: "快照标题",
	sharedAt: 2_000,
	threadCreatedAt: 1_000,
	lastSeq: 3,
	leafEntryId: "e3",
	transcriptRev: 2,
	attachments: [],
};
const LIVE: PublicShare = { ...SNAPSHOT, mode: "live", state: "working" };

const store = () => getThreadStore(shareKey(SLUG)).getState();

beforeEach(() => {
	resetThreadStores();
	FakeWS.instances = [];
});
afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

describe("ShareFeed 快照", () => {
	it("按 after 翻页直到 hasMore=false，首页与合成快照一起落地", async () => {
		const urls: string[] = [];
		vi.stubGlobal(
			"fetch",
			vi.fn(async (input: Request) => {
				const u = new URL(input.url);
				urls.push(`${u.pathname}${u.search}`);
				const after = Number(u.searchParams.get("after"));
				return Response.json(
					after === 0
						? { entries: [entry(1), entry(2)], hasMore: true, lastSeq: 3 }
						: {
								entries: [entry(3)],
								hasMore: false,
								lastSeq: 3,
								leafEntryId: "e3",
							},
				);
			}),
		);
		const onGone = vi.fn();
		const feed = new ShareFeed(SLUG, SNAPSHOT, onGone);
		feed.start();
		await vi.waitFor(() => expect(store().entries).toHaveLength(3));
		feed.stop();
		expect(urls).toEqual([
			`/api/v1/public/shares/${SLUG}/entries?after=0&limit=500`,
			`/api/v1/public/shares/${SLUG}/entries?after=2&limit=500`,
		]);
		expect(store().snapshotReceived).toBe(true);
		expect(store().thread).toMatchObject({
			id: shareKey(SLUG),
			title: "快照标题",
			state: "idle",
		});
		expect(store().leafEntryId).toBe("e3");
		expect(store().agentStatus).toBe("none");
		expect(onGone).not.toHaveBeenCalled();
		expect(FakeWS.instances).toHaveLength(0);
	});

	it("分页接口 404（已撤销）→ onGone", async () => {
		vi.stubGlobal(
			"fetch",
			vi.fn(async () =>
				Response.json({ code: "not_found", message: "x" }, { status: 404 }),
			),
		);
		const onGone = vi.fn();
		new ShareFeed(SLUG, SNAPSHOT, onGone).start();
		await vi.waitFor(() => expect(onGone).toHaveBeenCalledTimes(1));
	});
});

describe("ShareFeed 实时", () => {
	let onGone: Mock<() => void>;
	let feed: ShareFeed;
	beforeEach(() => {
		vi.stubGlobal("WebSocket", FakeWS);
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => {
				throw new Error("实时分享不应发 REST 请求");
			}),
		);
		vi.useFakeTimers({
			toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"],
		});
		onGone = vi.fn<() => void>();
		feed = new ShareFeed(SLUG, LIVE, onGone);
		feed.start();
	});
	afterEach(() => feed.stop());

	const ws = (i: number) => FakeWS.instances[i] as FakeWS;

	function snapshot(p: Json = {}): Json {
		return {
			type: "share.snapshot",
			title: "实时标题",
			state: "working",
			leafEntryId: "e2",
			lastSeq: 2,
			transcriptRev: 1,
			fullReplay: false,
			live: [],
			subagents: [],
			...p,
		};
	}

	it("连匿名 WS，快照 / 条目 / 帧 / 状态映射进线程 store", () => {
		expect(ws(0).url).toBe(
			`ws://${location.host}/api/v1/public/shares/${SLUG}/ws?after=0`,
		);
		ws(0).accept();
		ws(0).recv(snapshot());
		ws(0).recv({
			type: "entries",
			entries: [entry(1), entry(2)],
			leafEntryId: "e2",
			lastSeq: 2,
		});
		expect(store().snapshotReceived).toBe(true);
		expect(store().thread).toMatchObject({
			title: "实时标题",
			state: "working",
		});
		expect(store().entries.map((e) => e.seq)).toEqual([1, 2]);
		expect(store().transcriptRev).toBe(1);

		ws(0).recv({
			type: "frame",
			frame: {
				type: "message_start",
				message: { role: "assistant", content: [], timestamp: 9 },
			},
		});
		expect(store().liveMessage).not.toBeNull();
		ws(0).recv({ type: "live.reset" });
		expect(store().liveMessage).toBeNull();

		ws(0).recv({ type: "share.state", state: "idle", title: "新标题" });
		expect(store().thread).toMatchObject({ state: "idle", title: "新标题" });
		expect(store().leafEntryId).toBe("e2");
	});

	it("transcript.reset：清空条目并以 after=0 与新 rev 重连", () => {
		ws(0).accept();
		ws(0).recv(snapshot());
		ws(0).recv({
			type: "entries",
			entries: [entry(1), entry(2)],
			lastSeq: 2,
		});
		ws(0).recv({ type: "transcript.reset", transcriptRev: 5 });
		expect(store().entries).toEqual([]);
		expect(ws(0).closed).toBe(true);
		expect(FakeWS.instances).toHaveLength(2);
		expect(ws(1).url).toContain("?after=0&rev=5");
	});

	it("关闭码 4001 → onGone，不再重连", () => {
		ws(0).accept();
		ws(0).drop(4001);
		expect(onGone).toHaveBeenCalledTimes(1);
		vi.advanceTimersByTime(60_000);
		expect(FakeWS.instances).toHaveLength(1);
	});

	it("share.revoked → onGone", () => {
		ws(0).accept();
		ws(0).recv({ type: "share.revoked" });
		expect(onGone).toHaveBeenCalledTimes(1);
		expect(ws(0).closed).toBe(true);
	});

	it("其它断开按 0.5s 起的退避重连，从已有的 lastSeq 续传", () => {
		ws(0).accept();
		ws(0).recv(snapshot());
		ws(0).recv({ type: "entries", entries: [entry(1), entry(2)], lastSeq: 2 });
		ws(0).drop(1006);
		vi.advanceTimersByTime(499);
		expect(FakeWS.instances).toHaveLength(1);
		vi.advanceTimersByTime(1);
		expect(FakeWS.instances).toHaveLength(2);
		expect(ws(1).url).toContain("?after=2&rev=1");
		// 第二次失败退避翻倍
		ws(1).drop(1006);
		vi.advanceTimersByTime(999);
		expect(FakeWS.instances).toHaveLength(2);
		vi.advanceTimersByTime(1);
		expect(FakeWS.instances).toHaveLength(3);
		expect(onGone).not.toHaveBeenCalled();
	});

	it("每 25s 发 ping", () => {
		ws(0).accept();
		vi.advanceTimersByTime(25_000);
		expect(ws(0).sent).toEqual([{ type: "ping" }]);
	});
});
