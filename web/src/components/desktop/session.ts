/**
 * 桌面会话（M8 4.3）：一次性票据 → `/api/v1/orbs/<id>/desktop/ws` → noVNC（RFB over WebSocket）。
 * - noVNC 在这里按需动态导入（只进 Desktop 的按需分块，不进首屏；它的 `core/util/browser.js` 有顶层 await）。
 * - WebSocket 由这里创建再交给 RFB，好拿到关闭码（4401 / 4409 / 4410 / 4429 / 4502 / 4503）；RFB 的 `disconnect` 事件只带 `clean`。
 * - 不在这里重连：关闭后由面板决定是否重试。`close()` 之后不再回调。
 * - `VITE_MOCK === "1"`：不连后端，往 `target` 里放一张画了占位桌面的 `<canvas>` 并立即 `connected`（截图与无后端演示）。
 */
import type RFB from "@novnc/novnc";
import { api, unwrap } from "../../api/client";
import { indexStore } from "../../state/stores";
import type { DesktopScale } from "../../state/ui";

export type DesktopStatus = "connecting" | "connected" | "closed";

export interface DesktopSession {
	setViewOnly(v: boolean): void;
	setScale(mode: DesktopScale): void;
	paste(text: string): void;
	/** `down` 省略 = 按下再抬起 */
	sendKey(keysym: number, down?: boolean): void;
	focus(): void;
	/** 点 / 触画面时是否把焦点抢到画布（软键盘打开时关掉，保住软键盘输入框的焦点） */
	setFocusOnClick(on: boolean): void;
	close(): void;
}

export interface DesktopHandlers {
	onStatus(s: DesktopStatus, closeCode?: number): void;
	onClipboard(text: string): void;
}

/** 桌面 WS 地址（与 `/api/v1/ws` 同源同协议）。 */
export function desktopSocketUrl(orbId: string, ticket: string): string {
	const proto = location.protocol === "https:" ? "wss:" : "ws:";
	return `${proto}//${location.host}/api/v1/orbs/${encodeURIComponent(orbId)}/desktop/ws?ticket=${encodeURIComponent(ticket)}`;
}

/** 按缩放模式设置 RFB：跟随 = 远端分辨率随容器变化；适应 = 远端不变、本地等比缩放。两者都不裁剪。 */
export function applyScale(rfb: RFB, mode: DesktopScale) {
	rfb.clipViewport = false;
	rfb.scaleViewport = true;
	rfb.resizeSession = mode === "follow";
}

export function openDesktopSession(
	target: HTMLElement,
	orbId: string,
	h: DesktopHandlers,
): DesktopSession {
	if (import.meta.env.VITE_MOCK === "1") return mockSession(target, orbId, h);
	let rfb: RFB | null = null;
	let closed = false;
	let viewOnly = false;
	let focusOnClick = true;
	let scale: DesktopScale = "follow";
	const emit = (s: DesktopStatus, code?: number) => {
		if (!closed) h.onStatus(s, code);
	};
	emit("connecting");
	let ws: WebSocket | undefined;
	void (async () => {
		try {
			const { ticket } = await unwrap(api.POST("/api/v1/ws-ticket"));
			if (closed) return;
			// 动态导入：noVNC 的 browser.js 有顶层 await，且应只在打开 Desktop 面板时才下载
			const { default: Rfb } = await import("@novnc/novnc");
			if (closed) return;
			ws = new WebSocket(desktopSocketUrl(orbId, ticket));
			ws.binaryType = "arraybuffer";
			let code: number | undefined;
			// 先于 RFB 登记：RFB 在同一个 close 事件里派发 disconnect 时，关闭码已经记下
			ws.addEventListener("close", (e) => {
				code = e.code;
			});
			const r = new Rfb(target, ws, { shared: true });
			rfb = r;
			r.focusOnClick = focusOnClick;
			r.background = "var(--inset)";
			r.viewOnly = viewOnly;
			applyScale(r, scale);
			r.addEventListener("connect", () => emit("connected"));
			r.addEventListener("disconnect", () => {
				rfb = null;
				emit("closed", code);
			});
			r.addEventListener("clipboard", (e) => {
				const text = (e as CustomEvent<{ text?: string }>).detail?.text;
				if (!closed && typeof text === "string") h.onClipboard(text);
			});
		} catch {
			// RFB 构造失败时 WS 已经建立：关掉它，免得每次重试都在服务端多占一个观看者名额
			if (!rfb) ws?.close();
			emit("closed");
		}
	})();
	return {
		setViewOnly(v) {
			viewOnly = v;
			if (rfb) rfb.viewOnly = v;
		},
		setScale(mode) {
			scale = mode;
			if (rfb) applyScale(rfb, mode);
		},
		paste(text) {
			rfb?.clipboardPasteFrom(text);
		},
		sendKey(keysym, down) {
			if (!rfb || viewOnly) return;
			if (down === undefined) rfb.sendKey(keysym, null);
			else rfb.sendKey(keysym, null, down);
		},
		focus() {
			rfb?.focus({ preventScroll: true });
		},
		setFocusOnClick(on) {
			focusOnClick = on;
			if (rfb) rfb.focusOnClick = on;
		},
		close() {
			if (closed) return;
			closed = true;
			const r = rfb;
			rfb = null;
			r?.disconnect();
		},
	};
}

// ---------------------------------------------------------------- mock

/** 占位桌面的远端分辨率（与 orbd 的初始 1280x800 一致）。 */
const MOCK_W = 1280;
const MOCK_H = 800;

function token(name: string): string {
	return getComputedStyle(document.documentElement).getPropertyValue(name);
}

/** 画一块占位桌面：浏览器运行时是一扇最大化的浏览器窗口（标签条、地址栏、几行正文），否则是空桌面。 */
function drawMock(canvas: HTMLCanvasElement, browser: boolean) {
	const g = canvas.getContext("2d");
	if (!g) return;
	g.fillStyle = token("--bg");
	g.fillRect(0, 0, MOCK_W, MOCK_H);
	if (!browser) return;
	g.fillStyle = token("--raised");
	g.fillRect(0, 0, MOCK_W, 84);
	g.fillStyle = token("--panel");
	g.fillRect(12, 8, 240, 34);
	g.fillRect(0, 84, MOCK_W, MOCK_H - 84);
	g.fillStyle = token("--inset");
	g.fillRect(96, 48, MOCK_W - 192, 28);
	g.fillStyle = token("--text-faint");
	g.font = "16px sans-serif";
	g.fillText("m8-home", 28, 31);
	g.fillText("http://127.0.0.1:8765/", 112, 67);
	g.fillStyle = token("--text");
	g.font = "600 34px sans-serif";
	g.fillText("m8-home", 120, 180);
	g.fillStyle = token("--line-strong");
	for (let i = 0; i < 6; i++) g.fillRect(120, 230 + i * 34, 860 - i * 90, 12);
	g.fillStyle = token("--brand");
	g.fillRect(120, 470, 1040, 220);
	g.fillStyle = token("--brand-fg");
	g.font = "600 28px sans-serif";
	g.fillText("click me", 560, 590);
}

function mockSession(
	target: HTMLElement,
	orbId: string,
	h: DesktopHandlers,
): DesktopSession {
	let closed = false;
	const canvas = document.createElement("canvas");
	canvas.width = MOCK_W;
	canvas.height = MOCK_H;
	canvas.dataset.mock = "1";
	// 与 noVNC 的画布一致：可编程聚焦、不进 Tab 序列
	canvas.tabIndex = -1;
	canvas.style.width = "100%";
	canvas.style.height = "100%";
	canvas.style.objectFit = "contain";
	target.appendChild(canvas);
	const running = () =>
		indexStore.getState().orbs[orbId]?.desktop?.browser === "running";
	let shown = running();
	drawMock(canvas, shown);
	const unsub = indexStore.subscribe(() => {
		const next = running();
		if (next !== shown) {
			shown = next;
			drawMock(canvas, shown);
		}
	});
	h.onStatus("connecting");
	queueMicrotask(() => {
		if (closed) return;
		h.onStatus("connected");
		// 浏览器开着时给一条桌面剪贴板，弹层截图能看到「复制到本机」
		if (shown) h.onClipboard("https://github.com/Lynricsy/Oryn/pull/42");
	});
	return {
		setViewOnly() {},
		setScale() {},
		paste() {},
		sendKey() {},
		focus() {
			canvas.focus();
		},
		setFocusOnClick() {},
		close() {
			if (closed) return;
			closed = true;
			unsub();
			canvas.remove();
		},
	};
}
