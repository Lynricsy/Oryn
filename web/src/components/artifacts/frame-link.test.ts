import { afterEach, describe, expect, it, vi } from "vitest";
import { FrameLink } from "./frame-link";

const STYLE = {
	color: "rgb(0, 0, 0)",
	"background-color": "rgba(0, 0, 0, 0)",
	"font-size": "16px",
	"font-weight": "400",
	"text-align": "start",
	padding: "0px",
	"border-radius": "0px",
};

function setup() {
	const frame = document.createElement("iframe");
	document.body.appendChild(frame);
	const win = frame.contentWindow as Window;
	const post = vi.fn();
	vi.spyOn(win, "postMessage").mockImplementation(post);
	const link = new FrameLink(frame);
	const kinds = () =>
		post.mock.calls.map((c) => (c[0] as { kind: string }).kind);
	const load = () => {
		post.mockClear();
		frame.dispatchEvent(new Event("load"));
		return post.mock.calls.map((c) => c[0] as Record<string, unknown>);
	};
	const fromPage = (body: Record<string, unknown>) =>
		window.dispatchEvent(
			new MessageEvent("message", {
				data: { v: 1, channel: link.channel, ...body },
				source: win,
			}),
		);
	return { frame, link, post, kinds, load, fromPage };
}

afterEach(() => {
	vi.restoreAllMocks();
	document.body.innerHTML = "";
});

describe("FrameLink 的选择状态重发", () => {
	it("每次 load 后在 hello 之后重发最近一次的 select-mode、select-pins、select-source", () => {
		const { link, load } = setup();
		link.post({ kind: "select-mode", mode: "select", accent: "#000000" });
		link.post({ kind: "select-mode", mode: "comment", accent: "#2563eb" });
		link.post({
			kind: "select-pins",
			pins: [{ n: 1, src: 3, answered: false }],
		});
		link.post({ kind: "select-source", src: 42 });
		link.post({ kind: "preview-style", style: { color: "#ff0000" } });
		const first = load();
		expect(first.map((m) => m.kind)).toEqual([
			"hello",
			"select-mode",
			"select-pins",
			"select-source",
		]);
		expect(first[1]).toMatchObject({ mode: "comment", accent: "#2563eb" });
		expect(first[3]).toMatchObject({ src: 42 });
		// 第二次加载照样重发
		expect(load().map((m) => m.kind)).toEqual([
			"hello",
			"select-mode",
			"select-pins",
			"select-source",
		]);
	});

	it("查看器的 anchor-pins 也在每次 load 后重发", () => {
		const { link, load } = setup();
		link.post({ kind: "select-mode", mode: "comment", accent: "" });
		const anchor = {
			page: { number: 1, name: "" },
			x: 0.5,
			y: 0.5,
			range: null,
			block: null,
			text: "",
		};
		link.post({
			kind: "anchor-pins",
			pins: [{ n: 1, answered: false, anchor }],
		});
		const msgs = load();
		expect(msgs.map((m) => m.kind)).toEqual([
			"hello",
			"select-mode",
			"anchor-pins",
		]);
		expect(msgs[2]).toMatchObject({ pins: [{ n: 1, anchor }] });
	});

	it("页面里点选的元素成为重载后要重选的那个；没有源码偏移或清掉选中就不重发", () => {
		const { link, load, fromPage } = setup();
		link.post({ kind: "select-mode", mode: "select", accent: "" });
		fromPage({
			kind: "select",
			selection: {
				path: "index.html",
				src: 7,
				tag: "h1",
				text: "",
				textOnly: true,
				style: STYLE,
			},
		});
		expect(load().at(-1)).toMatchObject({ kind: "select-source", src: 7 });

		// select-locate 的回应不改记住的选中
		fromPage({
			kind: "select",
			selection: null,
			located: { src: 9, box: null },
		});
		expect(load().at(-1)).toMatchObject({ kind: "select-source", src: 7 });

		link.post({ kind: "select-source", src: null });
		expect(load().map((m) => m.kind)).toEqual(["hello", "select-mode"]);

		link.post({ kind: "select-source", src: 5 });
		link.post({ kind: "select-mode", mode: null, accent: "" });
		const after = load();
		expect(after.map((m) => m.kind)).toEqual(["hello", "select-mode"]);
		expect(after[1]).toMatchObject({ mode: null });
	});

	it("没有发过选择类消息时 load 只发 hello", () => {
		const { kinds, load } = setup();
		load();
		expect(kinds()).toEqual(["hello"]);
	});
});
