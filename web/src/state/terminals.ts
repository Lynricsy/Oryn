/**
 * 终端同步（计划 M3 2.5）：PTY 属于 orb 内的 omp 进程，控制面不保存终端状态；客户端靠
 * `terminal_list` + `terminal_snapshot` + `seq` 恢复，实时 `terminal_output` 只发给 `term.watch` 的连接。
 *
 * - 每线程一个 zustand store（元数据：终端表、顺序、当前终端、遮罩 / 同步状态），供 React 渲染。
 * - xterm 实例与 `lastSeq` 放在模块级 `TerminalSession`（不进 React 状态）；xterm 工厂由终端面板模块加载时注册，
 *   主包不含 xterm。线程页卸载（`detach`）时销毁。
 * - 任何 `cmd` 都以「agent 就绪且 orb 为 ready」为前提，否则会唤醒冻结的 orb。
 */
import { useStore } from "zustand";
import { createStore, type StoreApi } from "zustand/vanilla";
import type { Orb } from "../api/client";
import { applyOutput, type OutputChunk } from "../lib/terminal-seq";
import type {
	OmpCommand,
	ResponseData,
	RpcTerminalFrame,
	RpcTerminalInfo,
} from "../omp/types";
import { realtime } from "../realtime/socket";
import { wakeThread } from "./actions";
import { getThreadStore, indexStore, peekThreadStore } from "./stores";
import { uiStore } from "./ui";

/** xterm 的最小接口（测试里用替身）。 */
export interface TermLike {
	readonly cols: number;
	readonly rows: number;
	write(data: string): void;
	reset(): void;
	resize(cols: number, rows: number): void;
	dispose(): void;
	/** 缓冲区全文（含回滚；软换行的行拼回一行）。 */
	text(): string;
	/** 把终端挂进容器（可重复调用以换容器）。 */
	mount?(el: HTMLElement): void;
	/** 按容器计算的建议尺寸（未挂载时为 undefined）。 */
	propose?(): { cols: number; rows: number } | undefined;
	focus?(): void;
	/** 粘贴（走 xterm 的括号粘贴，经 onInput 发出）。 */
	paste?(text: string): void;
}

export interface TermHandlers {
	onInput(data: string): void;
	onFocus(): void;
}

export type TermFactory = (handlers: TermHandlers) => TermLike;

export interface Readiness {
	/** agent 就绪且 orb 为 ready：可以发 `cmd` 而不唤醒。 */
	ready: boolean;
	/** 尚未收到线程快照：状态未知，不显示遮罩（快照到达后会再同步）。 */
	pending?: boolean;
	orb?: Orb;
}

export interface TerminalHubDeps {
	cmd<T extends OmpCommand>(
		threadId: string,
		frame: T,
		timeoutMs?: number,
	): Promise<ResponseData<T["type"]>>;
	termWatch(threadId: string, on: boolean): void;
	wake(threadId: string): Promise<void>;
	readiness(threadId: string): Readiness;
	documentVisible(): boolean;
	/** 打开右侧面板并切到 Terminal 标签。 */
	reveal(): void;
	/** 订阅「就绪前提可能变化」（线程 / orb 更新）；返回取消函数。 */
	watchReadiness(threadId: string, cb: () => void): () => void;
	now(): number;
}

export type TerminalStatus = "live" | "exited" | "lost";

export interface TerminalEntry {
	info: RpcTerminalInfo;
	status: TerminalStatus;
	/** agent 的 `pty:true` 命令或会话在等用户输入。 */
	attention: boolean;
}

/** `__orynDebug.terminals()` 的一行。 */
export interface TerminalDebugInfo extends RpcTerminalInfo {
	status: TerminalStatus;
	lastSeq: number;
	/** xterm 实际尺寸（可能与 omp 的不同：非属主窗口按属主尺寸显示）。 */
	termCols?: number;
	termRows?: number;
}

export interface ThreadTerminalsState {
	terminals: Record<string, TerminalEntry>;
	order: string[];
	activeId: string | null;
	/** Terminal 标签可见但前提不满足（orb 未运行 / agent 未就绪）。 */
	offline: boolean;
	/** 正在 list + snapshot。 */
	syncing: boolean;
	/** 最近一次同步失败的原因。 */
	error: string | null;
	/** 任一终端需要注意（Terminal 标签上的注意点）。 */
	attention: boolean;
}

const DEFAULT_COLS = 100;
const DEFAULT_ROWS = 30;
const SYNC_MIN_INTERVAL_MS = 1000;
const LAGGED_DELAY_MS = 500;
const INPUT_CHUNK = 64 * 1024;
/** 同步期间缓冲的输出上限（字符）；溢出则同步后再来一次。 */
const SYNC_BUFFER_LIMIT = 4 << 20;
const SYNC_RETRY_MS = 3000;
const SYNC_MAX_RETRIES = 3;

function needsAttention(info: RpcTerminalInfo): boolean {
	return info.running && (info.kind === "command" || info.state === "input");
}

function emptyState(): ThreadTerminalsState {
	return {
		terminals: {},
		order: [],
		activeId: null,
		offline: false,
		syncing: false,
		error: null,
		attention: false,
	};
}

/** 模块级终端会话：xterm 与续接游标。 */
export class TerminalSession {
	readonly threadId: string;
	readonly terminalId: string;
	term: TermLike | null = null;
	lastSeq = 0;
	syncing = false;
	buffer: OutputChunk[] = [];
	bufferChars = 0;
	overflow = false;
	/** 本窗口是否为尺寸属主（最近操作过的窗口）。 */
	owner = false;
	/** 最近一次发出（或作为非属主时采纳）的尺寸。 */
	lastSize: { cols: number; rows: number } | null = null;
	/** 视图注册：同步完成后按容器重新 fit（属主时会发 resize）。 */
	refit: (() => void) | null = null;
	inputQueue = "";
	inputScheduled = false;
	/** 移动端按键条的粘滞 Ctrl：改写下一次键盘输入。 */
	transform: ((data: string) => string) | null = null;
	constructor(threadId: string, terminalId: string) {
		this.threadId = threadId;
		this.terminalId = terminalId;
	}
}

interface ThreadRecord {
	store: StoreApi<ThreadTerminalsState>;
	sessions: Map<string, TerminalSession>;
	visible: boolean;
	attached: number;
	unwatchReadiness: (() => void) | null;
	wasReady: boolean;
	timer: ReturnType<typeof setTimeout> | null;
	running: boolean;
	again: boolean;
	lastStart: number;
	retries: number;
	/** 用户主动关掉了最后一个 shell：不再自动新开。 */
	userClosedShell: boolean;
	/** 已因注意状态自动展示过的终端（每个只展示一次）。 */
	revealed: Set<string>;
	/** 最近一次视图 fit 出的尺寸（新开 shell 时用）。 */
	fitSize: { cols: number; rows: number } | null;
	exitListeners: Set<() => void>;
	/** 每次 detach 递增：作废在途的异步同步。 */
	epoch: number;
}

export class TerminalHub {
	private readonly deps: TerminalHubDeps;
	private readonly threads = new Map<string, ThreadRecord>();
	private factory: TermFactory | null = null;

	constructor(deps: TerminalHubDeps) {
		this.deps = deps;
	}

	/** 终端面板模块加载时注册 xterm 工厂。 */
	setTermFactory(factory: TermFactory) {
		this.factory = factory;
	}

	private rec(threadId: string): ThreadRecord {
		let r = this.threads.get(threadId);
		if (!r) {
			r = {
				store: createStore<ThreadTerminalsState>(() => emptyState()),
				sessions: new Map(),
				visible: false,
				attached: 0,
				unwatchReadiness: null,
				wasReady: false,
				timer: null,
				running: false,
				again: false,
				lastStart: Number.NEGATIVE_INFINITY,
				retries: 0,
				userClosedShell: false,
				revealed: new Set(),
				fitSize: null,
				exitListeners: new Set(),
				epoch: 0,
			};
			this.threads.set(threadId, r);
		}
		return r;
	}

	store(threadId: string): StoreApi<ThreadTerminalsState> {
		return this.rec(threadId).store;
	}

	session(threadId: string, terminalId: string): TerminalSession | undefined {
		return this.threads.get(threadId)?.sessions.get(terminalId);
	}

	/** 视图挂载时取会话（必要时补建 xterm）。 */
	ensure(threadId: string, terminalId: string): TerminalSession | undefined {
		const r = this.threads.get(threadId);
		if (!r?.store.getState().terminals[terminalId]) return undefined;
		return this.ensureSession(r, threadId, terminalId);
	}

	private ensureSession(r: ThreadRecord, threadId: string, id: string) {
		let s = r.sessions.get(id);
		if (!s) {
			s = new TerminalSession(threadId, id);
			r.sessions.set(id, s);
		}
		if (!s.term && this.factory) {
			s.term = this.factory({
				onInput: (data) => this.input(threadId, id, data),
				onFocus: () => this.claimSize(threadId, id),
			});
		}
		return s;
	}

	// ------------------------------------------------------------ 生命周期

	/** 线程页挂载：开始跟踪就绪前提。返回 detach。 */
	attach(threadId: string): () => void {
		const r = this.rec(threadId);
		r.attached++;
		if (!r.unwatchReadiness) {
			r.wasReady = this.deps.readiness(threadId).ready;
			r.unwatchReadiness = this.deps.watchReadiness(threadId, () =>
				this.onReadinessChange(threadId),
			);
		}
		return () => this.detach(threadId);
	}

	private detach(threadId: string) {
		const r = this.threads.get(threadId);
		if (!r) return;
		r.attached--;
		if (r.attached > 0) return;
		r.unwatchReadiness?.();
		clearTimeout(r.timer ?? undefined);
		if (r.visible) this.deps.termWatch(threadId, false);
		for (const s of r.sessions.values()) s.term?.dispose();
		r.epoch++;
		this.threads.delete(threadId);
	}

	/** Terminal 标签可见性。可见 → 同步；不可见 → 关掉 `term.watch`。 */
	setVisible(threadId: string, visible: boolean) {
		// 面板（子组件）的挂载早于线程页的 attach，卸载晚于 detach：隐藏时不为已 detach 的线程重建记录
		const r = visible ? this.rec(threadId) : this.threads.get(threadId);
		if (!r || r.visible === visible) return;
		r.visible = visible;
		if (visible) {
			r.retries = 0;
			this.requestSync(threadId);
		} else {
			clearTimeout(r.timer ?? undefined);
			r.timer = null;
			r.store.setState({ offline: false });
			this.deps.termWatch(threadId, false);
		}
	}

	private onReadinessChange(threadId: string) {
		const r = this.threads.get(threadId);
		if (!r) return;
		const ready = this.deps.readiness(threadId).ready;
		if (ready === r.wasReady) return;
		r.wasReady = ready;
		if (ready) {
			r.store.setState({ offline: false });
			void this.refreshList(threadId);
			if (r.visible) this.requestSync(threadId);
		} else if (r.visible && !this.deps.readiness(threadId).pending)
			r.store.setState({ offline: true });
	}

	/** `thread.snapshot`（含重连后）：断档期间可能漏帧。 */
	onSnapshot(threadId: string) {
		const r = this.threads.get(threadId);
		if (!r) return;
		r.wasReady = this.deps.readiness(threadId).ready;
		if (r.visible) this.requestSync(threadId);
		else void this.refreshList(threadId);
	}

	/** `live.reset`：agent 断档或重启，终端可能已全部销毁。 */
	onLiveReset(threadId: string) {
		this.onSnapshot(threadId);
	}

	/** `term.lagged`：服务端丢了终端输出并关了开关。 */
	onLagged(threadId: string) {
		const r = this.threads.get(threadId);
		if (!r?.visible) return;
		for (const s of r.sessions.values()) s.syncing = true;
		this.requestSync(threadId, LAGGED_DELAY_MS);
	}

	/** 订阅该线程的 `terminal_exit`。 */
	onExit(threadId: string, cb: () => void): () => void {
		const r = this.rec(threadId);
		r.exitListeners.add(cb);
		return () => {
			this.threads.get(threadId)?.exitListeners.delete(cb);
		};
	}

	// ------------------------------------------------------------ 帧

	onFrame(threadId: string, frame: RpcTerminalFrame) {
		const r = this.threads.get(threadId);
		if (!r) return;
		switch (frame.type) {
			case "terminal_output": {
				const known = r.store.getState().terminals[frame.terminalId];
				if (!known) {
					this.requestSync(threadId);
					return;
				}
				const s = this.ensureSession(r, threadId, frame.terminalId);
				this.applyChunk(r, threadId, s, frame);
				return;
			}
			case "terminal_opened": {
				this.upsertInfo(r, threadId, frame.terminal);
				if (frame.reveal && this.deps.documentVisible()) {
					r.revealed.add(frame.terminal.terminalId);
					r.store.setState({ activeId: frame.terminal.terminalId });
					this.deps.reveal();
				} else this.maybeReveal(r);
				return;
			}
			case "terminal_exit": {
				const st = r.store.getState();
				const e = st.terminals[frame.terminalId];
				if (e) {
					const info = { ...e.info, running: false, exitCode: frame.exitCode };
					this.setEntries(r, {
						...st.terminals,
						[frame.terminalId]: {
							info,
							status: e.status === "lost" ? "lost" : "exited",
							attention: false,
						},
					});
				}
				for (const cb of [...r.exitListeners]) cb();
				return;
			}
			case "terminal_closed":
				this.removeTerminal(r, frame.terminalId);
				return;
		}
	}

	private applyChunk(
		r: ThreadRecord,
		threadId: string,
		s: TerminalSession,
		chunk: OutputChunk,
	) {
		if (s.syncing) {
			if (s.bufferChars + chunk.data.length > SYNC_BUFFER_LIMIT) {
				s.overflow = true;
				return;
			}
			s.buffer.push({ seq: chunk.seq, data: chunk.data });
			s.bufferChars += chunk.data.length;
			return;
		}
		const res = applyOutput(s.lastSeq, chunk);
		if (res === "drop") return;
		if (res === "gap") {
			if (r.visible) {
				s.syncing = true;
				s.buffer = [{ seq: chunk.seq, data: chunk.data }];
				s.bufferChars = chunk.data.length;
				this.requestSync(threadId);
			}
			return;
		}
		s.term?.write(res.write);
		s.lastSeq = res.lastSeq;
	}

	// ------------------------------------------------------------ 元数据

	private setEntries(
		r: ThreadRecord,
		terminals: Record<string, TerminalEntry>,
	) {
		const st = r.store.getState();
		const order = st.order.filter((id) => terminals[id]);
		for (const id of Object.keys(terminals))
			if (!order.includes(id)) order.push(id);
		const activeId =
			st.activeId && terminals[st.activeId] ? st.activeId : (order[0] ?? null);
		r.store.setState({
			terminals,
			order,
			activeId,
			attention: order.some((id) => terminals[id]?.attention),
		});
	}

	private upsertInfo(r: ThreadRecord, threadId: string, info: RpcTerminalInfo) {
		const st = r.store.getState();
		const prev = st.terminals[info.terminalId];
		this.ensureSession(r, threadId, info.terminalId);
		this.setEntries(r, {
			...st.terminals,
			[info.terminalId]: {
				info,
				status: info.running ? "live" : "exited",
				attention: needsAttention(info),
			},
		});
		if (!prev && info.kind === "shell" && info.running)
			r.userClosedShell = false;
	}

	private removeTerminal(r: ThreadRecord, id: string) {
		const st = r.store.getState();
		if (!st.terminals[id]) return;
		const terminals = { ...st.terminals };
		delete terminals[id];
		r.sessions.get(id)?.term?.dispose();
		r.sessions.delete(id);
		this.setEntries(r, terminals);
	}

	/** 合并 `terminal_list`：本地 live 而列表没有 → lost（保留画面）；本地 exited 而列表没有 → 移除。 */
	private mergeList(
		r: ThreadRecord,
		threadId: string,
		list: RpcTerminalInfo[],
	) {
		const st = r.store.getState();
		const terminals: Record<string, TerminalEntry> = {};
		const listed = new Set(list.map((i) => i.terminalId));
		for (const [id, e] of Object.entries(st.terminals)) {
			if (listed.has(id)) continue;
			if (e.status === "exited") {
				r.sessions.get(id)?.term?.dispose();
				r.sessions.delete(id);
				continue;
			}
			terminals[id] = {
				info: { ...e.info, running: false },
				status: "lost",
				attention: false,
			};
		}
		for (const info of list) {
			this.ensureSession(r, threadId, info.terminalId);
			terminals[info.terminalId] = {
				info,
				status: info.running ? "live" : "exited",
				attention: needsAttention(info),
			};
		}
		this.setEntries(r, terminals);
		this.maybeReveal(r);
	}

	/** 有需要注意的终端且文档可见：自动打开面板并切到它（每个终端只做一次）。 */
	private maybeReveal(r: ThreadRecord) {
		if (!this.deps.documentVisible()) return;
		const st = r.store.getState();
		const id = st.order.find(
			(x) => st.terminals[x]?.attention && !r.revealed.has(x),
		);
		if (!id) return;
		r.revealed.add(id);
		r.store.setState({ activeId: id });
		this.deps.reveal();
	}

	/** 只刷新元数据（`terminal_list`）；前提不满足时不发。 */
	async refreshList(threadId: string) {
		const r = this.threads.get(threadId);
		if (!r || !this.deps.readiness(threadId).ready) return;
		const epoch = r.epoch;
		try {
			const data = await this.deps.cmd(threadId, { type: "terminal_list" });
			if (this.threads.get(threadId) !== r || r.epoch !== epoch) return;
			this.mergeList(r, threadId, data.terminals);
		} catch {
			// agent 不可用：下次就绪或快照时再刷
		}
	}

	// ------------------------------------------------------------ 同步

	/** 请求一次完整同步；同线程串行，两次开始间隔 ≥1s。 */
	requestSync(threadId: string, delayMs = 0) {
		const r = this.threads.get(threadId);
		if (!r?.visible) return;
		if (r.running) {
			r.again = true;
			return;
		}
		if (r.timer) return;
		const wait = Math.max(
			delayMs,
			r.lastStart + SYNC_MIN_INTERVAL_MS - this.deps.now(),
		);
		r.timer = setTimeout(
			() => {
				r.timer = null;
				void this.runSync(threadId);
			},
			Math.max(0, wait),
		);
	}

	private async runSync(threadId: string) {
		const r = this.threads.get(threadId);
		if (!r?.visible) return;
		const epoch = r.epoch;
		const stale = () => this.threads.get(threadId) !== r || r.epoch !== epoch;
		r.running = true;
		r.lastStart = this.deps.now();
		let failed = false;
		try {
			const rd = this.deps.readiness(threadId);
			if (!rd.ready) {
				r.store.setState({ offline: !rd.pending });
				return;
			}
			r.store.setState({ offline: false, syncing: true, error: null });
			for (const s of r.sessions.values()) {
				s.syncing = true;
				s.buffer = [];
				s.bufferChars = 0;
				s.overflow = false;
			}
			this.deps.termWatch(threadId, true);
			const { terminals } = await this.deps.cmd(threadId, {
				type: "terminal_list",
			});
			if (stale()) return;
			this.mergeList(r, threadId, terminals);
			for (const info of terminals) {
				const s = this.ensureSession(r, threadId, info.terminalId);
				s.syncing = true;
			}
			await Promise.all(
				terminals.map((info) => this.syncOne(r, threadId, info.terminalId)),
			);
			if (stale()) return;
			const hasShell = terminals.some((i) => i.kind === "shell" && i.running);
			if (!hasShell && !r.userClosedShell) await this.openShell(threadId);
			r.retries = 0;
		} catch (err) {
			failed = true;
			if (!stale())
				r.store.setState({
					error: err instanceof Error ? err.message : String(err),
				});
		} finally {
			for (const s of r.sessions.values()) {
				if (!s.syncing) continue;
				// 同步失败或终端已不在列表：放弃缓冲，按当前游标继续
				s.syncing = false;
				s.buffer = [];
				s.bufferChars = 0;
			}
			r.running = false;
			if (!stale()) {
				r.store.setState({ syncing: false });
				if (r.again) {
					r.again = false;
					this.requestSync(threadId);
				} else if (failed && r.retries < SYNC_MAX_RETRIES) {
					r.retries++;
					this.requestSync(threadId, SYNC_RETRY_MS);
				}
			}
		}
	}

	private async syncOne(r: ThreadRecord, threadId: string, id: string) {
		const s = this.ensureSession(r, threadId, id);
		let snap: ResponseData<"terminal_snapshot">;
		try {
			snap = await this.deps.cmd(threadId, {
				type: "terminal_snapshot",
				terminalId: id,
			});
		} catch {
			// 终端在 list 与 snapshot 之间消失：等 terminal_closed
			s.syncing = false;
			s.buffer = [];
			s.bufferChars = 0;
			return;
		}
		if (r.sessions.get(id) !== s) return;
		const term = s.term;
		term?.reset();
		term?.resize(snap.terminal.cols, snap.terminal.rows);
		if (!s.owner)
			s.lastSize = { cols: snap.terminal.cols, rows: snap.terminal.rows };
		if (snap.data) term?.write(snap.data);
		s.lastSeq = snap.seq;
		const st = r.store.getState();
		const prev = st.terminals[id];
		if (prev)
			this.setEntries(r, {
				...st.terminals,
				[id]: {
					...prev,
					info: snap.terminal,
					attention: needsAttention(snap.terminal),
				},
			});
		const buffered = s.buffer;
		const overflow = s.overflow;
		s.buffer = [];
		s.bufferChars = 0;
		s.overflow = false;
		s.syncing = false;
		let gap = overflow;
		for (const chunk of buffered) {
			const res = applyOutput(s.lastSeq, chunk);
			if (res === "drop") continue;
			if (res === "gap") {
				gap = true;
				break;
			}
			term?.write(res.write);
			s.lastSeq = res.lastSeq;
		}
		if (gap) {
			s.syncing = true;
			r.again = true;
		}
		s.refit?.();
	}

	private async openShell(threadId: string) {
		const r = this.threads.get(threadId);
		if (!r) return;
		const size = r.fitSize ?? { cols: DEFAULT_COLS, rows: DEFAULT_ROWS };
		const info = await this.deps.cmd(threadId, {
			type: "terminal_open",
			cols: size.cols,
			rows: size.rows,
		});
		if (this.threads.get(threadId) !== r) return;
		const fresh = !r.store.getState().terminals[info.terminalId];
		this.upsertInfo(r, threadId, info);
		const s = this.ensureSession(r, threadId, info.terminalId);
		if (fresh) s.term?.resize(info.cols, info.rows);
		r.store.setState({ activeId: info.terminalId });
	}

	// ------------------------------------------------------------ 用户操作

	/** 用户点 `+`：新开 shell。 */
	async newShell(threadId: string) {
		const r = this.threads.get(threadId);
		if (!r || !this.deps.readiness(threadId).ready) return;
		r.userClosedShell = false;
		try {
			await this.openShell(threadId);
		} catch (err) {
			r.store.setState({
				error: err instanceof Error ? err.message : String(err),
			});
		}
	}

	/** 用户关闭终端：live / exited 发 `terminal_close`；lost 只在本地移除。 */
	async close(threadId: string, terminalId: string) {
		const r = this.threads.get(threadId);
		if (!r) return;
		const st = r.store.getState();
		const e = st.terminals[terminalId];
		if (!e) return;
		const shellsLeft = st.order.filter(
			(id) =>
				id !== terminalId &&
				st.terminals[id]?.info.kind === "shell" &&
				st.terminals[id]?.status === "live",
		);
		if (e.info.kind === "shell" && shellsLeft.length === 0)
			r.userClosedShell = true;
		if (e.status === "lost" || !this.deps.readiness(threadId).ready) {
			this.removeTerminal(r, terminalId);
			return;
		}
		try {
			await this.deps.cmd(threadId, { type: "terminal_close", terminalId });
		} catch {
			// 已不存在：本地移除
		}
		this.removeTerminal(r, terminalId);
	}

	/** 在任一已挂载线程里找到该终端并激活（工具卡片不知道线程 id）。 */
	activateAnywhere(terminalId: string) {
		for (const [threadId, r] of this.threads)
			if (r.store.getState().terminals[terminalId])
				this.activate(threadId, terminalId);
	}

	activate(threadId: string, terminalId: string) {
		const r = this.threads.get(threadId);
		if (!r?.store.getState().terminals[terminalId]) return;
		r.store.setState({ activeId: terminalId });
	}

	/** 唤醒 orb（遮罩上的按钮）。 */
	wake(threadId: string): Promise<void> {
		return this.deps.wake(threadId);
	}

	/** xterm 的输入：同一事件循环内合并，超过 64KiB 分片顺序发送，不等应答。 */
	input(threadId: string, terminalId: string, data: string) {
		const r = this.threads.get(threadId);
		const s = r?.sessions.get(terminalId);
		if (!r || !s || !data) return;
		const e = r.store.getState().terminals[terminalId];
		if (e?.status !== "live") return;
		s.inputQueue += s.transform ? s.transform(data) : data;
		if (s.inputScheduled) return;
		s.inputScheduled = true;
		queueMicrotask(() => {
			s.inputScheduled = false;
			const all = s.inputQueue;
			s.inputQueue = "";
			if (!this.deps.readiness(threadId).ready) return;
			for (let i = 0; i < all.length; i += INPUT_CHUNK) {
				this.deps
					.cmd(threadId, {
						type: "terminal_input",
						terminalId,
						data: all.slice(i, i + INPUT_CHUNK),
					})
					.catch(() => {});
			}
		});
	}

	/** 视图 fit 出新尺寸：属主（或 `claim`）时本地应用并发 `terminal_resize`；非属主只记下供新开 shell 使用。 */
	fitted(
		threadId: string,
		terminalId: string,
		size: { cols: number; rows: number },
		claim = false,
	) {
		const r = this.threads.get(threadId);
		const s = r?.sessions.get(terminalId);
		if (!r || !s) return;
		r.fitSize = size;
		if (claim) s.owner = true;
		if (!s.owner) return;
		if (s.term && (s.term.cols !== size.cols || s.term.rows !== size.rows))
			s.term.resize(size.cols, size.rows);
		const e = r.store.getState().terminals[terminalId];
		if (e?.status !== "live") return;
		if (s.lastSize?.cols === size.cols && s.lastSize.rows === size.rows) return;
		if (!this.deps.readiness(threadId).ready) return;
		s.lastSize = size;
		this.deps
			.cmd(threadId, {
				type: "terminal_resize",
				terminalId,
				cols: size.cols,
				rows: size.rows,
			})
			.catch(() => {});
	}

	/** 终端获得焦点 / 标签激活：成为属主并按容器尺寸发 resize。 */
	claimSize(threadId: string, terminalId: string) {
		const s = this.session(threadId, terminalId);
		const size = s?.term?.propose?.();
		if (s && size) this.fitted(threadId, terminalId, size, true);
		else if (s) s.owner = true;
	}

	/** `term.resized`：别的窗口改了尺寸 → 放弃属主身份并按该尺寸显示。 */
	onResized(threadId: string, terminalId: string, cols: number, rows: number) {
		const r = this.threads.get(threadId);
		if (!r) return;
		const st = r.store.getState();
		const e = st.terminals[terminalId];
		if (e)
			this.setEntries(r, {
				...st.terminals,
				[terminalId]: { ...e, info: { ...e.info, cols, rows } },
			});
		const s = r.sessions.get(terminalId);
		if (!s) return;
		if (s.lastSize?.cols === cols && s.lastSize.rows === rows) return;
		s.owner = false;
		s.lastSize = { cols, rows };
		s.term?.resize(cols, rows);
	}

	// ------------------------------------------------------------ 调试

	terminalText(threadId: string, terminalId: string): string {
		return this.session(threadId, terminalId)?.term?.text() ?? "";
	}

	terminals(threadId: string): TerminalDebugInfo[] {
		const r = this.threads.get(threadId);
		if (!r) return [];
		const st = r.store.getState();
		return st.order.flatMap((id) => {
			const e = st.terminals[id];
			if (!e) return [];
			const s = r.sessions.get(id);
			return [
				{
					...e.info,
					status: e.status,
					lastSeq: s?.lastSeq ?? 0,
					termCols: s?.term?.cols,
					termRows: s?.term?.rows,
				},
			];
		});
	}
}

// ---------------------------------------------------------------- 默认依赖

/** 线程的 orb（线程 store 与索引里较新者，同 `useOrbOf`）。 */
function orbOf(threadId: string): Orb | undefined {
	const ts = peekThreadStore(threadId)?.getState();
	const orbId = ts?.thread?.orbId;
	const fromIndex = orbId ? indexStore.getState().orbs[orbId] : undefined;
	const fromThread = ts?.orb ?? undefined;
	if (fromIndex && fromThread)
		return fromIndex.stateChangedAt >= fromThread.stateChangedAt
			? fromIndex
			: fromThread;
	return fromIndex ?? fromThread;
}

/**
 * agent 就绪：快照说 ready；或快照时还在启动、之后线程离开了 provisioning（控制面只在 agent 首次就绪后才离开该状态）。
 */
export function defaultReadiness(threadId: string): Readiness {
	const ts = peekThreadStore(threadId)?.getState();
	const orb = orbOf(threadId);
	if (!ts?.snapshotReceived) return { ready: false, pending: true, orb };
	if (!orb || !ts.thread?.orbId) return { ready: false, orb };
	const st = ts.thread.state;
	const agentReady =
		ts.agentStatus === "ready" ||
		((ts.agentStatus === "starting" || ts.agentStatus === "none") &&
			st !== "provisioning" &&
			st !== "error");
	return { ready: agentReady && orb.state === "ready", orb };
}

function defaultWatchReadiness(threadId: string, cb: () => void): () => void {
	const a = getThreadStore(threadId).subscribe(cb);
	const b = indexStore.subscribe(cb);
	return () => {
		a();
		b();
	};
}

// socket.ts 也导入本模块（循环依赖）：realtime / wakeThread 只在调用时取用
export const terminalHub = new TerminalHub({
	cmd: (threadId, frame, timeoutMs) => realtime.cmd(threadId, frame, timeoutMs),
	termWatch: (threadId, on) => realtime.termWatch(threadId, on),
	wake: (threadId) => wakeThread(threadId),
	readiness: defaultReadiness,
	documentVisible: () =>
		typeof document === "undefined" || document.visibilityState === "visible",
	reveal: () => {
		const ui = uiStore.getState();
		ui.setPanel(true);
		ui.setPanelTab("terminal");
	},
	watchReadiness: defaultWatchReadiness,
	now: () => Date.now(),
});

export function useTerminals<T>(
	threadId: string,
	selector: (s: ThreadTerminalsState) => T,
): T {
	return useStore(terminalHub.store(threadId), selector);
}

/** 工具卡片「在终端面板查看」：打开面板并切到终端（给了 id 且该终端存在时激活它）。 */
export function revealTerminal(terminalId?: string) {
	if (terminalId) terminalHub.activateAnywhere(terminalId);
	const ui = uiStore.getState();
	ui.setPanel(true);
	ui.setPanelTab("terminal");
}

// 验收脚本读终端文本：localStorage["oryn.debug"]==="1" 时挂到 window
declare global {
	interface Window {
		__orynDebug?: {
			terminalText(threadId: string, terminalId: string): string;
			terminals(threadId: string): TerminalDebugInfo[];
		};
	}
}

try {
	if (
		typeof window !== "undefined" &&
		window.localStorage?.getItem("oryn.debug") === "1"
	) {
		window.__orynDebug = {
			...window.__orynDebug,
			terminalText: (threadId, terminalId) =>
				terminalHub.terminalText(threadId, terminalId),
			terminals: (threadId) => terminalHub.terminals(threadId),
		};
	}
} catch {
	// localStorage 不可用
}
