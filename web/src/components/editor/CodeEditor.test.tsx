import { openSearchPanel } from "@codemirror/search";
import { EditorView } from "@codemirror/view";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { installContextMenu } from "../../lib/context-menu/dispatch";
import { closeContextMenu } from "../../lib/context-menu/store";
import { ContextMenuHost } from "../ui/ContextMenu";
import CodeEditor from "./CodeEditor";

function viewOf(container: HTMLElement): EditorView {
	const dom = container.querySelector(".cm-editor");
	if (!(dom instanceof HTMLElement)) throw new Error("no .cm-editor");
	const v = EditorView.findFromDOM(dom);
	if (!v) throw new Error("no view");
	return v;
}

describe("CodeEditor", () => {
	it("渲染 CodeMirror，编辑触发 onChange，Mod-s 触发 onSave", () => {
		const onChange = vi.fn();
		const onSave = vi.fn();
		const { container } = render(
			<CodeEditor
				value="hello"
				path="a.md"
				onChange={onChange}
				onSave={onSave}
			/>,
		);
		const view = viewOf(container);
		expect(view.state.doc.toString()).toBe("hello");
		act(() => view.dispatch({ changes: { from: 5, insert: " world" } }));
		expect(onChange).toHaveBeenLastCalledWith("hello world");
		fireEvent.keyDown(view.contentDOM, { key: "s", ctrlKey: true });
		expect(onSave).toHaveBeenCalledTimes(1);
	});

	it("外部 value 变化（重新加载）时整体替换文档，且不当作用户编辑触发 onChange", () => {
		const onChange = vi.fn();
		const { container, rerender } = render(
			<CodeEditor value="v1" path="a.txt" onChange={onChange} />,
		);
		rerender(
			<CodeEditor value="v2 reloaded" path="a.txt" onChange={onChange} />,
		);
		expect(viewOf(container).state.doc.toString()).toBe("v2 reloaded");
		expect(onChange).not.toHaveBeenCalled();
	});

	it("readOnly 时不可编辑", () => {
		const { container, rerender } = render(
			<CodeEditor value="x" path="a.txt" readOnly />,
		);
		const view = viewOf(container);
		expect(view.state.readOnly).toBe(true);
		expect(view.contentDOM.getAttribute("contenteditable")).toBe("false");
		rerender(<CodeEditor value="x" path="a.txt" />);
		expect(view.state.readOnly).toBe(false);
	});

	describe("内置文案本地化", () => {
		afterEach(async () => {
			await act(() => i18n.changeLanguage("zh-CN"));
		});

		it("搜索面板跟随界面语言，切换语言后已打开的面板随之更新", async () => {
			const { container } = render(<CodeEditor value="x" path="a.txt" />);
			const view = viewOf(container);
			act(() => {
				openSearchPanel(view);
			});
			const find = () =>
				container.querySelector<HTMLInputElement>(
					".cm-search input[name=search]",
				);
			expect(view.state.phrase("Find")).toBe(i18n.t("editor.phrases.find"));
			expect(view.state.phrase("Find")).not.toBe("Find");
			expect(find()?.placeholder).toBe(i18n.t("editor.phrases.find"));
			expect(view.state.phrase("replaced $ matches", 3)).toBe(
				i18n.t("editor.phrases.replacedMatches").replace("$", "3"),
			);

			await act(() => i18n.changeLanguage("en"));
			expect(view.state.phrase("Find")).toBe("Find");
			expect(view.state.phrase("match case")).toBe("match case");
			expect(find()?.placeholder).toBe("Find");
		});
	});

	describe("上下文菜单", () => {
		let off: () => void;
		beforeEach(() => {
			off = installContextMenu();
			// jsdom 的 Range 没有矩形接口，CodeMirror 的 coordsAtPos 要用
			const empty = () => ({
				length: 0,
				item: () => null,
				[Symbol.iterator]: [][Symbol.iterator],
			});
			Object.assign(Range.prototype, {
				getClientRects: empty,
				getBoundingClientRect: () => new DOMRect(),
			});
		});
		afterEach(() => {
			off();
			act(() => closeContextMenu());
			const proto: Partial<Range> = Range.prototype;
			delete proto.getClientRects;
			delete proto.getBoundingClientRect;
		});

		function rightClick(el: Element): boolean {
			let prevented = false;
			act(() => {
				prevented = !fireEvent.contextMenu(el, { clientX: 5, clientY: 5 });
			});
			return prevented;
		}

		it("没有选区：放行原生菜单（保住粘贴）", () => {
			const { container } = render(
				<>
					<ContextMenuHost />
					<CodeEditor value="one\ntwo" path="/w/a.ts" />
				</>,
			);
			expect(rightClick(viewOf(container).contentDOM)).toBe(false);
			expect(screen.queryByTestId("context-menu")).toBeNull();
		});

		it("有选区：复制、剪切与复制路径与行号；只读时没有剪切 / 粘贴", async () => {
			const writeText = vi.fn(async () => undefined);
			Object.defineProperty(navigator, "clipboard", {
				configurable: true,
				value: { writeText },
			});
			const { container, rerender } = render(
				<>
					<ContextMenuHost />
					<CodeEditor value={"one\ntwo\nthree"} path="/w/a.ts" />
				</>,
			);
			const view = viewOf(container);
			act(() => view.dispatch({ selection: { anchor: 0, head: 7 } }));
			expect(rightClick(view.contentDOM)).toBe(true);
			const menu = screen.getByTestId("context-menu");
			const keys = [...menu.querySelectorAll("[data-menu-key]")].map((b) =>
				b.getAttribute("data-menu-key"),
			);
			expect(keys).toEqual(["copy", "cut", "copyLocation"]);
			fireEvent.click(
				within(menu).getByRole("menuitem", {
					name: i18n.t("contextMenu.copyLocation"),
				}),
			);
			await waitFor(() =>
				expect(writeText).toHaveBeenCalledWith("/w/a.ts:1-2"),
			);

			rerender(
				<>
					<ContextMenuHost />
					<CodeEditor value={"one\ntwo\nthree"} path="/w/a.ts" readOnly />
				</>,
			);
			expect(rightClick(view.contentDOM)).toBe(true);
			expect(
				[
					...screen
						.getByTestId("context-menu")
						.querySelectorAll("[data-menu-key]"),
				].map((b) => b.getAttribute("data-menu-key")),
			).toEqual(["copy", "copyLocation"]);
		});
	});
});
