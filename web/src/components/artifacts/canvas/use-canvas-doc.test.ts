import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CanvasDoc, CanvasOp } from "../../../lib/artifacts/canvas-doc";
import {
	ACK_WAIT_MS,
	type CanvasMessage,
	CanvasSync,
	type CanvasTransport,
} from "./use-canvas-doc";

const DOC = {
	format: 1,
	canvases: [{ id: "c1", name: "首页" }],
	pages: [
		{ id: "a", canvas: "c1", name: "A", file: "a.html", x: 0, y: 0 },
		{ id: "b", canvas: "c1", name: "B", file: "b.html", x: 2000, y: 0 },
	],
	notes: [],
};

interface Posted {
	ops: CanvasOp[];
	clientSeq: number;
	resolve(seq: number): void;
	reject(err: unknown): void;
}

function setup() {
	const posted: Posted[] = [];
	let state = { version: 1, seq: 10, doc: DOC as Record<string, unknown> };
	const fetchState = vi.fn(async () => state);
	const transport: CanvasTransport = {
		fetchState,
		post: (ops, _client, clientSeq) =>
			new Promise<number>((resolve, reject) => {
				posted.push({ ops, clientSeq, resolve, reject });
			}),
	};
	const rejected: unknown[] = [];
	const sync = new CanvasSync(transport, (e) => rejected.push(e));
	return {
		sync,
		posted,
		rejected,
		fetchState,
		setState(s: typeof state) {
			state = s;
		},
	};
}

function page(doc: CanvasDoc | null, id: string) {
	return doc?.pages.find((p) => p.id === id);
}

function opsMsg(
	seq: number,
	ops: CanvasOp[],
	client = "someone-else",
	clientSeq = 1,
): CanvasMessage {
	return {
		type: "artifact.canvas",
		id: "art_X",
		seq,
		ops: ops as CanvasMessage["ops"],
		client,
		clientSeq,
	};
}

const setX = (id: string, x: number): CanvasOp => ({
	op: "set",
	id,
	key: "x",
	value: x,
});

async function flush() {
	await Promise.resolve();
	await Promise.resolve();
}

beforeEach(() => {
	vi.useFakeTimers();
});
afterEach(() => {
	vi.useRealTimers();
});

describe("CanvasSync", () => {
	it("取到状态后本地文档就是服务端文档", async () => {
		const { sync } = setup();
		sync.resync();
		await flush();
		expect(page(sync.doc, "a")?.x).toBe(0);
		expect(sync.getSnapshot().version).toBe(1);
	});

	it("别人的操作到达时，自己的待确认操作仍在最上层", async () => {
		const { sync, posted } = setup();
		sync.resync();
		await flush();
		expect(sync.commit([setX("a", 100)])).toBe(true);
		expect(page(sync.doc, "a")?.x).toBe(100);
		expect(posted).toHaveLength(1);

		// 别人在之后也改了 a.x，并动了 b
		sync.receive(opsMsg(11, [setX("a", 50), setX("b", 3000)]));
		expect(page(sync.doc, "a")?.x).toBe(100);
		expect(page(sync.doc, "b")?.x).toBe(3000);

		// 自己的那批被确认：底稿里按服务端的顺序应用，之后再没有待确认的
		sync.receive(opsMsg(12, [setX("a", 100)], sync.client, 1));
		expect(page(sync.doc, "a")?.x).toBe(100);
		// 再来一个别人的改动，没有被自己压住
		sync.receive(opsMsg(13, [setX("a", 7)]));
		expect(page(sync.doc, "a")?.x).toBe(7);
	});

	it("撤销只撤自己的", async () => {
		const { sync } = setup();
		sync.resync();
		await flush();
		sync.commit([setX("a", 100)]);
		sync.receive(opsMsg(11, [setX("b", 3000)]));
		sync.undo();
		expect(page(sync.doc, "a")?.x).toBe(0);
		expect(page(sync.doc, "b")?.x).toBe(3000);
		expect(sync.getSnapshot().canRedo).toBe(true);
		sync.redo();
		expect(page(sync.doc, "a")?.x).toBe(100);
		expect(page(sync.doc, "b")?.x).toBe(3000);
	});

	it("撤销新增 = 删掉它；再重做又回来", async () => {
		const { sync } = setup();
		sync.resync();
		await flush();
		sync.commit([
			{
				op: "add",
				object: {
					type: "note",
					id: "n1",
					canvas: "c1",
					kind: "sticky",
					x: 10,
					y: 10,
					text: "hi",
				},
			},
		]);
		expect(sync.doc?.notes).toHaveLength(1);
		sync.undo();
		expect(sync.doc?.notes).toHaveLength(0);
		sync.redo();
		expect(sync.doc?.notes.map((n) => n.id)).toEqual(["n1"]);
	});

	it("本地放不上的操作不提交", async () => {
		const { sync, posted } = setup();
		sync.resync();
		await flush();
		expect(sync.commit([{ op: "remove", id: "c1" }])).toBe(false);
		expect(posted).toHaveLength(0);
	});

	it("一次只在途一批，按顺序发送", async () => {
		const { sync, posted } = setup();
		sync.resync();
		await flush();
		sync.commit([setX("a", 1)]);
		sync.commit([setX("a", 2)]);
		expect(posted.map((p) => p.clientSeq)).toEqual([1]);
		posted[0]?.resolve(11);
		await flush();
		expect(posted.map((p) => p.clientSeq)).toEqual([1, 2]);
	});

	it("服务端拒绝：丢掉那一批与它的撤销项并报告", async () => {
		const { sync, posted, rejected } = setup();
		sync.resync();
		await flush();
		sync.commit([setX("a", 100)]);
		posted[0]?.reject(new Error("invalid"));
		await flush();
		expect(page(sync.doc, "a")?.x).toBe(0);
		expect(sync.getSnapshot().canUndo).toBe(false);
		expect(rejected).toHaveLength(1);
	});

	it("seq 跳号时重新取整份状态，待确认的照样压在上面", async () => {
		const { sync, fetchState, setState } = setup();
		sync.resync();
		await flush();
		sync.commit([setX("a", 100)]);
		setState({
			version: 2,
			seq: 20,
			doc: {
				...DOC,
				pages: [
					{ ...DOC.pages[0], x: 5 },
					{ ...DOC.pages[1], x: 4000 },
				],
			},
		});
		sync.receive(opsMsg(15, [setX("b", 1)]));
		await flush();
		expect(fetchState).toHaveBeenCalledTimes(2);
		expect(page(sync.doc, "b")?.x).toBe(4000);
		expect(page(sync.doc, "a")?.x).toBe(100);
		expect(sync.getSnapshot().version).toBe(2);
	});

	it("整份文档替换后重放待确认操作", async () => {
		const { sync } = setup();
		sync.resync();
		await flush();
		sync.commit([setX("b", 9)]);
		sync.receive({
			type: "artifact.canvas",
			id: "art_X",
			seq: 11,
			version: 3,
			doc: { ...DOC, pages: [DOC.pages[1]] },
		});
		expect(sync.doc?.pages.map((p) => p.id)).toEqual(["b"]);
		expect(page(sync.doc, "b")?.x).toBe(9);
		expect(sync.getSnapshot().version).toBe(3);
	});

	it("POST 返回后广播迟迟不到：按返回的 seq 重新取状态", async () => {
		const { sync, posted, fetchState } = setup();
		sync.resync();
		await flush();
		sync.commit([setX("a", 100)]);
		posted[0]?.resolve(11);
		await flush();
		expect(fetchState).toHaveBeenCalledTimes(1);
		vi.advanceTimersByTime(ACK_WAIT_MS);
		await flush();
		expect(fetchState).toHaveBeenCalledTimes(2);
	});
});
