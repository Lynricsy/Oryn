/**
 * 目标对话框：分区（当前目标 / 预算 / 设定或替换）；只调预算时 `set_goal` 只带 `tokenBudget`；放弃前确认；
 * 有进行中的目标时提示会替换；暂停的目标也显示（可继续）且挡住新目标；失败提示显示在出错的那个分区里。
 * 目标在对话框外变化（goal_updated、回合结束、重新打开）时重读；Core 因状态过时拒绝时给中文说明并自动刷新（M12.1 问题 6）。
 */
import { act, fireEvent, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { SessionState } from "../../omp/types";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import type { OmpFrame } from "../../state/thread-reducer";
import { uiStore } from "../../state/ui";
import {
	type Frame,
	openThreadDialog,
	readCount,
	sentFrames,
} from "./reports/test-helpers";

const { cmd } = vi.hoisted(() => ({
	cmd: vi.fn<(threadId: string, frame: Frame) => Promise<unknown>>(),
}));
vi.mock("../../realtime/socket", () => ({
	realtime: {
		cmd: (t: string, f: Frame) => cmd(t, f),
		subscribe: () => () => undefined,
		focus: () => undefined,
	},
	OmpCommandError: class extends Error {},
}));

const T = "T-goal";

const ACTIVE = {
	enabled: true,
	status: "active",
	objective: "把报告对话框打磨到可发布",
	tokenBudget: 200_000,
	tokensUsed: 150_000,
	timeUsedSeconds: 200,
	mode: "active",
};

let goal: unknown = ACTIVE;
let failOn: string | null = null;

beforeEach(() => {
	cmd.mockReset();
	resetThreadStores();
	goal = ACTIVE;
	failOn = null;
	cmd.mockImplementation(async (_t, f) => {
		if (f.type === "get_goal") return goal;
		if (f.type === "get_state") return {};
		if (failOn && f.action === failOn)
			throw new Error("No active goal to pause.");
		return {};
	});
});

afterEach(() => uiStore.getState().close());

const open = () => openThreadDialog(T, { kind: "goal" });

/** 某个分区（按小标题找到它的 <section>）。 */
const section = async (title: string) => {
	const h = await screen.findByRole("heading", { name: title });
	const s = h.closest("section");
	if (!s) throw new Error(`分区 ${title} 缺失`);
	return s;
};

describe("GoalDialog", () => {
	it("进行中的目标：状态、用量量表、用时；三个分区；设定会替换并提示", async () => {
		open();
		const current = await section("当前目标");
		expect(within(current).getByText(ACTIVE.objective)).toBeTruthy();
		expect(within(current).getByText("进行中")).toBeTruthy();
		expect(
			within(current).getByText("已用 150,000 / 200,000 token"),
		).toBeTruthy();
		expect(
			within(current).getByRole("meter").getAttribute("aria-valuenow"),
		).toBe("150000");
		expect(within(current).getByText("用时 3m 20s")).toBeTruthy();
		expect(await section("预算")).toBeTruthy();
		const replace = await section("替换目标");
		const warning = "已有进行中的目标。设定新目标会替换它。";
		// 没开始输入时不提醒；输入新目标后在按钮旁提醒会替换
		expect(within(replace).queryByText(warning)).toBeNull();
		fireEvent.change(within(replace).getByRole("textbox", { name: "目标" }), {
			target: { value: "改做 Goal 截图" },
		});
		expect(within(replace).getByText(warning)).toBeTruthy();
		// 已有目标时不给引导式设定（Core 会拒绝）
		expect(
			within(replace).queryByRole("button", { name: "引导式设定" }),
		).toBeNull();
		fireEvent.click(within(replace).getByRole("button", { name: "替换目标" }));
		await vi.waitFor(() =>
			expect(sentFrames(cmd)).toEqual([
				{ type: "set_goal", objective: "改做 Goal 截图" },
			]),
		);
	});

	it("只调预算：set_goal 只带 tokenBudget，不带 objective", async () => {
		open();
		const budget = await section("预算");
		fireEvent.change(
			within(budget).getByRole("textbox", { name: "新的 token 预算" }),
			{
				target: { value: "50k000" },
			},
		);
		expect(
			within(budget).getByText("预算将改为 50,000 token，目标不变。"),
		).toBeTruthy();
		fireEvent.click(within(budget).getByRole("button", { name: "更新预算" }));
		await vi.waitFor(() =>
			expect(sentFrames(cmd)).toEqual([
				{ type: "set_goal", tokenBudget: 50_000 },
			]),
		);
	});

	it("不限预算：set_goal 的 tokenBudget 为 null", async () => {
		open();
		fireEvent.click(
			within(await section("预算")).getByRole("button", { name: "不限预算" }),
		);
		await vi.waitFor(() =>
			expect(sentFrames(cmd)).toEqual([
				{ type: "set_goal", tokenBudget: null },
			]),
		);
	});

	it("放弃前确认：点「放弃」只出确认条；「保留」收回；确认后才发 drop", async () => {
		open();
		const current = await section("当前目标");
		fireEvent.click(within(current).getByRole("button", { name: "放弃" }));
		expect(sentFrames(cmd)).toEqual([]);
		expect(
			within(current).getByText(
				"放弃后 agent 不再围绕这个目标工作，无法撤销。",
			),
		).toBeTruthy();
		fireEvent.click(within(current).getByRole("button", { name: "保留" }));
		expect(sentFrames(cmd)).toEqual([]);
		fireEvent.click(within(current).getByRole("button", { name: "放弃" }));
		fireEvent.click(within(current).getByRole("button", { name: "放弃目标" }));
		await vi.waitFor(() =>
			expect(sentFrames(cmd)).toEqual([{ type: "set_goal", action: "drop" }]),
		);
	});

	it("暂停的目标仍显示并能继续；设定新目标被挡住并说明原因", async () => {
		goal = { ...ACTIVE, enabled: false, status: "paused" };
		open();
		const current = await section("当前目标");
		expect(within(current).getByText("已暂停")).toBeTruthy();
		fireEvent.click(within(current).getByRole("button", { name: "继续" }));
		await vi.waitFor(() =>
			expect(sentFrames(cmd)).toEqual([{ type: "set_goal", action: "resume" }]),
		);
		expect(screen.queryByRole("heading", { name: "预算" })).toBeNull();
		const set = await section("设定目标");
		expect(
			within(set).getByText(/当前目标已暂停：先继续或放弃它/),
		).toBeTruthy();
		// 不摆一套禁用的表单
		expect(within(set).queryByRole("button", { name: "设定目标" })).toBeNull();
		expect(within(set).queryByRole("textbox")).toBeNull();
	});

	it("没有目标：说明为空，设定目标带上预算，可以引导式设定", async () => {
		goal = { enabled: false };
		open();
		expect(await screen.findByText("当前没有目标。")).toBeTruthy();
		const set = await section("设定目标");
		fireEvent.change(within(set).getByRole("textbox", { name: "目标" }), {
			target: { value: "修好登录" },
		});
		fireEvent.change(within(set).getByRole("textbox", { name: /token 预算/ }), {
			target: { value: "80000" },
		});
		expect(
			within(set).getByRole("button", { name: "引导式设定" }),
		).toBeTruthy();
		fireEvent.click(within(set).getByRole("button", { name: "设定目标" }));
		await vi.waitFor(() =>
			expect(sentFrames(cmd)).toEqual([
				{ type: "set_goal", objective: "修好登录", tokenBudget: 80_000 },
			]),
		);
	});

	it("失败提示显示在出错的分区里，说清哪一步与原因", async () => {
		failOn = "pause";
		open();
		const current = await section("当前目标");
		fireEvent.click(within(current).getByRole("button", { name: "暂停" }));
		const alert = await within(current).findByRole("alert");
		expect(alert.textContent).toContain("暂停目标失败");
		expect(alert.textContent).toContain("No active goal to pause.");
		expect(within(await section("替换目标")).queryByRole("alert")).toBeNull();
	});

	it("Core 因目标已在别处完成而拒绝暂停：中文说明 + 保留原文，并自动重读为已完成", async () => {
		failOn = "pause";
		open();
		const current = await section("当前目标");
		const reads = readCount(cmd, "get_goal");
		// agent 在后台把目标做完了，对话框还显示「进行中」
		goal = { ...ACTIVE, enabled: false, status: "complete", mode: "exiting" };
		fireEvent.click(within(current).getByRole("button", { name: "暂停" }));
		const alert = await within(current).findByRole("alert");
		expect(alert.textContent).toContain(
			"没有进行中的目标可以暂停，它可能已完成或已在别处暂停。",
		);
		expect(alert.textContent).toContain("已重新读取目标的当前状态。");
		expect(alert.textContent).toContain("No active goal to pause.");
		await vi.waitFor(() =>
			expect(readCount(cmd, "get_goal")).toBeGreaterThan(reads),
		);
		expect(await within(current).findByText("已完成")).toBeTruthy();
		expect(within(current).queryByRole("button", { name: "暂停" })).toBeNull();
	});

	describe("目标在对话框外变化时重读", () => {
		it("goal_updated 帧改变了目标状态：重读并显示新状态", async () => {
			const store = getThreadStore(T).getState();
			act(() => store.apply([{ type: "state", state: {} as SessionState }]));
			open();
			const current = await section("当前目标");
			expect(within(current).getByText("进行中")).toBeTruthy();
			const reads = readCount(cmd, "get_goal");
			goal = { ...ACTIVE, enabled: false, status: "complete", mode: "exiting" };
			// Core 的 goal_updated 经控制面以 frame 消息到达
			const frame = {
				type: "goal_updated",
				goal: null,
				state: {
					enabled: false,
					mode: "exiting",
					reason: "completed",
					goal: {
						id: "g1",
						objective: ACTIVE.objective,
						status: "complete",
						tokensUsed: ACTIVE.tokensUsed,
						timeUsedSeconds: 200,
						createdAt: 1,
						updatedAt: 2,
					},
				},
			} as OmpFrame;
			act(() => store.apply([{ type: "frame", frame }]));
			expect(await within(current).findByText("已完成")).toBeTruthy();
			expect(readCount(cmd, "get_goal")).toBe(reads + 1);
		});

		it("agent 回合结束：重读一次；回合进行中不重读", async () => {
			open();
			const current = await section("当前目标");
			const reads = readCount(cmd, "get_goal");
			act(() => getThreadStore(T).getState().patchLocal({ running: true }));
			expect(readCount(cmd, "get_goal")).toBe(reads);
			goal = { ...ACTIVE, enabled: false, status: "paused" };
			act(() => getThreadStore(T).getState().patchLocal({ running: false }));
			expect(await within(current).findByText("已暂停")).toBeTruthy();
			expect(readCount(cmd, "get_goal")).toBe(reads + 1);
		});

		it("关掉再打开：重读，不沿用上次的缓存", async () => {
			open();
			await section("当前目标");
			act(() => uiStore.getState().close());
			goal = { ...ACTIVE, enabled: false, status: "paused" };
			act(() => uiStore.getState().open({ kind: "goal" }));
			const current = await section("当前目标");
			expect(await within(current).findByText("已暂停")).toBeTruthy();
		});
	});

	it("读取失败可重试", async () => {
		let fail = true;
		cmd.mockImplementation(async (_t, f) => {
			if (f.type === "get_goal") {
				if (fail) throw new Error("agent is not running");
				return ACTIVE;
			}
			return {};
		});
		open();
		expect((await screen.findByRole("alert")).textContent).toContain(
			"读取目标失败",
		);
		fail = false;
		fireEvent.click(screen.getByRole("button", { name: "重试" }));
		expect(await screen.findByText(ACTIVE.objective)).toBeTruthy();
	});
});
