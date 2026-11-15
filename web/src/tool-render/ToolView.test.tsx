import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ToolView } from "./ToolView";

describe("工具卡片的结构化部分结果（M12 步骤 5）", () => {
	it("运行中的 task 用 details.progress 渲染进度行，不输出文本尾巴", () => {
		const { container } = render(
			<ToolView
				name="task"
				args={{ tasks: [{ name: "Alpha", agent: "scout", task: "看一眼" }] }}
				running
				defaultOpen
				partial="Running agent Alpha..."
				partialResult={{
					content: [{ type: "text", text: "Running agent Alpha..." }],
					details: {
						results: [],
						progress: [{ id: "Alpha", agent: "scout", status: "running" }],
					},
				}}
			/>,
		);
		const badge = screen.getByText("running");
		expect(badge.closest(".tv-row")).toHaveTextContent("Alpha");
		expect(container.querySelector(".tv-partial")).toBeNull();
	});

	it("未声明 livePartial 的 bash 仍输出文本尾巴", () => {
		const { container } = render(
			<ToolView
				name="bash"
				args={{ command: "ls" }}
				running
				partial="line one"
				partialResult={{ content: [{ type: "text", text: "line one" }] }}
			/>,
		);
		expect(container.querySelector(".tv-partial")).toHaveTextContent(
			"line one",
		);
	});

	it("task 有最终结果时渲染结果行，不渲染进度行", () => {
		render(
			<ToolView
				name="task"
				args={{ tasks: [{ name: "Alpha", agent: "scout", task: "看一眼" }] }}
				defaultOpen
				result={{
					content: [{ type: "text", text: "done" }],
					details: {
						results: [
							{ id: "Alpha", exitCode: 0, output: "all good", durationMs: 10 },
						],
						progress: [{ id: "Alpha", agent: "scout", status: "running" }],
					},
				}}
				partialResult={{
					content: [],
					details: {
						progress: [{ id: "Alpha", agent: "scout", status: "running" }],
					},
				}}
			/>,
		);
		expect(screen.getByText("done")).toBeInTheDocument();
		expect(screen.getByText("1 succeeded")).toBeInTheDocument();
		expect(screen.queryByText("running")).toBeNull();
	});

	it("运行中的 todo 用 details.phases 渲染看板，不输出文本尾巴", () => {
		const { container } = render(
			<ToolView
				name="todo"
				args={{ op: "start", task: "写测试" }}
				running
				defaultOpen
				partial="updating board"
				partialResult={{
					content: [{ type: "text", text: "updating board" }],
					details: {
						phases: [
							{
								name: "实现",
								tasks: [{ content: "写测试", status: "in_progress" }],
							},
						],
					},
				}}
			/>,
		);
		expect(container.querySelector(".tv-todo")).toHaveTextContent("写测试");
		expect(
			container.querySelector(".tv-task--in_progress"),
		).toBeInTheDocument();
		expect(container.querySelector(".tv-partial")).toBeNull();
	});

	it("运行中的 eval 用 details.cells 逐格渲染输出，不输出文本尾巴", () => {
		const { container } = render(
			<ToolView
				name="eval"
				args={{ language: "py", code: "print(42)" }}
				running
				defaultOpen
				partial="partial-42"
				partialResult={{
					content: [{ type: "text", text: "partial-42" }],
					details: {
						language: "python",
						languages: ["python"],
						cells: [
							{
								index: 0,
								code: "print(42)",
								language: "python",
								output: "partial-42",
								status: "running",
							},
						],
					},
				}}
			/>,
		);
		expect(container.querySelector(".tv-cell")).toHaveTextContent("partial-42");
		expect(container.querySelector(".tv-partial")).toBeNull();
	});
});
