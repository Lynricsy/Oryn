/** 回放 fakeomp 风格的一轮：WS 订阅者依次收到流式帧，结束后条目完整、叠加层清空。 */
import { describe, expect, it } from "vitest";
import type { Entry, WsThreadSnapshot } from "../api/client";
import { reduceThread } from "../state/stores";
import {
	initialThreadState,
	type OmpFrame,
	type ThreadViewState,
} from "../state/thread-reducer";
import { buildTranscript } from "../state/transcript";
import { MAIN_THREAD } from "./fixtures";
import { MockServer, type MockSocket } from "./server";

class Client implements MockSocket {
	state: ThreadViewState = initialThreadState(MAIN_THREAD);
	frames: string[] = [];
	deliver(msg: Record<string, unknown>) {
		if (msg.type === "thread.snapshot") {
			this.state = reduceThread(this.state, {
				type: "thread.snapshot",
				snapshot: msg as unknown as WsThreadSnapshot,
			});
		} else if (msg.type === "entries") {
			this.state = reduceThread(this.state, {
				type: "entries",
				msg: {
					entries: msg.entries as Entry[],
					lastSeq: Number(msg.lastSeq),
					leafEntryId: msg.leafEntryId as string,
				},
			});
		} else if (msg.type === "frame") {
			const frame = msg.frame as OmpFrame;
			this.frames.push(frame.type);
			this.state = reduceThread(this.state, { type: "frame", frame });
		}
	}
}

describe("mock 帧序列", () => {
	it("prompt → 流式消息 → 工具 → agent_end，条目入库后叠加层清空", async () => {
		const server = new MockServer();
		server.delay = 0;
		const c = new Client();
		server.connect(c);
		server.onClient(c, { type: "sub", threadId: MAIN_THREAD, afterSeq: 0 });
		const before = c.state.entries.length;
		await server.runPrompt(MAIN_THREAD, "用 bash 执行 uname -a");
		const order = [
			"agent_start",
			"message_start",
			"message_update",
			"message_end",
			"tool_execution_start",
			"tool_execution_end",
			"agent_end",
			"prompt_result",
			"session_settled",
		];
		let idx = -1;
		for (const type of order) {
			const next = c.frames.indexOf(type, idx + 1);
			expect(next, type).toBeGreaterThan(idx);
			idx = next;
		}
		expect(c.state.entries.length).toBe(before + 4);
		expect(c.state.liveMessage).toBeNull();
		expect(c.state.liveTools).toEqual({});
		expect(c.state.running).toBe(false);
		const last = buildTranscript(c.state).items.at(-1);
		expect(
			last?.kind === "message" && JSON.stringify(last.message.content),
		).toContain("Linux");
	});
});
