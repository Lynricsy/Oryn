/**
 * `debug` 卡片：展开体是调试器面板（会话状态栏 + 按动作的结构化内容），头部读数给这一步的落点或条目数。
 */
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ToolView } from "../ToolView";
import type { ToolResultLike } from "../types";

const STOPPED = {
	id: "1",
	adapter: "dlv",
	status: "stopped",
	program: "bin/orynd",
	stopReason: "breakpoint",
	frameName: "realtime.(*Hub).Resume",
	source: { path: "/repo/internal/realtime/hub.go" },
	line: 118,
	column: 2,
	instructionPointerReference: "0x28",
};

function ok(text: string, details: Record<string, unknown>): ToolResultLike {
	return { content: [{ type: "text", text }], details };
}

function card(
	args: Record<string, unknown>,
	result?: ToolResultLike,
	running = false,
) {
	return render(
		<ToolView
			name="debug"
			args={args}
			result={result}
			running={running}
			defaultOpen
		/>,
	).container;
}

const q = (c: HTMLElement, sel: string) => c.querySelector(sel);

describe("debug 卡片", () => {
	it("launch 停在断点：读数给落点，状态栏给状态、停止原因、帧名与位置，不再堆快照原文", () => {
		const c = card(
			{ action: "launch", program: "bin/orynd", args: ["serve"] },
			ok("Session 1\nAdapter: dlv\nStatus: stopped", {
				action: "launch",
				success: true,
				snapshot: STOPPED,
			}),
		);
		expect(q(c, ".tv-sum")).toHaveTextContent("启动");
		expect(q(c, ".tv-meta")).toHaveTextContent("停在 hub.go:118");
		const bar = q(c, ".tv-dbg-bar");
		expect(bar?.className).toContain("tv-dbg-bar--stop");
		expect(bar).toHaveTextContent("已暂停");
		expect(bar).toHaveTextContent("breakpoint");
		expect(bar).toHaveTextContent("realtime.(*Hub).Resume");
		expect(bar).toHaveTextContent("hub.go:118:2");
		expect(bar).toHaveTextContent("dlv · #1");
		expect(q(c, ".tv-sec--in")).toHaveTextContent("bin/orynd serve");
		expect(q(c, ".tv-sec--out")).not.toHaveTextContent("Adapter: dlv");
	});

	it("stack_trace：读数给帧数，第 0 帧标为当前，subtle 帧弱化", () => {
		const c = card(
			{ action: "stack_trace" },
			ok("Stack trace:", {
				action: "stack_trace",
				snapshot: STOPPED,
				stackFrames: [
					{
						id: 1,
						name: "Resume",
						source: { path: "/repo/hub.go" },
						line: 118,
						column: 2,
					},
					{
						id: 2,
						name: "goexit",
						line: 1700,
						column: 0,
						presentationHint: "subtle",
					},
				],
			}),
		);
		expect(q(c, ".tv-meta")).toHaveTextContent("2 帧");
		const rows = [...c.querySelectorAll(".tv-dbg-list .tv-dbg-row")];
		expect(rows).toHaveLength(2);
		expect(rows[0]?.className).toContain("tv-dbg-row--current");
		expect(rows[0]).toHaveTextContent("#0Resumehub.go:118:2");
		expect(rows[1]?.className).toContain("tv-dbg-row--subtle");
	});

	it("variables：名字、值、类型三列，有子项的变量带展开标记", () => {
		const c = card(
			{ action: "variables", variable_ref: 7 },
			ok("Variables:", {
				action: "variables",
				variables: [
					{ name: "n", value: "3", type: "int", variablesReference: 0 },
					{
						name: "m",
						value: "map[2]",
						type: "map",
						variablesReference: 9,
					},
				],
			}),
		);
		expect(q(c, ".tv-meta")).toHaveTextContent("2 个变量");
		const rows = [...c.querySelectorAll(".tv-dbg-vars tr")];
		expect(rows.map((r) => r.textContent)).toEqual(["▸n3int", "▸mmap[2]map"]);
		expect(rows[1]?.querySelector(".tv-dbg-expand[data-on]")).not.toBeNull();
		expect(rows[0]?.querySelector(".tv-dbg-expand[data-on]")).toBeNull();
		expect(q(c, ".tv-sec--in")).toHaveTextContent("variable_ref=7");
	});

	it("set_breakpoint：读数给断点数与未验证数，条件写成 if …", () => {
		const c = card(
			{
				action: "set_breakpoint",
				file: "internal/hub.go",
				line: 131,
				condition: "n > 3",
			},
			ok("Breakpoints", {
				action: "set_breakpoint",
				breakpoints: [
					{ id: 1, verified: true, line: 118 },
					{ id: 2, verified: false, line: 131, condition: "n > 3" },
				],
			}),
		);
		expect(q(c, ".tv-meta")).toHaveTextContent("2 个断点");
		expect(q(c, ".tv-meta .tv-meta-warn")).toHaveTextContent("1 个未验证");
		const dots = [...c.querySelectorAll(".tv-dbg-bp")].map((d) =>
			d.classList.contains("tv-dbg-bp--on"),
		);
		expect(dots).toEqual([true, false]);
		expect(q(c, ".tv-sec--in .tv-dbg-cond")).toHaveTextContent("if n > 3");
	});

	it("continue 后程序非零退出：读数为红色退出码，状态栏为红", () => {
		const c = card(
			{ action: "continue" },
			ok("Program terminated with exit code 2.", {
				action: "continue",
				state: "terminated",
				timedOut: false,
				snapshot: {
					id: "1",
					adapter: "dlv",
					status: "terminated",
					exitCode: 2,
				},
			}),
		);
		expect(q(c, ".tv-meta .tv-meta-err")).toHaveTextContent("已退出 2");
		expect(q(c, ".tv-dbg-bar")?.className).toContain("tv-dbg-bar--err");
	});

	it("continue 超时：读数「仍在运行」，并提示 pause", () => {
		const c = card(
			{ action: "continue" },
			ok("still running", {
				action: "continue",
				state: "running",
				timedOut: true,
				snapshot: { id: "1", adapter: "dlv", status: "running" },
			}),
		);
		expect(q(c, ".tv-meta .tv-meta-warn")).toHaveTextContent("仍在运行");
		expect(q(c, ".tv-note--warn")).toHaveTextContent("pause");
	});

	it("disassemble：当前指令指针所在行高亮", () => {
		const c = card(
			{ action: "disassemble", memory_reference: "0x20", instruction_count: 2 },
			ok("Disassembly:", {
				action: "disassemble",
				snapshot: STOPPED,
				disassembly: [
					{ address: "0x20", instruction: "nop" },
					{ address: "0x28", instruction: "push rbp" },
				],
			}),
		);
		expect(q(c, ".tv-meta")).toHaveTextContent("2 条指令");
		expect(q(c, ".tv-dbg-asm--ip")).toHaveTextContent("push rbp");
	});

	it("read_memory：十六进制转储带地址与 ASCII", () => {
		const c = card(
			{ action: "read_memory", memory_reference: "0x1000", count: 4 },
			ok("Memory at 0x1000:", {
				action: "read_memory",
				memoryAddress: "0x1000",
				memoryData: btoa("ABCD"),
			}),
		);
		expect(q(c, ".tv-meta")).toHaveTextContent("4 字节");
		expect(q(c, ".tv-sec--out pre")).toHaveTextContent(
			"0x00001000 41 42 43 44",
		);
		expect(q(c, ".tv-sec--out pre")).toHaveTextContent("ABCD");
	});

	it("出错：首行醒目，其余可折叠，没有读数", () => {
		const c = card(
			{ action: "launch", program: "./a.py" },
			{
				content: [
					{
						type: "text",
						text: "adapter 'debugpy' is not available\nConfigured adapters: dlv",
					},
				],
				isError: true,
			},
		);
		expect(q(c, ".tv-meta")).toBeEmptyDOMElement();
		expect(q(c, ".tv-note--err")).toHaveTextContent(
			"adapter 'debugpy' is not available",
		);
		expect(q(c, ".tv-sec--out")).toHaveTextContent("Configured adapters: dlv");
	});

	it("运行中、参数残缺：不崩，摘要给出已到的动作", () => {
		const c = card({ action: "step_over" }, undefined, true);
		expect(q(c, ".tv-sum")).toHaveTextContent("单步跳过");
		expect(q(c, ".tv-meta")).toBeEmptyDOMElement();
	});
});
