/**
 * 确认条卸载回归（M12.1 走查问题 1）：较新的 Chromium 里 `scrollIntoView` 返回 Promise，
 * 确认条若把它当 effect 的返回值交给 React，卸载时会被当成清理函数调用而抛 TypeError，整页白屏。
 * 这里让 `scrollIntoView` 返回 Promise，走一遍各条会卸载确认条的路径：停止任务、放弃目标、重置额度、「保留」/「取消」。
 */
import { fireEvent, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetThreadStores } from "../../../state/stores";
import { uiStore } from "../../../state/ui";
import { type Frame, openThreadDialog, sentFrames } from "./test-helpers";

const { cmd } = vi.hoisted(() => ({
	cmd: vi.fn<(threadId: string, frame: Frame) => Promise<unknown>>(),
}));
vi.mock("../../../realtime/socket", () => ({
	realtime: {
		cmd: (t: string, f: Frame) => cmd(t, f),
		subscribe: () => () => undefined,
		focus: () => undefined,
	},
	OmpCommandError: class extends Error {},
}));

const T = "T-confirm-strip";

const BASH = {
	id: "bash-1",
	type: "bash",
	status: "running",
	label: "sleep 300",
	startTime: Date.now() - 5_000,
};

const ACCOUNT = {
	label: "owner@example.com",
	provider: "anthropic",
	providerLabel: "Claude",
	availableCount: 2,
	redeemableCount: 1,
	target: { provider: "anthropic", credentialId: 7 },
	active: true,
};

const GOAL = {
	enabled: true,
	status: "active",
	objective: "把报告对话框打磨到可发布",
	tokensUsed: 1_000,
	mode: "active",
};

const scrollIntoView = vi.fn(() => Promise.resolve());

beforeEach(() => {
	cmd.mockReset();
	scrollIntoView.mockClear();
	resetThreadStores();
	// Chromium 156 的 scrollIntoView 返回 Promise（jsdom 没有这个方法）
	Element.prototype.scrollIntoView =
		scrollIntoView as unknown as Element["scrollIntoView"];
	cmd.mockImplementation(async (_t, f) => {
		switch (f.type) {
			case "get_jobs":
				return {
					jobs: [BASH],
					tree: [{ depth: 0, job: BASH, cancellable: true }],
				};
			case "oryn_cancel_job":
				return { cancelled: true };
			case "get_goal":
				return GOAL;
			case "get_usage":
				return {
					reports: [],
					session: {
						input: 1,
						output: 1,
						cacheRead: 0,
						cacheWrite: 0,
						totalTokens: 2,
						orchestrationTokens: 0,
						premiumRequests: 0,
						cost: 0.01,
					},
				};
			case "list_reset_credits":
				return { accounts: [ACCOUNT] };
			case "redeem_reset_credit":
				return { ok: true, code: "reset", message: "Reset applied" };
			default:
				return {};
		}
	});
});

afterEach(() => {
	uiStore.getState().close();
	Reflect.deleteProperty(Element.prototype, "scrollIntoView");
});

const jobRow = () =>
	vi.waitFor(() => {
		const el = document.querySelector<HTMLElement>('[data-job="bash-1"]');
		if (!el) throw new Error("任务未渲染");
		return el;
	});

const creditRow = async () => {
	fireEvent.click(await screen.findByRole("button", { name: /查看重置额度/ }));
	return vi.waitFor(() => {
		const el = document.querySelector<HTMLElement>(
			'[data-credit="anthropic/7"]',
		);
		if (!el) throw new Error("重置额度未就绪");
		return el;
	});
};

const goalCard = async () => {
	const h = await screen.findByRole("heading", { name: "当前目标" });
	const s = h.closest("section");
	if (!s) throw new Error("当前目标分区缺失");
	return s;
};

describe("ConfirmStrip：scrollIntoView 返回 Promise 时卸载不崩", () => {
	it("后台任务：确认条出现时滚进视野；「保留」与「停止任务」卸载确认条后对话框仍在", async () => {
		openThreadDialog(T, { kind: "report", report: "get_jobs" });
		const row = await jobRow();
		fireEvent.click(within(row).getByRole("button", { name: "停止" }));
		expect(scrollIntoView).toHaveBeenCalledWith({ block: "nearest" });
		fireEvent.click(within(row).getByRole("button", { name: "保留" }));
		expect(
			within(await jobRow()).getByRole("button", { name: "停止" }),
		).toBeTruthy();
		fireEvent.click(
			within(await jobRow()).getByRole("button", { name: "停止" }),
		);
		fireEvent.click(
			within(await jobRow()).getByRole("button", { name: "停止任务" }),
		);
		await vi.waitFor(() =>
			expect(sentFrames(cmd)).toEqual([
				{ type: "oryn_cancel_job", jobId: "bash-1" },
			]),
		);
		await vi.waitFor(() =>
			expect(
				within(document.body).queryByRole("button", { name: "停止任务" }),
			).toBeNull(),
		);
		expect(document.querySelector('[data-job="bash-1"]')).not.toBeNull();
	});

	it("目标：「保留」与「放弃目标」卸载确认条后对话框仍在", async () => {
		openThreadDialog(T, { kind: "goal" });
		fireEvent.click(
			within(await goalCard()).getByRole("button", { name: "放弃" }),
		);
		fireEvent.click(
			within(await goalCard()).getByRole("button", { name: "保留" }),
		);
		fireEvent.click(
			within(await goalCard()).getByRole("button", { name: "放弃" }),
		);
		fireEvent.click(
			within(await goalCard()).getByRole("button", { name: "放弃目标" }),
		);
		await vi.waitFor(() =>
			expect(sentFrames(cmd)).toEqual([{ type: "set_goal", action: "drop" }]),
		);
		await vi.waitFor(() =>
			expect(screen.queryByRole("button", { name: "放弃目标" })).toBeNull(),
		);
		expect(await goalCard()).toBeTruthy();
	});

	it("重置额度：「取消」与「确认使用」卸载确认条后对话框仍在", async () => {
		openThreadDialog(T, { kind: "report", report: "get_usage" });
		const row = await creditRow();
		fireEvent.click(within(row).getByRole("button", { name: "使用一次" }));
		fireEvent.click(within(row).getByRole("button", { name: "取消" }));
		fireEvent.click(await screen.findByRole("button", { name: "使用一次" }));
		fireEvent.click(screen.getByRole("button", { name: "确认使用" }));
		expect(
			await screen.findByText(/已为「owner@example.com」重置限额/),
		).toBeTruthy();
		expect(screen.queryByRole("button", { name: "确认使用" })).toBeNull();
	});
});
