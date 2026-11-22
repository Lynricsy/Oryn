/**
 * 客户端 WebSocket（计划 C3）：票据 → `/api/v1/ws?ticket=`；断线指数退避重连（0.5s→15s），
 * 重连后重发 `focus` 与各线程 `sub{afterSeq}` 补拉；`cmd` 以客户端 id 关联 `res`；
 * 收到的线程消息按动画帧批量写入线程 store，避免流式时逐帧重渲染。
 * 终端（M3）：四种 `terminal_*` 帧不进 reducer，直接交给 `terminalHub`；`term.watch` 开关按线程记住，重连后重发；
 * `term.lagged` / `term.resized` 转给 `terminalHub`。
 * focus（M5）：`focus()` 只记「想看的线程」，实际发出的随页面是否在前台变化（`host.isForeground()`：浏览器里 = 标签页可见，
 * 桌面壳里另要求窗口未隐藏 / 最小化，M6）——不在前台时发 `focus null`，否则后台页面会一直占着 focus，通知永远被抑制。
 * `notification` / `notifications.read` 交给通知 store。
 * `schedules.changed` 与每次连接建立都通知 `onSchedulesChanged` 的监听者（定时查询据此失效，断线期间错过的推送靠 open 时对账）。
 * orb 从挂起回到 `ready` 时重取 state 与命令列表：挂起期间的只读查询被控制面以 `agent_unavailable` 拒绝，不会自行重放。
 * 移动壳（M7 计划 4.9）：转入后台 30 秒后挂起连接（关掉 socket、取消重连，状态 `idle`），不让 WebView 在后台保活网络；
 * 回到前台立即重连（退避计数归零），重连后照常按 `afterSeq` 补齐。
 * Artifacts（M14）：`artifacts.changed` 与每次连接建立都通知 `onArtifactsChanged` 的监听者；`artifact.version` /
 * `artifact.activity` / `artifact.canvas` / `artifact.comments` 按 artifact id 分发给 `onArtifact` 的订阅者。`watchArtifact` 按引用计数登记观看，重连后重发 `artifact.watch`。
 */

import { ApiError, api, unwrap, type WsServerMessage } from "../api/client";
import { host } from "../lib/host";
import type {
	OmpCommand,
	ResponseData,
	RpcTerminalFrame,
	SessionState,
} from "../omp/types";
import { notificationsStore } from "../state/notifications";
import {
	acceptOrbPush,
	connectionStore,
	getThreadStore,
	indexStore,
	peekThreadStore,
	type ThreadMessage,
} from "../state/stores";
import { terminalHub } from "../state/terminals";
import {
	effectsOf,
	type FrameEffect,
	type OmpFrame,
	snapshotEffects,
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

/** 只发给观看者的 artifact 消息。 */
export type ArtifactEvent = Extract<
	WsServerMessage,
	{
		type:
			| "artifact.version"
			| "artifact.activity"
			| "artifact.canvas"
			| "artifact.comments";
	}
>;

/** 某个 artifact 的在场信息（观看者、各线程在读 / 改什么）。 */
export type ArtifactActivityEvent = Extract<
	WsServerMessage,
	{ type: "artifact.activity" }
>;

/** 页面能力的同意请求与结果（M14 步骤 7.3）。 */
export type ArtifactConsentEvent = Extract<
	WsServerMessage,
	{ type: "artifact.consent" }
>;

interface PendingCmd {
	command: string;
	resolve(data: unknown): void;
	reject(err: Error): void;
	timer: Timer;
}

const DEFAULT_TIMEOUT_MS = 120_000;

const TERMINAL_FRAMES: Record<string, true> = {
	terminal_opened: true,
	terminal_output: true,
	terminal_exit: true,
	terminal_closed: true,
};
const PING_INTERVAL_MS = 25_000;
/** 移动壳转入后台后多久挂起连接。 */
export const MOBILE_SUSPEND_MS = 30_000;

function backoffMs(attempt: number): number {
	return Math.min(15_000, 500 * 2 ** attempt);
}

export class RealtimeClient {
	private ws: WebSocket | null = null;
	private closedByUser = false;
	private reconnectTimer: Timer | null = null;
	private pingTimer: ReturnType<typeof setInterval> | undefined;
	/** 发出 ping 后还没收到任何消息（下一次心跳时仍为真 = 连接已死）。 */
	private awaitingPong = false;
	private networkBound = false;
	private readonly subs = new Map<string, number>();
	/** 带 `tail` 订阅的线程 → 窗口条数（M19）。 */
	private readonly tails = new Map<string, number>();
	/** 想看的线程（线程页挂载时设置）；实际发出的见 `effectiveFocus`。 */
	private focused: string | null = null;
	/** 最近一次发出的 focus（`undefined` = 本连接尚未发过）。 */
	private sentFocus: string | null | undefined = undefined;
	/** 取消前台变化监听（连接期间有效）。 */
	private offForeground: (() => void) | null = null;
	/** 已开启 `term.watch` 的线程。 */
	private readonly watching = new Set<string>();
	private readonly pending = new Map<string, PendingCmd>();
	private seq = 0;
	private batch = new Map<string, ThreadMessage[]>();
	private flushScheduled = false;
	private readonly effectTimers = new Map<string, Timer>();
	/** 因「store 还没有 state」而补拉、应答尚未写回的线程：回合中逐帧到达时不重复查询。 */
	private readonly stateBackfill = new Set<string>();
	/** open() 正在等待票据 */
	private opening = false;
	/** 每次 close() 递增；open() 在 await 之后据此判断自己是否已被作废 */
	private generation = 0;
	/** 移动壳后台挂起计时器与挂起标记（挂起 ≠ 用户关闭：前台监听仍在，回前台即重连）。 */
	private suspendTimer: Timer | null = null;
	private suspended = false;
	/** 定时变更的监听者（M9 审查 W6）：收到 `schedules.changed` 与每次连接建立时调用。 */
	private readonly scheduleListeners = new Set<() => void>();

	/**
	 * 订阅「定时可能变了」：服务端推送 `schedules.changed`，或连接（重新）建立——断线期间错过的推送无从补发，
	 * 只能整体对账一次。返回取消订阅。
	 */
	onSchedulesChanged(cb: () => void): () => void {
		this.scheduleListeners.add(cb);
		return () => {
			this.scheduleListeners.delete(cb);
		};
	}

	private emitSchedulesChanged() {
		for (const cb of [...this.scheduleListeners]) cb();
	}

	/** GitHub 集成变更的监听者（M14）：收到 `github.changed` 与每次连接建立时调用。 */
	private readonly githubListeners = new Set<() => void>();

	/** 订阅「GitHub 集成状态可能变了」（App、身份、安装、项目绑定、触发规则）；连接（重新）建立时也通知。返回取消订阅。 */
	onGitHubChanged(cb: () => void): () => void {
		this.githubListeners.add(cb);
		return () => {
			this.githubListeners.delete(cb);
		};
	}

	private emitGitHubChanged() {
		for (const cb of [...this.githubListeners]) cb();
	}

	/** SSH 主机与钥匙变更的监听者（M18）：收到 `ssh.changed` 与每次连接建立时调用。 */
	private readonly sshListeners = new Set<() => void>();

	/** 订阅「SSH 主机、钥匙、指纹或测试结果可能变了」；连接（重新）建立时也通知。返回取消订阅。 */
	onSshChanged(cb: () => void): () => void {
		this.sshListeners.add(cb);
		return () => {
			this.sshListeners.delete(cb);
		};
	}

	private emitSshChanged() {
		for (const cb of [...this.sshListeners]) cb();
	}

	/** Boxes 变更的监听者（M23）：收到 `boxes.changed` 与每次连接建立时调用。 */
	private readonly boxesListeners = new Set<() => void>();

	/** 订阅「box、app、部署、活动或用量可能变了」；连接（重新）建立时也通知。返回取消订阅。 */
	onBoxesChanged(cb: () => void): () => void {
		this.boxesListeners.add(cb);
		return () => {
			this.boxesListeners.delete(cb);
		};
	}

	private emitBoxesChanged() {
		for (const cb of [...this.boxesListeners]) cb();
	}

	/** 项目仓库镜像变更的监听者（M24）：收到 `project_repo.changed`（带项目 id）与每次连接建立时（null = 全部项目）调用。 */
	private readonly projectRepoListeners = new Set<
		(projectId: string | null) => void
	>();

	/** 订阅「某个项目的仓库镜像状态可能变了」；连接（重新）建立时以 null 通知（全部项目）。返回取消订阅。 */
	onProjectRepoChanged(cb: (projectId: string | null) => void): () => void {
		this.projectRepoListeners.add(cb);
		return () => {
			this.projectRepoListeners.delete(cb);
		};
	}

	private emitProjectRepoChanged(projectId: string | null) {
		for (const cb of [...this.projectRepoListeners]) cb(projectId);
	}

	/** artifact 列表变更的监听者（M14）：收到 `artifacts.changed` 与每次连接建立时调用。 */
	private readonly artifactsListeners = new Set<() => void>();
	/** 连接（重新）建立、artifact 观看已重发之后调用：断线期间错过的实时状态（画布等）靠它补回。 */
	private readonly watchesResumedListeners = new Set<() => void>();
	/** 按 artifact id 分发 `artifact.version` / `artifact.activity` / `artifact.canvas`。 */
	private readonly artifactListeners = new Map<
		string,
		Set<(msg: ArtifactEvent) => void>
	>();
	/** 正在观看的 artifact：引用计数与最近一次的画布页；重连后重发 `artifact.watch`。 */
	private readonly artifactWatches = new Map<
		string,
		{ count: number; page?: string }
	>();
	/** `artifact.consent` 的监听者（不按 artifact 分发：同意对话框挂在外壳上）。 */
	private readonly consentListeners = new Set<
		(msg: ArtifactConsentEvent) => void
	>();
	/** 观看集合变化（开始或停止观看某个 artifact）的监听者。 */
	private readonly watchListeners = new Set<() => void>();
	/** 每个正在观看的 artifact 最近一条 `artifact.activity`；停止观看时清掉。 */
	private readonly artifactActivity = new Map<string, ArtifactActivityEvent>();

	/** 正在观看的 artifact 最近一条在场信息；还没收到或没在观看时为 `undefined`。 */
	lastArtifactActivity(id: string): ArtifactActivityEvent | undefined {
		return this.artifactActivity.get(id);
	}

	/** 订阅 `artifact.consent`。返回取消订阅。 */
	onArtifactConsent(cb: (msg: ArtifactConsentEvent) => void): () => void {
		this.consentListeners.add(cb);
		return () => {
			this.consentListeners.delete(cb);
		};
	}

	/** 本客户端是否正在观看 artifact id。 */
	isWatchingArtifact(id: string): boolean {
		return this.artifactWatches.has(id);
	}

	/** 订阅观看集合的变化（开始或停止观看某个 artifact）。返回取消订阅。 */
	onArtifactWatchesChanged(cb: () => void): () => void {
		this.watchListeners.add(cb);
		return () => {
			this.watchListeners.delete(cb);
		};
	}

	private emitWatchesChanged() {
		for (const cb of [...this.watchListeners]) cb();
	}

	/** 订阅「artifact 列表可能变了」；连接（重新）建立时也通知。返回取消订阅。 */
	onArtifactsChanged(cb: () => void): () => void {
		this.artifactsListeners.add(cb);
		return () => {
			this.artifactsListeners.delete(cb);
		};
	}

	private emitArtifactsChanged() {
		for (const cb of [...this.artifactsListeners]) cb();
	}

	/**
	 * 订阅「连接（重新）建立、artifact 观看已重发」：服务端重新登记观看时不回放实时状态（如画布），断线期间
	 * 别处的改动要由订阅者自己重新取。返回取消订阅。
	 */
	onArtifactWatchesResumed(cb: () => void): () => void {
		this.watchesResumedListeners.add(cb);
		return () => {
			this.watchesResumedListeners.delete(cb);
		};
	}

	/** 订阅某个 artifact 的观看者消息（需另外 `watchArtifact` 才会收到）。返回取消订阅。 */
	onArtifact(id: string, cb: (msg: ArtifactEvent) => void): () => void {
		let set = this.artifactListeners.get(id);
		if (!set) {
			set = new Set();
			this.artifactListeners.set(id, set);
		}
		set.add(cb);
		return () => {
			const cur = this.artifactListeners.get(id);
			cur?.delete(cb);
			if (cur?.size === 0) this.artifactListeners.delete(id);
		};
	}

	private sendArtifactWatch(id: string, page?: string) {
		this.send(
			page === undefined
				? { type: "artifact.watch", id }
				: { type: "artifact.watch", id, page },
		);
	}

	/**
	 * 观看一个 artifact（引用计数）；`page` 是画布 artifact 当前所在的画布页（重复调用用来更新）。
	 * 返回取消函数：最后一个观看者取消时发 `artifact.unwatch`。
	 */
	watchArtifact(id: string, page?: string): () => void {
		const cur = this.artifactWatches.get(id);
		const next = { count: (cur?.count ?? 0) + 1, page: page ?? cur?.page };
		this.artifactWatches.set(id, next);
		if (!cur || next.page !== cur.page) this.sendArtifactWatch(id, next.page);
		if (!cur) this.emitWatchesChanged();
		return () => {
			const w = this.artifactWatches.get(id);
			if (!w) return;
			if (w.count > 1)
				this.artifactWatches.set(id, { ...w, count: w.count - 1 });
			else {
				this.artifactWatches.delete(id);
				this.artifactActivity.delete(id);
				this.send({ type: "artifact.unwatch", id });
				this.emitWatchesChanged();
			}
		};
	}

	connect() {
		this.closedByUser = false;
		this.suspended = false;
		this.bindVisibility(true);
		this.bindNetwork(true);
		if (this.ws || this.reconnectTimer || this.opening) return;
		void this.open();
	}

	private readonly onVisibility = () => {
		this.sendFocus(false);
		if (!host.mobile || this.closedByUser) return;
		if (!host.isForeground()) {
			if (this.suspendTimer || this.suspended) return;
			this.suspendTimer = setTimeout(() => {
				this.suspendTimer = null;
				this.suspend();
			}, MOBILE_SUSPEND_MS);
			return;
		}
		clearTimeout(this.suspendTimer ?? undefined);
		this.suspendTimer = null;
		if (!this.suspended) return;
		this.suspended = false;
		connectionStore.setState({ attempts: 0 });
		if (!this.ws && !this.opening) void this.open();
	};

	/** 摘掉并关闭 socket、作废在等票据的 open、取消重连；不解绑前台监听、不算用户关闭。 */
	private suspend() {
		if (this.closedByUser) return;
		this.suspended = true;
		this.dropConnection();
	}

	/** 同步断开当前连接（close 与后台挂起共用）。 */
	private dropConnection() {
		// 作废仍在等票据的 open()，避免 close→connect 快速交替时建出两条连接
		this.generation++;
		this.opening = false;
		clearTimeout(this.reconnectTimer ?? undefined);
		this.reconnectTimer = null;
		const ws = this.ws;
		if (ws) {
			// 原生 close 事件是异步的：先同步清理，再关闭；旧 socket 的回调全部摘掉
			this.teardown(ws, "connection closed");
			ws.onopen = null;
			ws.onmessage = null;
			ws.onclose = null;
			ws.close();
		}
		for (const timer of this.effectTimers.values()) clearTimeout(timer);
		this.effectTimers.clear();
		this.stateBackfill.clear();
		connectionStore.setState({ status: "idle" });
	}

	private bindVisibility(on: boolean) {
		if (on) this.offForeground ??= host.onForegroundChange(this.onVisibility);
		else {
			this.offForeground?.();
			this.offForeground = null;
		}
	}

	/** 页面不在前台时不 focus 任何线程。 */
	private effectiveFocus(): string | null {
		return host.isForeground() ? this.focused : null;
	}

	/** 发出当前应有的 focus；`force` = 不管与上次是否相同（新连接）。 */
	private sendFocus(force: boolean) {
		const threadId = this.effectiveFocus();
		if (!force && threadId === this.sentFocus) return;
		if (this.send({ type: "focus", threadId })) this.sentFocus = threadId;
	}

	close() {
		this.closedByUser = true;
		this.bindVisibility(false);
		this.bindNetwork(false);
		clearTimeout(this.suspendTimer ?? undefined);
		this.suspendTimer = null;
		this.suspended = false;
		this.dropConnection();
	}

	/** 连接作废时的统一清理：停 ping、拒绝在途命令、放开连接引用。 */
	private teardown(ws: WebSocket, reason: string) {
		if (this.ws === ws) {
			this.ws = null;
			this.sentFocus = undefined;
		}
		clearInterval(this.pingTimer);
		this.pingTimer = undefined;
		this.awaitingPong = false;
		for (const [id, p] of this.pending) {
			clearTimeout(p.timer);
			this.pending.delete(id);
			p.reject(new OmpCommandError(p.command, "disconnected", reason));
		}
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
			this.sendFocus(true);
			for (const [threadId] of this.subs) this.sendSub(threadId);
			for (const threadId of this.watching)
				if (this.subs.has(threadId))
					this.send({ type: "term.watch", threadId, on: true });
			// 心跳：上一次 ping 到下一次之间一条消息都没收到 = 连接已死（半开），放弃并重连
			this.awaitingPong = false;
			this.pingTimer = setInterval(() => {
				if (this.awaitingPong) {
					this.abandon("heartbeat timeout");
					return;
				}
				if (this.send({ type: "ping" })) this.awaitingPong = true;
			}, PING_INTERVAL_MS);
			this.emitSchedulesChanged();
			this.emitGitHubChanged();
			this.emitSshChanged();
			this.emitProjectRepoChanged(null);
			this.emitBoxesChanged();
			for (const [id, w] of this.artifactWatches)
				this.sendArtifactWatch(id, w.page);
			this.emitArtifactsChanged();
			for (const cb of [...this.watchesResumedListeners]) cb();
		};
		ws.onmessage = (ev) => {
			if (this.ws !== ws || typeof ev.data !== "string") return;
			// 任何消息都说明连接活着（不只 pong）
			this.awaitingPong = false;
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
			this.teardown(ws, "connection closed");
			if (!this.closedByUser) this.scheduleReconnect("closed");
		};
	}

	/**
	 * 放弃一条已死的连接（心跳无回音、浏览器报断网）：同步清理、摘掉旧回调再关闭，然后按退避重连。
	 * 半开的 TCP（断网、休眠唤醒、换网络）不会触发 close 事件，只靠 onclose 会一直显示「已连接」。
	 */
	private abandon(reason: string) {
		const ws = this.ws;
		if (!ws) return;
		this.teardown(ws, reason);
		ws.onopen = null;
		ws.onmessage = null;
		ws.onclose = null;
		ws.close();
		if (!this.closedByUser) this.scheduleReconnect(reason);
	}

	private readonly onOffline = () => {
		if (this.closedByUser || this.suspended) return;
		// 还在等票据的 open 作废（断网时它只会失败）；已有连接直接放弃
		if (this.opening) {
			this.generation++;
			this.opening = false;
			this.scheduleReconnect("offline");
		}
		this.abandon("offline");
	};

	private readonly onOnline = () => {
		if (this.closedByUser || this.suspended) return;
		if (!this.ws && !this.opening) this.retryNow();
	};

	private bindNetwork(on: boolean) {
		if (typeof window === "undefined") return;
		if (on && !this.networkBound) {
			window.addEventListener("offline", this.onOffline);
			window.addEventListener("online", this.onOnline);
			this.networkBound = true;
		} else if (!on && this.networkBound) {
			window.removeEventListener("offline", this.onOffline);
			window.removeEventListener("online", this.onOnline);
			this.networkBound = false;
		}
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
		const st = peekThreadStore(threadId)?.getState();
		const afterSeq = st?.lastSeq ?? 0;
		const transcriptRev = st?.transcriptRev;
		const tail = this.tails.get(threadId);
		// tail 只在服务端实际从 seq 0 补发（本地没有条目或需要全量重放）时生效，续传时它被忽略
		this.send({
			type: "sub",
			threadId,
			afterSeq,
			...(transcriptRev != null ? { transcriptRev } : {}),
			...(tail != null ? { tail } : {}),
		});
	}

	/** 重新订阅一个线程（拿权威快照恢复待答 UI 等状态）；未订阅则无操作。 */
	resync(threadId: string) {
		if (!this.subs.has(threadId)) return;
		this.flush();
		this.sendSub(threadId);
	}

	/**
	 * 订阅线程（引用计数）；连接恢复后自动以 afterSeq 补拉。
	 * `tail`（M19）：从头补发时只补最近 tail 条，更早的经 `loadEarlierEntries` 翻页；以首个带它的订阅为准。
	 */
	subscribe(threadId: string, opts?: { tail?: number }): () => void {
		const n = this.subs.get(threadId) ?? 0;
		this.subs.set(threadId, n + 1);
		if (opts?.tail != null && !this.tails.has(threadId))
			this.tails.set(threadId, opts.tail);
		if (n === 0) this.sendSub(threadId);
		return () => {
			const left = (this.subs.get(threadId) ?? 1) - 1;
			if (left > 0) this.subs.set(threadId, left);
			else {
				this.subs.delete(threadId);
				this.tails.delete(threadId);
				this.send({ type: "unsub", threadId });
			}
		};
	}

	/** 开关某线程的实时终端输出（服务端要求已订阅该线程；未订阅时只记下）。 */
	termWatch(threadId: string, on: boolean) {
		if (on) this.watching.add(threadId);
		else this.watching.delete(threadId);
		if (this.subs.has(threadId))
			this.send({ type: "term.watch", threadId, on });
	}

	/** 记下正在看的线程（`null` = 不在任何线程页）；标签页可见时才真正 focus。 */
	focus(threadId: string | null) {
		this.focused = threadId;
		this.sendFocus(false);
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
				if (msg.orb) acceptOrbPush(msg.orb);
				this.enqueue(msg.threadId, { type: "thread.snapshot", snapshot: msg });
				for (const eff of snapshotEffects(msg))
					this.scheduleEffect(msg.threadId, eff);
				// 终端同步读 store 里的就绪前提：先落地快照
				this.flush();
				terminalHub.onSnapshot(msg.threadId);
				return;
			case "entries":
				this.enqueue(msg.threadId, { type: "entries", msg });
				return;
			case "transcript.reset":
				// 先同步落地（清空条目、记下新 rev），再以 afterSeq=0 重新订阅
				this.enqueue(msg.threadId, {
					type: "transcript.reset",
					transcriptRev: msg.transcriptRev,
				});
				this.resync(msg.threadId);
				return;
			case "plan.resolved":
				this.enqueue(msg.threadId, { type: "plan.resolved" });
				return;
			case "frame": {
				if (TERMINAL_FRAMES[String(msg.frame.type)]) {
					terminalHub.onFrame(
						msg.threadId,
						msg.frame as unknown as RpcTerminalFrame,
					);
					return;
				}
				const frame = msg.frame as OmpFrame;
				this.enqueue(msg.threadId, { type: "frame", frame });
				const effects = effectsOf(frame);
				// 订阅时 agent 还没就绪（新建线程、唤醒中）的快照不带 state，控制面就绪后拉到的 state 也不会推给已订阅的
				// 客户端：agent 一出帧就说明它能应答，补拉一次。否则输入坞显示「模型未知」、头像行写模型 id，要等 agent_end。
				if (
					!effects.includes("refresh_state") &&
					!this.stateBackfill.has(msg.threadId) &&
					!peekThreadStore(msg.threadId)?.getState().state
				) {
					this.stateBackfill.add(msg.threadId);
					effects.push("refresh_state");
				}
				for (const eff of effects) this.scheduleEffect(msg.threadId, eff);
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
				// 工作标志与待答状态已作废，拉回权威 state
				this.scheduleEffect(msg.threadId, "refresh_state");
				this.flush();
				terminalHub.onLiveReset(msg.threadId);
				return;
			case "term.lagged":
				terminalHub.onLagged(msg.threadId);
				return;
			case "term.resized":
				terminalHub.onResized(msg.threadId, msg.terminalId, msg.cols, msg.rows);
				return;
			case "orb": {
				const prev = indexStore.getState().orbs[msg.orb.id]?.state;
				acceptOrbPush(msg.orb);
				for (const tid of msg.orb.threadIds) {
					const store = peekThreadStore(tid);
					if (!store) continue;
					// 线程 store 按帧批量落地，可能还没写入上一条 orb 消息：以同步更新的索引为准
					const before = prev ?? store.getState().orb?.state;
					this.enqueue(tid, { type: "orb", orb: msg.orb });
					// 挂起 → 就绪：挂起期间被拒的只读查询不会重放，主动重取（组件里的查询随 orb 状态自行重发）
					if (
						msg.orb.state === "ready" &&
						before !== undefined &&
						before !== "ready" &&
						store.getState().agentStatus === "ready"
					) {
						this.scheduleEffect(tid, "refresh_state");
						this.scheduleEffect(tid, "refresh_commands");
					}
				}
				return;
			}
			case "notification":
				notificationsStore.getState().onNotification(msg.notification);
				return;
			case "schedules.changed":
				this.emitSchedulesChanged();
				return;
			case "github.changed":
				this.emitGitHubChanged();
				return;
			case "ssh.changed":
				this.emitSshChanged();
				return;
			case "boxes.changed":
				this.emitBoxesChanged();
				return;
			case "project_repo.changed":
				this.emitProjectRepoChanged(msg.projectId);
				return;
			case "artifacts.changed":
				this.emitArtifactsChanged();
				return;
			case "artifact.version":
			case "artifact.activity":
			case "artifact.canvas":
			case "artifact.comments":
				if (msg.type === "artifact.activity")
					this.artifactActivity.set(msg.id, msg);
				for (const cb of [...(this.artifactListeners.get(msg.id) ?? [])])
					cb(msg);
				return;
			case "artifact.consent":
				for (const cb of [...this.consentListeners]) cb(msg);
				return;
			case "notifications.read":
				notificationsStore
					.getState()
					.onRead(
						msg.id
							? { id: msg.id }
							: msg.threadId
								? { threadId: msg.threadId }
								: undefined,
					);
				return;
			case "notifications.resolved":
				notificationsStore.getState().onResolved(msg.ids, msg.at);
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
				this.pushError(msg.code, msg.message, msg.threadId);
				// ui.response 被拒（对话框已被乐观关闭）：重订阅取回权威的待答请求
				if (msg.code === "invalid_frame" && msg.threadId)
					this.resync(msg.threadId);
				return;
			}
			case "pong":
				return;
		}
	}

	private pushError(code: string, message: string, threadId?: string) {
		const errors = connectionStore.getState().errors;
		connectionStore.setState({
			errors: [
				...errors.slice(-9),
				{ key: Date.now() + Math.random(), code, message, threadId },
			],
		});
	}

	/** 关闭一条全局错误提示。 */
	dismissError(key: number) {
		connectionStore.setState({
			errors: connectionStore.getState().errors.filter((e) => e.key !== key),
		});
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
				// 只在拿到 state 后放开：查询失败时不随回合逐帧重试，回合结束的 agent_end 照常刷新
				this.stateBackfill.delete(threadId);
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

/** 观看一个 artifact（见 `RealtimeClient.watchArtifact`）。 */
export function watchArtifact(id: string, page?: string): () => void {
	return realtime.watchArtifact(id, page);
}
