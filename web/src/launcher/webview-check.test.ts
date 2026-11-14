/** 旧 WebView 提示（public/webview-check.js）：读源码在 jsdom 里执行，假 UA 下判定插入与否。 */
import { afterEach, describe, expect, it } from "vitest";
// 源码原文（vite 的 ?raw），不经构建处理
import SOURCE from "../../public/webview-check.js?raw";

const ORIGINAL_UA = Object.getOwnPropertyDescriptor(navigator, "userAgent");

function runWithUa(ua: string) {
	Object.defineProperty(navigator, "userAgent", {
		value: ua,
		configurable: true,
	});
	// 与浏览器里经典脚本一样在全局作用域执行
	new Function(SOURCE)();
}

const androidUa = (major: number) =>
	`Mozilla/5.0 (Linux; Android 13; Pixel 7 Build/TQ3A; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/${major}.0.5481.153 Mobile Safari/537.36`;

afterEach(() => {
	document.getElementById("webview-outdated")?.remove();
	if (ORIGINAL_UA) Object.defineProperty(navigator, "userAgent", ORIGINAL_UA);
	else Reflect.deleteProperty(navigator, "userAgent");
});

describe("webview-check.js", () => {
	it("Chrome 110：在 body 末尾插入中英提示", () => {
		runWithUa(androidUa(110));
		const box = document.getElementById("webview-outdated");
		expect(box).not.toBeNull();
		expect(document.body.lastElementChild).toBe(box);
		expect(box?.textContent).toContain(
			"系统 WebView 版本过旧，请在应用商店更新 Android System WebView",
		);
		expect(box?.textContent).toContain(
			"Your system WebView is outdated. Update Android System WebView from the app store.",
		);
	});

	it("Chrome 111：不插入", () => {
		runWithUa(androidUa(111));
		expect(document.getElementById("webview-outdated")).toBeNull();
	});

	it("非 Chrome UA：不插入", () => {
		runWithUa(
			"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148",
		);
		expect(document.getElementById("webview-outdated")).toBeNull();
	});
});
