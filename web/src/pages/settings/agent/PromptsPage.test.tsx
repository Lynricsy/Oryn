/**
 * 设置 → Agent → 提示词与上下文：六个文件的存在状态；AGENTS.md 启停写 `context-file:user:AGENTS.md`，
 * RULES.md 写 `rule:RULES`；未创建的文件开关不可用、可直接创建；SYSTEM.md 存在时与编辑时都有警示；删除前确认。
 */
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	agentFetch,
	home,
	lastDisabled,
	renderPage,
	resetHome,
} from "./agent-config-mock";
import PromptsPage from "./PromptsPage";

vi.mock("../../../components/editor/CodeEditor", () => ({
	default: ({
		value,
		path,
		onChange,
	}: {
		value: string;
		path: string;
		onChange?(v: string): void;
	}) => (
		<textarea
			aria-label={`editor:${path}`}
			value={value}
			onChange={(e) => onChange?.(e.target.value)}
		/>
	),
}));

const WARNING = "SYSTEM.md 会整段替换内置系统提示词";

beforeEach(() => {
	resetHome({
		"AGENTS.md": "# 约定\n",
		"SYSTEM.md": "你是一个助手。\n",
	});
	vi.stubGlobal("fetch", agentFetch);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("PromptsPage", () => {
	it("AGENTS.md 启停写 context-file:user:AGENTS.md；未创建的 RULES.md 开关不可用", async () => {
		renderPage(<PromptsPage />);
		const agents = await screen.findByRole("checkbox", {
			name: "启用 AGENTS.md",
		});
		expect(
			screen.getByRole("checkbox", { name: "启用 RULES.md" }),
		).toBeDisabled();
		await waitFor(() => expect(agents).toBeEnabled());
		fireEvent.click(agents);
		await waitFor(() =>
			expect(lastDisabled()).toEqual(["context-file:user:AGENTS.md"]),
		);
	});

	it("SYSTEM.md 存在时显示警示，编辑时也有；保存带新内容", async () => {
		renderPage(<PromptsPage />);
		expect(await screen.findByText(WARNING)).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "编辑 SYSTEM.md" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText(WARNING)).toBeInTheDocument();
		const editor = await within(dialog).findByLabelText("editor:SYSTEM.md");
		expect(editor).toHaveValue("你是一个助手。\n");
		fireEvent.change(editor, { target: { value: "新的系统提示\n" } });
		fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
		await waitFor(() =>
			expect(home.puts).toEqual([
				{ path: "SYSTEM.md", body: "新的系统提示\n" },
			]),
		);
	});

	it("创建未存在的 PERSONALITY.md；删除 AGENTS.md 前确认", async () => {
		renderPage(<PromptsPage />);
		fireEvent.click(
			await screen.findByRole("button", { name: "创建 PERSONALITY.md" }),
		);
		const dialog = await screen.findByRole("dialog");
		const editor = await within(dialog).findByLabelText(
			"editor:PERSONALITY.md",
		);
		expect(editor).toHaveValue("");
		fireEvent.change(editor, { target: { value: "简洁、直接。\n" } });
		fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
		await waitFor(() =>
			expect(home.puts).toEqual([
				{ path: "PERSONALITY.md", body: "简洁、直接。\n" },
			]),
		);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

		fireEvent.click(screen.getByRole("button", { name: "删除 AGENTS.md" }));
		const confirm = await screen.findByRole("dialog");
		fireEvent.click(within(confirm).getByRole("button", { name: "删除" }));
		await waitFor(() => expect(home.deletes).toEqual(["AGENTS.md"]));
		expect(
			await screen.findByRole("button", { name: "创建 AGENTS.md" }),
		).toBeInTheDocument();
	});
});
