/** 计划提案：收起成胶囊后，`/plan-review`（uiStore.reviewPlan）能让对话框重新出现。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import type { RpcPlanProposalFrame } from "../../omp/types";
import { uiStore } from "../../state/ui";
import { PlanProposalDialog } from "./Dialogs";

vi.mock("../../realtime/socket", () => ({
	realtime: {
		cmd: async () => ({}),
		uiResponse: () => true,
		subscribe: () => () => undefined,
		focus: () => undefined,
	},
	OmpCommandError: class extends Error {},
}));

const proposal = {
	type: "plan_proposal",
	title: "重构登录",
	planContent: "1. 拆分表单",
	planFilePath: "/repo/.omp/plan.md",
	suggestedFileName: "plan.md",
} as unknown as RpcPlanProposalFrame;

describe("PlanProposalDialog", () => {
	beforeEach(() => uiStore.setState({ planReviewNonce: 0 }));

	it("关闭后收成胶囊；reviewPlan() 让对话框重新出现", () => {
		render(
			<QueryClientProvider client={new QueryClient()}>
				<MemoryRouter>
					<PlanProposalDialog threadId="T-1" proposal={proposal} />
				</MemoryRouter>
			</QueryClientProvider>,
		);
		expect(screen.getByRole("dialog")).toBeInTheDocument();
		fireEvent.keyDown(window, { key: "Escape" });
		expect(screen.queryByRole("dialog")).toBeNull();
		expect(
			screen.getByRole("button", { name: i18n.t("plan.review") }),
		).toBeInTheDocument();
		act(() => uiStore.getState().reviewPlan());
		expect(screen.getByRole("dialog")).toBeInTheDocument();
		// 再次收起仍然有效（记下的是新的序号）
		fireEvent.keyDown(window, { key: "Escape" });
		expect(screen.queryByRole("dialog")).toBeNull();
	});
});
