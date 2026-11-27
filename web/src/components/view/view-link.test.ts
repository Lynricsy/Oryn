/** HTML 视图的帧通道：只认自己帧与通道的消息，load 后发 hello。 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { ViewLink } from "./view-link";

function setup() {
	const frame = document.createElement("iframe");
	document.body.appendChild(frame);
	const win = frame.contentWindow as Window;
	const post = vi.fn();
	vi.spyOn(win, "postMessage").mockImplementation(post);
	const handlers = { ready: vi.fn(), resize: vi.fn(), prompt: vi.fn() };
	const link = new ViewLink(frame, handlers);
	const fromPage = (data: unknown, source: unknown = win) =>
		window.dispatchEvent(
			new MessageEvent("message", {
				data,
				source: source as MessageEventSource,
			}),
		);
	return { frame, win, post, handlers, link, fromPage };
}

afterEach(() => {
	vi.restoreAllMocks();
	document.body.innerHTML = "";
});

describe("ViewLink", () => {
	it("load 后发 hello，通道是 16 位十六进制", () => {
		const { frame, post, link } = setup();
		expect(link.channel).toMatch(/^[0-9a-f]{16}$/);
		frame.dispatchEvent(new Event("load"));
		expect(post).toHaveBeenCalledWith(
			{ v: 1, channel: link.channel, kind: "hello" },
			"*",
		);
	});

	it("只处理来自自己帧、通道一致的消息", () => {
		const { link, handlers, fromPage } = setup();
		const other = document.createElement("iframe");
		document.body.appendChild(other);
		const ch = link.channel;
		fromPage(
			{ v: 1, channel: ch, kind: "prompt", text: "别的帧" },
			other.contentWindow,
		);
		fromPage({ v: 1, channel: ch, kind: "prompt", text: "父页面自己" }, window);
		fromPage({
			v: 1,
			channel: "0123456789abcdef",
			kind: "prompt",
			text: "错通道",
		});
		fromPage({ v: 2, channel: ch, kind: "prompt", text: "错版本" });
		fromPage({ v: 1, channel: ch, kind: "prompt", text: "   " });
		fromPage({ v: 1, channel: ch, kind: "resize", height: Number.NaN });
		expect(handlers.prompt).not.toHaveBeenCalled();
		expect(handlers.resize).not.toHaveBeenCalled();

		fromPage({ v: 1, channel: ch, kind: "ready" });
		fromPage({ v: 1, channel: ch, kind: "resize", height: 320 });
		fromPage({ v: 1, channel: ch, kind: "prompt", text: "好" });
		expect(handlers.ready).toHaveBeenCalledOnce();
		expect(handlers.resize).toHaveBeenCalledWith(320);
		expect(handlers.prompt).toHaveBeenCalledWith("好");
	});

	it("超过 64 KiB 的消息丢弃；prompt 截到 4000 字", () => {
		const { link, handlers, fromPage } = setup();
		const ch = link.channel;
		fromPage({ v: 1, channel: ch, kind: "prompt", text: "x".repeat(70_000) });
		expect(handlers.prompt).not.toHaveBeenCalled();
		fromPage({ v: 1, channel: ch, kind: "prompt", text: "y".repeat(5000) });
		expect(handlers.prompt).toHaveBeenCalledWith("y".repeat(4000));
	});

	it("dispose 后不再处理", () => {
		const { link, handlers, fromPage } = setup();
		link.dispose();
		fromPage({ v: 1, channel: link.channel, kind: "ready" });
		expect(handlers.ready).not.toHaveBeenCalled();
	});

	it("帧再次 load（页面导航走了）后通道作废：不重发 hello、不收消息", () => {
		const { frame, post, link, fromPage } = setup();
		const navigated = vi.fn();
		const prompt = vi.fn();
		const relink = new ViewLink(frame, { navigated, prompt });
		link.dispose();
		frame.dispatchEvent(new Event("load"));
		expect(post).toHaveBeenCalledTimes(1);
		frame.dispatchEvent(new Event("load"));
		expect(navigated).toHaveBeenCalledOnce();
		expect(post).toHaveBeenCalledTimes(1);
		fromPage({ v: 1, channel: relink.channel, kind: "prompt", text: "冒充" });
		expect(prompt).not.toHaveBeenCalled();
		relink.post({ kind: "hello" });
		expect(post).toHaveBeenCalledTimes(1);
	});
});
