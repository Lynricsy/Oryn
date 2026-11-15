/** 当前工具报告：按来源分组（含 xd:// 挂载设备）、来源徽标、搜索与无结果、描述第一段 + 展开全文、空态。 */
import { fireEvent, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetThreadStores } from "../../../state/stores";
import { uiStore } from "../../../state/ui";
import { type Frame, openThreadDialog } from "./test-helpers";

const { cmd } = vi.hoisted(() => ({
	cmd: vi.fn<(threadId: string, frame: Frame) => Promise<unknown>>(),
}));
vi.mock("../../../realtime/socket", () => ({
	realtime: {
		cmd: (t: string, f: Frame) => cmd(t, f),
		subscribe: () => () => undefined,
		focus: () => undefined,
	},
	OmpCommandError: class extends Error {},
}));

const T = "T-tools";

const TOOLS = {
	tools: [
		{
			name: "bash",
			source: "builtin",
			description:
				"Run a shell command.\n\nLong usage notes that only show when expanded.",
		},
		{ name: "read", source: "builtin", description: "Read a file." },
		{
			name: "mcp__github__create_issue",
			source: "mcp",
			description: "Create a GitHub issue.",
		},
		{ name: "todo", source: "extension" },
		{
			name: "browser",
			source: "builtin",
			mounted: true,
			description: "Drive a real browser tab.",
		},
	],
};

let tools: unknown = TOOLS;

beforeEach(() => {
	cmd.mockReset();
	resetThreadStores();
	tools = TOOLS;
	cmd.mockImplementation(async (_t, f) =>
		f.type === "get_active_tools" ? tools : {},
	);
});

afterEach(() => uiStore.getState().close());

const open = () =>
	openThreadDialog(T, { kind: "report", report: "get_active_tools" });

const group = (g: string) =>
	vi.waitFor(() => {
		const el = document.querySelector<HTMLElement>(`[data-group="${g}"]`);
		if (!el) throw new Error(`分组 ${g} 未渲染`);
		return el;
	});

describe("ToolsReport", () => {
	it("按来源分组：内置 / MCP / 扩展 / xd:// 挂载设备，挂载项不混进内置", async () => {
		open();
		const builtin = await group("builtin");
		expect(
			[...builtin.querySelectorAll("[data-tool]")].map((e) =>
				e.getAttribute("data-tool"),
			),
		).toEqual(["bash", "read"]);
		const mounted = await group("mounted");
		expect(within(mounted).getByText("xd://browser")).toBeTruthy();
		// MCP 工具带服务器名徽标
		expect(within(await group("mcp")).getByText("github")).toBeTruthy();
		expect(within(await group("extension")).getByText("没有描述")).toBeTruthy();
		for (const label of ["内置", "MCP", "扩展", "xd:// 挂载设备"])
			expect(screen.getByText(label)).toBeTruthy();
		expect(screen.getByText("共 5 个")).toBeTruthy();
	});

	it("描述先显示第一段，点「展开全文」看全部", async () => {
		open();
		const bash = (await group("builtin")).querySelector<HTMLElement>(
			'[data-tool="bash"]',
		);
		if (!bash) throw new Error("bash 行缺失");
		expect(within(bash).queryByText(/Long usage notes/)).toBeNull();
		fireEvent.click(within(bash).getByRole("button", { name: "展开全文" }));
		expect(within(bash).getByText(/Long usage notes/)).toBeTruthy();
		expect(within(bash).getByRole("button", { name: "收起" })).toBeTruthy();
	});

	it("搜索按名称和描述过滤；没有匹配时说明", async () => {
		open();
		const input = await screen.findByRole("searchbox", {
			name: "搜索工具名或描述…",
		});
		fireEvent.change(input, { target: { value: "github" } });
		expect(document.querySelectorAll("[data-tool]")).toHaveLength(1);
		expect(screen.getByText("1 / 5 个")).toBeTruthy();
		fireEvent.change(input, { target: { value: "browser tab" } });
		expect(document.querySelector('[data-tool="browser"]')).not.toBeNull();
		fireEvent.change(input, { target: { value: "zzz" } });
		expect(screen.getByText("没有匹配「zzz」的工具")).toBeTruthy();
	});

	it("没有工具：空态说明", async () => {
		tools = { tools: [] };
		open();
		expect(await screen.findByText("agent 当前看不到任何工具")).toBeTruthy();
	});
});
