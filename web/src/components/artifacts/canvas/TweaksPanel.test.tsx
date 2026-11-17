/**
 * 「调节」面板（计划 M14 6.5）：按类型给控件；改动立即发 `params` 预览；停手 1000 ms 后保存，`baseVersion` 取画框
 * 回报声明时的版本与上次保存返回版本中较大的；保存失败显示原因并把预览还原；空态；`useFrameParams` 从 `ready` 收集声明。
 */
import {
	act,
	cleanup,
	fireEvent,
	render,
	renderHook,
	screen,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type {
	PageMessage,
	ParentMessageBody,
} from "../../../../sandbox/runtime/messages";
import type { CanvasPage } from "../../../lib/artifacts/canvas-doc";
import { parseParamsDecl } from "../../../lib/artifacts/params";
import type { FrameLink } from "../frame-link";
import {
	type FrameParams,
	TWEAK_SAVE_DELAY_MS,
	TweaksPanel,
	useFrameParams,
} from "./TweaksPanel";

const ID = "art_01JDNAWEB0000000000000000A";
const PAGE: CanvasPage = {
	id: "p1",
	canvas: "c1",
	name: "首页",
	file: "home.html",
	x: 0,
	y: 0,
	width: 1280,
	height: 800,
	locked: false,
};

const DECL = {
	accent: { type: "color", value: "#2563eb", label: "强调色" },
	headline: { type: "text", value: "欢迎" },
	density: { type: "number", value: 16, min: 8, max: 32, step: 2, unit: "px" },
	dark: { type: "boolean", value: false, label: "深色" },
	layout: { type: "choice", value: "grid", options: ["grid", "list"] },
};

const FRAME: FrameParams = { params: parseParamsDecl(DECL), version: 3 };

let bodies: Record<string, unknown>[];
let failNext: boolean;
let posts: ParentMessageBody[];
const post = (b: ParentMessageBody) => {
	posts.push(b);
};

beforeEach(() => {
	vi.useFakeTimers();
	bodies = [];
	posts = [];
	failNext = false;
	let next = 4;
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const url = new URL(req.url);
			if (
				url.pathname === `/api/v1/artifacts/${ID}/params` &&
				req.method === "PUT"
			) {
				bodies.push((await req.json()) as Record<string, unknown>);
				if (failNext) {
					failNext = false;
					return Response.json(
						{ code: "conflict", message: "home.html 在你的版本之后被改过" },
						{ status: 409 },
					);
				}
				return Response.json({ version: next++ });
			}
			return new Response(null, { status: 404 });
		}),
	);
});

afterEach(async () => {
	// 卸载会立即排队保存草稿；等请求与回滚结束后才能恢复 mock，避免串到下一个用例。
	cleanup();
	await act(() => vi.advanceTimersByTimeAsync(0));
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

async function idle() {
	await act(() => vi.advanceTimersByTimeAsync(TWEAK_SAVE_DELAY_MS));
	await act(() => vi.advanceTimersByTimeAsync(0));
}

function panel(frame: FrameParams | null = FRAME) {
	return render(
		<TweaksPanel
			artifactId={ID}
			page={PAGE}
			frame={frame ?? undefined}
			post={post}
		/>,
	);
}

describe("TweaksPanel", () => {
	it("画框没加载好与没有参数时显示空态", () => {
		const { rerender } = panel(null);
		expect(screen.getByText("画框加载好之后才能调节它的参数。")).toBeTruthy();
		rerender(
			<TweaksPanel
				artifactId={ID}
				page={PAGE}
				frame={{ params: [], version: 1 }}
				post={post}
			/>,
		);
		expect(
			screen.getByText(
				"这一页的画框还没有可调节的参数。可以让 Agent 加上，比如主题、强调色或密度。",
			),
		).toBeTruthy();
	});

	it("按类型给控件；改动立即预览，停手 1 秒后一次保存", async () => {
		panel();
		const color = screen.getByLabelText("强调色：取色") as HTMLInputElement;
		expect(color.value).toBe("#2563eb");
		fireEvent.input(color, { target: { value: "#ff0000" } });
		expect(posts).toEqual([{ kind: "params", values: { accent: "#ff0000" } }]);

		const slider = screen.getByRole("slider") as HTMLInputElement;
		expect(slider.min).toBe("8");
		expect(slider.step).toBe("2");
		fireEvent.change(slider, { target: { value: "24" } });
		fireEvent.click(screen.getByLabelText("深色"));
		fireEvent.change(screen.getByRole("combobox"), {
			target: { value: "list" },
		});
		expect(screen.getByText("24px")).toBeTruthy();

		await act(() => vi.advanceTimersByTimeAsync(TWEAK_SAVE_DELAY_MS - 100));
		expect(bodies).toEqual([]);
		await idle();
		expect(bodies).toEqual([
			{
				baseVersion: 3,
				path: "home.html",
				values: { accent: "#ff0000", density: 24, dark: true, layout: "list" },
			},
		]);
		expect(screen.getByTestId("tweaks-status").textContent).toBe(
			"已保存为 v4，历史里能恢复之前的",
		);

		// 下一次保存的 baseVersion 取上次保存返回的版本
		fireEvent.change(screen.getByRole("combobox"), {
			target: { value: "grid" },
		});
		await idle();
		expect(bodies[1]?.baseVersion).toBe(4);
	});

	it("不合规的文字不套用、标出来", async () => {
		panel();
		const input = screen.getByDisplayValue("欢迎");
		fireEvent.focus(input);
		fireEvent.change(input, { target: { value: "x".repeat(201) } });
		expect(screen.getByText("这个值不合规，没有套用")).toBeTruthy();
		expect(posts).toEqual([]);
		fireEvent.change(input, { target: { value: "你好" } });
		expect(posts).toEqual([{ kind: "params", values: { headline: "你好" } }]);
	});

	it("保存失败：显示原因，预览还原成画框里的值", async () => {
		failNext = true;
		panel();
		fireEvent.click(screen.getByLabelText("深色"));
		await idle();
		expect(screen.getByTestId("tweaks-status").textContent).toContain(
			"调节没有保存。",
		);
		expect(posts.at(-1)).toEqual({ kind: "params", values: { dark: false } });
		expect((screen.getByLabelText("深色") as HTMLInputElement).checked).toBe(
			false,
		);
	});

	it("卸载时立即保存没保存的改动", async () => {
		const { unmount } = panel();
		fireEvent.click(screen.getByLabelText("深色"));
		unmount();
		await act(() => vi.advanceTimersByTimeAsync(0));
		expect(bodies).toEqual([
			{ baseVersion: 3, path: "home.html", values: { dark: true } },
		]);
	});

	it("画框以新值回报后草稿清掉；没跟上的重新预览", () => {
		const { rerender } = panel();
		fireEvent.click(screen.getByLabelText("深色"));
		fireEvent.change(screen.getByRole("combobox"), {
			target: { value: "list" },
		});
		posts = [];
		const next = parseParamsDecl({
			...DECL,
			dark: { type: "boolean", value: true, label: "深色" },
		});
		rerender(
			<TweaksPanel
				artifactId={ID}
				page={PAGE}
				frame={{ params: next, version: 4 }}
				post={post}
			/>,
		);
		expect(posts).toEqual([{ kind: "params", values: { layout: "list" } }]);
	});
});

/** 只实现 `on` 的假通道：测试里手动派发 `ready`。 */
function fakeLink() {
	const handlers = new Set<(m: PageMessage) => void>();
	const posted: ParentMessageBody[] = [];
	const link = {
		on: (_kind: string, cb: (m: PageMessage) => void) => {
			handlers.add(cb);
			return () => handlers.delete(cb);
		},
		post: (b: ParentMessageBody) => posted.push(b),
	} as unknown as FrameLink;
	const ready = (params: Record<string, unknown>) => {
		for (const cb of handlers)
			cb({ v: 1, channel: "0123456789abcdef", kind: "ready", params });
	};
	return { link, ready, posted, handlers };
}

describe("useFrameParams", () => {
	it("收集画框 ready 里的声明并记下当时的版本；拆通道时清掉", () => {
		const { result, rerender } = renderHook(
			({ head }) => useFrameParams(head),
			{
				initialProps: { head: 5 },
			},
		);
		const f = fakeLink();
		act(() => result.current.track("p1", f.link));
		expect(result.current.declared("p1")).toBe(false);
		act(() =>
			f.ready({ ...DECL, "Bad-Name": { type: "boolean", value: true } }),
		);
		expect(result.current.declared("p1")).toBe(true);
		expect(result.current.byPage.p1?.version).toBe(5);
		expect(result.current.byPage.p1?.params.map((p) => p.name)).toEqual([
			"accent",
			"headline",
			"density",
			"dark",
			"layout",
		]);

		rerender({ head: 6 });
		act(() => f.ready({}));
		expect(result.current.byPage.p1).toEqual({ params: [], version: 6 });
		expect(result.current.declared("p1")).toBe(false);

		result.current.post("p1", { kind: "params", values: { accent: "#000" } });
		expect(f.posted).toEqual([{ kind: "params", values: { accent: "#000" } }]);

		act(() => result.current.track("p1", null));
		expect(result.current.byPage.p1).toBeUndefined();
		expect(f.handlers.size).toBe(0);
	});
});
