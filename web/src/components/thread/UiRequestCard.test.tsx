import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ExtensionUiRequest } from "../../omp/types";
import { answerUi } from "../../state/actions";
import { UiRequestCard } from "./UiRequestCard";

vi.mock("../../state/actions", async (importOriginal) => ({
	...(await importOriginal<Record<string, unknown>>()),
	answerUi: vi.fn(),
}));

const RULE = [
	"---",
	"name: no-npm",
	'description: "Use bun"',
	'condition: "\\\\bnpm install\\\\b"',
	'scope: "text"',
	"---",
	"",
	"Use `bun install`.",
].join("\n");

function card(req: ExtensionUiRequest) {
	return render(<UiRequestCard threadId="T-ui" req={req} queued={0} />);
}

describe("UiRequestCard", () => {
	it("/omfg confirm：message 尾部的规则拆出来用规则卡，前半句照常", () => {
		const { container } = card({
			type: "extension_ui_request",
			id: "c1",
			method: "confirm",
			title: "Validation",
			message: `Couldn't confirm this rule matches the conversation. Save anyway?\n\n${RULE}`,
		});
		const rule = container.querySelector("[data-rule-card]");
		expect(rule?.querySelector("h4")).toHaveTextContent("no-npm");
		expect(
			rule?.querySelector('[data-field="condition"] code')?.textContent,
		).toBe("\\bnpm install\\b");
		const p = container.querySelector("p.whitespace-pre-wrap");
		expect(p?.textContent).toBe(
			"Couldn't confirm this rule matches the conversation. Save anyway?",
		);
		// confirm 里没有校验状态
		expect(rule?.querySelector("[data-validation]")).toBeNull();
	});

	it("普通 confirm 原样", () => {
		const { container } = card({
			type: "extension_ui_request",
			id: "c2",
			method: "confirm",
			title: "Overwrite TTSR rule?",
			message: "a.md already exists. Overwrite it?",
		});
		expect(container.querySelector("[data-rule-card]")).toBeNull();
		expect(
			screen.getByText("a.md already exists. Overwrite it?"),
		).toBeVisible();
	});

	it("工具审批 select：标题栏只放首行，Reason 成键值，参数等宽原样可见", () => {
		const { container } = card({
			type: "extension_ui_request",
			id: "s1",
			method: "select",
			title:
				"Allow tool: bash\nReason: writes outside cwd\nCommand: rm -rf /tmp/x",
			options: ["Approve", "Deny"],
		});
		expect(container.querySelector("h3")?.textContent).toBe("Allow tool: bash");
		expect(container.querySelector("dd")?.textContent).toBe(
			"writes outside cwd",
		);
		expect(container.querySelector("pre")?.textContent).toBe(
			"Command: rm -rf /tmp/x",
		);
	});
});

describe("UiRequestCard：/omfg 保存位置的中文映射（M12.2）", () => {
	const save = (options: string[], title = "Save TTSR rule where?") =>
		card({
			type: "extension_ui_request",
			id: "o1",
			method: "select",
			title,
			options,
		});

	it("新 Core 的三项显示中文，标题也映射；点选回传 Core 原文", () => {
		vi.mocked(answerUi).mockClear();
		const { container } = save([
			"This project (Oryn project config)",
			"Global — all projects (Oryn central config)",
			"Amend with feedback…",
		]);
		expect(container.querySelector("h3")?.textContent).toBe(
			"把这条规则保存到哪里？",
		);
		expect(
			[...container.querySelectorAll("button > div:first-child")].map(
				(el) => el.textContent,
			),
		).toEqual([
			"本项目（Oryn 项目配置）",
			"全局（Oryn 中央配置）",
			"修改反馈…",
		]);
		fireEvent.click(screen.getByText("全局（Oryn 中央配置）"));
		expect(answerUi).toHaveBeenCalledWith("T-ui", {
			type: "extension_ui_response",
			id: "o1",
			value: "Global — all projects (Oryn central config)",
		});
	});

	it("旧 Core / runner 的上游原文同样映射，回传原文", () => {
		vi.mocked(answerUi).mockClear();
		save([
			"This project (.omp/rules)",
			"Global — all projects (~/.omp/agent/rules)",
			"Amend with feedback…",
		]);
		fireEvent.click(screen.getByText("本项目（仓库 .omp/rules）"));
		expect(answerUi).toHaveBeenCalledWith("T-ui", {
			type: "extension_ui_response",
			id: "o1",
			value: "This project (.omp/rules)",
		});
		expect(screen.getByText("全局（~/.omp/agent/rules）")).toBeVisible();
	});

	it("不在表里的选项原样；别的对话框里同样的文案不映射", () => {
		const first = save([
			"This project (Oryn project config)",
			"Somewhere else",
		]);
		expect(screen.getByText("Somewhere else")).toBeVisible();
		first.unmount();
		const { container } = save(
			["Amend with feedback…", "Skip"],
			"Pick an action",
		);
		expect(container.querySelector("h3")?.textContent).toBe("Pick an action");
		expect(container).toHaveTextContent("Amend with feedback…");
		expect(container).not.toHaveTextContent("修改反馈…");
	});
});
