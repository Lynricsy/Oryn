/**
 * `@novnc/novnc` 1.7.0 的最小类型（包本身不带声明）：只声明 Desktop 面板用到的部分。
 * 事件：`connect`、`disconnect`（`detail.clean`）、`clipboard`（`detail.text`）。
 */
declare module "@novnc/novnc" {
	export default class RFB extends EventTarget {
		constructor(
			target: HTMLElement,
			urlOrChannel: string | WebSocket,
			options?: { shared?: boolean; wsProtocols?: string[] },
		);
		viewOnly: boolean;
		focusOnClick: boolean;
		clipViewport: boolean;
		scaleViewport: boolean;
		resizeSession: boolean;
		background: string;
		qualityLevel: number;
		compressionLevel: number;
		disconnect(): void;
		focus(options?: FocusOptions): void;
		blur(): void;
		sendKey(keysym: number, code: string | null, down?: boolean): void;
		clipboardPasteFrom(text: string): void;
	}
}
