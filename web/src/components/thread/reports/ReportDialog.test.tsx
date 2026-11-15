/** 报告对话框通用：每个报告加载时有文案，读取失败说清哪一步、为什么，可重试；读到后有「刷新」。 */
import { fireEvent, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReportKind } from "../../../lib/slash";
import { resetThreadStores } from "../../../state/stores";
import { uiStore } from "../../../state/ui";
import { type Frame, openThreadDialog, readCount } from "./test-helpers";

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

const T = "T-reports";

const OK: Record<ReportKind, unknown> = {
	get_context_report: { model: "claude-opus-5-5" },
	get_usage: {
		reports: [],
		session: {
			input: 0,
			output: 0,
			cacheRead: 0,
			cacheWrite: 0,
			totalTokens: 0,
			orchestrationTokens: 0,
			premiumRequests: 0,
			cost: 0,
		},
	},
	get_jobs: { jobs: [] },
	get_active_tools: { tools: [] },
	get_session_stats: {
		sessionId: "s1",
		userMessages: 0,
		assistantMessages: 0,
		toolCalls: 0,
		toolResults: 0,
		totalMessages: 0,
		tokens: {
			input: 0,
			output: 0,
			reasoning: 0,
			cacheRead: 0,
			cacheWrite: 0,
			total: 0,
		},
		premiumRequests: 0,
		cost: 0,
	},
};

const CASES: [ReportKind, string, string][] = [
	["get_context_report", "正在读取上下文报告…", "读取上下文报告失败"],
	["get_usage", "正在向供应商查询用量…", "读取用量失败"],
	["get_jobs", "正在读取后台任务…", "读取后台任务失败"],
	["get_active_tools", "正在读取工具列表…", "读取工具列表失败"],
	["get_session_stats", "正在读取会话统计…", "读取会话统计失败"],
];

beforeEach(() => {
	cmd.mockReset();
	resetThreadStores();
});

afterEach(() => uiStore.getState().close());

describe("ReportDialog", () => {
	it.each(CASES)("%s：加载中显示文案", async (report, loading) => {
		cmd.mockImplementation(() => new Promise(() => undefined));
		openThreadDialog(T, { kind: "report", report });
		expect(await screen.findByText(loading)).toBeTruthy();
	});

	it.each(CASES)(
		"%s：失败显示哪一步、原因与下一步，点「重试」重新读取",
		async (report, _loading, failed) => {
			let fail = true;
			cmd.mockImplementation(async (_t, f) => {
				if (f.type !== report) return {};
				if (fail) throw new Error("agent is not running");
				return OK[report];
			});
			openThreadDialog(T, { kind: "report", report });
			const alert = await screen.findByRole("alert");
			expect(alert.textContent).toContain(failed);
			expect(alert.textContent).toContain("agent is not running");
			expect(alert.textContent).toContain("确认线程在运行后点「重试」");
			fail = false;
			fireEvent.click(screen.getByRole("button", { name: "重试" }));
			await vi.waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
			expect(readCount(cmd, report)).toBe(2);
			// 读到之后底部有「刷新」
			const refresh = await screen.findByRole("button", { name: "刷新" });
			fireEvent.click(refresh);
			await vi.waitFor(() => expect(readCount(cmd, report)).toBe(3));
		},
	);
});
