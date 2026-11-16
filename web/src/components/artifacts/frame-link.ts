/**
 * 父页面这一侧的 postMessage 通道：绑定一个 artifact 帧（`iframe`），随机生成 16 位十六进制 `channel`，
 * 帧每次 `load` 后发 `hello` 定下通道；只处理来源正是这个帧的窗口、且 `channel` 相符的消息。
 * 协议与校验在 `sandbox/runtime/messages.ts`（与沙箱运行时共用）。帧里是不透明源，`postMessage` 的目标源只能写 `*`；
 * 通道号不是秘密，防的是同页里别的帧冒充。
 */
import {
	fitsMessageLimit,
	type PageMessage,
	type PageMessageKind,
	type ParentMessageBody,
	PROTOCOL_VERSION,
	parsePageMessage,
} from "../../../sandbox/runtime/messages";

type PageOf<K extends PageMessageKind> = Extract<PageMessage, { kind: K }>;

/** 16 位小写十六进制。 */
export function newChannel(): string {
	const bytes = new Uint8Array(8);
	crypto.getRandomValues(bytes);
	return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

export class FrameLink {
	readonly channel = newChannel();
	private readonly frame: HTMLIFrameElement;
	private readonly handlers = new Map<
		PageMessageKind,
		Set<(msg: PageMessage) => void>
	>();
	private readonly loadHandlers = new Set<() => void>();
	// 帧每次加载后要重发的选择状态：页面重载（新版本、帧重新申请）会丢掉它们
	private mode: Extract<ParentMessageBody, { kind: "select-mode" }> | null =
		null;
	private pins: Extract<ParentMessageBody, { kind: "select-pins" }> | null =
		null;
	private source: Extract<ParentMessageBody, { kind: "select-source" }> | null =
		null;

	private readonly onMessage = (event: MessageEvent) => {
		const win = this.frame.contentWindow;
		if (!win || event.source !== win) return;
		const msg = parsePageMessage(event.data);
		if (!msg || msg.channel !== this.channel) return;
		// 用户在页面里点选的元素也算「最近一次的选中」，重载后照样重新选中它
		if (msg.kind === "select" && !msg.located) {
			const src = msg.selection?.src ?? null;
			this.source = src === null ? null : { kind: "select-source", src };
		}
		for (const cb of [...(this.handlers.get(msg.kind) ?? [])]) cb(msg);
	};

	private readonly onLoad = () => {
		this.post({ kind: "hello" });
		if (this.mode) this.post(this.mode);
		if (this.pins) this.post(this.pins);
		if (this.source) this.post(this.source);
		for (const cb of [...this.loadHandlers]) cb();
	};

	constructor(frame: HTMLIFrameElement) {
		this.frame = frame;
		window.addEventListener("message", this.onMessage);
		frame.addEventListener("load", this.onLoad);
	}

	/** 发给帧里的页面；帧还没有窗口或消息超限时丢弃。最近一次的 `select-mode`、`select-pins`、`select-source` 在帧每次加载后重发。 */
	post(body: ParentMessageBody): void {
		switch (body.kind) {
			case "select-mode":
				this.mode = body;
				// 关掉模式时页面清掉选中，重载后也不再恢复
				if (body.mode === null) this.source = null;
				break;
			case "select-pins":
				this.pins = body;
				break;
			case "select-source":
				this.source = body.src === null ? null : body;
				break;
		}
		const win = this.frame.contentWindow;
		if (!win) return;
		const msg = { v: PROTOCOL_VERSION, channel: this.channel, ...body };
		if (!fitsMessageLimit(msg)) return;
		win.postMessage(msg, "*");
	}

	/** 订阅页面发来的某种消息。返回取消订阅。 */
	on<K extends PageMessageKind>(
		kind: K,
		cb: (msg: PageOf<K>) => void,
	): () => void {
		let set = this.handlers.get(kind);
		if (!set) {
			set = new Set();
			this.handlers.set(kind, set);
		}
		const fn = cb as (msg: PageMessage) => void;
		set.add(fn);
		return () => {
			this.handlers.get(kind)?.delete(fn);
		};
	}

	/** 帧每次加载完成（`hello` 已发出之后）调用。返回取消订阅。 */
	onLoaded(cb: () => void): () => void {
		this.loadHandlers.add(cb);
		return () => {
			this.loadHandlers.delete(cb);
		};
	}

	dispose(): void {
		window.removeEventListener("message", this.onMessage);
		this.frame.removeEventListener("load", this.onLoad);
		this.handlers.clear();
		this.loadHandlers.clear();
	}
}
