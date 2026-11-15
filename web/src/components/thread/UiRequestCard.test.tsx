import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ExtensionUiRequest } from "../../omp/types";
import { UiRequestCard } from "./UiRequestCard";

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
