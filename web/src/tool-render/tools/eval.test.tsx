/**
 * `eval` 卡片：notebook 式的单元格（输入段 `In [n]`、输出段 `Out [n]`），头部读数给多格进度、出错格的异常与耗时。
 */
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ToolView } from "../ToolView";
import type { ToolResultLike } from "../types";

const CELLS_ARGS = {
	cells: [
		{ language: "py", title: "加载", code: "rows = load()" },
		{ language: "py", title: "聚合", code: "agg(rows)", timeout: 30 },
		{ language: "py", title: "画图", code: "plot(rows)" },
	],
};

const TRACEBACK =
	"Traceback (most recent call last):\n  File \"<cell 2>\", line 1, in <module>\nKeyError: 'region'";

function cellsResult(
	cells: Record<string, unknown>[],
	extra: Partial<ToolResultLike> = {},
	details: Record<string, unknown> = {},
): ToolResultLike {
	return {
		content: [{ type: "text", text: "" }],
		details: { language: "python", cells, ...details },
		...extra,
	};
}

const q = (c: HTMLElement, sel: string) => c.querySelector(sel);

describe("eval 卡片", () => {
	it("多格出错：读数给一格一点的进度与出错格的异常，后面的格子标为未运行", () => {
		const { container } = render(
			<ToolView
				name="notebook"
				args={CELLS_ARGS}
				defaultOpen
				result={cellsResult(
					[
						{
							index: 0,
							code: "",
							output: "ok",
							status: "complete",
							durationMs: 800,
						},
						{
							index: 1,
							code: "",
							output: `scanning…\n${TRACEBACK}`,
							status: "error",
							durationMs: 30,
							exitCode: 1,
						},
						{ index: 2, code: "", output: "", status: "pending" },
					],
					{ isError: true },
				)}
			/>,
		);
		const dots = [...container.querySelectorAll(".tv-meta .tv-cell-dot")].map(
			(d) => d.className.replace("tv-cell-dot tv-cell-dot--", ""),
		);
		expect(dots).toEqual(["ok", "err", "idle"]);
		expect(q(container, ".tv-meta .tv-meta-err")).toHaveTextContent(
			"#2 KeyError",
		);
		// 输入段 In [n]，输出段 Out [n]
		const prompts = [...container.querySelectorAll(".tv-nb-prompt")].map(
			(p) => p.textContent,
		);
		expect(prompts).toEqual([
			"In [1]",
			"In [2]",
			"In [3]",
			"Out [1]",
			"Out [2]",
			"Out [3]",
		]);
		expect(q(container, ".tv-nb-out--skipped")).toHaveTextContent("未运行");
		const err = q(container, ".tv-nb-out--error");
		expect(err?.querySelector(".tv-pre--error")).toHaveTextContent(
			"KeyError: 'region'",
		);
		// traceback 之前的普通打印不按错误显示
		expect(err?.querySelector(".tv-pre--error")).not.toHaveTextContent(
			"scanning…",
		);
		expect(err).toHaveTextContent("scanning…");
		// 输入段的超时写成说明，不是 `t=30s`
		expect(q(container, ".tv-sec--in")).toHaveTextContent("超时 30s");
	});

	it("运行中：读数给完成数，当前格强调并显示最近的状态事件", () => {
		const { container } = render(
			<ToolView
				name="notebook"
				args={CELLS_ARGS}
				running
				defaultOpen
				partialResult={cellsResult([
					{
						index: 0,
						code: "",
						output: "ok",
						status: "complete",
						durationMs: 800,
					},
					{
						index: 1,
						code: "",
						output: "",
						status: "running",
						statusEvents: [
							{ op: "phase", title: "按区域聚合" },
							{ op: "read", path: "/home/me/latency.json" },
						],
					},
					{ index: 2, code: "", output: "", status: "pending" },
				])}
			/>,
		);
		expect(q(container, ".tv-meta")).toHaveTextContent("1/3");
		const live = q(container, ".tv-nb-out--running");
		expect(live).toHaveTextContent("运行中");
		expect(live?.querySelector(".tv-nb-event--phase")).toHaveTextContent(
			"按区域聚合",
		);
		expect(live?.querySelector(".tv-nb-event--live")).toHaveTextContent(
			"~/latency.json",
		);
		expect(q(container, ".tv-nb-out--pending")).toHaveTextContent("等待");
		expect(q(container, ".tv-partial")).toBeNull();
	});

	it("单格无输出：给明确的空状态，读数给耗时，没有格头", () => {
		const { container } = render(
			<ToolView
				name="python"
				args={{ language: "py", code: "gc.collect()", reset: true }}
				defaultOpen
				result={cellsResult([
					{
						index: 0,
						code: "",
						output: "",
						status: "complete",
						durationMs: 4,
					},
				])}
			/>,
		);
		expect(q(container, ".tv-meta")).toHaveTextContent("4ms");
		expect(q(container, ".tv-cells-bar")).toBeNull();
		expect(q(container, ".tv-sec--out")).toHaveTextContent("无输出");
		expect(q(container, ".tv-nb-prompt")).toBeNull();
		const input = q(container, ".tv-sec--in");
		expect(input).toHaveTextContent("Python");
		expect(input).toHaveTextContent("重置内核");
	});

	it("截断：读数给截断提示，输出段说明显示了多少行", () => {
		const { container } = render(
			<ToolView
				name="eval"
				args={{ language: "js", code: "print(1)" }}
				defaultOpen
				result={cellsResult(
					[
						{
							index: 0,
							code: "",
							output: "1",
							status: "complete",
							durationMs: 10,
						},
					],
					{},
					{ meta: { truncation: { totalLines: 4210, outputLines: 2000 } } },
				)}
			/>,
		);
		expect(q(container, ".tv-meta .tv-meta-warn")).toHaveTextContent("已截断");
		expect(q(container, ".tv-sec--out")).toHaveTextContent(
			"显示末尾 2000 / 4210 行",
		);
	});

	it("表格视图：渲染成表格，数字列右对齐", () => {
		const text = "   ms\nmean  412.3";
		const { container } = render(
			<ToolView
				name="eval"
				args={{ language: "py", code: "df.describe()" }}
				defaultOpen
				result={cellsResult([
					{
						index: 0,
						code: "",
						output: text,
						status: "complete",
						views: [
							{
								text,
								view: {
									kind: "table",
									columns: [{ head: "" }, { head: "ms", numeric: true }],
									rows: [["mean", "412.3"]],
								},
							},
						],
					},
				])}
			/>,
		);
		const cells = [...container.querySelectorAll(".tv-nb-table td")];
		expect(cells.map((c) => c.textContent)).toEqual(["mean", "412.3"]);
		expect(cells[1]?.className).toBe("tv-nb-num");
	});

	it("旧结果（没有 details.cells）退回结果原文", () => {
		const { container } = render(
			<ToolView
				name="eval"
				args={{ language: "py", code: "1 + 1" }}
				defaultOpen
				result={{ content: [{ type: "text", text: "2" }] }}
			/>,
		);
		expect(q(container, ".tv-sec--out pre")).toHaveTextContent("2");
	});

	it("参数残缺（流式生成中）：没有单元格时不崩，也不报参数错误", () => {
		const { container } = render(
			<ToolView name="eval" args={{ language: "py" }} running defaultOpen />,
		);
		expect(q(container, ".tv-sum")).toHaveTextContent("py");
		expect(q(container, ".tv-body")).not.toHaveTextContent("invalid");
	});
});
