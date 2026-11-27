/**
 * 父页面这一侧的 HTML 视图帧通道（M33，docs/plan-m33.md 的 C9）：随机生成 16 位十六进制 `channel`，帧每次 `load`
 * 后发 `hello` 定下通道；只处理来源正是这个帧的窗口、`channel` 相符、且不超过 64 KiB 的消息。
 * 协议与校验在 `sandbox/view/messages.ts`（与视图运行时共用）。视图帧是不透明源，`postMessage` 的目标源只能写 `*`；
 * 通道号不是秘密，防的是同页里别的帧（别的视图、artifact）冒充。
 */
import {
	fitsViewMessage,
	parseViewPageMessage,
	VIEW_PROTOCOL_VERSION,
	type ViewParentBody,
} from "../../../sandbox/view/messages";
import { newChannel } from "../artifacts/frame-link";

export interface ViewLinkHandlers {
	/** 帧每次加载完成（`hello` 已发出之后）。 */
	load?(): void;
	ready?(): void;
	resize?(height: number): void;
	prompt?(text: string): void;
}

export class ViewLink {
	readonly channel = newChannel();
	private readonly frame: HTMLIFrameElement;
	private readonly handlers: ViewLinkHandlers;

	private readonly onMessage = (event: MessageEvent) => {
		const win = this.frame.contentWindow;
		if (!win || event.source !== win) return;
		const msg = parseViewPageMessage(event.data);
		if (!msg || msg.channel !== this.channel) return;
		switch (msg.kind) {
			case "ready":
				this.handlers.ready?.();
				break;
			case "resize":
				this.handlers.resize?.(msg.height);
				break;
			case "prompt":
				this.handlers.prompt?.(msg.text);
				break;
		}
	};

	private readonly onLoad = () => {
		this.post({ kind: "hello" });
		this.handlers.load?.();
	};

	constructor(frame: HTMLIFrameElement, handlers: ViewLinkHandlers) {
		this.frame = frame;
		this.handlers = handlers;
		window.addEventListener("message", this.onMessage);
		frame.addEventListener("load", this.onLoad);
	}

	/** 发给帧里的视图；帧还没有窗口或消息超限时丢弃。 */
	post(body: ViewParentBody): void {
		const win = this.frame.contentWindow;
		if (!win) return;
		const msg = { v: VIEW_PROTOCOL_VERSION, channel: this.channel, ...body };
		if (!fitsViewMessage(msg)) return;
		win.postMessage(msg, "*");
	}

	dispose(): void {
		window.removeEventListener("message", this.onMessage);
		this.frame.removeEventListener("load", this.onLoad);
	}
}
