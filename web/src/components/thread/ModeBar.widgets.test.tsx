import { render } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import "../../i18n";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import type { ThreadViewState } from "../../state/thread-reducer";
import { AboveEditorWidgets } from "./ModeBar";

const T = "T-widgets";

function seed(lines: string[]) {
	getThreadStore(T)
		.getState()
		.patchLocal({
			widgets: { omfg: { lines, placement: "aboveEditor" } },
		} as unknown as Partial<ThreadViewState>);
	return render(<AboveEditorWidgets threadId={T} />);
}

describe("输入坞上方的扩展小部件", () => {
	beforeEach(() => resetThreadStores());

	it("整段 Markdown（如 /omfg 的规则全文）按 Markdown 渲染，用法占位符原样显示", () => {
		const { container } = seed([
			"TTSR rule — matches this conversation",
			"",
			"## Never use npm",
			"- run `bun install` instead of <cmd>",
		]);
		const box = container.querySelector('[data-widget="omfg"]');
		expect(box?.tagName).toBe("DIV");
		expect(box?.querySelector("h2")).toHaveTextContent("Never use npm");
		expect(box).toHaveTextContent("instead of <cmd>");
	});

	it("对齐的状态行保持等宽原样", () => {
		const { container } = seed(["job  1   running", "job  2   done"]);
		const box = container.querySelector('[data-widget="omfg"]');
		expect(box?.tagName).toBe("PRE");
		expect(box?.textContent).toBe("job  1   running\njob  2   done");
	});
});
