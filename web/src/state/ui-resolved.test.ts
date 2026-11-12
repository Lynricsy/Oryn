/**
 * ask / select 对话框多端镜像、先答者生效：两个客户端订阅同一线程，各自持有待答请求；
 * 一端作答后控制面广播 `ui.resolved`，另一端自动关闭；后到的应答被忽略。
 */
import { describe, expect, it } from "vitest";
import type { WsThreadSnapshot } from "../api/client";
import { MockServer, type MockSocket } from "../mock/server";
import { reduceThread, type ThreadMessage } from "./stores";
import {
	initialThreadState,
	type OmpFrame,
	type ThreadViewState,
} from "./thread-reducer";

const THREAD = "T-0193a1b2-0000-7000-8000-00000000a004";

class Client implements MockSocket {
	state: ThreadViewState = initialThreadState(THREAD);
	resolved: string[] = [];
	deliver(msg: Record<string, unknown>) {
		let m: ThreadMessage | null = null;
		switch (msg.type) {
			case "thread.snapshot":
				m = {
					type: "thread.snapshot",
					snapshot: msg as unknown as WsThreadSnapshot,
				};
				break;
			case "frame":
				m = { type: "frame", frame: msg.frame as OmpFrame };
				break;
			case "ui.resolved":
				this.resolved.push(String(msg.requestId));
				m = { type: "ui.resolved", requestId: String(msg.requestId) };
				break;
		}
		if (m) this.state = reduceThread(this.state, m);
	}
}

describe("UI 请求先答者生效", () => {
	it("A 作答后 B 自动关闭，B 的迟到应答被忽略", () => {
		const server = new MockServer();
		server.delay = 0;
		const a = new Client();
		const b = new Client();
		server.connect(a);
		server.connect(b);
		server.onClient(a, { type: "sub", threadId: THREAD, afterSeq: 0 });
		server.onClient(b, { type: "sub", threadId: THREAD, afterSeq: 0 });
		expect(a.state.pendingUi.map((r) => r.id)).toEqual(["ui-mock-1"]);
		expect(b.state.pendingUi.map((r) => r.id)).toEqual(["ui-mock-1"]);

		server.onClient(a, {
			type: "ui.response",
			threadId: THREAD,
			frame: {
				type: "extension_ui_response",
				id: "ui-mock-1",
				value: "允许一次",
			},
		});
		expect(a.state.pendingUi).toEqual([]);
		expect(b.state.pendingUi).toEqual([]);
		expect(b.resolved).toEqual(["ui-mock-1"]);

		server.onClient(b, {
			type: "ui.response",
			threadId: THREAD,
			frame: { type: "extension_ui_response", id: "ui-mock-1", value: "拒绝" },
		});
		expect(a.resolved).toEqual(["ui-mock-1"]);
		expect(b.resolved).toEqual(["ui-mock-1"]);
	});
});
