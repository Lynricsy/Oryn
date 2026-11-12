/** mock 终端替身（同 fakeomp 2.4）：seq 为累计输出字符数、term.watch 只放行观看者、resize 广播、洪泛背压。 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { MAIN_THREAD } from "./fixtures";
import { MockServer, type MockSocket } from "./server";

type Msg = Record<string, unknown>;

class Client implements MockSocket {
	msgs: Msg[] = [];
	deliver(msg: Msg) {
		this.msgs.push(msg);
	}
	outputs(): { terminalId: string; data: string; seq: number }[] {
		return this.msgs
			.filter(
				(m) =>
					m.type === "frame" && (m.frame as Msg).type === "terminal_output",
			)
			.map((m) => m.frame as { terminalId: string; data: string; seq: number });
	}
	res(id: string): Msg | undefined {
		return this.msgs.find((m) => m.type === "res" && m.id === id)?.response as
			| Msg
			| undefined;
	}
}

function setup() {
	const server = new MockServer();
	server.delay = 0;
	const a = new Client();
	const b = new Client();
	for (const c of [a, b]) {
		server.connect(c);
		server.onClient(c, { type: "sub", threadId: MAIN_THREAD, afterSeq: 0 });
	}
	let n = 0;
	// 终端命令在 handleCmd 的同步段里应答
	const cmd = (c: Client, frame: Msg) => {
		const id = `c${++n}`;
		server.onClient(c, { type: "cmd", threadId: MAIN_THREAD, id, frame });
		return c.res(id);
	};
	return { server, a, b, cmd };
}

afterEach(() => {
	vi.useRealTimers();
});

describe("mock 终端", () => {
	it("open → input → snapshot：seq 等于各 terminal_output.data 长度之和；只有 term.watch 的连接收到输出", () => {
		const { server, a, b, cmd } = setup();
		server.onClient(a, { type: "term.watch", threadId: MAIN_THREAD, on: true });
		const open = cmd(a, { type: "terminal_open", cols: 90, rows: 20 });
		const info = open?.data as { terminalId: string; cols: number };
		expect(info.cols).toBe(90);
		cmd(a, {
			type: "terminal_input",
			terminalId: info.terminalId,
			data: "echo hi\r",
		});
		const snap = cmd(a, {
			type: "terminal_snapshot",
			terminalId: info.terminalId,
		})?.data as { data: string; seq: number };
		const outs = a.outputs();
		const total = outs.reduce((n, o) => n + o.data.length, 0);
		expect(snap.seq).toBe(total);
		expect(outs.at(-1)?.seq).toBe(total);
		expect(snap.data).toBe(outs.map((o) => o.data).join(""));
		expect(snap.data).toContain("hi\r\nfake$ ");
		// b 没开 term.watch：收得到 terminal_opened，收不到输出
		expect(b.outputs()).toEqual([]);
		expect(
			b.msgs.some(
				(m) =>
					m.type === "frame" && (m.frame as Msg).type === "terminal_opened",
			),
		).toBe(true);
	});

	it("terminal_resize 广播 term.resized 给全部订阅者（含发起方）", () => {
		const { a, b, cmd } = setup();
		const info = cmd(a, { type: "terminal_open" })?.data as {
			terminalId: string;
		};
		cmd(b, {
			type: "terminal_resize",
			terminalId: info.terminalId,
			cols: 120,
			rows: 40,
		});
		for (const c of [a, b])
			expect(c.msgs).toContainEqual({
				type: "term.resized",
				threadId: MAIN_THREAD,
				terminalId: info.terminalId,
				cols: 120,
				rows: 40,
			});
	});

	it("洪泛超过单连接阈值：丢输出、关开关并发 term.lagged，之后 snapshot 仍有应答", async () => {
		vi.useFakeTimers();
		const { server, a, cmd } = setup();
		server.termLag = 100_000;
		server.onClient(a, { type: "term.watch", threadId: MAIN_THREAD, on: true });
		const info = cmd(a, { type: "terminal_open" })?.data as {
			terminalId: string;
		};
		cmd(a, {
			type: "terminal_input",
			terminalId: info.terminalId,
			data: "flood 300000\r",
		});
		// 洪泛循环每 8 帧让出一次（sleep(0)）
		await vi.advanceTimersByTimeAsync(10);
		expect(a.msgs).toContainEqual({
			type: "term.lagged",
			threadId: MAIN_THREAD,
		});
		const got = a.outputs().reduce((n, o) => n + o.data.length, 0);
		expect(got).toBeLessThan(300_000);
		const snap = cmd(a, {
			type: "terminal_snapshot",
			terminalId: info.terminalId,
		})?.data as { seq: number };
		expect(snap.seq).toBeGreaterThan(300_000);
	});

	it("restartAgent 清空终端并发 live.reset", () => {
		const { server, a, cmd } = setup();
		cmd(a, { type: "terminal_open" });
		server.restartAgent(MAIN_THREAD);
		expect(a.msgs).toContainEqual({
			type: "live.reset",
			threadId: MAIN_THREAD,
		});
		const list = cmd(a, { type: "terminal_list" })?.data as {
			terminals: unknown[];
		};
		expect(list.terminals).toEqual([]);
	});
});
