/**
 * 桌面会话：票据换 URL、两种缩放对应的 RFB 属性、只读 / 粘贴 / 发键透传、事件转成 onStatus / onClipboard（带关闭码），
 * close 之后不再回调。noVNC 用假 RFB 替换。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const rfbs: FakeRfb[] = [];

class FakeRfb extends EventTarget {
	viewOnly = false;
	focusOnClick = false;
	clipViewport = true;
	scaleViewport = false;
	resizeSession = false;
	background = "";
	qualityLevel = 6;
	compressionLevel = 2;
	readonly target: HTMLElement;
	readonly channel: unknown;
	readonly options: unknown;
	disconnect = vi.fn(() => {
		this.dispatchEvent(
			new CustomEvent("disconnect", { detail: { clean: true } }),
		);
	});
	focus = vi.fn();
	blur = vi.fn();
	sendKey = vi.fn();
	clipboardPasteFrom = vi.fn();
	constructor(target: HTMLElement, channel: unknown, options: unknown) {
		super();
		this.target = target;
		this.channel = channel;
		this.options = options;
		rfbs.push(this);
	}
}

vi.mock("@novnc/novnc", () => ({ default: FakeRfb }));

class FakeSocket extends EventTarget {
	static instances: FakeSocket[] = [];
	binaryType = "blob";
	readonly url: string;
	constructor(url: string) {
		super();
		this.url = url;
		FakeSocket.instances.push(this);
	}
	/** 模拟服务端关闭：先派发 close（带码），RFB 随后派发 disconnect */
	serverClose(code: number) {
		const ev = new Event("close") as Event & { code: number };
		Object.defineProperty(ev, "code", { value: code });
		this.dispatchEvent(ev);
		const rfb = rfbs.find((r) => r.channel === this);
		rfb?.dispatchEvent(
			new CustomEvent("disconnect", { detail: { clean: false } }),
		);
	}
}

const { openDesktopSession, desktopSocketUrl } = await import("./session");

let ticketCalls = 0;

beforeEach(() => {
	rfbs.length = 0;
	FakeSocket.instances = [];
	ticketCalls = 0;
	vi.stubGlobal("WebSocket", FakeSocket);
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL) => {
			const url = input instanceof Request ? input.url : String(input);
			if (url.endsWith("/api/v1/ws-ticket")) {
				ticketCalls++;
				return Response.json({ ticket: "tk/1", expiresIn: 30 });
			}
			return new Response(null, { status: 404 });
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

function open() {
	const target = document.createElement("div");
	const onStatus = vi.fn();
	const onClipboard = vi.fn();
	const s = openDesktopSession(target, "orb-abc", { onStatus, onClipboard });
	return { target, onStatus, onClipboard, s };
}

async function connected() {
	const h = open();
	await vi.waitFor(() => expect(rfbs).toHaveLength(1));
	const rfb = rfbs[0] as FakeRfb;
	return { ...h, rfb, ws: FakeSocket.instances[0] as FakeSocket };
}

describe("openDesktopSession", () => {
	it("先取票据，WebSocket 地址带票据（同源、按页面协议），交给 RFB 并设为二进制", async () => {
		const { target, onStatus, rfb, ws } = await connected();
		expect(ticketCalls).toBe(1);
		expect(ws.url).toBe(
			`ws://${location.host}/api/v1/orbs/orb-abc/desktop/ws?ticket=tk%2F1`,
		);
		expect(ws.url).toBe(desktopSocketUrl("orb-abc", "tk/1"));
		expect(ws.binaryType).toBe("arraybuffer");
		expect(rfb.target).toBe(target);
		expect(rfb.channel).toBe(ws);
		expect(rfb.focusOnClick).toBe(true);
		expect(onStatus).toHaveBeenCalledWith("connecting", undefined);
		rfb.dispatchEvent(new CustomEvent("connect"));
		expect(onStatus).toHaveBeenLastCalledWith("connected", undefined);
	});

	it("缩放：follow = resizeSession + scaleViewport；fit = 只 scaleViewport；都不裁剪", async () => {
		const { s, rfb } = await connected();
		// 连接前的设置在 RFB 建好时生效（默认 follow）
		expect(rfb.resizeSession).toBe(true);
		expect(rfb.scaleViewport).toBe(true);
		expect(rfb.clipViewport).toBe(false);
		s.setScale("fit");
		expect(rfb.resizeSession).toBe(false);
		expect(rfb.scaleViewport).toBe(true);
		s.setScale("follow");
		expect(rfb.resizeSession).toBe(true);
	});

	it("连接前设的只读与缩放在 RFB 建好时生效", async () => {
		const { s } = open();
		s.setViewOnly(true);
		s.setScale("fit");
		await vi.waitFor(() => expect(rfbs).toHaveLength(1));
		expect(rfbs[0]?.viewOnly).toBe(true);
		expect(rfbs[0]?.resizeSession).toBe(false);
	});

	it("paste / sendKey 透传；只读时不发键", async () => {
		const { s, rfb } = await connected();
		s.paste("你好");
		expect(rfb.clipboardPasteFrom).toHaveBeenCalledWith("你好");
		s.sendKey(0x61);
		expect(rfb.sendKey).toHaveBeenLastCalledWith(0x61, null);
		s.sendKey(0xffe3, true);
		expect(rfb.sendKey).toHaveBeenLastCalledWith(0xffe3, null, true);
		s.setViewOnly(true);
		expect(rfb.viewOnly).toBe(true);
		s.sendKey(0x62);
		expect(rfb.sendKey).toHaveBeenCalledTimes(2);
	});

	it("剪贴板事件转成 onClipboard；断开带上 WebSocket 的关闭码", async () => {
		const { onClipboard, onStatus, rfb, ws } = await connected();
		rfb.dispatchEvent(
			new CustomEvent("clipboard", { detail: { text: "来自桌面" } }),
		);
		expect(onClipboard).toHaveBeenCalledWith("来自桌面");
		ws.serverClose(4429);
		expect(onStatus).toHaveBeenLastCalledWith("closed", 4429);
	});

	it("票据失败 → closed（无关闭码）", async () => {
		vi.stubGlobal(
			"fetch",
			vi.fn(async () =>
				Response.json({ code: "x", message: "x" }, { status: 500 }),
			),
		);
		const { onStatus } = open();
		await vi.waitFor(() =>
			expect(onStatus).toHaveBeenLastCalledWith("closed", undefined),
		);
		expect(rfbs).toHaveLength(0);
	});

	it("close 断开 RFB，之后不再回调", async () => {
		const { s, onStatus, rfb } = await connected();
		const n = onStatus.mock.calls.length;
		s.close();
		expect(rfb.disconnect).toHaveBeenCalled();
		expect(onStatus).toHaveBeenCalledTimes(n);
	});
});
