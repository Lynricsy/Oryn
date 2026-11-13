/**
 * 匿名分享页的数据源（M5 计划 5.2）：把公开接口的数据喂进 `getThreadStore(shareKey(slug))`，
 * 转录渲染因此与线程页共用同一套 reducer 与组件。只用 `/api/v1/public/shares/<slug>/…`，不碰任何私有接口。
 *
 * - 快照：`GET …/entries?after=` 循环到 `hasMore=false`，首页与一条合成的 `thread.snapshot` 一起落地。
 * - 实时：匿名 WebSocket。`share.snapshot` 合成为 `thread.snapshot`；`entries` / `frame` / `live.reset` 原样映射；
 *   `transcript.reset` 清空后以 `after=0` 重连；`share.state` 更新线程摘要；`share.revoked` 或关闭码 4001 → `onGone`；
 *   其它断开先用公开 GET 复核链接（握手被 404 拒绝时浏览器拿不到 4001，只能这样发现撤销）：404 → `onGone`，
 *   成功则把新元数据交给 `onShare`（附件的公开地址可能变了），再按 0.5s→15s 退避重连；每 25s 发 `ping`。
 */
import {
	ApiError,
	api,
	type Schemas,
	type Thread,
	unwrap,
} from "../api/client";
import { getThreadStore } from "../state/stores";
import type { OmpFrame } from "../state/thread-reducer";

export type PublicShare = Schemas["PublicShare"];
type PublicMessage = Schemas["WsPublicServerMessage"];

/** 分享被撤销时服务端关闭匿名连接用的状态码。 */
export const SHARE_REVOKED_CLOSE = 4001;
const PING_INTERVAL_MS = 25_000;
const PAGE_LIMIT = 500;

function backoffMs(attempt: number): number {
	return Math.min(15_000, 500 * 2 ** attempt);
}

/** 分享页线程 store 的键（不与真实线程 id 冲突）。 */
export function shareKey(slug: string): string {
	return `share:${slug}`;
}

/** 链接不存在或已被撤销（公开接口一律 404）。 */
export class ShareGoneError extends Error {
	constructor() {
		super("share not found");
		this.name = "ShareGoneError";
	}
}

export async function fetchShare(slug: string): Promise<PublicShare> {
	try {
		return await unwrap(
			api.GET("/api/v1/public/shares/{slug}", { params: { path: { slug } } }),
		);
	} catch (e) {
		if (e instanceof ApiError && e.status === 404) throw new ShareGoneError();
		throw e;
	}
}

/** 合成线程摘要：分享页只有标题与状态，其余必填项给空值（不含线程 id、orb、项目等）。 */
function shareThread(
	key: string,
	share: PublicShare,
	title: string,
	state: Thread["state"],
	leafEntryId: string | undefined,
): Thread {
	return {
		id: key,
		title,
		titleSource: "auto",
		state,
		stateChangedAt: share.sharedAt,
		pinned: false,
		labels: [],
		unread: false,
		createdAt: share.threadCreatedAt,
		updatedAt: share.sharedAt,
		leafEntryId,
	};
}

export class ShareFeed {
	private readonly slug: string;
	private readonly share: PublicShare;
	private readonly onGone: () => void;
	private readonly onShare: ((share: PublicShare) => void) | undefined;
	private readonly key: string;
	private stopped = true;
	private ws: WebSocket | null = null;
	private attempts = 0;
	private timer: ReturnType<typeof setTimeout> | undefined;
	private pingTimer: ReturnType<typeof setInterval> | undefined;

	constructor(
		slug: string,
		share: PublicShare,
		onGone: () => void,
		onShare?: (share: PublicShare) => void,
	) {
		this.slug = slug;
		this.share = share;
		this.onGone = onGone;
		this.onShare = onShare;
		this.key = shareKey(slug);
	}

	start(): void {
		if (!this.stopped) return;
		this.stopped = false;
		if (this.share.mode === "snapshot") void this.loadPages(0, true);
		else this.connect();
	}

	stop(): void {
		this.stopped = true;
		clearTimeout(this.timer);
		clearInterval(this.pingTimer);
		const ws = this.ws;
		this.ws = null;
		if (ws) {
			ws.onclose = null;
			ws.onmessage = null;
			ws.close();
		}
	}

	private get store() {
		return getThreadStore(this.key);
	}

	private gone() {
		this.stop();
		this.onGone();
	}

	// ------------------------------------------------------------ 快照

	private async loadPages(after: number, first: boolean): Promise<void> {
		let cursor = after;
		let head = first;
		while (!this.stopped) {
			let page: Schemas["PublicEntryPage"];
			try {
				page = await unwrap(
					api.GET("/api/v1/public/shares/{slug}/entries", {
						params: {
							path: { slug: this.slug },
							query: { after: cursor, limit: PAGE_LIMIT },
						},
					}),
				);
			} catch (e) {
				if (this.stopped) return;
				if (e instanceof ApiError && e.status === 404) return this.gone();
				const n = this.attempts++;
				this.timer = setTimeout(
					() => void this.loadPages(cursor, head),
					backoffMs(n),
				);
				return;
			}
			if (this.stopped) return;
			this.attempts = 0;
			const entries = {
				type: "entries" as const,
				msg: { entries: page.entries, leafEntryId: page.leafEntryId },
			};
			if (head) {
				// 首页与合成快照一起落地：页面从加载中直接切到有内容
				const s = this.share;
				this.store.getState().apply([
					{
						type: "thread.snapshot",
						snapshot: {
							type: "thread.snapshot",
							threadId: this.key,
							thread: shareThread(
								this.key,
								s,
								s.title,
								s.state ?? "idle",
								s.leafEntryId,
							),
							agent: { status: "none", pendingUi: [], subagents: [], live: [] },
							leafEntryId: s.leafEntryId,
							lastSeq: 0,
							transcriptRev: s.transcriptRev,
							fullReplay: false,
						},
					},
					entries,
				]);
				head = false;
			} else this.store.getState().apply([entries]);
			const last = page.entries[page.entries.length - 1];
			if (!page.hasMore || !last) return;
			cursor = last.seq;
		}
	}

	// ------------------------------------------------------------ 实时

	private connect(): void {
		if (this.stopped) return;
		const s = this.store.getState();
		const proto = location.protocol === "https:" ? "wss:" : "ws:";
		// rev 未知（还没收到过快照）时不带，服务端按 after 补发
		const q = new URLSearchParams({ after: String(s.lastSeq) });
		if (s.transcriptRev !== null) q.set("rev", String(s.transcriptRev));
		const ws = new WebSocket(
			`${proto}//${location.host}/api/v1/public/shares/${encodeURIComponent(this.slug)}/ws?${q.toString()}`,
		);
		this.ws = ws;
		ws.onopen = () => {
			clearInterval(this.pingTimer);
			this.pingTimer = setInterval(() => {
				if (ws.readyState === WebSocket.OPEN)
					ws.send(JSON.stringify({ type: "ping" }));
			}, PING_INTERVAL_MS);
		};
		ws.onmessage = (ev) => {
			let msg: PublicMessage;
			try {
				msg = JSON.parse(String(ev.data)) as PublicMessage;
			} catch {
				return;
			}
			this.onMessage(msg);
		};
		ws.onclose = (ev) => {
			if (this.ws !== ws) return;
			this.ws = null;
			clearInterval(this.pingTimer);
			if (this.stopped) return;
			if (ev.code === SHARE_REVOKED_CLOSE) return this.gone();
			void this.recheck();
		};
	}

	/** 断开后复核链接：已撤销 → onGone；仍有效 → 交出新元数据；之后（含网络错误）按退避重连。 */
	private async recheck(): Promise<void> {
		const n = this.attempts++;
		try {
			const fresh = await fetchShare(this.slug);
			if (this.stopped) return;
			this.onShare?.(fresh);
		} catch (e) {
			if (this.stopped) return;
			if (e instanceof ShareGoneError) return this.gone();
		}
		this.timer = setTimeout(() => this.connect(), backoffMs(n));
	}

	/** 立即断开并重连（不计退避）。 */
	private reconnect(): void {
		const ws = this.ws;
		this.ws = null;
		clearInterval(this.pingTimer);
		if (ws) {
			ws.onclose = null;
			ws.onmessage = null;
			ws.close();
		}
		this.connect();
	}

	private onMessage(msg: PublicMessage): void {
		const store = this.store.getState();
		switch (msg.type) {
			case "share.snapshot":
				this.attempts = 0;
				store.apply([
					{
						type: "thread.snapshot",
						snapshot: {
							type: "thread.snapshot",
							threadId: this.key,
							thread: shareThread(
								this.key,
								this.share,
								msg.title,
								msg.state,
								msg.leafEntryId,
							),
							agent: {
								status: "none",
								pendingUi: [],
								subagents: msg.subagents,
								live: msg.live,
							},
							leafEntryId: msg.leafEntryId,
							lastSeq: msg.lastSeq,
							transcriptRev: msg.transcriptRev,
							fullReplay: msg.fullReplay,
						},
					},
				]);
				return;
			case "entries":
				store.apply([
					{
						type: "entries",
						msg: { entries: msg.entries, leafEntryId: msg.leafEntryId },
					},
				]);
				return;
			case "frame":
				store.apply([{ type: "frame", frame: msg.frame as OmpFrame }]);
				return;
			case "live.reset":
				store.apply([{ type: "live.reset" }]);
				return;
			case "transcript.reset":
				// 服务端重写了转录：清空本地条目、记下新 rev，再从头补发
				store.apply([
					{ type: "transcript.reset", transcriptRev: msg.transcriptRev },
				]);
				this.reconnect();
				return;
			case "share.state": {
				const cur = store.thread;
				if (!cur) return;
				store.apply([
					{
						type: "thread",
						thread: {
							...cur,
							state: msg.state,
							title: msg.title,
							leafEntryId: store.leafEntryId,
						},
					},
				]);
				return;
			}
			case "share.revoked":
				this.gone();
				return;
			case "pong":
				return;
		}
	}
}
