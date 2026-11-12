import { EditorView } from "@codemirror/view";
import { act, fireEvent, render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
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

	it("外部 value 变化（重新加载）时整体替换文档", () => {
		const onChange = vi.fn();
		const { container, rerender } = render(
			<CodeEditor value="v1" path="a.txt" onChange={onChange} />,
		);
		rerender(
			<CodeEditor value="v2 reloaded" path="a.txt" onChange={onChange} />,
		);
		expect(viewOf(container).state.doc.toString()).toBe("v2 reloaded");
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
});
