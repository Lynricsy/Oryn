/** 计划提案：收起成胶囊后 `/plan-review` 能重新打开；审批应答（保存路径、未派发原因）落成转录提示。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import type { RpcPlanProposalFrame } from "../../omp/types";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { PlanProposalDialog } from "./Dialogs";

const cmd = vi.hoisted(() => vi.fn(async (..._args: unknown[]) => ({})));

vi.mock("../../realtime/socket", () => ({
	realtime: {
		cmd,
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

function mount() {
	render(
		<QueryClientProvider client={new QueryClient()}>
			<MemoryRouter>
				<PlanProposalDialog threadId="T-1" proposal={proposal} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

function lastNotice() {
	return getThreadStore("T-1").getState().notices.at(-1);
}

describe("PlanProposalDialog", () => {
	beforeEach(() => {
		uiStore.setState({ planReviewNonce: 0 });
		resetThreadStores();
		cmd.mockReset();
		cmd.mockResolvedValue({});
	});

	it("关闭后收成胶囊；reviewPlan() 让对话框重新出现", () => {
		mount();
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

	it("仅保存：转录里提示实际写入的路径", async () => {
		cmd.mockResolvedValue({
			approved: true,
			dispatched: false,
			reason: "saved",
			savedPath: "/repo/plans/login.md",
			freshSessionStarted: true,
		});
		mount();
		const save = screen.getByRole("button", {
			name: i18n.t("plan.option.save"),
		});
		fireEvent.click(save);
		fireEvent.change(screen.getByDisplayValue("plan.md"), {
			target: { value: "/repo/plans/login.md" },
		});
		fireEvent.click(save);
		await waitFor(() =>
			expect(lastNotice()).toMatchObject({
				level: "info",
				text: i18n.t("plan.result.saved", { path: "/repo/plans/login.md" }),
			}),
		);
		expect(cmd.mock.calls[0]?.[1]).toEqual({
			type: "plan_approval",
			approved: true,
			option: "save",
			savePath: "/repo/plans/login.md",
		});
	});

	it("批准但未派发（压缩失败）：转录里给出原因", async () => {
		cmd.mockResolvedValue({
			approved: true,
			dispatched: false,
			reason: "compaction failed: boom",
		});
		mount();
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("plan.option.compact") }),
		);
		await waitFor(() =>
			expect(lastNotice()).toMatchObject({
				level: "warning",
				text: i18n.t("plan.result.approvedNotDispatched", {
					reason: "compaction failed: boom",
				}),
			}),
		);
	});

	it("已派发的执行不另加提示", async () => {
		cmd.mockResolvedValue({ approved: true, dispatched: true });
		mount();
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("plan.option.execute") }),
		);
		await waitFor(() =>
			expect(getThreadStore("T-1").getState().planProposal).toBeNull(),
		);
		expect(lastNotice()).toBeUndefined();
	});
});
