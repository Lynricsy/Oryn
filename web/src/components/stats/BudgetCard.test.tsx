import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import type { BudgetStatus } from "../../api/stats-queries";
import { BudgetCard, BudgetSettingsLink, hasBudget } from "./BudgetCard";

const status: BudgetStatus = {
	budget: {
		dailyUsd: 10,
		monthlyUsd: 200,
		warnPct: 80,
		timezone: "UTC",
		enabled: true,
	},
	day: {
		key: "2026-11-15",
		from: 0,
		to: 1,
		spentUsd: 8.5,
		limitUsd: 10,
		level: "warn",
	},
	month: {
		key: "2026-11",
		from: 0,
		to: 1,
		spentUsd: 214.5,
		limitUsd: 200,
		level: "over",
	},
};

function wrap(node: React.ReactNode) {
	return render(<MemoryRouter>{node}</MemoryRouter>);
}

describe("BudgetCard", () => {
	it("当日、当月两条：data-budget-period / data-level，量表按级别着色", () => {
		wrap(<BudgetCard status={status} />);
		const card = screen.getByTestId("budget-card");
		const day = card.querySelector("[data-budget-period=day]") as HTMLElement;
		const month = card.querySelector(
			"[data-budget-period=month]",
		) as HTMLElement;
		expect(day).toHaveAttribute("data-level", "warn");
		expect(month).toHaveAttribute("data-level", "over");
		expect(day).toHaveTextContent("$8.50 / $10.00");
		// 文字用 ink 令牌（对比度见 BudgetCard.contrast.test），量表仍是原警告色
		expect(within(day).getByText("接近上限")).toHaveClass("text-warn-ink");
		expect(within(month).getByText("已超出")).toHaveClass("text-err");
		const fill = (el: HTMLElement) =>
			within(el).getByRole("meter").firstElementChild as HTMLElement;
		expect(fill(day).className).toMatch(/bg-warn/);
		expect(fill(month).className).toMatch(/bg-err/);
	});

	it("只看不改：没有编辑按钮，「到设置里修改」链到设置 → 通知与预算；告警停用时带徽标", () => {
		wrap(
			<BudgetCard
				status={{ ...status, budget: { ...status.budget, enabled: false } }}
			/>,
		);
		expect(screen.queryByRole("button")).toBeNull();
		expect(screen.getByRole("link", { name: "到设置里修改" })).toHaveAttribute(
			"href",
			"/settings/notifications",
		);
		expect(screen.getByText("告警已停用")).toBeInTheDocument();
	});

	it("BudgetSettingsLink（页头「预算…」）也是链到设置的链接", () => {
		wrap(<BudgetSettingsLink>预算…</BudgetSettingsLink>);
		expect(screen.getByRole("link", { name: "预算…" })).toHaveAttribute(
			"href",
			"/settings/notifications",
		);
	});

	it("hasBudget：日或月任一设了上限", () => {
		expect(hasBudget(status)).toBe(true);
		expect(
			hasBudget({
				...status,
				budget: { warnPct: 80, timezone: "UTC", enabled: false },
			}),
		).toBe(false);
		expect(hasBudget(undefined)).toBe(false);
	});
});
