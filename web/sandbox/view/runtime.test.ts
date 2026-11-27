/** HTML 视图运行时：首帧主题、基础样式、握手、sendPrompt 只发一次、theme 消息。 */
import { describe, expect, it, vi } from "vitest";
import { encodeThemeFragment } from "./messages";
import { install } from "./runtime";

describe("视图运行时", () => {
	it("按片段同步应用主题，hello 后报 ready 与高度，prompt 只发第一条", () => {
		window.location.hash = encodeThemeFragment({
			scheme: "dark",
			vars: { "--accent": "red", "--c1": "rgb(1, 2, 3)" },
		});
		const host = document.createElement("iframe");
		document.body.appendChild(host);
		const parent = host.contentWindow as Window;
		const post = vi.spyOn(parent, "postMessage").mockImplementation(() => {});
		install(window, parent, ":root{--x:1}");

		const root = document.documentElement;
		expect(root.style.getPropertyValue("--accent")).toBe("red");
		expect(root.getAttribute("data-scheme")).toBe("dark");
		expect(document.head.querySelector("style")?.textContent).toBe(
			":root{--x:1}",
		);

		const api = (window as unknown as { oryn: { sendPrompt(t: string): void } })
			.oryn;
		// 通道定下之前先暂存
		api.sendPrompt("第一条");
		api.sendPrompt("第二条");
		expect(post).not.toHaveBeenCalled();

		const channel = "0123456789abcdef";
		const fromParent = (data: unknown, source: Window = parent) =>
			window.dispatchEvent(new MessageEvent("message", { data, source }));
		fromParent({ v: 1, channel, kind: "theme", scheme: "light", vars: {} });
		expect(post).not.toHaveBeenCalled();
		fromParent({ v: 1, channel, kind: "hello" }, window);
		expect(post).not.toHaveBeenCalled();

		fromParent({ v: 1, channel, kind: "hello" });
		const kinds = post.mock.calls.map((c) => (c[0] as { kind: string }).kind);
		expect(kinds).toEqual(["ready", "resize", "prompt"]);
		expect(post.mock.calls[2]?.[0]).toEqual({
			v: 1,
			channel,
			kind: "prompt",
			text: "第一条",
		});

		api.sendPrompt("第三条");
		expect(post).toHaveBeenCalledTimes(3);

		fromParent({
			v: 1,
			channel,
			kind: "theme",
			scheme: "light",
			vars: { "--accent": "blue;}body{display:none" },
		});
		expect(root.style.getPropertyValue("--accent")).toBe(
			"bluebodydisplay:none",
		);
		expect(root.getAttribute("data-scheme")).toBe("light");
		fromParent({
			v: 1,
			channel: "fedcba9876543210",
			kind: "theme",
			scheme: "dark",
			vars: {},
		});
		expect(root.getAttribute("data-scheme")).toBe("light");
	});
});
