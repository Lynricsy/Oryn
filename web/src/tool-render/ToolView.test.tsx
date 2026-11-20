import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { baseToolName, resolveToolRenderer } from "./registry";
import { ToolView } from "./ToolView";

describe("助手的电脑的工具卡（M19 步骤 4.4）", () => {
	it("baseToolName 去掉 computer_ 前缀；status / upload / download 保留自己的名字", () => {
		expect(baseToolName("computer_bash")).toBe("bash");
		expect(baseToolName("computer_ast_grep")).toBe("ast_grep");
		expect(baseToolName("computer_status")).toBe("computer_status");
		expect(baseToolName("computer_upload")).toBe("computer_upload");
		expect(baseToolName("computer_download")).toBe("computer_download");
		expect(baseToolName("computer_")).toBe("computer_");
		expect(baseToolName("bash")).toBe("bash");
		expect(resolveToolRenderer("computer_bash")).toBe(
			resolveToolRenderer("bash"),
		);
		expect(resolveToolRenderer("computer_read")).toBe(
			resolveToolRenderer("read"),
		);
	});

	it("computer_bash 用 bash 的卡片（图标、类别、命令行），头部带「电脑」小标", () => {
		const { container } = render(
			<ToolView
				name="computer_bash"
				args={{ command: "uname -a" }}
				defaultOpen
				result={{ content: [{ type: "text", text: "Linux orb 6.1.0" }] }}
			/>,
		);
		const { container: plain } = render(
			<ToolView
				name="bash"
				args={{ command: "uname -a" }}
				defaultOpen
				result={{ content: [{ type: "text", text: "Linux orb 6.1.0" }] }}
			/>,
		);
		expect(container.querySelector(".tv-cat")?.className).toBe(
			plain.querySelector(".tv-cat")?.className,
		);
		expect(container.querySelector(".tv-cat")?.innerHTML).toBe(
			plain.querySelector(".tv-cat")?.innerHTML,
		);
		expect(container.querySelector(".tv-cmd-text")).toHaveTextContent(
			"uname -a",
		);
		expect(container.querySelector(".tv-name")).toHaveTextContent("bash");
		expect(container.querySelector(".tv-name")).toHaveAttribute(
			"title",
			"computer_bash",
		);
		expect(screen.getByTestId("tool-computer-badge")).toHaveTextContent("电脑");
		expect(
			plain.querySelector('[data-testid="tool-computer-badge"]'),
		).toBeNull();
	});

	it("computer_status 不当作遥控工具：没有小标", () => {
		render(<ToolView name="computer_status" args={{}} />);
		expect(screen.queryByTestId("tool-computer-badge")).toBeNull();
	});
});

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
		expect(screen.getByRole("region", { name: "输入" })).toHaveTextContent(
			"needle in src",
		);
	});
});

describe("从 xd:// URL 推断的设备名不命中 Object 原型（M12 审查 cards#1）", () => {
	const PROTO_NAMES = [
		"constructor",
		"__proto__",
		"toString",
		"hasOwnProperty",
	];

	it.each(PROTO_NAMES)(
		"运行中的 write xd://%s 用通用卡片渲染，不崩溃",
		(device) => {
			const { container } = render(
				<ToolView
					name="write"
					args={{ path: `xd://${device}`, content: "{}" }}
					running
					defaultOpen
				/>,
			);
			expect(container.querySelector(".tv-name")).toHaveTextContent(
				`xd://${device}`,
			);
			expect(container.querySelector(".tv-cat--other")).not.toBeNull();
		},
	);

	it.each(PROTO_NAMES)(
		"write xd://%s 的最终错误结果显示 Core 的错误文本",
		(device) => {
			const { container } = render(
				<ToolView
					name="write"
					args={{ path: `xd://${device}`, content: "{}" }}
					result={{
						content: [{ type: "text", text: `No such tool: ${device}` }],
						isError: true,
					}}
					defaultOpen
				/>,
			);
			expect(container.querySelector(".tv-body")).toHaveTextContent(
				`No such tool: ${device}`,
			);
		},
	);

	it.each(PROTO_NAMES)("顶层工具名 %s 同样走通用渲染器", (name) => {
		const { container } = render(
			<ToolView name={name} args={{}} running defaultOpen />,
		);
		expect(container.querySelector(".tv-name")).toHaveTextContent(name);
	});
});

describe("卡片头部的意图与展开体的输入 / 输出分段", () => {
	const bashResult = {
		content: [{ type: "text" as const, text: "a-1\nb-1" }],
		details: { exitCode: 0 },
	};

	it("收起时头部显示意图而不是命令；展开后头部换回命令摘要", () => {
		const { container } = render(
			<ToolView
				name="bash"
				args={{ command: "make test", i: "跑单元测试" }}
				result={bashResult}
			/>,
		);
		expect(summaryOf(container)).toBe("跑单元测试");
		fireEvent.click(container.querySelector(".tv-head") as HTMLElement);
		expect(summaryOf(container)).toContain("make test");
		expect(summaryOf(container)).not.toContain("跑单元测试");
		expect(container.querySelector(".tv-intent")).toHaveTextContent(
			"跑单元测试",
		);
	});

	it("没有意图时收起的头部仍显示参数摘要", () => {
		const { container } = render(
			<ToolView name="bash" args={{ command: "make test" }} />,
		);
		expect(summaryOf(container)).toContain("make test");
	});

	it("命令在输入段、结果文本在输出段", () => {
		const { container } = render(
			<ToolView
				name="bash"
				args={{ command: "make test" }}
				result={bashResult}
				defaultOpen
			/>,
		);
		const input = screen.getByRole("region", { name: "输入" });
		const output = screen.getByRole("region", { name: "输出" });
		expect(input).toHaveTextContent("make test");
		expect(input).not.toHaveTextContent("a-1");
		expect(output).toHaveTextContent("a-1");
		expect(output).not.toHaveTextContent("make test");
		expect(container.querySelectorAll(".tv-sec")).toHaveLength(2);
	});

	it("还没有结果时输出段内容为空（由 CSS 整段隐去）", () => {
		const { container } = render(
			<ToolView
				name="bash"
				args={{ command: "sleep 1" }}
				running
				defaultOpen
			/>,
		);
		expect(
			container.querySelector(".tv-sec--out .tv-sec-body"),
		).toBeEmptyDOMElement();
	});

	it("未手动开合时跟随 defaultOpen（宿主收起旧一批）；用户点过后以用户为准", () => {
		const args = { command: "sleep 1", i: "等一秒" };
		const { container, rerender } = render(
			<ToolView name="bash" args={args} running defaultOpen />,
		);
		const head = () => container.querySelector(".tv-head") as HTMLElement;
		expect(head()).toHaveAttribute("aria-expanded", "true");
		// 结束了但仍是当前一批：保持展开
		rerender(
			<ToolView name="bash" args={args} result={{ content: [] }} defaultOpen />,
		);
		expect(head()).toHaveAttribute("aria-expanded", "true");
		// 模型开始下一批或回合结束：宿主不再给 defaultOpen，自动收起
		rerender(
			<ToolView
				name="bash"
				args={args}
				result={{ content: [] }}
				defaultOpen={false}
			/>,
		);
		expect(head()).toHaveAttribute("aria-expanded", "false");
		fireEvent.click(head());
		rerender(
			<ToolView
				name="bash"
				args={args}
				result={{ content: [] }}
				defaultOpen={false}
			/>,
		);
		expect(head()).toHaveAttribute("aria-expanded", "true");
	});
});

describe("bash 的 Pseudo 视图（details.orynPseudo）", () => {
	// 由 omp 原生 ShellVisualization 对 `for x in a b; do Y="$x-1"; echo "$Y"; done && ls /nonexistent-dir || echo fallback`
	// 的真实轨迹渲染、再经 oryn-orb.ts 的 compactPseudo 压缩得到
	const orynPseudo = {
		v: 1,
		rows: [
			{
				depth: 0,
				segments: [
					{ text: "for ", style: "kw" },
					{ text: "x", style: "var def" },
					{ text: " in ", style: "kw" },
					{ text: "[", style: "op" },
					{ text: '"a"', style: "str", state: "ok" },
					{ text: ", ", style: "op" },
					{ text: '"b"', style: "str", state: "ok" },
					{ text: "]", style: "op" },
					{ text: ":", style: "op" },
				],
				meta: "↻ 2",
				annotations: [{ name: "x", value: "b" }],
			},
			{
				depth: 1,
				segments: [
					{ text: "and ", style: "op" },
					{ text: "ls", style: "cmd", state: "fail", failMark: true },
					{ text: " ", state: "fail" },
					{ text: "/nonexistent-dir", style: "arg", state: "fail" },
				],
				state: "fail",
				meta: "exit 2 · 20ms",
			},
			{
				depth: 1,
				segments: [
					{ text: "or ", style: "op" },
					{ text: "print(", style: "fn", state: "skipped" },
				],
				state: "skipped",
			},
		],
	};
	const command =
		'for x in a b; do Y="$x-1"; echo "$Y"; done && ls /nonexistent-dir || echo fallback';

	it("默认显示 pseudo 行：关键字 / 命令按令牌着色、失败打 ×、跳过的行弱化、变量终值标注", () => {
		const { container } = render(
			<ToolView
				name="bash"
				args={{ command }}
				result={{
					content: [{ type: "text", text: "a-1\nb-1" }],
					details: { exitCode: 0, orynPseudo },
				}}
				defaultOpen
			/>,
		);
		const pseudo = screen.getByTestId("bash-pseudo");
		const rows = pseudo.querySelectorAll(".tv-pseudo-row");
		expect(rows).toHaveLength(3);
		expect(rows[0]).toHaveTextContent('for x in ["a", "b"]:');
		expect(rows[0]?.querySelector(".hljs-keyword")).toHaveTextContent("for");
		expect(rows[0]?.querySelector(".tv-pseudo-ann")).toHaveTextContent(
			'x = "b"',
		);
		expect(rows[1]?.querySelector(".tv-pseudo-fail")).toHaveTextContent("×");
		expect(rows[1]?.querySelector(".hljs-title")).toHaveTextContent("ls");
		expect(rows[2]).toHaveClass("tv-pseudo-row--skipped");
		expect(container.querySelector(".tv-cmd")).toBeNull();
	});

	it("切到「原始」显示完整命令行，再切回 pseudo", () => {
		const { container } = render(
			<ToolView
				name="bash"
				args={{ command }}
				result={{ content: [], details: { orynPseudo } }}
				defaultOpen
			/>,
		);
		fireEvent.click(screen.getByRole("button", { name: "原始" }));
		expect(screen.queryByTestId("bash-pseudo")).toBeNull();
		expect(container.querySelector(".tv-cmd-text")).toHaveTextContent(command);
		fireEvent.click(screen.getByRole("button", { name: "伪代码" }));
		expect(screen.getByTestId("bash-pseudo")).toBeInTheDocument();
	});

	it("没有 orynPseudo（旧会话、还没有轨迹）只显示原始命令，没有视图切换", () => {
		const { container } = render(
			<ToolView name="bash" args={{ command }} running defaultOpen />,
		);
		expect(container.querySelector(".tv-cmd-text")).toHaveTextContent(command);
		expect(screen.queryByRole("button", { name: "原始" })).toBeNull();
	});

	it("运行中读部分结果里的 pseudo：正在执行的行标出，其余行弱化", () => {
		const live = {
			v: 1,
			running: true,
			rows: [
				{ depth: 0, state: "ok", segments: [{ text: "x = 1" }] },
				{
					depth: 0,
					state: "running",
					active: true,
					segments: [{ text: "sleep 1" }],
				},
			],
		};
		render(
			<ToolView
				name="bash"
				args={{ command: "x=1; sleep 1" }}
				running
				defaultOpen
				partialResult={{ content: [], details: { orynPseudo: live } }}
			/>,
		);
		const rows = screen
			.getByTestId("bash-pseudo")
			.querySelectorAll(".tv-pseudo-row");
		expect(rows[0]).toHaveClass("tv-pseudo-row--idle");
		expect(rows[1]).toHaveClass("tv-pseudo-row--active");
		expect(rows[1]).toHaveTextContent("›sleep 1");
	});
});

describe("ask 的参数形态（Core 19 `recoverAskHtml`）", () => {
	const form =
		'<form><fieldset><legend>Which DB?</legend><label><input type="radio" name="db" value="sqlite"> SQLite</label></fieldset></form>';

	it("裸字符串 HTML 参数照样显示表单，不标参数无效", () => {
		const { container } = render(
			<ToolView name="ask" args={form} defaultOpen />,
		);
		expect(container).toHaveTextContent("Which DB?");
		expect(container.querySelector(".tv-err-text")).toBeNull();
	});

	it("旧版 questions 列表重建成表单显示", () => {
		const { container } = render(
			<ToolView
				name="ask"
				args={{
					questions: [
						{ id: "db", question: "Which DB?", options: [{ label: "SQLite" }] },
					],
				}}
				defaultOpen
			/>,
		);
		expect(container).toHaveTextContent("Which DB?");
		expect(container.querySelector(".tv-err-text")).toBeNull();
	});
});
