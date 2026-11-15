/** 会话统计报告：分组键值，数字按单位格式化（计数分组、成本 `$`），可选分组没有数据时不出现。 */
import { screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetThreadStores } from "../../../state/stores";
import { uiStore } from "../../../state/ui";
import { type Frame, openThreadDialog } from "./test-helpers";

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

const T = "T-session";

const BASE = {
	sessionId: "019a1b2c-3d4e",
	sessionFile: "/home/user/.omp/sessions/019a1b2c.jsonl",
	userMessages: 1,
	assistantMessages: 5,
	toolCalls: 3,
	toolResults: 3,
	totalMessages: 9,
	tokens: {
		input: 98_000,
		output: 4_200,
		reasoning: 800,
		cacheRead: 310_000,
		cacheWrite: 0,
		total: 413_000,
	},
	premiumRequests: 0,
	cost: 0.2381,
};

const FULL = {
	...BASE,
	contextUsage: { tokens: 60_000, contextWindow: 200_000, percent: 30 },
	history: {
		input: 400_000,
		output: 20_000,
		cacheRead: 0,
		cacheWrite: 0,
		totalTokens: 1_250_000,
		orchestrationInput: 0,
		orchestrationOutput: 0,
		orchestrationCacheRead: 0,
		premiumRequests: 0,
		cost: 1.5,
		subagentCost: 0.4,
		sampledAt: Date.now() - 60_000,
	},
	routedModels: { "claude-opus-5-5": 4, "gpt-6.1-sol": 1 },
	editRejections: { stale_anchor: 2, conflict: 0 },
};

let stats: unknown = FULL;

beforeEach(() => {
	cmd.mockReset();
	resetThreadStores();
	stats = FULL;
	cmd.mockImplementation(async (_t, f) =>
		f.type === "get_session_stats" ? stats : {},
	);
});

afterEach(() => uiStore.getState().close());

const open = () =>
	openThreadDialog(T, { kind: "report", report: "get_session_stats" });

describe("SessionReport", () => {
	it("分组显示：会话、Token、成本、整段历史、路由模型、被拒绝的编辑；数字格式化，不出现字段名", async () => {
		open();
		expect(await screen.findByText("019a1b2c-3d4e")).toBeTruthy();
		for (const title of [
			"会话",
			"Token",
			"成本",
			"整段历史",
			"实际路由的模型",
			"被拒绝的编辑",
		])
			expect(screen.getByRole("heading", { name: title })).toBeTruthy();
		expect(screen.getByText("共 9 条（用户 1 · 助手 5）")).toBeTruthy();
		expect(screen.getByText("413,000")).toBeTruthy();
		expect(screen.getByText("310,000")).toBeTruthy();
		expect(screen.getByText("$0.24")).toBeTruthy();
		expect(screen.getByText("$1.50")).toBeTruthy();
		expect(screen.getByText("其中子 agent $0.40")).toBeTruthy();
		expect(screen.getByText("30%")).toBeTruthy();
		expect(screen.getByText("4 个回合")).toBeTruthy();
		expect(screen.getByText("stale_anchor")).toBeTruthy();
		// 计数为 0 的拒绝类型不列
		expect(screen.queryByText("conflict")).toBeNull();
		expect(document.body.textContent).not.toMatch(
			/cacheRead|totalMessages|premiumRequests|routedModels/,
		);
	});

	it("只有必填字段：可选分组不出现", async () => {
		stats = BASE;
		open();
		expect(await screen.findByText("019a1b2c-3d4e")).toBeTruthy();
		expect(screen.queryByRole("heading", { name: "整段历史" })).toBeNull();
		expect(
			screen.queryByRole("heading", { name: "实际路由的模型" }),
		).toBeNull();
		expect(screen.queryByRole("heading", { name: "被拒绝的编辑" })).toBeNull();
		expect(screen.queryByRole("meter")).toBeNull();
	});
});
