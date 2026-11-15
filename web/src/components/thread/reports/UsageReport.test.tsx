/**
 * 用量报告：每个供应商一张卡（量表 + 百分比 + 重置倒计时），本会话统计成本是 `$0.24`，字段名不再原样显示；
 * 重置额度：查看 → 使用一次 → 确认后才发 `redeem_reset_credit`，结果按结果码说人话并重读用量。
 */
import { fireEvent, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetThreadStores } from "../../../state/stores";
import { uiStore } from "../../../state/ui";
import {
	type Frame,
	openThreadDialog,
	readCount,
	sentFrames,
} from "./test-helpers";

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

const T = "T-usage";
const NOW = Date.now();

const SESSION = {
	input: 98_000,
	output: 4_200,
	cacheRead: 310_000,
	cacheWrite: 0,
	totalTokens: 412_200,
	orchestrationTokens: 0,
	premiumRequests: 0,
	cost: 0.2381,
};

const USAGE = {
	reports: [
		{
			provider: "anthropic",
			fetchedAt: NOW - 120_000,
			account: "owner@example.com",
			resetCreditsAvailable: 2,
			limits: [
				{
					id: "anthropic:5h",
					label: "5 小时窗口",
					usedFraction: 0.43,
					resetsAt: NOW + 3 * 3600_000 + 60_000,
				},
				{
					id: "anthropic:7d",
					label: "每周",
					usedFraction: 0.91,
					used: 910,
					limit: 1000,
					unit: "requests",
				},
			],
		},
	],
	session: SESSION,
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

let usage: unknown = USAGE;

beforeEach(() => {
	cmd.mockReset();
	resetThreadStores();
	usage = USAGE;
	cmd.mockImplementation(async (_t, f) => {
		if (f.type === "get_usage") return usage;
		if (f.type === "list_reset_credits") return { accounts: [ACCOUNT] };
		if (f.type === "redeem_reset_credit")
			return { ok: true, code: "reset", message: "Reset applied" };
		return {};
	});
});

afterEach(() => uiStore.getState().close());

const open = () => openThreadDialog(T, { kind: "report", report: "get_usage" });

describe("UsageReport", () => {
	it("每个供应商一张卡：量表百分比、已用 / 上限带单位、重置倒计时；成本 $0.24；不显示原始字段名", async () => {
		open();
		const card = await screen.findByText("anthropic");
		expect(card).toBeTruthy();
		const meters = screen.getAllByRole("meter");
		expect(meters.map((m) => m.getAttribute("aria-valuenow"))).toEqual([
			"43",
			"91",
		]);
		expect(screen.getByText("43%")).toBeTruthy();
		// 91% 超过 85：错误色
		expect(screen.getByText("91%").className).toContain("text-err");
		expect(screen.getByText("910 / 1,000 次请求")).toBeTruthy();
		expect(screen.getByText(/小时后重置$/)).toBeTruthy();
		expect(screen.getByText("2 个重置额度")).toBeTruthy();
		expect(screen.getByText("$0.24")).toBeTruthy();
		expect(screen.getByText("412,200")).toBeTruthy();
		expect(document.body.textContent).not.toMatch(
			/usedFraction|resetsAt|fetchedAt|cacheRead|totalTokens/,
		);
	});

	it("没有供应商用量接口：说明只有本地统计，本会话统计照常显示", async () => {
		usage = { reports: [], session: SESSION };
		open();
		expect(await screen.findByText(/当前供应商没有用量接口/)).toBeTruthy();
		expect(screen.getByText("$0.24")).toBeTruthy();
	});

	it("兑换重置额度：先确认才发 redeem_reset_credit，结果说人话，并重读用量", async () => {
		open();
		fireEvent.click(
			await screen.findByRole("button", { name: /查看重置额度/ }),
		);
		const row = await vi.waitFor(() => {
			const el = document.querySelector<HTMLElement>(
				'[data-credit="anthropic/7"]',
			);
			if (!el) throw new Error("重置额度未就绪");
			return el;
		});
		expect(within(row).getByText("已存 2 个 · 现在可用 1 个")).toBeTruthy();
		fireEvent.click(within(row).getByRole("button", { name: "使用一次" }));
		// 只出现确认条，还没发命令
		expect(sentFrames(cmd)).toEqual([]);
		const usageReads = readCount(cmd, "get_usage");
		fireEvent.click(within(row).getByRole("button", { name: "确认使用" }));
		await screen.findByText(/已为「owner@example.com」重置限额/);
		expect(sentFrames(cmd)).toEqual([
			{
				type: "redeem_reset_credit",
				target: ACCOUNT.target,
				label: ACCOUNT.label,
			},
		]);
		await vi.waitFor(() =>
			expect(readCount(cmd, "get_usage")).toBeGreaterThan(usageReads),
		);
	});

	it("确认条里点取消：不发命令，回到「使用一次」", async () => {
		open();
		fireEvent.click(
			await screen.findByRole("button", { name: /查看重置额度/ }),
		);
		const use = await screen.findByRole("button", { name: "使用一次" });
		fireEvent.click(use);
		fireEvent.click(screen.getByRole("button", { name: "取消" }));
		expect(screen.getByRole("button", { name: "使用一次" })).toBeTruthy();
		expect(sentFrames(cmd)).toEqual([]);
	});

	it("现在没有可用的重置：按钮禁用并说明原因", async () => {
		cmd.mockImplementation(async (_t, f) => {
			if (f.type === "get_usage") return USAGE;
			if (f.type === "list_reset_credits")
				return {
					accounts: [
						{
							...ACCOUNT,
							redeemableCount: 0,
							unavailableReason: "限额没有用满",
						},
					],
				};
			return {};
		});
		open();
		fireEvent.click(
			await screen.findByRole("button", { name: /查看重置额度/ }),
		);
		const use = await screen.findByRole("button", { name: "使用一次" });
		expect((use as HTMLButtonElement).disabled).toBe(true);
		expect(screen.getByText("限额没有用满")).toBeTruthy();
	});

	it("兑换失败：在该账号行里说清失败原因", async () => {
		cmd.mockImplementation(async (_t, f) => {
			if (f.type === "get_usage") return USAGE;
			if (f.type === "list_reset_credits") return { accounts: [ACCOUNT] };
			if (f.type === "redeem_reset_credit") throw new Error("network down");
			return {};
		});
		open();
		fireEvent.click(
			await screen.findByRole("button", { name: /查看重置额度/ }),
		);
		fireEvent.click(await screen.findByRole("button", { name: "使用一次" }));
		fireEvent.click(screen.getByRole("button", { name: "确认使用" }));
		expect((await screen.findByRole("alert")).textContent).toBe(
			"使用重置额度失败：network down",
		);
	});
});
