/**
 * 元素直接编辑面板：三种不能编辑的说明；字段变化立即发预览；停手 800 ms 后保存，`baseVersion` 取帧版本与上次保存
 * 返回版本中较大的；保存成功后发 `select-source`；409 提示后还原预览并清除选中；文字框填完整原文；卸载时立即保存。
 */
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type {
	PageSelection,
	ParentMessageBody,
} from "../../../sandbox/runtime/messages";
import { dismissToast, toastsStore } from "../../state/toasts";
import { pickOption } from "../../test/select";
import { EDIT_SAVE_DELAY_MS, EditPanel, fieldValue } from "./EditPanel";

const ID = "art_01JDNAWEB0000000000000000A";
const HTML = `<!doctype html>\n<h1 style="margin: 0">标题</h1>\n<p class="x">很长的一段 &amp; 文字\n第二行</p>\n`;
const enc = new TextEncoder();
const offsetOf = (needle: string) =>
	enc.encode(HTML.slice(0, HTML.indexOf(needle))).length;

const STYLE = {
	color: "rgb(0, 0, 0)",
	"background-color": "rgba(0, 0, 0, 0)",
	"font-size": "32px",
	"font-weight": "700",
	"text-align": "start",
	padding: "0px",
	"border-radius": "0px",
};

function sel(over: Partial<PageSelection> = {}): PageSelection {
	return {
		path: "index.html",
		src: offsetOf("<h1"),
		tag: "h1",
		text: "标题",
		textOnly: false,
		style: { ...STYLE },
		...over,
	};
}

let editBodies: Record<string, unknown>[];
let editStatus: number[];
let posts: ParentMessageBody[];
const post = (b: ParentMessageBody) => {
	posts.push(b);
};

beforeEach(() => {
	vi.useFakeTimers({ shouldAdvanceTime: true });
	editBodies = [];
	editStatus = [];
	posts = [];
	let next = 2;
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const url = new URL(req.url);
			if (url.pathname === `/api/v1/artifacts/${ID}/element`) {
				editBodies.push((await req.json()) as Record<string, unknown>);
				if (editStatus.shift() === 409)
					return Response.json(
						{
							code: "conflict",
							message: "现在是版本 9",
							state: { version: 9, deleted: false, changed: ["index.html"] },
						},
						{ status: 409 },
					);
				return Response.json({ version: next++ });
			}
			if (url.pathname === `/api/v1/artifacts/${ID}/file`)
				return new Response(enc.encode(HTML));
			return new Response(null, { status: 404 });
		}),
	);
});

afterEach(() => {
	for (const t of toastsStore.getState().toasts) dismissToast(t.id);
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

function panel(over: Partial<Parameters<typeof EditPanel>[0]> = {}) {
	const props = {
		artifactId: ID,
		selection: sel(),
		version: 1,
		block: null,
		post,
		onConflict: vi.fn(),
		onClose: vi.fn(),
		...over,
	};
	return { props, ...render(<EditPanel {...props} />) };
}

async function idle() {
	await act(() => vi.advanceTimersByTimeAsync(EDIT_SAVE_DELAY_MS));
	await act(() => vi.advanceTimersByTimeAsync(0));
}

describe("EditPanel", () => {
	it("看旧版本、在回收站、脚本生成的元素：只显示原因", () => {
		const { rerender, props } = panel({ block: "old-version" });
		expect(screen.getByTestId("edit-blocked").textContent).toBe(
			"只有最新版本能编辑",
		);
		rerender(<EditPanel {...props} block="deleted" />);
		expect(screen.getByTestId("edit-blocked").textContent).toBe(
			"恢复这个 artifact 后才能编辑",
		);
		rerender(
			<EditPanel {...props} block={null} selection={sel({ src: null })} />,
		);
		expect(screen.getByTestId("edit-blocked").textContent).toBe(
			"这个元素由页面的脚本生成，只能评论",
		);
		expect(screen.queryByTestId("edit-color")).toBeNull();
		rerender(
			<EditPanel
				{...props}
				block={null}
				selection={sel({ loc: "src/frames/home.tsx:12:5" })}
			/>,
		);
		expect(screen.getByTestId("edit-blocked").textContent).toBe(
			"这是设计画布的源码元素，请在对话里让 Agent 修改",
		);
		rerender(<EditPanel {...props} block="design-source" />);
		expect(screen.getByTestId("edit-blocked").textContent).toBe(
			"这是设计画布的源码元素，请在对话里让 Agent 修改",
		);
	});

	it("初始值来自计算样式；改颜色立即预览，停手后保存并重新选中", async () => {
		panel();
		const color = screen.getByTestId("edit-color") as HTMLInputElement;
		expect(color.value).toBe("#000000");
		expect(
			(screen.getByTestId("edit-font-size") as HTMLInputElement).value,
		).toBe("32");
		expect(
			screen.getByTestId("edit-align-left").getAttribute("aria-pressed"),
		).toBe("true");

		fireEvent.change(color, { target: { value: "#f00" } });
		expect(posts).toEqual([
			{ kind: "preview-style", style: { color: "#ff0000" } },
		]);
		await act(() => vi.advanceTimersByTimeAsync(EDIT_SAVE_DELAY_MS - 100));
		expect(editBodies).toHaveLength(0);
		await idle();
		expect(editBodies).toEqual([
			{
				baseVersion: 1,
				path: "index.html",
				offset: offsetOf("<h1"),
				tag: "h1",
				style: { color: "#ff0000" },
			},
		]);
		expect(screen.getByTestId("edit-status").textContent).toBe("已保存为 v2");
		expect(posts.at(-1)).toEqual({
			kind: "select-source",
			src: offsetOf("<h1"),
		});
	});

	it("不合规的值不预览不保存；第二次保存的 baseVersion 取上次保存返回的版本", async () => {
		panel();
		fireEvent.change(screen.getByTestId("edit-font-size"), {
			target: { value: "0.5" },
		});
		expect(
			screen.getByTestId("edit-font-size").getAttribute("aria-invalid"),
		).toBe("true");
		await idle();
		expect(posts).toEqual([]);
		expect(editBodies).toHaveLength(0);

		fireEvent.change(screen.getByTestId("edit-font-size"), {
			target: { value: "20" },
		});
		await idle();
		fireEvent.click(screen.getByTestId("edit-align-center"));
		fireEvent.change(screen.getByTestId("edit-padding"), {
			target: { value: "4px 8px" },
		});
		await idle();
		expect(editBodies.map((b) => [b.baseVersion, b.style])).toEqual([
			[1, { "font-size": "20px" }],
			[2, { "text-align": "center", padding: "4px 8px" }],
		]);
	});

	it("409：提示、还原预览、清除选中，状态不显示已保存", async () => {
		editStatus = [409];
		const { props } = panel();
		pickOption(screen.getByRole("combobox", { name: /字重/ }), "400");
		await idle();
		expect(editBodies).toHaveLength(1);
		expect(props.onConflict).toHaveBeenCalledTimes(1);
		expect(posts.at(-1)).toEqual({ kind: "preview-reset" });
		expect(toastsStore.getState().toasts.map((t) => t.text)).toContain(
			"保存前来了新版本，所以没有保存。在新版本上重新选中这个元素再改。",
		);
		expect(screen.getByTestId("edit-status").textContent).toBe("");
	});

	it("文字框填完整原文（解码实体）；改文字预览并随样式一起保存", async () => {
		const p = sel({
			src: offsetOf("<p"),
			tag: "p",
			text: "很长的一段 & 文字 第二行",
			textOnly: true,
		});
		panel({ selection: p });
		const text = screen.getByTestId("edit-text") as HTMLTextAreaElement;
		expect(text.disabled).toBe(true);
		await act(() => vi.advanceTimersByTimeAsync(0));
		expect(text.disabled).toBe(false);
		expect(text.value).toBe("很长的一段 & 文字\n第二行");

		fireEvent.change(text, { target: { value: "新文字 <b>" } });
		fireEvent.change(screen.getByTestId("edit-border-radius"), {
			target: { value: "6" },
		});
		expect(posts).toEqual([
			{ kind: "preview-text", text: "新文字 <b>" },
			{ kind: "preview-style", style: { "border-radius": "6px" } },
		]);
		await idle();
		expect(editBodies).toEqual([
			expect.objectContaining({
				offset: offsetOf("<p"),
				tag: "p",
				text: "新文字 <b>",
				style: { "border-radius": "6px" },
			}),
		]);
	});

	it("换元素或关掉面板时，没保存的改动立即保存", async () => {
		const { rerender, props, unmount } = panel();
		fireEvent.change(screen.getByTestId("edit-background-color"), {
			target: { value: "#123456" },
		});
		rerender(
			<EditPanel
				{...props}
				selection={sel({ src: offsetOf("<p"), tag: "p" })}
			/>,
		);
		await act(() => vi.advanceTimersByTimeAsync(0));
		expect(editBodies).toEqual([
			expect.objectContaining({
				tag: "h1",
				style: { "background-color": "#123456" },
			}),
		]);
		fireEvent.change(screen.getByTestId("edit-color"), {
			target: { value: "#abcdef" },
		});
		unmount();
		await act(() => vi.advanceTimersByTimeAsync(0));
		expect(editBodies.at(-1)).toEqual(
			expect.objectContaining({ tag: "p", style: { color: "#abcdef" } }),
		);
	});
});

describe("fieldValue", () => {
	it("颜色展开简写、字号与圆角补单位、其余原样校验", () => {
		expect(fieldValue("color", "abc")).toBe("#aabbcc");
		expect(fieldValue("color", "red")).toBeNull();
		expect(fieldValue("font-size", "16")).toBe("16px");
		expect(fieldValue("font-size", "0")).toBeNull();
		expect(fieldValue("border-radius", "0")).toBe("0px");
		expect(fieldValue("padding", "1px 2px")).toBe("1px 2px");
		expect(fieldValue("text-align", "")).toBeNull();
	});
});
