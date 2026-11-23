/**
 * MCP 工具卡片：运行中读数与输出段给进度（有总数时按比例的进度条、没有总数时不确定进度），
 * 结束后 JSON 结果是可折叠树、其余按 Markdown，出错首行醒目，空结果给空状态。
 */
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ToolView } from "../ToolView";
import type { ToolResultLike } from "../types";

const progress = (p: Record<string, unknown>): ToolResultLike => ({
	content: [],
	details: { serverName: "db", mcpToolName: "query", progress: p },
});

describe("MCP 卡片", () => {
	it("运行中：有总数时读数与输出段各一条按比例的进度条，输出段给百分比", () => {
		const { container } = render(
			<ToolView
				name="mcp__db_query"
				args={{ sql: "select 1" }}
				running
				partialResult={progress({
					progress: 3,
					total: 10,
					message: "Scanning",
				})}
				defaultOpen
			/>,
		);
		expect(container.querySelector(".tv-meta")).toHaveTextContent(
			"Scanning (3/10)",
		);
		const bars = container.querySelectorAll('[role="progressbar"]');
		expect(bars).toHaveLength(2);
		expect(bars[0]).toHaveAttribute("aria-valuenow", "3");
		expect(bars[0]).toHaveAttribute("aria-valuemax", "10");
		expect(container.querySelector(".tv-mcp-progress")).toHaveTextContent(
			"30%",
		);
	});

	it("运行中：没有总数时是不确定进度", () => {
		const { container } = render(
			<ToolView
				name="mcp__db_query"
				args={{}}
				running
				partialResult={progress({ progress: 7 })}
				defaultOpen
			/>,
		);
		expect(container.querySelector(".tv-mcp-bar--indet")).not.toBeNull();
		expect(container.querySelector(".tv-meta")).toHaveTextContent("7");
	});

	it("结束：JSON 数组给项数与可折叠树；非 JSON 文本按 Markdown；出错与空结果", () => {
		const json = render(
			<ToolView
				name="mcp__db_query"
				args={{}}
				result={{ content: [{ type: "text", text: '[{"n":1},{"n":2}]' }] }}
				defaultOpen
			/>,
		);
		expect(json.container.querySelector(".tv-meta")).toHaveTextContent("2 项");
		expect(json.container.querySelector(".tv-json")).not.toBeNull();
		json.unmount();

		const md = render(
			<ToolView
				name="mcp__linear_search"
				args={{ query: "x" }}
				result={{ content: [{ type: "text", text: "Found **2** issues" }] }}
				defaultOpen
			/>,
		);
		expect(md.container.querySelector(".tv-body strong")).toHaveTextContent(
			"2",
		);
		md.unmount();

		const err = render(
			<ToolView
				name="mcp__figma_get"
				args={{}}
				result={{
					content: [
						{ type: "text", text: "MCP error -32001: timed out\nhint" },
					],
					isError: true,
				}}
				defaultOpen
			/>,
		);
		expect(err.container.querySelector(".tv-errblock-head")).toHaveTextContent(
			"MCP error -32001: timed out",
		);
		err.unmount();

		const empty = render(
			<ToolView
				name="mcp__figma_get"
				args={{}}
				result={{ content: [] }}
				defaultOpen
			/>,
		);
		expect(empty.container.querySelector(".tv-empty")).toHaveTextContent(
			"工具没有返回内容",
		);
	});

	it("残缺的进度（不是对象、数字是字符串）不崩，也不画进度", () => {
		for (const p of [null, "3/10", { progress: "3", total: 10 }]) {
			const { container, unmount } = render(
				<ToolView
					name="mcp__db_query"
					args={{}}
					running
					partialResult={{ content: [], details: { progress: p } }}
					defaultOpen
				/>,
			);
			expect(container.querySelector('[role="progressbar"]')).toBeNull();
			unmount();
		}
	});
});
