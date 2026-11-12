/**
 * 客户端 WebSocket（计划 C3）：票据 → `/api/v1/ws?ticket=`；断线指数退避重连（0.5s→15s），
 * 重连后重发 `focus` 与各线程 `sub{afterSeq}` 补拉；`cmd` 以客户端 id 关联 `res`；
 * 收到的线程消息按动画帧批量写入线程 store，避免流式时逐帧重渲染。
 */
import { ApiError, api, unwrap, type WsServerMessage } from "../api/client";
import type { OmpCommand, ResponseData, SessionState } from "../omp/types";
import {
	connectionStore,
	getThreadStore,
	indexStore,
	peekThreadStore,
	type ThreadMessage,
} from "../state/stores";
import {
	effectsOf,
	type FrameEffect,
	type OmpFrame,
} from "../state/thread-reducer";

export class OmpCommandError extends Error {
	readonly code: string;
	readonly command: string;
	constructor(command: string, code: string, message: string) {
		super(message || code);
		this.name = "OmpCommandError";
		this.code = code;
		this.command = command;
	}
}

/** 平台计时器句柄（浏览器为 number，Node 类型下为 Timeout）。 */
type Timer = ReturnType<typeof setTimeout>;

interface PendingCmd {
	command: string;
	resolve(data: unknown): void;
	reject(err: Error): void;
	timer: Timer;
}

const DEFAULT_TIMEOUT_MS = 120_000;
const PING_INTERVAL_MS = 25_000;

function backoffMs(attempt: number): number {
	return Math.min(15_000, 500 * 2 ** attempt);
}

export class RealtimeClient {
	private ws: WebSocket | null = null;
	private closedByUser = false;
	private reconnectTimer: Timer | null = null;
	private pingTimer: ReturnType<typeof setInterval> | undefined;
	private readonly subs = new Map<string, number>();
	private focused: string | null = null;
	private readonly pending = new Map<string, PendingCmd>();
	private seq = 0;
	private batch = new Map<string, ThreadMessage[]>();
	private flushScheduled = false;
	private readonly effectTimers = new Map<string, Timer>();
	/** open() 正在等待票据 */
	private opening = false;
	/** 每次 close() 递增；open() 在 await 之后据此判断自己是否已被作废 */
	private generation = 0;

	connect() {
		this.closedByUser = false;
		if (this.ws || this.reconnectTimer || this.opening) return;
		void this.open();
	}

	close() {
		this.closedByUser = true;
		// 作废仍在等票据的 open()，避免 close→connect 快速交替时建出两条连接
		this.generation++;
		this.opening = false;
		clearTimeout(this.reconnectTimer ?? undefined);
		this.reconnectTimer = null;
		this.ws?.close();
		this.ws = null;
		connectionStore.setState({ status: "idle" });
	}

	private async open() {
		const generation = this.generation;
		this.opening = true;
		const attempts = connectionStore.getState().attempts;
		connectionStore.setState({
			status: attempts > 0 ? "reconnecting" : "connecting",
		});
		let ticket: string;
		try {
			const res = await unwrap(api.POST("/api/v1/ws-ticket"));
			ticket = res.ticket;
		} catch (err) {
			if (generation !== this.generation) return;
			this.opening = false;
			if (err instanceof ApiError && err.status === 401) {
				connectionStore.setState({ status: "unauthorized" });
				return;
			}
			this.scheduleReconnect(String(err));
			return;
		}
		if (this.closedByUser || generation !== this.generation) return;
		this.opening = false;
		const proto = location.protocol === "https:" ? "wss:" : "ws:";
		const ws = new WebSocket(
			`${proto}//${location.host}/api/v1/ws?ticket=${encodeURIComponent(ticket)}`,
		);
		this.ws = ws;
		ws.onopen = () => {
			if (this.ws !== ws) return;
			connectionStore.setState({
				status: "open",
				attempts: 0,
				lastError: undefined,
			});
			this.send({ type: "focus", threadId: this.focused });
			for (const [threadId] of this.subs) this.sendSub(threadId);
			this.pingTimer = setInterval(
				() => this.send({ type: "ping" }),
				PING_INTERVAL_MS,
			);
		};
		ws.onmessage = (ev) => {
			if (this.ws !== ws || typeof ev.data !== "string") return;
			let msg: WsServerMessage;
			try {
				msg = JSON.parse(ev.data) as WsServerMessage;
			} catch {
				return;
			}
			this.handle(msg);
		};
		ws.onclose = () => {
			if (this.ws !== ws) return;
			this.ws = null;
			clearInterval(this.pingTimer);
			this.pingTimer = undefined;
			for (const [id, p] of this.pending) {
				clearTimeout(p.timer);
				p.reject(
					new OmpCommandError(p.command, "disconnected", "connection closed"),
				);
				this.pending.delete(id);
			}
			if (!this.closedByUser) this.scheduleReconnect("closed");
		};
	}

	private scheduleReconnect(reason: string) {
		const attempts = connectionStore.getState().attempts;
		connectionStore.setState({
			status: "reconnecting",
			attempts: attempts + 1,
			lastError: reason,
		});
		this.reconnectTimer = setTimeout(() => {
			this.reconnectTimer = null;
			void this.open();
		}, backoffMs(attempts));
	}

	/** 立即重连（用户点「重试」）。 */
	retryNow() {
		clearTimeout(this.reconnectTimer ?? undefined);
		this.reconnectTimer = null;
		connectionStore.setState({ attempts: 0 });
		if (!this.ws) void this.open();
	}

	private send(msg: object): boolean {
		if (this.ws?.readyState !== WebSocket.OPEN) return false;
		this.ws.send(JSON.stringify(msg));
		return true;
	}

	private sendSub(threadId: string) {
		const afterSeq = peekThreadStore(threadId)?.getState().lastSeq ?? 0;
		this.send({ type: "sub", threadId, afterSeq });
	}

	/** 订阅线程（引用计数）；连接恢复后自动以 afterSeq 补拉。 */
	subscribe(threadId: string): () => void {
		const n = this.subs.get(threadId) ?? 0;
		this.subs.set(threadId, n + 1);
		if (n === 0) this.sendSub(threadId);
		return () => {
			const left = (this.subs.get(threadId) ?? 1) - 1;
			if (left > 0) this.subs.set(threadId, left);
			else {
				this.subs.delete(threadId);
				this.send({ type: "unsub", threadId });
			}
		};
	}

	focus(threadId: string | null) {
		this.focused = threadId;
		this.send({ type: "focus", threadId });
	}

	/** 发送 omp 命令；成功返回应答 `data`，失败抛 OmpCommandError。 */
	cmd<T extends OmpCommand>(
		threadId: string,
		frame: T,
		timeoutMs = DEFAULT_TIMEOUT_MS,
	): Promise<ResponseData<T["type"]>> {
		const id = `c${++this.seq}`;
		// 不用 Promise.withResolvers：macOS 13 / 旧 iPadOS 的 WebView 不支持
		return new Promise((resolve, reject) => {
			if (!this.send({ type: "cmd", threadId, id, frame })) {
				reject(
					new OmpCommandError(frame.type, "disconnected", "not connected"),
				);
				return;
			}
			const timer = setTimeout(() => {
				this.pending.delete(id);
				reject(new OmpCommandError(frame.type, "timeout", "command timed out"));
			}, timeoutMs);
			// 应答 data 的形状由 omp 的命令表决定，按命令类型信任之
			const settle = resolve as (data: unknown) => void;
			this.pending.set(id, {
				command: frame.type,
				resolve: settle,
				reject,
				timer,
			});
		});
	}

	/** `extension_ui_response` 等侧通道应答（先到先得）。 */
	uiResponse(threadId: string, frame: object): boolean {
		return this.send({ type: "ui.response", threadId, frame });
	}

	private handle(msg: WsServerMessage) {
		switch (msg.type) {
			case "index":
				indexStore.getState().setAll(msg.threads, msg.rev);
				return;
			case "index.patch":
				indexStore.getState().patch(msg.upsert, msg.remove, msg.rev);
				for (const t of msg.upsert) {
					if (peekThreadStore(t.id))
						this.enqueue(t.id, { type: "thread", thread: t });
				}
				return;
			case "thread.snapshot":
				indexStore.getState().upsertThread(msg.thread);
				if (msg.orb) indexStore.getState().upsertOrb(msg.orb);
				this.enqueue(msg.threadId, { type: "thread.snapshot", snapshot: msg });
				if (!msg.agent.state && msg.agent.status === "ready")
					this.scheduleEffect(msg.threadId, "refresh_state");
				if (msg.agent.status === "ready")
					this.scheduleEffect(msg.threadId, "refresh_commands");
				return;
			case "entries":
				this.enqueue(msg.threadId, { type: "entries", msg });
				return;
			case "frame": {
				const frame = msg.frame as OmpFrame;
				this.enqueue(msg.threadId, { type: "frame", frame });
				for (const eff of effectsOf(frame))
					this.scheduleEffect(msg.threadId, eff);
				return;
			}
			case "res": {
				const p = this.pending.get(msg.id);
				if (!p) return;
				this.pending.delete(msg.id);
				clearTimeout(p.timer);
				const r = msg.response;
				if (r.success === false) {
					const code = typeof r.code === "string" ? r.code : "command_failed";
					p.reject(
						new OmpCommandError(
							p.command,
							code,
							typeof r.error === "string" ? r.error : "",
						),
					);
				} else p.resolve(r.data);
				return;
			}
			case "ui.resolved":
				this.enqueue(msg.threadId, {
					type: "ui.resolved",
					requestId: msg.requestId,
				});
				return;
			case "live.reset":
				this.enqueue(msg.threadId, { type: "live.reset" });
				return;
			case "orb":
				indexStore.getState().upsertOrb(msg.orb);
				for (const tid of msg.orb.threadIds) {
					if (peekThreadStore(tid))
						this.enqueue(tid, { type: "orb", orb: msg.orb });
				}
				return;
			case "error": {
				if (msg.id) {
					const p = this.pending.get(msg.id);
					if (p) {
						this.pending.delete(msg.id);
						clearTimeout(p.timer);
						p.reject(new OmpCommandError(p.command, msg.code, msg.message));
						return;
					}
				}
				const errors = connectionStore.getState().errors;
				connectionStore.setState({
					errors: [
						...errors.slice(-9),
						{
							key: Date.now() + Math.random(),
							code: msg.code,
							message: msg.message,
							threadId: msg.threadId,
						},
					],
				});
				return;
			}
			case "pong":
				return;
		}
	}

	private enqueue(threadId: string, m: ThreadMessage) {
		let list = this.batch.get(threadId);
		if (!list) {
			list = [];
			this.batch.set(threadId, list);
		}
		list.push(m);
		if (this.flushScheduled) return;
		this.flushScheduled = true;
		const run = () => this.flush();
		if (
			typeof requestAnimationFrame === "function" &&
			typeof document !== "undefined" &&
			!document.hidden
		) {
			requestAnimationFrame(run);
		} else setTimeout(run, 16);
	}

	/** 立即把排队的消息写入 store（测试与可见性切换时使用）。 */
	flush() {
		this.flushScheduled = false;
		const batch = this.batch;
		this.batch = new Map();
		for (const [threadId, messages] of batch)
			getThreadStore(threadId).getState().apply(messages);
	}

	private scheduleEffect(threadId: string, effect: FrameEffect) {
		const key = `${threadId}:${effect}`;
		if (this.effectTimers.has(key)) return;
		this.effectTimers.set(
			key,
			setTimeout(() => {
				this.effectTimers.delete(key);
				void this.runEffect(threadId, effect);
			}, 250),
		);
	}

	private async runEffect(threadId: string, effect: FrameEffect) {
		try {
			if (effect === "refresh_state") {
				const state = (await this.cmd(threadId, {
					type: "get_state",
				})) as SessionState;
				this.enqueue(threadId, { type: "state", state });
			} else if (effect === "refresh_commands") {
				const data = await this.cmd(threadId, {
					type: "get_available_commands",
				});
				this.enqueue(threadId, {
					type: "frame",
					frame: { type: "available_commands_update", commands: data.commands },
				});
			} else if (effect === "refresh_queue") {
				const data = await this.cmd(threadId, { type: "get_queue" });
				getThreadStore(threadId).getState().patchLocal({ queueItems: data });
			}
		} catch {
			// agent 不可用时忽略；下次事件会再触发
		}
	}
}

export const realtime = new RealtimeClient();
