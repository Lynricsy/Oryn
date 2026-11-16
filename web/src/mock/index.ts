/** 安装 mock 模式：替换 `fetch` 与 `WebSocket`，由浏览器内 MockServer 应答。 */
import { MockServer, type MockSocket } from "./server";

export const mockServer = new MockServer();

class FakeWebSocket implements MockSocket {
	static readonly CONNECTING = 0;
	static readonly OPEN = 1;
	static readonly CLOSING = 2;
	static readonly CLOSED = 3;
	readonly CONNECTING = 0;
	readonly OPEN = 1;
	readonly CLOSING = 2;
	readonly CLOSED = 3;
	readyState = 0;
	readonly url: string;
	onopen: ((ev: Event) => void) | null = null;
	onmessage: ((ev: MessageEvent) => void) | null = null;
	onclose: ((ev: CloseEvent) => void) | null = null;
	onerror: ((ev: Event) => void) | null = null;

	constructor(url: string) {
		this.url = url;
		setTimeout(() => {
			this.readyState = 1;
			// 先登记连接，再触发 onopen（客户端在 onopen 里立即发 sub）
			mockServer.connect(this);
			this.onopen?.(new Event("open"));
		}, 20);
	}

	send(data: string) {
		if (this.readyState !== 1) return;
		mockServer.onClient(this, JSON.parse(data) as Record<string, unknown>);
	}

	deliver(msg: Record<string, unknown>) {
		if (this.readyState !== 1) return;
		const data = JSON.stringify(msg);
		setTimeout(
			() => this.onmessage?.(new MessageEvent("message", { data })),
			0,
		);
	}

	close() {
		if (this.readyState === 3) return;
		this.readyState = 3;
		mockServer.disconnect(this);
		this.onclose?.(new CloseEvent("close"));
	}
}

export function installMock() {
	const realFetch = globalThis.fetch.bind(globalThis);
	globalThis.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(input, init);
		const res = await mockServer.handleFetch(req);
		return res ?? realFetch(input, init);
	};
	globalThis.WebSocket = FakeWebSocket as unknown as typeof WebSocket;
	interceptCanvasFrames();
	console.info("[oryn] mock 模式已启用");
}

/**
 * 画布帧的画框地址（`<origin>/__mock_af/<令牌>/_oryn/v/<n>/<路径>`）在 mock 里没有服务器应答：
 * 拦截 iframe 的 `src`（属性与 `setAttribute` 两条路），换成 mock 生成的 blob。
 */
function interceptCanvasFrames() {
	const desc = Object.getOwnPropertyDescriptor(
		HTMLIFrameElement.prototype,
		"src",
	);
	const resolve = (v: unknown) =>
		mockServer.artifacts.resolveFrameUrl(String(v)) ?? String(v);
	if (desc?.set) {
		const set = desc.set;
		Object.defineProperty(HTMLIFrameElement.prototype, "src", {
			...desc,
			set(this: HTMLIFrameElement, v: unknown) {
				set.call(this, resolve(v));
			},
		});
	}
	const setAttribute = Element.prototype.setAttribute;
	Element.prototype.setAttribute = function (name: string, value: string) {
		const v =
			this instanceof HTMLIFrameElement && name.toLowerCase() === "src"
				? resolve(value)
				: value;
		setAttribute.call(this, name, v);
	};
}
