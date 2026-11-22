import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ExtensionUiRequest } from "../../omp/types";
import { answerUi } from "../../state/actions";
import { uiStore } from "../../state/ui";
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

describe("UiRequestCard：desktop_ask_user", () => {
	it("按约定的标题画成桌面卡片：事项进标题、原因进正文，作答回传原文选项；「打开桌面」切到 Desktop 面板", () => {
		vi.mocked(answerUi).mockClear();
		const { container } = card({
			type: "extension_ui_request",
			id: "d1",
			method: "select",
			title: "需要你操作桌面（输入验证码）：输入短信验证码",
			options: ["完成", "跳过"],
		});
		expect(container.querySelector("h3")?.textContent).toBe(
			"需要你在桌面上输入验证码",
		);
		expect(screen.getByTestId("desktop-ask-card")).toHaveTextContent(
			"输入短信验证码",
		);
		fireEvent.click(screen.getByRole("button", { name: "打开桌面" }));
		expect(uiStore.getState()).toMatchObject({
			panelTab: "desktop",
			panelOpen: true,
		});
		fireEvent.click(screen.getByRole("button", { name: "跳过" }));
		expect(answerUi).toHaveBeenCalledWith("T-ui", {
			type: "extension_ui_response",
			id: "d1",
			value: "跳过",
		});
	});

	it("选项不符约定时仍是普通 select", () => {
		card({
			type: "extension_ui_request",
			id: "d2",
			method: "select",
			title: "需要你操作桌面：确认",
			options: ["完成", "跳过", "取消"],
		});
		expect(screen.queryByTestId("desktop-ask-card")).toBeNull();
	});
});

describe("UiRequestCard：助手的确认卡（M19）", () => {
	const base = {
		type: "extension_ui_request",
		method: "confirm",
		title: 'Delete schedule "Daily report" (sch_1)?',
		message: "The schedule will be deleted.",
	} as const;

	it("带 oryn.action 时标题用语言包文案，作答照常回传", () => {
		const req = {
			...base,
			id: "cfm_1",
			oryn: { action: "delete_schedule", targetLabel: "Daily report" },
		};
		const { container } = card(req);
		expect(container.querySelector("h3")?.textContent).toBe(
			"删除定时「Daily report」？",
		);
		fireEvent.click(screen.getByRole("button", { name: "是" }));
		expect(answerUi).toHaveBeenCalledWith("T-ui", {
			type: "extension_ui_response",
			id: "cfm_1",
			confirmed: true,
		});
	});

	it("没有对应文案的动作回落到帧的 title", () => {
		const req = {
			...base,
			id: "cfm_2",
			oryn: { action: "launch_rocket", targetLabel: "x" },
		};
		const { container } = card(req);
		expect(container.querySelector("h3")?.textContent).toBe(base.title);
	});
});
