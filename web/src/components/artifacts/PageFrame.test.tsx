/**
 * PageFrame：沙箱属性、帧的申请与关闭、跟随最新版本时的「先 keep-scroll 再换 src 再 restore-scroll」、被拦截的跳转。
 */
import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type ArtifactEvent, realtime } from "../../realtime/socket";
import { ARTIFACT_SANDBOX, PageFrame } from "./PageFrame";

const ID = "art_01JDNAWEB0000000000000000A";
const FRAME_URL = "https://a-abcdefghijklmnopqrstuvwxyz.example.test/";
const TOKEN = "abcdefghijklmnopqrstuvwxyz";

let calls: { method: string; path: string }[];

beforeEach(() => {
	calls = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const url = new URL(req.url);
			calls.push({ method: req.method, path: url.pathname });
			if (url.pathname === `/api/v1/artifacts/${ID}/frames`)
				return Response.json({ url: FRAME_URL, token: TOKEN }, { status: 201 });
			if (url.pathname === `/api/v1/artifact-frames/${TOKEN}`)
				return new Response(null, { status: 204 });
			return new Response(null, { status: 404 });
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

/** 从帧窗口的 postMessage 记录里取出发给页面的消息。 */
function posted(spy: ReturnType<typeof vi.fn>): Record<string, unknown>[] {
	return spy.mock.calls.map((c) => c[0] as Record<string, unknown>);
}

/** 以帧窗口的身份向父页面发一条消息。 */
function fromPage(frame: HTMLIFrameElement, data: Record<string, unknown>) {
	act(() => {
		window.dispatchEvent(
			new MessageEvent("message", {
				data: { v: 1, ...data },
				source: frame.contentWindow,
			}),
		);
	});
}

async function mountFrame(onBlocked = vi.fn()) {
	let versionCb: ((m: ArtifactEvent) => void) | undefined;
	const onArtifact = realtime.onArtifact.bind(realtime);
	vi.spyOn(realtime, "onArtifact").mockImplementation((id, cb) => {
		versionCb = cb;
		return onArtifact(id, cb);
	});
	const view = render(
		<PageFrame artifactId={ID} title="帧" onBlocked={onBlocked} />,
	);
	const frame = (await screen.findByTestId(
		"artifact-frame",
	)) as HTMLIFrameElement;
	const win = frame.contentWindow;
	if (!win) throw new Error("iframe 没有窗口");
	const post = vi.fn();
	vi.spyOn(win, "postMessage").mockImplementation(post);
	return {
		view,
		frame,
		post,
		onBlocked,
		emitVersion: (version: number) =>
			act(() => versionCb?.({ type: "artifact.version", id: ID, version })),
	};
}

describe("PageFrame", () => {
	it("iframe 的 sandbox 恰为三项、不含 allow-same-origin；allow 为空、不带 referrer", async () => {
		const { frame } = await mountFrame();
		expect(ARTIFACT_SANDBOX).toBe("allow-scripts allow-forms allow-downloads");
		// jsdom 的 iframe.sandbox 不是可迭代的 DOMTokenList，按属性文本拆
		const tokens = (frame.getAttribute("sandbox") ?? "").split(/\s+/);
		expect(tokens.sort()).toEqual(
			["allow-downloads", "allow-forms", "allow-scripts"].sort(),
		);
		expect(frame.getAttribute("sandbox")).not.toContain("allow-same-origin");
		expect(frame.getAttribute("allow")).toBe("");
		expect(frame.getAttribute("referrerpolicy")).toBe("no-referrer");
		expect(frame.getAttribute("src")).toBe(FRAME_URL);
	});

	it("挂载时申请帧，卸载时关掉", async () => {
		const { view } = await mountFrame();
		expect(calls).toContainEqual({
			method: "POST",
			path: `/api/v1/artifacts/${ID}/frames`,
		});
		view.unmount();
		await waitFor(() =>
			expect(calls).toContainEqual({
				method: "DELETE",
				path: `/api/v1/artifact-frames/${TOKEN}`,
			}),
		);
	});

	it("load 后发 hello；收到 artifact.version 先发 keep-scroll，拿到 scroll 才换 src，load 后恢复滚动", async () => {
		const setSrc = vi.spyOn(HTMLIFrameElement.prototype, "src", "set");
		const { frame, post, emitVersion } = await mountFrame();
		act(() => {
			frame.dispatchEvent(new Event("load"));
		});
		const hello = posted(post).find((m) => m.kind === "hello");
		expect(hello).toMatchObject({ v: 1 });
		const channel = String(hello?.channel);
		expect(channel).toMatch(/^[0-9a-f]{16}$/);
		fromPage(frame, { channel, kind: "ready", params: {} });

		post.mockClear();
		setSrc.mockClear();
		emitVersion(2);
		expect(posted(post)).toEqual([{ v: 1, channel, kind: "keep-scroll" }]);
		expect(setSrc).not.toHaveBeenCalled();

		// 别的通道（或别的来源）发来的 scroll 不算
		fromPage(frame, {
			channel: "0000000000000000",
			kind: "scroll",
			x: 0,
			y: 9,
		});
		expect(setSrc).not.toHaveBeenCalled();

		fromPage(frame, { channel, kind: "scroll", x: 0, y: 480 });
		expect(setSrc).toHaveBeenCalledWith(FRAME_URL);

		// 换 src 之后帧里是新的窗口
		const next = vi.fn();
		vi.spyOn(frame.contentWindow as Window, "postMessage").mockImplementation(
			next,
		);
		act(() => {
			frame.dispatchEvent(new Event("load"));
		});
		expect(posted(next)).toEqual([
			{ v: 1, channel, kind: "hello" },
			{ v: 1, channel, kind: "restore-scroll", x: 0, y: 480 },
		]);
	});

	it("页面 300 ms 内不回报滚动位置时直接重载", async () => {
		const setSrc = vi.spyOn(HTMLIFrameElement.prototype, "src", "set");
		const { post, emitVersion } = await mountFrame();
		setSrc.mockClear();
		emitVersion(3);
		expect(posted(post).map((m) => m.kind)).toEqual(["keep-scroll"]);
		await waitFor(() => expect(setSrc).toHaveBeenCalledWith(FRAME_URL), {
			timeout: 1000,
		});
	});

	it("运行时报告被拦截的跳转时交给 onBlocked；别的来源的消息被忽略", async () => {
		const { frame, post, onBlocked } = await mountFrame();
		act(() => {
			frame.dispatchEvent(new Event("load"));
		});
		const channel = String(
			posted(post).find((m) => m.kind === "hello")?.channel,
		);
		act(() => {
			window.dispatchEvent(
				new MessageEvent("message", {
					data: { v: 1, channel, kind: "blocked", url: "https://evil.test/" },
					source: window,
				}),
			);
		});
		expect(onBlocked).not.toHaveBeenCalled();
		fromPage(frame, { channel, kind: "blocked", url: "https://example.com/a" });
		expect(onBlocked).toHaveBeenCalledWith("https://example.com/a");
	});

	it("查看器的 present 交给 onPresent；帧重新加载按退出处理", async () => {
		const onPresent = vi.fn();
		render(
			<PageFrame
				artifactId={ID}
				title="帧"
				onBlocked={vi.fn()}
				onPresent={onPresent}
			/>,
		);
		const frame = (await screen.findByTestId(
			"artifact-frame",
		)) as HTMLIFrameElement;
		const win = frame.contentWindow;
		if (!win) throw new Error("iframe 没有窗口");
		const post = vi.fn();
		vi.spyOn(win, "postMessage").mockImplementation(post);
		act(() => {
			frame.dispatchEvent(new Event("load"));
		});
		expect(onPresent).toHaveBeenLastCalledWith(false);
		const channel = String(
			posted(post).find((m) => m.kind === "hello")?.channel,
		);
		fromPage(frame, { channel, kind: "present", on: true });
		expect(onPresent).toHaveBeenLastCalledWith(true);
		act(() => {
			frame.dispatchEvent(new Event("load"));
		});
		expect(onPresent).toHaveBeenLastCalledWith(false);
	});
});
