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

	it("运行中的 write xd://task 解包部分结果的 xdev.inner，用 task 渲染器显示进度", () => {
		const { container } = render(
			<ToolView
				name="write"
				args={{ path: "xd://task", content: "{}" }}
				running
				defaultOpen
				partial="Running agent Alpha..."
				partialResult={{
					content: [{ type: "text", text: "Running agent Alpha..." }],
					details: {
						xdev: {
							tool: "task",
							mode: "execute",
							args: {
								tasks: [{ name: "Alpha", agent: "task", task: "看一眼" }],
							},
							inner: {
								results: [],
								progress: [{ id: "Alpha", agent: "task", status: "running" }],
							},
						},
					},
				}}
			/>,
		);
		expect(container.querySelector(".tv-name")).toHaveTextContent("xd://task");
		expect(screen.getByText("running").closest(".tv-row")).toHaveTextContent(
			"Alpha",
		);
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

const PNG_1PX =
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";

function nameOf(container: HTMLElement): string {
	return container.querySelector(".tv-name")?.textContent ?? "";
}

function summaryOf(container: HTMLElement): string {
	return container.querySelector(".tv-sum")?.textContent ?? "";
}

describe("xd:// 设备调用的卡片", () => {
	it("调用已发出、尚无结果也无部分结果：从 args 识别为真实工具卡片", () => {
		const { container } = render(
			<ToolView
				name="write"
				args={{
					path: "xd://grep",
					content: JSON.stringify({ pattern: "needle", path: "src", i: "找" }),
				}}
				running
				defaultOpen
			/>,
		);
		expect(nameOf(container)).toBe("xd://grep");
		expect(summaryOf(container)).toContain("needle");
		expect(summaryOf(container)).toContain("src");
		// 设备参数里的 `i` 作为意图展示，不当成真实参数
		expect(container.querySelector(".tv-intent")).toHaveTextContent("找");
	});

	it("纯文本设备 resolve 未派发时按 resolve 卡片显示理由", () => {
		const { container } = render(
			<ToolView
				name="write"
				args={{ path: "xd://resolve", content: "看过了，没问题" }}
			/>,
		);
		expect(nameOf(container)).toBe("xd://resolve");
		expect(summaryOf(container)).toContain("apply");
		expect(summaryOf(container)).toContain("看过了，没问题");
	});

	it("xd://report_issue 拆出工具名交给 report_tool_issue 渲染器", () => {
		const { container } = render(
			<ToolView
				name="write"
				args={{ path: "xd://report_issue", content: "read: 偏移量不对" }}
				result={{
					content: [{ type: "text", text: "Noted, thanks!" }],
					details: {
						xdev: {
							tool: "report_issue",
							mode: "execute",
							args: { report: "read: 偏移量不对" },
						},
					},
				}}
			/>,
		);
		expect(nameOf(container)).toBe("xd://report_issue");
		const sum = container.querySelector(".tv-sum");
		expect(sum?.querySelector(".tv-badge")).toHaveTextContent("read");
		expect(sum).toHaveTextContent("偏移量不对");
	});

	it("派发失败（设备不存在）显示为 xd://<name> 错误卡片与错误文本", () => {
		const { container } = render(
			<ToolView
				name="write"
				args={{ path: "xd://nope", content: '{"a":1}' }}
				result={{
					content: [
						{
							type: "text",
							text: "No such tool: xd://nope. Mounted devices: task.",
						},
					],
					details: { xdev: { tool: "nope", mode: "execute" } },
					isError: true,
				}}
				defaultOpen
			/>,
		);
		expect(nameOf(container)).toBe("xd://nope");
		expect(container.querySelector(".tv-card--error")).not.toBeNull();
		expect(summaryOf(container)).toContain("No such tool: xd://nope");
		// 展开体给出写入的原始内容
		expect(container.querySelector(".tv-body")).toHaveTextContent('{"a":1}');
	});

	it("参数校验失败不交给真实工具渲染器，显示错误文本", () => {
		const { container } = render(
			<ToolView
				name="write"
				args={{ path: "xd://grep", content: '{"pattern":1}' }}
				result={{
					content: [
						{ type: "text", text: "Invalid args for xd://grep: pattern" },
					],
					details: { xdev: { tool: "grep", mode: "execute" } },
					isError: true,
				}}
			/>,
		);
		expect(nameOf(container)).toBe("xd://grep");
		expect(summaryOf(container)).toContain("Invalid args for xd://grep");
	});

	it("help 模式显示为 xd://<name> · 文档，展开体按 Markdown 渲染", () => {
		const { container } = render(
			<ToolView
				name="write"
				args={{ path: "xd://task", content: "?" }}
				result={{
					content: [{ type: "text", text: "# Task\n\nRuns **agents**." }],
					details: { xdev: { tool: "task", mode: "help" } },
				}}
				defaultOpen
			/>,
		);
		expect(nameOf(container)).toBe("xd://task");
		expect(summaryOf(container)).toBe("文档");
		expect(screen.getByRole("heading", { name: "Task" })).toBeInTheDocument();
		expect(container.querySelector(".tv-body strong")).toHaveTextContent(
			"agents",
		);
	});

	it("read xd://<name> 显示为设备文档卡；xd:// 根是设备目录，/topic 带专题名", () => {
		const { container, rerender } = render(
			<ToolView
				name="read"
				args={{ path: "xd://lsp" }}
				result={{ content: [{ type: "text", text: "## LSP\n\nhover…" }] }}
				defaultOpen
			/>,
		);
		expect(nameOf(container)).toBe("xd://lsp");
		expect(summaryOf(container)).toBe("文档");
		expect(screen.getByRole("heading", { name: "LSP" })).toBeInTheDocument();

		rerender(
			<ToolView
				name="read"
				args={{ path: "xd://" }}
				result={{ content: [{ type: "text", text: "- task" }] }}
			/>,
		);
		expect(nameOf(container)).toBe("xd://");
		expect(summaryOf(container)).toBe("设备目录");

		rerender(
			<ToolView
				name="read"
				args={{ path: "xd://read/sources" }}
				result={{ content: [{ type: "text", text: "sources" }] }}
			/>,
		);
		expect(nameOf(container)).toBe("xd://read");
		expect(summaryOf(container)).toContain("sources");
	});

	it("MCP 设备：头部名字分开写 server / tool，摘要给主要参数，展开体给参数、图片与文本结果", () => {
		const { container } = render(
			<ToolView
				name="write"
				args={{
					path: "xd://mcp__github_search_issues",
					content: '{"query":"flaky"}',
				}}
				result={{
					content: [
						{ type: "text", text: "Found **2** issues" },
						{ type: "image", data: PNG_1PX, mimeType: "image/png" },
					],
					details: {
						xdev: {
							tool: "mcp__github_search_issues",
							mode: "execute",
							args: { query: "flaky" },
							inner: { serverName: "github", mcpToolName: "search_issues" },
						},
					},
				}}
				defaultOpen
			/>,
		);
		const nameEl = container.querySelector(".tv-name");
		expect(nameEl).toHaveTextContent("github / search_issues");
		expect(nameEl?.querySelector(".tv-muted")).toHaveTextContent("github");
		expect(nameEl).toHaveAttribute("title", "xd://mcp__github_search_issues");
		expect(summaryOf(container)).toBe("flaky");
		const body = container.querySelector(".tv-body");
		expect(body).toHaveTextContent('"query": "flaky"');
		expect(body?.querySelector("img.tv-img")).not.toBeNull();
		expect(body?.querySelector("strong")).toHaveTextContent("2");
	});

	it("MCP 服务名含下划线时以结果 details 的 serverName / mcpToolName 为准", () => {
		const { container } = render(
			<ToolView
				name="write"
				args={{ path: "xd://mcp__my_srv_lookup", content: '{"id":7}' }}
				result={{
					content: [{ type: "text", text: "ok" }],
					details: {
						xdev: {
							tool: "mcp__my_srv_lookup",
							mode: "execute",
							args: { id: 7 },
							inner: { serverName: "my_srv", mcpToolName: "lookup" },
						},
					},
				}}
			/>,
		);
		expect(nameOf(container)).toBe("my_srv / lookup");
		expect(summaryOf(container)).toBe("id=7");
	});

	it("运行中的 MCP 设备在读数显示进度，不输出文本尾巴", () => {
		const { container } = render(
			<ToolView
				name="write"
				args={{ path: "xd://mcp__db_query", content: '{"sql":"select 1"}' }}
				running
				partial="Scanning"
				partialResult={{
					content: [{ type: "text", text: "Scanning" }],
					details: {
						xdev: {
							tool: "mcp__db_query",
							mode: "execute",
							args: { sql: "select 1" },
							inner: {
								serverName: "db",
								mcpToolName: "query",
								progress: { progress: 3, total: 10, message: "Scanning" },
							},
						},
					},
				}}
			/>,
		);
		expect(container.querySelector(".tv-meta")).toHaveTextContent(
			"Scanning (3/10)",
		);
		expect(container.querySelector(".tv-partial")).toBeNull();
	});

	it("普通文件的 write 不受影响，即使内容是 JSON 对象", () => {
		const { container } = render(
			<ToolView
				name="write"
				args={{ path: "config.json", content: '{"tool":"grep"}' }}
				result={{ content: [{ type: "text", text: "Wrote config.json" }] }}
			/>,
		);
		expect(nameOf(container)).toBe("write");
		expect(summaryOf(container)).toContain("config.json");
	});

	it("xd:// 写入的 content 不是 JSON 对象时不误判为真实工具调用", () => {
		const { container } = render(
			<ToolView
				name="write"
				args={{ path: "xd://grep", content: "needle in src" }}
				running
				defaultOpen
			/>,
		);
		expect(nameOf(container)).toBe("xd://grep");
		// 不交给 grep 渲染器（它会显示「in <路径>」），原样展示写入内容
		expect(summaryOf(container)).toBe("needle in src");
		expect(container.querySelector(".tv-out-title")).toHaveTextContent(
			"写入内容",
		);
	});
});
