import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { OmpCommand, RpcTerminalInfo } from "../omp/types";
import {
	type Readiness,
	TerminalHub,
	type TerminalHubDeps,
	type TermLike,
} from "./terminals";

const TID = "T-term-test";

interface Deferred {
	promise: Promise<void>;
	resolve(): void;
}
function deferred(): Deferred {
	let resolve: () => void = () => {};
	const promise = new Promise<void>((r) => {
		resolve = r;
	});
	return { promise, resolve };
}

function info(id: string, p: Partial<RpcTerminalInfo> = {}): RpcTerminalInfo {
	return {
		terminalId: id,
		kind: "shell",
		title: "bash",
		cwd: "/home/user",
		running: true,
		cols: 100,
		rows: 30,
		...p,
	};
}

class FakeTerm implements TermLike {
	cols = 100;
	rows = 30;
	buf = "";
	resizes: [number, number][] = [];
	write(d: string) {
		this.buf += d;
	}
	reset() {
		this.buf = "";
	}
	resize(c: number, r: number) {
		this.cols = c;
		this.rows = r;
		this.resizes.push([c, r]);
	}
	dispose() {}
	text() {
		return this.buf;
	}
	propose() {
		return { cols: 120, rows: 40 };
	}
}

/** 假 omp：按命令类型给出应答；`snapshot` 可延迟以模拟应答在途时到达的帧。 */
class FakeOmp {
	calls: OmpCommand[] = [];
	terminals: RpcTerminalInfo[] = [];
	snapshots = new Map<string, { data: string; seq: number }>();
	snapshotGate: Deferred | null = null;
	nextOpen = 1;
	async handle(frame: OmpCommand): Promise<unknown> {
		this.calls.push(frame);
		switch (frame.type) {
			case "terminal_list":
				return { terminals: this.terminals };
			case "terminal_snapshot": {
				if (this.snapshotGate) await this.snapshotGate.promise;
				const t = this.terminals.find((x) => x.terminalId === frame.terminalId);
				const s = this.snapshots.get(frame.terminalId) ?? { data: "", seq: 0 };
				if (!t) throw new Error("gone");
				return { terminal: t, ...s };
			}
			case "terminal_open": {
				const t = info(`term-new-${this.nextOpen++}`, {
					cols: frame.cols ?? 100,
					rows: frame.rows ?? 30,
				});
				this.terminals = [...this.terminals, t];
				return t;
			}
			default:
				return undefined;
		}
	}
	count(type: string) {
		return this.calls.filter((c) => c.type === type).length;
	}
}

function setup(opts: { ready?: Readiness } = {}) {
	const omp = new FakeOmp();
	const terms: FakeTerm[] = [];
	let readiness: Readiness = opts.ready ?? { ready: true };
	const readinessCbs = new Set<() => void>();
	const watch: boolean[] = [];
	const reveal = vi.fn();
	const deps: TerminalHubDeps = {
		cmd: ((_tid: string, frame: OmpCommand) =>
			omp.handle(frame)) as TerminalHubDeps["cmd"],
		termWatch: (_tid, on) => watch.push(on),
		wake: vi.fn(async () => {}),
		readiness: () => readiness,
		documentVisible: () => true,
		reveal,
		watchReadiness: (_tid, cb) => {
			readinessCbs.add(cb);
			return () => readinessCbs.delete(cb);
		},
		now: () => Date.now(),
	};
	const hub = new TerminalHub(deps);
	hub.setTermFactory(() => {
		const t = new FakeTerm();
		terms.push(t);
		return t;
	});
	const detach = hub.attach(TID);
	return {
		hub,
		omp,
		terms,
		watch,
		reveal,
		detach,
		setReady(r: Readiness) {
			readiness = r;
			for (const cb of readinessCbs) cb();
		},
		state: () => hub.store(TID).getState(),
		text: (id: string) => hub.terminalText(TID, id),
	};
}

const out = (terminalId: string, seq: number, data: string) =>
	({ type: "terminal_output", terminalId, seq, data }) as const;

beforeEach(() => {
	vi.useFakeTimers();
});
afterEach(() => {
	vi.useRealTimers();
});

describe("TerminalHub 同步", () => {
	it("快照期间到达的重叠帧回放后不重复不丢失", async () => {
		const h = setup();
		h.omp.terminals = [info("t1")];
		h.omp.snapshots.set("t1", { data: "abcdef", seq: 6 });
		h.omp.snapshotGate = deferred();
		h.hub.setVisible(TID, true);
		await vi.advanceTimersByTimeAsync(0);
		expect(h.watch).toEqual([true]);
		expect(h.omp.count("terminal_snapshot")).toBe(1);
		// 快照应答在途：一帧完全旧、一帧与快照末尾重叠、一帧首尾相接
		h.hub.onFrame(TID, out("t1", 4, "cd"));
		h.hub.onFrame(TID, out("t1", 8, "efgh"));
		h.hub.onFrame(TID, out("t1", 10, "ij"));
		h.omp.snapshotGate.resolve();
		await vi.advanceTimersByTimeAsync(0);
		expect(h.text("t1")).toBe("abcdefghij");
		// 之后的实时帧：旧帧丢弃，新帧续接
		h.hub.onFrame(TID, out("t1", 10, "ij"));
		h.hub.onFrame(TID, out("t1", 12, "kl"));
		expect(h.text("t1")).toBe("abcdefghijkl");
		// 已有运行中的 shell：不新开
		expect(h.omp.count("terminal_open")).toBe(0);
		expect(h.state().activeId).toBe("t1");
	});

	it("自检出缺口时重新同步（间隔 ≥1s），以新快照为准", async () => {
		const h = setup();
		h.omp.terminals = [info("t1")];
		h.omp.snapshots.set("t1", { data: "abc", seq: 3 });
		h.hub.setVisible(TID, true);
		await vi.advanceTimersByTimeAsync(0);
		expect(h.text("t1")).toBe("abc");
		h.omp.snapshots.set("t1", { data: "abcdefghij", seq: 10 });
		h.hub.onFrame(TID, out("t1", 10, "hij")); // 4..7 缺失
		expect(h.text("t1")).toBe("abc");
		await vi.advanceTimersByTimeAsync(500);
		expect(h.omp.count("terminal_list")).toBe(1);
		await vi.advanceTimersByTimeAsync(600);
		expect(h.omp.count("terminal_list")).toBe(2);
		expect(h.text("t1")).toBe("abcdefghij");
	});

	it("未知终端的输出触发同步", async () => {
		const h = setup();
		h.hub.setVisible(TID, true);
		h.omp.terminals = [info("t1")];
		h.omp.snapshots.set("t1", { data: "x", seq: 1 });
		await vi.advanceTimersByTimeAsync(0);
		h.omp.terminals = [info("t1"), info("t2")];
		h.omp.snapshots.set("t2", { data: "hello", seq: 5 });
		h.hub.onFrame(TID, out("t2", 5, "hello"));
		await vi.advanceTimersByTimeAsync(1100);
		expect(h.state().order).toEqual(["t1", "t2"]);
		expect(h.text("t2")).toBe("hello");
	});

	it("agent 重启后列表里没有的 live 终端标 lost 并保留画面，随后自动新开 shell", async () => {
		const h = setup();
		h.omp.terminals = [info("t1")];
		h.omp.snapshots.set("t1", { data: "old screen", seq: 10 });
		h.hub.setVisible(TID, true);
		await vi.advanceTimersByTimeAsync(0);
		h.omp.terminals = [];
		h.hub.onLiveReset(TID);
		await vi.advanceTimersByTimeAsync(1100);
		const st = h.state();
		expect(st.terminals.t1?.status).toBe("lost");
		expect(h.text("t1")).toBe("old screen");
		expect(h.omp.count("terminal_open")).toBe(1);
		expect(st.order).toEqual(["t1", "term-new-1"]);
		expect(st.activeId).toBe("term-new-1");
		// 手动关闭 lost 终端只在本地移除，不发命令
		const before = h.omp.calls.length;
		await h.hub.close(TID, "t1");
		expect(h.omp.calls.length).toBe(before);
		expect(h.state().terminals.t1).toBeUndefined();
	});

	it("用户关掉最后一个 shell 后不再自动新开", async () => {
		const h = setup();
		h.omp.terminals = [info("t1")];
		h.hub.setVisible(TID, true);
		await vi.advanceTimersByTimeAsync(0);
		await h.hub.close(TID, "t1");
		expect(h.omp.count("terminal_close")).toBe(1);
		h.omp.terminals = [];
		h.hub.onLiveReset(TID);
		await vi.advanceTimersByTimeAsync(1100);
		expect(h.omp.count("terminal_open")).toBe(0);
	});

	it("前提不满足时不发任何 cmd，只显示遮罩；就绪后自动同步", async () => {
		const h = setup({ ready: { ready: false } });
		h.hub.setVisible(TID, true);
		h.hub.onSnapshot(TID);
		h.hub.input(TID, "t1", "ls\r");
		await vi.advanceTimersByTimeAsync(2000);
		expect(h.omp.calls).toEqual([]);
		expect(h.watch).toEqual([]);
		expect(h.state().offline).toBe(true);
		h.omp.terminals = [info("t1")];
		h.setReady({ ready: true });
		await vi.advanceTimersByTimeAsync(1100);
		expect(h.state().offline).toBe(false);
		expect(h.omp.count("terminal_snapshot")).toBe(1);
	});

	it("快照未到（状态未知）时不显示遮罩", async () => {
		const h = setup({ ready: { ready: false, pending: true } });
		h.hub.setVisible(TID, true);
		await vi.advanceTimersByTimeAsync(0);
		expect(h.state().offline).toBe(false);
		expect(h.omp.calls).toEqual([]);
	});

	it("标签不可见：关闭 term.watch，且不做完整同步", async () => {
		const h = setup();
		h.omp.terminals = [info("t1")];
		h.hub.onSnapshot(TID);
		await vi.advanceTimersByTimeAsync(0);
		// 不可见时只刷新元数据
		expect(h.omp.count("terminal_list")).toBe(1);
		expect(h.omp.count("terminal_snapshot")).toBe(0);
		h.hub.setVisible(TID, true);
		await vi.advanceTimersByTimeAsync(1100);
		h.hub.setVisible(TID, false);
		expect(h.watch).toEqual([true, false]);
	});
});

describe("TerminalHub 帧与尺寸", () => {
	it("reveal 的 terminal_opened 打开面板并激活；pty 命令带注意点", async () => {
		const h = setup();
		h.hub.onFrame(TID, {
			type: "terminal_opened",
			terminal: info("c1", { kind: "command", title: "read -p" }),
			reveal: true,
		});
		expect(h.reveal).toHaveBeenCalledTimes(1);
		expect(h.state().activeId).toBe("c1");
		expect(h.state().attention).toBe(true);
		h.hub.onFrame(TID, {
			type: "terminal_exit",
			terminalId: "c1",
			exitCode: 0,
			cancelled: false,
			timedOut: false,
		});
		expect(h.state().terminals.c1?.status).toBe("exited");
		expect(h.state().attention).toBe(false);
		h.hub.onFrame(TID, { type: "terminal_closed", terminalId: "c1" });
		expect(h.state().order).toEqual([]);
	});

	it("terminal_exit 通知订阅者", () => {
		const h = setup();
		const cb = vi.fn();
		const off = h.hub.onExit(TID, cb);
		h.hub.onFrame(TID, {
			type: "terminal_exit",
			terminalId: "x",
			cancelled: false,
			timedOut: false,
		});
		expect(cb).toHaveBeenCalledTimes(1);
		off();
		h.hub.onFrame(TID, {
			type: "terminal_exit",
			terminalId: "x",
			cancelled: false,
			timedOut: false,
		});
		expect(cb).toHaveBeenCalledTimes(1);
	});

	it("属主发 resize；别的窗口的 term.resized 让出属主并按其尺寸显示，自己的回显不受影响", async () => {
		const h = setup();
		h.omp.terminals = [info("t1")];
		h.hub.setVisible(TID, true);
		await vi.advanceTimersByTimeAsync(0);
		h.hub.claimSize(TID, "t1");
		expect(h.omp.calls.at(-1)).toEqual({
			type: "terminal_resize",
			terminalId: "t1",
			cols: 120,
			rows: 40,
		});
		const term = h.terms[0];
		if (!term) throw new Error("no term");
		expect([term.cols, term.rows]).toEqual([120, 40]);
		h.hub.onResized(TID, "t1", 120, 40); // 自己的回显
		expect(h.hub.session(TID, "t1")?.owner).toBe(true);
		h.hub.onResized(TID, "t1", 80, 24); // 另一窗口
		expect(h.hub.session(TID, "t1")?.owner).toBe(false);
		expect([term.cols, term.rows]).toEqual([80, 24]);
		// 非属主的容器变化不发 resize
		const n = h.omp.count("terminal_resize");
		h.hub.fitted(TID, "t1", { cols: 130, rows: 41 });
		expect(h.omp.count("terminal_resize")).toBe(n);
		expect([term.cols, term.rows]).toEqual([80, 24]);
	});

	it("同一轮事件循环的输入合并为一条，超过 64KiB 分片", async () => {
		const h = setup();
		h.omp.terminals = [info("t1")];
		h.hub.setVisible(TID, true);
		await vi.advanceTimersByTimeAsync(0);
		h.hub.input(TID, "t1", "l");
		h.hub.input(TID, "t1", "s\r");
		await Promise.resolve();
		const inputs = h.omp.calls.filter((c) => c.type === "terminal_input");
		expect(inputs).toEqual([
			{ type: "terminal_input", terminalId: "t1", data: "ls\r" },
		]);
		h.hub.input(TID, "t1", "y".repeat(70 * 1024));
		await Promise.resolve();
		const big = h.omp.calls
			.filter((c) => c.type === "terminal_input")
			.slice(1) as { data: string }[];
		expect(big.map((c) => c.data.length)).toEqual([64 * 1024, 6 * 1024]);
	});

	it("term.lagged 后延迟重同步", async () => {
		const h = setup();
		h.omp.terminals = [info("t1")];
		h.hub.setVisible(TID, true);
		await vi.advanceTimersByTimeAsync(1100);
		h.hub.onLagged(TID);
		await vi.advanceTimersByTimeAsync(400);
		expect(h.omp.count("terminal_list")).toBe(1);
		await vi.advanceTimersByTimeAsync(200);
		expect(h.omp.count("terminal_list")).toBe(2);
		expect(h.watch).toEqual([true, true]);
	});
});
