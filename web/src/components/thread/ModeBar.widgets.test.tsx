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

	it("/omfg 的候选规则渲染成规则卡：条件逐条原样、范围徽标、未知字段进网格、正文 Markdown", () => {
		const { container } = seed([
			"TTSR rule — matches this conversation",
			"",
			"---",
			"name: no-ack-filler-replies",
			'description: "不用「收到」这类应答套话"',
			'condition: ["收到", "^ok\\\\s*$", "<think>"]',
			'scope: "text"',
			"priority: high",
			"---",
			"",
			"## 只给要点",
		]);
		const card = container.querySelector(
			'[data-widget="omfg"] [data-rule-card]',
		);
		expect(card).not.toBeNull();
		expect(card?.querySelector("h4")).toHaveTextContent(
			"no-ack-filler-replies",
		);
		expect(
			card?.querySelector("[data-validation]")?.getAttribute("data-validation"),
		).toBe("matched");
		const conds = [
			...(card?.querySelectorAll('[data-field="condition"] code') ?? []),
		].map((c) => c.textContent);
		expect(conds).toEqual(["收到", "^ok\\s*$", "<think>"]);
		expect(card?.querySelector('[data-field="scope"]')).toHaveTextContent(
			"text",
		);
		expect(card?.querySelector('[data-field^="x"] dt')).toHaveTextContent(
			"priority",
		);
		// frontmatter 不再被当成 setext 标题；正文标题照常
		expect(card?.querySelectorAll("h1, h2")).toHaveLength(1);
		expect(card?.querySelector("h2")).toHaveTextContent("只给要点");
	});

	it("未知校验状态显示原文，不丢", () => {
		const { container } = seed([
			"TTSR rule — brand new state",
			"",
			"---",
			"name: r",
			'description: "d"',
			'question: "Is it verbose?"',
			'scope: "text"',
			"---",
		]);
		const badge = container.querySelector('[data-validation="unknown"]');
		expect(badge).toHaveTextContent("brand new state");
		expect(
			container.querySelector('[data-field="question"]'),
		).toHaveTextContent("Is it verbose?");
	});
});
