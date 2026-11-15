/**
 * 后台任务报告：按 tree 缩进成一份列表（同一任务只出现一次），状态徽标、开始时间与时长；
 * 运行中自动刷新、时长走动；取消只给 `cancellable` 行，确认后发 `oryn_cancel_job`，cancelled=false 说人话。
 */
import { act, fireEvent, screen, within } from "@testing-library/react";
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

const T = "T-jobs";
const NOW = Date.now();

const BASH = {
	id: "bash-1",
	type: "bash",
	status: "running",
	label: "bun run build",
	intent: "构建前端",
	command: "cd web && bun run build --mode production",
	startTime: NOW - 65_000,
};
const SCOUT = {
	id: "Scout",
	type: "task",
	status: "running",
	label: "Scout",
	intent: "调研路由",
	startTime: NOW - 30_000,
	agentId: "Scout",
};
const NESTED = {
	id: "bash-9",
	type: "bash",
	status: "running",
	label: "rg foo",
	startTime: NOW - 5_000,
	ownerId: "Scout",
};
const DONE = {
	id: "eval-2",
	type: "eval",
	status: "failed",
	label: "eval",
	intent: "跑一段脚本",
	startTime: NOW - 120_000,
	endTime: NOW - 100_000,
	errorPreview: "SyntaxError: bad",
};

// Core 的 `jobs` 只有当前 agent 直属的任务，子 agent 的后代只在 `tree` 里
const JOBS = {
	jobs: [BASH, SCOUT, DONE],
	tree: [
		{ depth: 0, job: BASH, cancellable: true },
		{ depth: 0, job: SCOUT, agentId: "Scout", cancellable: true },
		{ depth: 1, job: NESTED },
		{ depth: 0, job: DONE },
	],
};

let jobs: unknown = JOBS;
let cancelled = true;

beforeEach(() => {
	cmd.mockReset();
	resetThreadStores();
	jobs = JOBS;
	cancelled = true;
	cmd.mockImplementation(async (_t, f) => {
		if (f.type === "get_jobs") return jobs;
		if (f.type === "oryn_cancel_job") return { cancelled };
		return {};
	});
});

afterEach(() => {
	uiStore.getState().close();
	vi.useRealTimers();
});

const open = () => openThreadDialog(T, { kind: "report", report: "get_jobs" });

const jobRow = (id: string) =>
	vi.waitFor(() => {
		const el = document.querySelector<HTMLElement>(`[data-job="${id}"]`);
		if (!el) throw new Error(`任务 ${id} 未渲染`);
		return el;
	});

describe("JobsReport", () => {
	it("树按 depth 缩进成一份列表：每个任务只出现一次，意图在前、状态徽标、开始时间与时长", async () => {
		open();
		const bash = await jobRow("bash-1");
		// jobs 与 tree 同时给出，界面上只出现一次
		expect(document.querySelectorAll('[data-job="bash-1"]')).toHaveLength(1);
		expect(within(bash).getByText("构建前端")).toBeTruthy();
		expect(
			within(bash).getByText("cd web && bun run build --mode production"),
		).toBeTruthy();
		expect(within(bash).getByText("运行中")).toBeTruthy();
		expect(within(bash).getByText(/^已运行 1m \d+s$/)).toBeTruthy();
		expect(within(bash).getByText(/开始$/)).toBeTruthy();
		// 子 agent 的任务缩进一级
		const nested = await jobRow("bash-9");
		expect(nested.closest("li")?.style.paddingLeft).toBe("20px");
		expect(bash.closest("li")?.style.paddingLeft).toBe("0px");
		const failed = await jobRow("eval-2");
		expect(within(failed).getByText("失败")).toBeTruthy();
		expect(within(failed).getByText("运行了 20s")).toBeTruthy();
		expect(within(failed).getByText("SyntaxError: bad")).toBeTruthy();
		expect(screen.getByText("运行中 3 个 · 最近结束 1 个")).toBeTruthy();
		expect(document.body.textContent).not.toMatch(/startTime|endTime|depth/);
	});

	it("停止按钮只给 Core 标了 cancellable 的行", async () => {
		open();
		const bash = await jobRow("bash-1");
		expect(within(bash).queryByRole("button", { name: "停止" })).toBeTruthy();
		expect(
			within(await jobRow("Scout")).queryByRole("button", { name: "停止" }),
		).toBeTruthy();
		expect(
			within(await jobRow("bash-9")).queryByRole("button", { name: "停止" }),
		).toBeNull();
		expect(
			within(await jobRow("eval-2")).queryByRole("button", { name: "停止" }),
		).toBeNull();
	});

	it("停止任务：先确认，确认后发 oryn_cancel_job{jobId}，然后重读列表", async () => {
		open();
		const bash = await jobRow("bash-1");
		fireEvent.click(within(bash).getByRole("button", { name: "停止" }));
		expect(sentFrames(cmd)).toEqual([]);
		// 「保留」收起确认条，不发命令
		fireEvent.click(within(bash).getByRole("button", { name: "保留" }));
		expect(sentFrames(cmd)).toEqual([]);
		fireEvent.click(within(bash).getByRole("button", { name: "停止" }));
		const reads = readCount(cmd, "get_jobs");
		fireEvent.click(within(bash).getByRole("button", { name: "停止任务" }));
		await vi.waitFor(() =>
			expect(sentFrames(cmd)).toEqual([
				{ type: "oryn_cancel_job", jobId: "bash-1" },
			]),
		);
		await vi.waitFor(() =>
			expect(readCount(cmd, "get_jobs")).toBeGreaterThan(reads),
		);
	});

	it("Core 回 cancelled=false：说明没有停止任何任务以及可能原因", async () => {
		cancelled = false;
		open();
		const bash = await jobRow("bash-1");
		fireEvent.click(within(bash).getByRole("button", { name: "停止" }));
		fireEvent.click(within(bash).getByRole("button", { name: "停止任务" }));
		expect(
			await within(bash).findByText(/^没有停止任何任务：它可能刚好已经结束/),
		).toBeTruthy();
		expect(sentFrames(cmd)).toEqual([
			{ type: "oryn_cancel_job", jobId: "bash-1" },
		]);
	});

	it("停止失败：在该行说清原因", async () => {
		cmd.mockImplementation(async (_t, f) => {
			if (f.type === "get_jobs") return JOBS;
			if (f.type === "oryn_cancel_job")
				throw new Error(
					"Async background jobs are unavailable in this session.",
				);
			return {};
		});
		open();
		const bash = await jobRow("bash-1");
		fireEvent.click(within(bash).getByRole("button", { name: "停止" }));
		fireEvent.click(within(bash).getByRole("button", { name: "停止任务" }));
		expect((await within(bash).findByRole("alert")).textContent).toBe(
			"停止任务失败：Async background jobs are unavailable in this session.",
		);
	});

	it("有运行中的任务时每 2 秒自动刷新、时长随时间走动；没有运行中的任务时不轮询", async () => {
		vi.useFakeTimers({ shouldAdvanceTime: true });
		open();
		const bash = await jobRow("bash-1");
		const before = within(bash).getByText(/^已运行 /).textContent;
		const reads = readCount(cmd, "get_jobs");
		await act(() => vi.advanceTimersByTimeAsync(2_100));
		expect(readCount(cmd, "get_jobs")).toBeGreaterThan(reads);
		expect(within(bash).getByText(/^已运行 /).textContent).not.toBe(before);
		expect(screen.getByText("每 2 秒自动刷新")).toBeTruthy();
	});

	it("jobs 里没有运行中的，只有树中后代在跑：继续轮询、时长走动，计数按树算", async () => {
		vi.useFakeTimers({ shouldAdvanceTime: true });
		jobs = {
			jobs: [DONE],
			tree: [
				{ depth: 0, job: DONE },
				{ depth: 0, agentId: "Scout" },
				{ depth: 1, job: NESTED },
			],
		};
		open();
		const nested = await jobRow("bash-9");
		expect(screen.getByText("运行中 1 个 · 最近结束 1 个")).toBeTruthy();
		expect(screen.getByText("每 2 秒自动刷新")).toBeTruthy();
		const before = within(nested).getByText(/^已运行 /).textContent;
		const reads = readCount(cmd, "get_jobs");
		await act(() => vi.advanceTimersByTimeAsync(2_100));
		expect(readCount(cmd, "get_jobs")).toBeGreaterThan(reads);
		expect(within(nested).getByText(/^已运行 /).textContent).not.toBe(before);
	});

	it("停止唯一运行中的任务：cancellationPending 期间继续轮询，直到它真正退出", async () => {
		vi.useFakeTimers({ shouldAdvanceTime: true });
		const only = { ...BASH };
		const pending = { ...BASH, status: "cancelled", cancellationPending: true };
		const ended = { ...BASH, status: "cancelled", endedAt: NOW };
		let current: object = only;
		cmd.mockImplementation(async (_t, f) => {
			if (f.type === "get_jobs")
				return {
					jobs: [current],
					tree: [{ depth: 0, job: current, cancellable: current === only }],
				};
			if (f.type === "oryn_cancel_job") {
				current = pending;
				return { cancelled: true };
			}
			return {};
		});
		open();
		const row = await jobRow("bash-1");
		fireEvent.click(within(row).getByRole("button", { name: "停止" }));
		fireEvent.click(within(row).getByRole("button", { name: "停止任务" }));
		await vi.waitFor(() =>
			expect(within(row).getByText("正在停止")).toBeTruthy(),
		);
		expect(screen.getByText("每 2 秒自动刷新")).toBeTruthy();
		// 任务真正退出：下一次轮询读到 endedAt，「正在停止」消失、轮询停下
		current = ended;
		const reads = readCount(cmd, "get_jobs");
		await act(() => vi.advanceTimersByTimeAsync(2_100));
		expect(readCount(cmd, "get_jobs")).toBeGreaterThan(reads);
		await vi.waitFor(() =>
			expect(within(row).queryByText("正在停止")).toBeNull(),
		);
		expect(screen.queryByText("每 2 秒自动刷新")).toBeNull();
	});

	it("全部结束：不轮询", async () => {
		vi.useFakeTimers({ shouldAdvanceTime: true });
		jobs = { jobs: [DONE], tree: [{ depth: 0, job: DONE }] };
		open();
		await jobRow("eval-2");
		const reads = readCount(cmd, "get_jobs");
		await act(() => vi.advanceTimersByTimeAsync(5_000));
		expect(readCount(cmd, "get_jobs")).toBe(reads);
		expect(screen.queryByText("每 2 秒自动刷新")).toBeNull();
	});

	it("没有后台任务：保留 Core 的说明", async () => {
		jobs = { jobs: [], tree: [] };
		open();
		expect(await screen.findByText("没有后台任务")).toBeTruthy();
		expect(screen.getByText(/最多约 5 分钟/)).toBeTruthy();
	});

	it("旧 Core 没有 tree：平铺 jobs", async () => {
		jobs = { jobs: [DONE] };
		open();
		expect(within(await jobRow("eval-2")).getByText("跑一段脚本")).toBeTruthy();
	});
});
