/**
 * browser 卡片：open 的地址栏、call 的调用链、带窗口外框的截图与保存位置、tabs 的页面列表、出错的错误块；
 * `app` 新版是字符串、旧版是对象，都认；残缺 details 不崩。
 */
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ToolView } from "../ToolView";

const PNG_1PX =
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";

describe("browser 卡片", () => {
	it("open：输入段是地址栏，选项是徽标；app 字符串与旧的对象都显示", () => {
		const { container, unmount } = render(
			<ToolView
				name="browser"
				args={{
					action: "open",
					url: "https://example.com/docs?q=1",
					app: "relay",
					viewport: { width: 1280, height: 800 },
					wait_until: "load",
				}}
				result={{
					content: [{ type: "text", text: "Opened" }],
					details: {
						action: "open",
						name: "main",
						viewport: { width: 1280, height: 800 },
					},
				}}
				defaultOpen
			/>,
		);
		const input = container.querySelector(".tv-sec--in");
		expect(input?.querySelector(".tv-br-bar")).toHaveTextContent(
			"example.com/docs?q=1",
		);
		expect(input).toHaveTextContent("relay");
		expect(input).toHaveTextContent("1280×800");
		expect(input).toHaveTextContent('"wait_until": "load"');
		expect(container.querySelector(".tv-meta")).toHaveTextContent("1280×800");
		unmount();
		render(
			<ToolView
				name="puppeteer"
				args={{ action: "open", app: { cdp_url: "ws://127.0.0.1:9222" } }}
				defaultOpen
			/>,
		);
		expect(document.body).toHaveTextContent("ws://127.0.0.1:9222");
	});

	it("call：调用链逐步编号；截图在窗口外框里，保存位置另列，读数给截图张数", () => {
		const { container } = render(
			<ToolView
				name="browser"
				args={{
					action: "call",
					chain: [
						{ method: "goto", args: ["http://x.test/"] },
						{ method: "click", args: ["#go"] },
					],
				}}
				result={{
					content: [{ type: "image", data: PNG_1PX, mimeType: "image/png" }],
					details: {
						action: "call",
						name: "main",
						url: "http://x.test/done",
						screenshots: [
							{
								dest: "/tmp/s.png",
								mimeType: "image/png",
								bytes: 2048,
								width: 10,
								height: 10,
							},
						],
					},
				}}
				defaultOpen
			/>,
		);
		const steps = container.querySelectorAll(".tv-br-step");
		expect(steps).toHaveLength(2);
		expect(steps[1]).toHaveTextContent('click("#go")');
		expect(container.querySelector(".tv-br-frame img.tv-img")).not.toBeNull();
		expect(container.querySelector(".tv-br-frame")).toHaveTextContent(
			"x.test/done",
		);
		expect(container.querySelector(".tv-br-shots")).toHaveTextContent(
			"/tmp/s.png",
		);
		expect(container.querySelector(".tv-meta")).toHaveTextContent("1 张截图");
	});

	it("tabs：页面列表（标题、地址、标签页名），读数给页面数", () => {
		const { container } = render(
			<ToolView
				name="browser"
				args={{ action: "tabs" }}
				result={{
					content: [],
					details: {
						action: "tabs",
						value: [
							{ name: "main", url: "http://a.test/", title: "A" },
							{ name: "docs", url: "https://b.test/x", title: "B" },
						],
					},
				}}
				defaultOpen
			/>,
		);
		expect(container.querySelectorAll(".tv-br-page")).toHaveLength(2);
		expect(container.querySelector(".tv-meta")).toHaveTextContent("2 个页面");
		expect(screen.getByText("docs")).toBeInTheDocument();
	});

	it("出错：错误首行醒目", () => {
		const { container } = render(
			<ToolView
				name="browser"
				args={{ action: "run", code: "await page.click('#x')" }}
				result={{
					content: [
						{ type: "text", text: "TimeoutError: waiting\nCall log: …" },
					],
					isError: true,
				}}
				defaultOpen
			/>,
		);
		expect(container.querySelector(".tv-errblock-head")).toHaveTextContent(
			"TimeoutError: waiting",
		);
	});

	it.each<[string, Record<string, unknown>, unknown]>([
		["action 不认识", { action: 3 }, { action: 7 }],
		["截图残缺", { action: "run" }, { screenshots: [null, { dest: 3 }] }],
		["value 不是页面", { action: "tabs" }, { value: [1, "x"] }],
		[
			"viewport 残缺",
			{ action: "open", viewport: { width: "x" } },
			{ viewport: 1 },
		],
	])("残缺输入不崩（%s）", (_label, args, details) => {
		expect(() =>
			render(
				<ToolView
					name="browser"
					args={args}
					result={{ content: [], details }}
					defaultOpen
				/>,
			),
		).not.toThrow();
	});
});
