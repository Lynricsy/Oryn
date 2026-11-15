/**
 * 设置 → Agent → 技能：标注作用范围；停用技能 x 后 config.yml 的 `disabledExtensions` 含 `skill:x`。
 */
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	agentFetch,
	lastDisabled,
	renderPage,
	resetHome,
} from "./agent-config-mock";
import SkillsPage from "./SkillsPage";

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

describe("SkillsPage", () => {
	it("停用技能 x：PUT config.yml，disabledExtensions 含 skill:x", async () => {
		renderPage(<SkillsPage />);
		expect(
			screen.getByText("保存后下发到 orb，不作用于全局助手。"),
		).toBeInTheDocument();
		const sw = await screen.findByRole("checkbox", { name: "启用 x" });
		await waitFor(() => expect(sw).toBeEnabled());
		fireEvent.click(sw);
		await waitFor(() => expect(lastDisabled()).toEqual(["skill:x"]));
	});
});
