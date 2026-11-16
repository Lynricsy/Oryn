/**
 * 画布的实时文档（计划 M14 6.3）：本地文档 = 服务端底稿 + 自己还没被确认的操作。
 *
 * - 每个编辑器实例一个随机 `client`；提交操作时先本地应用，再 `POST …/canvas/ops`（串行发送，保证到达顺序）。
 * - 广播 `artifact.canvas` 带 `ops`：按 `seq` 顺序应用到底稿上；是自己的（`client` + `clientSeq`）就从待确认队列移除，
 *   再在新底稿上重放剩下的待确认操作——自己的操作因此始终压在别人的之上。`seq` 跳号时重新取整份状态。
 * - 广播带 `doc`：别处产生了新版本，整份替换底稿后重放待确认操作。
 * - 提交失败（服务端拒绝）：丢掉那一批及对应的撤销项并报告。`POST` 已返回而广播迟迟不到（例如还没登记观看）时，
 *   按返回的 `seq` 重新取状态。
 * - 撤销 / 重做只针对自己的操作，深度 200，逆操作由 `inverseOps` 相对于提交前的本地文档生成。
 */
import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { api, type Schemas, unwrap } from "../../../api/client";
import {
	applyCanvasOps,
	type CanvasDoc,
	type CanvasOp,
	inverseOps,
	parseCanvasDoc,
	parseCanvasOps,
} from "../../../lib/artifacts/canvas-doc";
import { realtime } from "../../../realtime/socket";

export type CanvasMessage = Schemas["WsServerArtifactCanvas"];

/** 撤销栈的深度。 */
export const UNDO_DEPTH = 200;
/** `POST` 返回之后等广播的上限；超时按返回的 `seq` 重新取状态。 */
export const ACK_WAIT_MS = 1500;
/** 取状态失败后的重试间隔。 */
export const RESYNC_RETRY_MS = 3000;

/** 画布的实时状态（底稿版本、实时序号、含未落版本用户操作的 `canvas.json`）。 */
export type CanvasState = Schemas["ArtifactCanvasState"];

export interface CanvasTransport {
	fetchState(): Promise<CanvasState>;
	post(ops: CanvasOp[], client: string, clientSeq: number): Promise<number>;
}

interface Batch {
	clientSeq: number;
	ops: CanvasOp[];
	/** 服务端接受后返回的 `seq`；还没返回时 null。 */
	ackSeq: number | null;
	/** 对应的撤销项（失败时一并撤掉）。 */
	undo: UndoEntry | null;
	/** 已经发出（在途或已返回）。 */
	sent: boolean;
}

interface UndoEntry {
	ops: CanvasOp[];
	inverse: CanvasOp[];
}

export interface CommitOptions {
	/** 记进撤销栈（缺省 true）；拖动途中的中间态传 false。 */
	record?: boolean;
	/** 自带的逆操作（拖动结束时相对于拖动开始的文档生成）。 */
	inverse?: CanvasOp[];
}

export interface CanvasSnapshot {
	/** 本地文档；还没取到时 null。 */
	doc: CanvasDoc | null;
	/** 底稿所在的版本。 */
	version: number;
	canUndo: boolean;
	canRedo: boolean;
	/** 取状态失败（会自动重试）。 */
	error: unknown;
}

function newClient(): string {
	const bytes = crypto.getRandomValues(new Uint8Array(8));
	return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

function docOf(raw: unknown): CanvasDoc {
	return parseCanvasDoc(JSON.stringify(raw));
}

export class CanvasSync {
	readonly client = newClient();
	private readonly transport: CanvasTransport;
	private readonly onReject: (err: unknown) => void;
	private base: CanvasDoc | null = null;
	private seq = 0;
	private version = 0;
	private local: CanvasDoc | null = null;
	private pending: Batch[] = [];
	private nextClientSeq = 1;
	private undoStack: UndoEntry[] = [];
	private redoStack: UndoEntry[] = [];
	// 取状态期间到达的广播先攒着，取到后按 seq 补上；null = 不在取
	private buffered: CanvasMessage[] | null = null;
	private error: unknown = null;
	private sending = false;
	private disposed = false;
	private timers = new Set<ReturnType<typeof setTimeout>>();
	private readonly listeners = new Set<() => void>();
	private snap: CanvasSnapshot = {
		doc: null,
		version: 0,
		canUndo: false,
		canRedo: false,
		error: null,
	};

	constructor(transport: CanvasTransport, onReject: (err: unknown) => void) {
		this.transport = transport;
		this.onReject = onReject;
	}

	subscribe = (cb: () => void): (() => void) => {
		this.listeners.add(cb);
		return () => {
			this.listeners.delete(cb);
		};
	};

	getSnapshot = (): CanvasSnapshot => this.snap;

	/** 当前本地文档（命令式读取，拖动等手势里用）。 */
	get doc(): CanvasDoc | null {
		return this.local;
	}

	/** 取整份状态（首次加载与跳号后）。 */
	resync(): void {
		if (this.disposed || this.buffered !== null) return;
		this.buffered = [];
		this.transport.fetchState().then(
			(state) => this.load(state),
			(err: unknown) => {
				if (this.disposed) return;
				this.buffered = null;
				this.error = err;
				this.emit();
				this.later(() => this.resync(), RESYNC_RETRY_MS);
			},
		);
	}

	dispose(): void {
		this.disposed = true;
		for (const t of this.timers) clearTimeout(t);
		this.timers.clear();
		this.listeners.clear();
	}

	private later(fn: () => void, ms: number) {
		const t = setTimeout(() => {
			this.timers.delete(t);
			if (!this.disposed) fn();
		}, ms);
		this.timers.add(t);
	}

	private load(state: CanvasState) {
		if (this.disposed) return;
		const queued = this.buffered ?? [];
		this.buffered = null;
		try {
			this.base = docOf(state.doc);
		} catch (err) {
			this.error = err;
			this.emit();
			return;
		}
		this.error = null;
		this.seq = state.seq;
		this.version = state.version;
		this.pending = this.pending.filter(
			(b) => b.ackSeq === null || b.ackSeq > this.seq,
		);
		for (const msg of queued) this.apply(msg);
		this.recompute();
	}

	/** 收到 `artifact.canvas`。 */
	receive(msg: CanvasMessage): void {
		if (this.disposed) return;
		if (this.buffered !== null) {
			this.buffered.push(msg);
			return;
		}
		if (this.base === null) return;
		if (this.apply(msg)) this.recompute();
	}

	/** 把一条广播应用到底稿；返回底稿是否变了。跳号或应用失败时重新取状态。 */
	private apply(msg: CanvasMessage): boolean {
		if (!this.base || msg.seq <= this.seq) return false;
		if (msg.doc !== undefined) {
			try {
				this.base = docOf(msg.doc);
			} catch {
				this.resync();
				return false;
			}
			this.seq = msg.seq;
			if (msg.version !== undefined) this.version = msg.version;
			return true;
		}
		if (msg.seq !== this.seq + 1 || msg.ops === undefined) {
			this.resync();
			return false;
		}
		try {
			this.base = applyCanvasOps(this.base, parseCanvasOps(msg.ops));
		} catch {
			this.resync();
			return false;
		}
		this.seq = msg.seq;
		if (msg.client === this.client)
			this.pending = this.pending.filter((b) => b.clientSeq !== msg.clientSeq);
		return true;
	}

	/** 本地文档 = 底稿 + 待确认操作（在新底稿上放不上的那一批跳过，等服务端的结论）。 */
	private recompute() {
		let doc = this.base;
		if (doc)
			for (const b of this.pending) {
				try {
					doc = applyCanvasOps(doc, b.ops);
				} catch {
					// 跳过
				}
			}
		this.local = doc;
		this.emit();
	}

	private emit() {
		this.snap = {
			doc: this.local,
			version: this.version,
			canUndo: this.undoStack.length > 0,
			canRedo: this.redoStack.length > 0,
			error: this.error,
		};
		for (const cb of [...this.listeners]) cb();
	}

	/**
	 * 提交一批操作：先本地应用（不合法时什么都不做，返回 false），再发给服务端。
	 */
	commit(ops: CanvasOp[], opts: CommitOptions = {}): boolean {
		if (!this.local || ops.length === 0) return false;
		let next: CanvasDoc;
		try {
			next = applyCanvasOps(this.local, ops);
		} catch {
			return false;
		}
		let undo: UndoEntry | null = null;
		if (opts.record !== false) {
			undo = { ops, inverse: opts.inverse ?? inverseOps(this.local, ops) };
			this.undoStack.push(undo);
			if (this.undoStack.length > UNDO_DEPTH) this.undoStack.shift();
			this.redoStack = [];
		}
		this.pending.push({
			clientSeq: this.nextClientSeq++,
			ops,
			ackSeq: null,
			undo,
			sent: false,
		});
		this.local = next;
		this.emit();
		this.flush();
		return true;
	}

	/** 撤销自己最近的一次操作。 */
	undo(): void {
		const entry = this.undoStack.pop();
		if (!entry) return;
		if (this.commit(entry.inverse, { record: false }))
			this.redoStack.push(entry);
		this.emit();
	}

	/** 重做最近撤销的那次操作。 */
	redo(): void {
		const entry = this.redoStack.pop();
		if (!entry || !this.local) return;
		const inverse = inverseOps(this.local, entry.ops);
		if (this.commit(entry.ops, { record: false }))
			this.undoStack.push({ ops: entry.ops, inverse });
		this.emit();
	}

	/** 依次发送还没发出的批次（一次只在途一批）。 */
	private flush() {
		if (this.sending || this.disposed) return;
		const batch = this.pending.find((b) => !b.sent);
		if (!batch) return;
		batch.sent = true;
		this.sending = true;
		this.transport.post(batch.ops, this.client, batch.clientSeq).then(
			(seq) => {
				this.sending = false;
				if (this.disposed) return;
				batch.ackSeq = seq;
				if (this.pending.includes(batch) && seq <= this.seq) {
					// 广播先到过、或者取状态时已含这批
					this.pending = this.pending.filter((b) => b !== batch);
					this.recompute();
				} else if (this.pending.includes(batch)) {
					this.later(() => {
						if (this.pending.includes(batch) && this.seq < seq) this.resync();
					}, ACK_WAIT_MS);
				}
				this.flush();
			},
			(err: unknown) => {
				this.sending = false;
				if (this.disposed) return;
				this.pending = this.pending.filter((b) => b !== batch);
				if (batch.undo)
					this.undoStack = this.undoStack.filter((u) => u !== batch.undo);
				this.recompute();
				this.onReject(err);
				this.flush();
			},
		);
	}
}

/** REST 传输：`GET …/canvas` 取状态，`POST …/canvas/ops` 提交（返回服务端的 `seq`）。 */
export function restTransport(artifactId: string): CanvasTransport {
	const path = { id: artifactId };
	return {
		fetchState: () =>
			unwrap(api.GET("/api/v1/artifacts/{id}/canvas", { params: { path } })),
		post: (ops, client, clientSeq) =>
			unwrap(
				api.POST("/api/v1/artifacts/{id}/canvas/ops", {
					params: { path },
					body: {
						ops: ops as Schemas["ArtifactCanvasOps"]["ops"],
						client,
						clientSeq,
					},
				}),
			).then((r) => r.seq),
	};
}

const EMPTY: CanvasSnapshot = {
	doc: null,
	version: 0,
	canUndo: false,
	canRedo: false,
	error: null,
};
const noSubscribe = () => () => {};
const emptySnapshot = () => EMPTY;

/** `connectCanvas` 用到的实时连接接口（`realtime` 的子集，测试可替换）。 */
export type CanvasRealtime = Pick<
	typeof realtime,
	"onArtifact" | "onArtifactWatchesResumed"
>;

/**
 * 把画布同步接到实时连接上：转交 `artifact.canvas`；连接重新建立后重新取整份状态——服务端重新登记观看时不回放
 * 画布，断线期间别处的改动之后若没有新广播，就只能靠这一步补回。返回断开函数。
 */
export function connectCanvas(
	sync: CanvasSync,
	artifactId: string,
	rt: CanvasRealtime = realtime,
): () => void {
	const offMsg = rt.onArtifact(artifactId, (msg) => {
		if (msg.type === "artifact.canvas") sync.receive(msg);
	});
	const offResumed = rt.onArtifactWatchesResumed(() => sync.resync());
	return () => {
		offMsg();
		offResumed();
	};
}

/**
 * 某个画布 artifact 的实时文档。`enabled` 为假（看旧版本、只读）时不连接。
 * 调用方另外负责 `watchArtifact`（查看器已经在看）。
 */
export function useCanvasDoc(
	artifactId: string,
	enabled: boolean,
	onReject: (err: unknown) => void,
): { sync: CanvasSync | null; snapshot: CanvasSnapshot } {
	const [sync, setSync] = useState<CanvasSync | null>(null);
	const rejectRef = useRef(onReject);
	rejectRef.current = onReject;

	useEffect(() => {
		if (!enabled) {
			setSync(null);
			return;
		}
		const s = new CanvasSync(restTransport(artifactId), (err) =>
			rejectRef.current(err),
		);
		const off = connectCanvas(s, artifactId);
		s.resync();
		setSync(s);
		return () => {
			off();
			s.dispose();
		};
	}, [artifactId, enabled]);

	const snapshot = useSyncExternalStore(
		sync?.subscribe ?? noSubscribe,
		sync?.getSnapshot ?? emptySnapshot,
	);
	return { sync, snapshot };
}
