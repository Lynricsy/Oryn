/** HTML 视图：骨架、开帧与主题片段、高度跟随、prompt 只交付一次、已作答时忽略、不可回传时暂存、开帧失败的错误态。 */
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { decodeThemeFragment } from "../../../sandbox/view/messages";
import { HtmlView, type HtmlViewProps, OPEN_RETRY_MS } from "./HtmlView";

const ANSWER = { block: "html", kind: "prompt" };

function props(over: Partial<HtmlViewProps> = {}): HtmlViewProps {
	return {
		toolCallId: `call_${Math.random().toString(36).slice(2)}`,
		spec: { libs: [] },
		mount: true,
		openFrame: vi.fn(async () => "http://a-tok.localhost:7600/"),
		answered: false,
		interactive: true,
		onAnswer: vi.fn(async () => {}),
		...over,
	};
}

/** 等帧挂上、模拟加载完成，返回通道与「页面发来消息」。 */
async function frameOf() {
	const frame = (await screen.findByTitle("HTML 视图")) as HTMLIFrameElement;
	const win = frame.contentWindow as Window;
	const post = vi.spyOn(win, "postMessage").mockImplementation(() => {});
	act(() => {
		frame.dispatchEvent(new Event("load"));
	});
	const hello = post.mock.calls.find(
		(c) => (c[0] as { kind: string }).kind === "hello",
	)?.[0] as { channel: string };
	const fromPage = (body: Record<string, unknown>) =>
		act(() => {
			window.dispatchEvent(
				new MessageEvent("message", {
					data: { v: 1, channel: hello.channel, ...body },
					source: win,
				}),
			);
		});
	return { frame, post, fromPage };
}

afterEach(() => {
	vi.useRealTimers();
	vi.restoreAllMocks();
});

describe("HtmlView", () => {
	it("mount 为假时只显示骨架，不申请帧", () => {
		const p = props({ mount: false, spec: { libs: [], title: "计算器" } });
		render(<HtmlView {...p} />);
		expect(screen.getByTestId("html-view-skeleton")).toBeInTheDocument();
		expect(screen.getByText("计算器")).toBeInTheDocument();
		expect(p.openFrame).not.toHaveBeenCalled();
	});

	it("开帧后 iframe 带沙箱与主题片段，load 后发 hello 与 theme", async () => {
		document.documentElement.style.setProperty("--brand", "rgb(1, 2, 3)");
		const p = props();
		render(<HtmlView {...p} />);
		const { frame, post } = await frameOf();
		expect(p.openFrame).toHaveBeenCalledWith(p.toolCallId, false);
		expect(frame.getAttribute("sandbox")).toBe("allow-scripts");
		expect(frame.getAttribute("referrerpolicy")).toBe("no-referrer");
		const src = frame.getAttribute("src") ?? "";
		expect(src.startsWith("http://a-tok.localhost:7600/#theme=")).toBe(true);
		const theme = decodeThemeFragment(src.slice(src.indexOf("#")));
		expect(theme?.vars["--accent"]).toBe("rgb(1, 2, 3)");
		expect(post.mock.calls.map((c) => (c[0] as { kind: string }).kind)).toEqual(
			["hello", "theme"],
		);
		document.documentElement.style.removeProperty("--brand");
	});

	it("高度跟随 resize，夹在 120–1600；固定高度时不跟随", async () => {
		const p = props();
		const { rerender } = render(<HtmlView {...p} />);
		const { frame, fromPage } = await frameOf();
		const box = frame.parentElement as HTMLElement;
		fromPage({ kind: "resize", height: 50 });
		expect(box.style.height).toBe("120px");
		fromPage({ kind: "resize", height: 480.4 });
		expect(box.style.height).toBe("480px");
		fromPage({ kind: "resize", height: 99_999 });
		expect(box.style.height).toBe("1600px");
		rerender(<HtmlView {...p} spec={{ libs: [], height: 300 }} />);
		fromPage({ kind: "resize", height: 500 });
		expect(box.style.height).toBe("300px");
	});

	it("prompt 每次调用只交付第一条", async () => {
		const p = props();
		render(<HtmlView {...p} />);
		const { fromPage } = await frameOf();
		fromPage({ kind: "prompt", text: "选 A" });
		fromPage({ kind: "prompt", text: "选 B" });
		expect(p.onAnswer).toHaveBeenCalledOnce();
		expect(p.onAnswer).toHaveBeenCalledWith(ANSWER, "选 A");
	});

	it("已作答（含在途）时忽略 prompt", async () => {
		const p = props({ answered: true });
		render(<HtmlView {...p} />);
		const { fromPage } = await frameOf();
		fromPage({ kind: "prompt", text: "再来" });
		expect(p.onAnswer).not.toHaveBeenCalled();
		expect(screen.getByTestId("html-view-sent")).toBeInTheDocument();
	});

	it("不可回传期间暂存第一条，可回传时交付", async () => {
		const p = props({ interactive: false });
		const { rerender } = render(<HtmlView {...p} />);
		const { fromPage } = await frameOf();
		fromPage({ kind: "prompt", text: "先到" });
		fromPage({ kind: "prompt", text: "后到" });
		expect(p.onAnswer).not.toHaveBeenCalled();
		rerender(<HtmlView {...p} interactive />);
		expect(p.onAnswer).toHaveBeenCalledOnce();
		expect(p.onAnswer).toHaveBeenCalledWith(ANSWER, "先到");
	});

	it("交付失败时显示错误，之后的 prompt 可以再交付", async () => {
		const onAnswer = vi
			.fn<NonNullable<HtmlViewProps["onAnswer"]>>()
			.mockRejectedValueOnce(new Error("离线"))
			.mockResolvedValue();
		const p = props({ onAnswer });
		render(<HtmlView {...p} />);
		const { fromPage } = await frameOf();
		fromPage({ kind: "prompt", text: "一" });
		expect(await screen.findByRole("alert")).toHaveTextContent("离线");
		fromPage({ kind: "prompt", text: "二" });
		expect(onAnswer).toHaveBeenCalledTimes(2);
		expect(onAnswer).toHaveBeenLastCalledWith(ANSWER, "二");
	});

	it("开帧失败退避重试 3 次后显示可重试的错误态", async () => {
		vi.useFakeTimers();
		const openFrame = vi
			.fn<NonNullable<HtmlViewProps["openFrame"]>>()
			.mockRejectedValue(new Error("404"));
		render(<HtmlView {...props({ openFrame })} />);
		for (const ms of OPEN_RETRY_MS)
			await act(async () => {
				await vi.advanceTimersByTimeAsync(ms);
			});
		expect(openFrame).toHaveBeenCalledTimes(4);
		expect(screen.getByTestId("html-view-error")).toBeInTheDocument();
		openFrame.mockResolvedValue("http://a-tok.localhost:7600/");
		await act(async () => {
			fireEvent.click(screen.getByRole("button", { name: /重试/ }));
		});
		expect(openFrame).toHaveBeenLastCalledWith(expect.any(String), true);
	});

	it("没有 openFrame 时直接显示错误态", () => {
		render(<HtmlView {...props({ openFrame: undefined })} />);
		expect(screen.getByTestId("html-view-error")).toHaveTextContent(
			"这里无法显示这个视图",
		);
	});
});
