import { describe, expect, it } from "vitest";
import type { Entry, Thread, WsThreadSnapshot } from "../api/client";
import { reduceThread } from "./stores";
import {
	applyEntries,
	applySnapshot,
	applyTranscriptReset,
	initialThreadState,
	prependEntries,
	type ThreadViewState,
} from "./thread-reducer";

const TID = "T-window";

function entry(seq: number): Entry {
	return {
		seq,
		file: "s.jsonl",
		entryId: `e${seq}`,
		type: "message",
		ts: 0,
		payload: { type: "message", id: `e${seq}`, message: { role: "user" } },
	};
}

const range = (from: number, to: number) =>
	Array.from({ length: to - from + 1 }, (_, i) => entry(from + i));

function snapshot(over: Partial<WsThreadSnapshot> = {}): WsThreadSnapshot {
	return {
		type: "thread.snapshot",
		threadId: TID,
		thread: { id: TID } as Thread,
		agent: { status: "none", pendingUi: [], subagents: [], live: [] },
		lastSeq: 1000,
		transcriptRev: 0,
		fullReplay: false,
		...over,
	};
}

/** 尾部订阅：快照开窗 900，随后补发 901..1000。 */
function windowed(): ThreadViewState {
	let s = applySnapshot(
		initialThreadState(TID),
		snapshot({ windowStart: 900 }),
	);
	s = applyEntries(s, { entries: range(901, 1000) });
	return s;
}

describe("尾部窗口（M19）", () => {
	it("快照的 windowStart 落地；不带窗口的快照为 0", () => {
		expect(windowed().windowStart).toBe(900);
		expect(applySnapshot(initialThreadState(TID), snapshot()).windowStart).toBe(
			0,
		);
	});

	it("续传快照（未开窗）保留本地窗口；全量重放以新快照为准", () => {
		const s = windowed();
		expect(applySnapshot(s, snapshot()).windowStart).toBe(900);
		expect(
			applySnapshot(s, snapshot({ fullReplay: true, transcriptRev: 2 }))
				.windowStart,
		).toBe(0);
		expect(applyTranscriptReset(s, 3).windowStart).toBe(0);
	});

	it("并入更早一页：顺序正确、lastSeq 不回退、窗口前移", () => {
		let s = windowed();
		s = reduceThread(s, {
			type: "entries.earlier",
			page: { entries: range(401, 900), hasMore: true },
		});
		expect(s.entries.map((e) => e.seq)).toEqual(
			range(401, 1000).map((e) => e.seq),
		);
		expect(s.lastSeq).toBe(1000);
		expect(s.windowStart).toBe(400);

		s = prependEntries(s, { entries: range(1, 400), hasMore: false });
		expect(s.entries).toHaveLength(1000);
		expect(s.entries[0]?.seq).toBe(1);
		expect(s.entries.at(-1)?.seq).toBe(1000);
		expect(s.lastSeq).toBe(1000);
		expect(s.windowStart).toBe(0);
	});

	it("并入后新条目照常追加在末尾", () => {
		let s = prependEntries(windowed(), {
			entries: range(401, 900),
			hasMore: true,
		});
		s = applyEntries(s, { entries: [entry(1001)] });
		expect(s.entries.at(-1)?.seq).toBe(1001);
		expect(s.lastSeq).toBe(1001);
		expect(s.windowStart).toBe(400);
	});
});
