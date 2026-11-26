/** 设置 → 通知与预算 → 预算：行里的当前值、告警开关立即保存、编辑对话框整体 PUT、无效金额不能保存。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Budget, BudgetStatus } from "../../api/stats-queries";
import { BudgetGroup } from "./BudgetSettings";

const status: BudgetStatus = {
	budget: {
		dailyUsd: 10,
		warnPct: 80,
		timezone: "Asia/Shanghai",
		enabled: true,
	},
	day: {
		key: "2026-11-27",
		from: 0,
		to: 1,
		spentUsd: 8.5,
		limitUsd: 10,
		level: "warn",
	},
	month: { key: "2026-11", from: 0, to: 1, spentUsd: 42, level: "none" },
};

let puts: Budget[];

beforeEach(() => {
	puts = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (req: Request) => {
			if (new URL(req.url).pathname !== "/api/v1/stats/budget")
				return new Response(null, { status: 404 });
			if (req.method === "PUT") {
				const body = (await req.json()) as Budget;
				puts.push(body);
				return Response.json({ ...status, budget: body });
			}
			return Response.json(status);
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

function mount() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	render(
		<MemoryRouter>
			<QueryClientProvider client={client}>
				<BudgetGroup />
			</QueryClientProvider>
		</MemoryRouter>,
	);
}

const group = () => screen.getByRole("region", { name: "预算" });
const row = (title: string) =>
	within(group())
		.getByText(title, { selector: "div" })
		.closest("li") as HTMLElement;

describe("预算分组", () => {
	it("各行显示当前值与本期已用；「查看用量」链到统计页", async () => {
		mount();
		await screen.findByText("今日已用 $8.50");
		expect(row("日上限")).toHaveTextContent("$10.00");
		expect(row("日上限")).toHaveTextContent("接近上限");
		expect(row("月上限")).toHaveTextContent("未设上限");
		expect(row("月上限")).toHaveTextContent("本月已用 $42.00");
		expect(row("预警线")).toHaveTextContent("80%");
		expect(row("时区")).toHaveTextContent("Asia/Shanghai");
		expect(within(row("预算告警")).getByText(/预算事件/)).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "查看用量" })).toHaveAttribute(
			"href",
			"/stats/usage",
		);
	});

	it("告警开关立即保存：整体 PUT，其余字段不变", async () => {
		mount();
		const sw = await within(group()).findByRole("switch", {
			name: "预算告警",
		});
		expect(sw).toBeChecked();
		fireEvent.click(sw);
		await waitFor(() => expect(puts).toHaveLength(1));
		expect(puts[0]).toEqual({ ...status.budget, enabled: false });
		await waitFor(() => expect(sw).not.toBeChecked());
	});

	it("编辑：清空日上限、填月上限与预警线，PUT 里留空的上限不提交，开关原样带上", async () => {
		mount();
		await screen.findByText("今日已用 $8.50");
		fireEvent.click(within(group()).getByRole("button", { name: "编辑" }));
		const dialog = await screen.findByRole("dialog", { name: "编辑预算" });
		fireEvent.change(within(dialog).getByLabelText("日上限（美元）"), {
			target: { value: "" },
		});
		fireEvent.change(within(dialog).getByLabelText("月上限（美元）"), {
			target: { value: "150" },
		});
		fireEvent.change(within(dialog).getByLabelText("预警线（%）"), {
			target: { value: "90" },
		});
		expect(within(dialog).getByLabelText("时区")).toHaveValue("Asia/Shanghai");
		fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(puts).toEqual([
			{
				monthlyUsd: 150,
				warnPct: 90,
				timezone: "Asia/Shanghai",
				enabled: true,
			},
		]);
	});

	it("金额小于 0.01 或预警线超出 0–99 时不能保存", async () => {
		mount();
		await screen.findByText("今日已用 $8.50");
		fireEvent.click(within(group()).getByRole("button", { name: "编辑" }));
		const dialog = await screen.findByRole("dialog", { name: "编辑预算" });
		const save = within(dialog).getByRole("button", { name: "保存" });
		fireEvent.change(within(dialog).getByLabelText("月上限（美元）"), {
			target: { value: "0.001" },
		});
		expect(save).toBeDisabled();
		expect(
			within(dialog).getByText("金额留空表示不设，填写时至少 0.01。"),
		).toBeInTheDocument();
		fireEvent.change(within(dialog).getByLabelText("月上限（美元）"), {
			target: { value: "" },
		});
		expect(save).toBeEnabled();
		fireEvent.change(within(dialog).getByLabelText("预警线（%）"), {
			target: { value: "100" },
		});
		expect(save).toBeDisabled();
	});
});
