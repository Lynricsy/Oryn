/**
 * 设置 → 技能与指令：作用范围说明只出现一次、各类一个分组；停用技能 x 后 config.yml 的 `disabledExtensions` 含 `skill:x`。
 */
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	agentFetch,
	lastDisabled,
	renderPage,
	resetHome,
} from "./agent-config-mock";
import InstructionsPage from "./InstructionsPage";

vi.mock("../../../components/editor/CodeEditor", () => ({
	default: () => null,
}));

beforeEach(() => {
	resetHome({
		"skills/x/SKILL.md": "---\ndescription: 示例技能\n---\n正文\n",
	});
	vi.stubGlobal("fetch", agentFetch);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("InstructionsPage", () => {
	it("作用范围说明只出现一次；技能、提示词与上下文、命令、规则、提示片段、instruction 各一组", async () => {
		renderPage(<InstructionsPage />);
		expect(
			screen.getAllByText("保存后下发到 orb，不作用于全局助手。"),
		).toHaveLength(1);
		await screen.findByRole("switch", { name: "启用 x" });
		expect(
			screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent),
		).toHaveLength(6);
	});

	it("停用技能 x：PUT config.yml，disabledExtensions 含 skill:x", async () => {
		renderPage(<InstructionsPage />);
		const sw = await screen.findByRole("switch", { name: "启用 x" });
		await waitFor(() => expect(sw).toBeEnabled());
		fireEvent.click(sw);
		await waitFor(() => expect(lastDisabled()).toEqual(["skill:x"]));
	});
});
