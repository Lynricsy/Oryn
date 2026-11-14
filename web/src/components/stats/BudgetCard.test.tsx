import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { BudgetStatus } from "../../api/stats-queries";
import { BudgetCard, BudgetDialog, hasBudget } from "./BudgetCard";

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
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<MemoryRouter>
			<QueryClientProvider client={client}>{node}</QueryClientProvider>
		</MemoryRouter>,
	);
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("BudgetCard", () => {
	it("当日、当月两条：data-budget-period / data-level，量表按级别着色", () => {
		wrap(<BudgetCard status={status} onEdit={() => {}} />);
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

describe("BudgetDialog", () => {
	it("提交 PUT stats/budget：留空的上限不提交；底部说明链到通知设置", async () => {
		const bodies: unknown[] = [];
		vi.stubGlobal(
			"fetch",
			vi.fn(async (req: Request) => {
				bodies.push(await req.json());
				return Response.json(status);
			}),
		);
		const onClose = vi.fn();
		wrap(<BudgetDialog status={undefined} onClose={onClose} />);
		const dialog = screen.getByRole("dialog", { name: "设置预算" });
		expect(
			within(dialog).getByRole("link", { name: "「设置 → 通知」" }),
		).toHaveAttribute("href", "/settings/notifications");
		fireEvent.change(within(dialog).getByLabelText("日上限（美元）"), {
			target: { value: "12.5" },
		});
		fireEvent.change(within(dialog).getByLabelText("时区（IANA）"), {
			target: { value: "Asia/Shanghai" },
		});
		fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
		await waitFor(() => expect(onClose).toHaveBeenCalled());
		expect(bodies).toEqual([
			{ dailyUsd: 12.5, warnPct: 80, timezone: "Asia/Shanghai", enabled: true },
		]);
	});

	it("金额小于 0.01 时不能保存", () => {
		wrap(<BudgetDialog status={status} onClose={() => {}} />);
		const dialog = screen.getByRole("dialog", { name: "设置预算" });
		fireEvent.change(within(dialog).getByLabelText("月上限（美元）"), {
			target: { value: "0.001" },
		});
		expect(within(dialog).getByRole("button", { name: "保存" })).toBeDisabled();
	});
});
