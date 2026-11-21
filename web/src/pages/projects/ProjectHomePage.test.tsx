/**
 * 项目主页（M24）：页头动作（复制 clone 命令、新建线程）、状态条各胶囊的口径与去向、最近线程、服务卡、
 * 归档项目与不存在的项目。fetch 桩与路由见 projects-test-kit。
 */
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../../api/client";
import i18n from "../../i18n";
import { indexStore } from "../../state/stores";
import { uiStore } from "../../state/ui";
import {
	CORE,
	kit,
	LEGACY,
	mount,
	ORYN,
	orb,
	resetKit,
	SETUP,
	usageStat,
	where,
} from "./projects-test-kit";

function th(id: string, p: Partial<Thread> = {}): Thread {
	return {
		id,
		title: id,
		titleSource: "auto",
		state: "idle",
		stateChangedAt: 1,
		pinned: false,
		labels: [],
		unread: false,
		createdAt: 1,
		updatedAt: 1,
		projectId: ORYN.id,
		...p,
	};
}

const pull = (
	number: number,
	over: Partial<NonNullable<Thread["pulls"]>[number]> = {},
) => ({
	repo: "x/oryn",
	number,
	title: `PR ${number}`,
	state: "open" as const,
	draft: false,
	url: `https://github.com/x/oryn/pull/${number}`,
	listening: true,
	...over,
});

const chip = (name: string) =>
	screen
		.getByTestId("project-status")
		.querySelector(`[data-chip="${name}"]`) as HTMLElement | null;

beforeEach(resetKit);

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
	indexStore.getState().setAll([], 0);
	uiStore.setState({ homeSeed: null });
	Reflect.deleteProperty(navigator, "clipboard");
});

describe("项目主页：页头与开头", () => {
	it("仓库行与描述；Clone 复制 git clone 命令", async () => {
		const writeText = vi.fn(async () => {});
		Object.defineProperty(navigator, "clipboard", {
			configurable: true,
			value: { writeText },
		});
		kit.projects = [
			{ ...ORYN, description: "一行说明", baseBranch: "main" },
			CORE,
		];
		mount(`/projects/${ORYN.id}`);
		const repo = await screen.findByTestId("project-repo");
		expect(repo).toHaveTextContent("github.com/x/oryn");
		expect(repo).toHaveTextContent("main");
		expect(screen.getByText("一行说明")).toBeInTheDocument();
		fireEvent.click(
			screen.getByRole("button", { name: "复制 git clone 命令" }),
		);
		await waitFor(() =>
			expect(writeText).toHaveBeenCalledWith(
				"git clone https://github.com/x/oryn",
			),
		);
	});

	it("没有仓库：没有 Clone 按钮，仓库行写「无仓库」", async () => {
		mount(`/projects/${CORE.id}`);
		expect(await screen.findByTestId("project-repo")).toHaveTextContent(
			i18n.t("projects.noRepo"),
		);
		expect(
			screen.queryByRole("button", { name: "复制 git clone 命令" }),
		).toBeNull();
		expect(
			screen.getByRole("button", { name: "复制项目链接" }),
		).toBeInTheDocument();
	});

	it.each([
		["notes", "oryn clone notes"],
		["my notes", "oryn clone 'my notes'"],
	])(
		"托管项目 %s：仓库行写「Oryn 托管」与基线，Clone 复制 %s",
		async (name, cmd) => {
			const writeText = vi.fn(async () => {});
			Object.defineProperty(navigator, "clipboard", {
				configurable: true,
				value: { writeText },
			});
			kit.projects = [
				{
					...CORE,
					name,
					hosted: true,
					cloneUrl: `https://oryn.test/git/${CORE.id}.git`,
					baseBranch: "main",
				},
			];
			mount(`/projects/${CORE.id}`);
			const repo = await screen.findByTestId("project-repo");
			expect(repo).toHaveTextContent("Oryn 托管");
			expect(repo).toHaveTextContent("main");
			expect(
				screen.queryByRole("button", { name: "复制 git clone 命令" }),
			).toBeNull();
			fireEvent.click(
				screen.getByRole("button", { name: "复制 oryn clone 命令" }),
			);
			await waitFor(() => expect(writeText).toHaveBeenCalledWith(cmd));
		},
	);

	it("「新建线程」把项目交给首屏并跳到 /", async () => {
		mount(`/projects/${ORYN.id}`);
		await screen.findByTestId("project-home");
		// 最近线程卡空着时也有一个「新建线程」，页头的在前
		fireEvent.click(
			screen.getAllByRole("button", { name: "新建线程" })[0] as HTMLElement,
		);
		expect(uiStore.getState().homeSeed).toEqual({
			text: "",
			projectId: ORYN.id,
		});
		expect(where()).toHaveTextContent(/^\/$/);
	});

	it("齿轮进项目设置（宽屏落到概览）", async () => {
		mount(`/projects/${ORYN.id}`);
		await screen.findByTestId("project-home");
		fireEvent.click(screen.getByRole("button", { name: "项目设置" }));
		await waitFor(() =>
			expect(where()).toHaveTextContent(
				`/projects/${ORYN.id}/settings/overview`,
			),
		);
	});

	it("已归档的项目：有归档提示，没有「新建线程」", async () => {
		kit.archived = [LEGACY];
		mount(`/projects/${LEGACY.id}`);
		expect(await screen.findByTestId("project-archived")).toBeInTheDocument();
		expect(screen.getByTestId("project-home")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "新建线程" })).toBeNull();
	});

	it("不存在的 id：提示并给回项目列表的链接", async () => {
		mount("/projects/prj_gone");
		const box = await screen.findByTestId("project-not-found");
		expect(box).toHaveTextContent(i18n.t("projectsPage.notFound"));
		expect(within(box).getByRole("link")).toHaveAttribute("href", "/projects");
	});
});

describe("项目主页：状态条", () => {
	it("等你回复、工作中（含 provisioning）、打开的 PR 与失败的 CI；别的项目、归档与协调线程不算", async () => {
		indexStore.getState().setAll(
			[
				th("A", { state: "awaiting_input" }),
				th("W", { state: "working" }),
				th("P", {
					state: "provisioning",
					pulls: [pull(7, { checks: "failure" })],
				}),
				// 同一个 PR 出现在两个线程里只算一次
				th("Q", {
					pulls: [pull(7, { checks: "failure" }), pull(8, { state: "merged" })],
				}),
				th("X", { state: "awaiting_input", archivedAt: 3 }),
				th("S", { state: "working", kind: "space" }),
				th("O", { state: "awaiting_input", projectId: CORE.id }),
			],
			1,
		);
		mount(`/projects/${ORYN.id}`);
		await screen.findByTestId("project-status");
		expect(chip("awaiting")).toHaveTextContent("1 个等你回复");
		expect(chip("working")).toHaveTextContent("2 个工作中");
		expect(chip("prs")).toHaveTextContent("1 个打开的 PR · 1 个 CI 失败");
		for (const k of ["awaiting", "working", "prs"])
			expect(chip(k)).toHaveAttribute(
				"href",
				`/projects/${ORYN.id}/threads?status=active`,
			);
		expect(chip("setup")).toBeNull();
		expect(chip("usage")).toBeNull();
	});

	it("近 7 天用量：成本与回合，链到按项目筛选的统计页", async () => {
		kit.usage = {
			...kit.usage,
			groupBy: "total",
			total: { ...usageStat("total", 12), costUsd: 1.5 },
		};
		mount(`/projects/${ORYN.id}`);
		await waitFor(() => expect(chip("usage")).not.toBeNull());
		expect(chip("usage")).toHaveTextContent("近 7 天 $1.50 · 12 个回合");
		expect(chip("usage")).toHaveAttribute(
			"href",
			`/stats/usage?range=7d&project=${ORYN.id}`,
		);
		const req = kit.reads.find((r) => r.startsWith("/api/v1/stats/usage"));
		expect(req).toContain("groupBy=total");
		expect(req).toContain(`project=${ORYN.id}`);
	});

	it("最近一次 setup 失败：胶囊链到所在线程", async () => {
		kit.orbs = [
			orb({
				id: "orb-1",
				threadIds: ["T-7"],
				lastSetup: { ...SETUP, ok: false },
			}),
		];
		mount(`/projects/${ORYN.id}`);
		await waitFor(() =>
			expect(screen.queryByTestId("project-status")).not.toBeNull(),
		);
		expect(chip("setup")).toHaveTextContent("上次环境配置失败");
		expect(chip("setup")).toHaveAttribute("href", "/t/T-7");
	});

	it("全部为 0：整条不渲染", async () => {
		kit.orbs = [orb({ lastSetup: SETUP })];
		mount(`/projects/${ORYN.id}`);
		await screen.findByTestId("project-home");
		await waitFor(() =>
			expect(kit.reads.some((r) => r.startsWith("/api/v1/stats/usage"))).toBe(
				true,
			),
		);
		expect(screen.queryByTestId("project-status")).toBeNull();
	});
});

describe("项目主页：最近线程与服务", () => {
	it("最近线程最多 5 行，含归档线程；「查看全部」去线程子页", async () => {
		indexStore.getState().setAll(
			Array.from({ length: 6 }, (_, i) =>
				th(`L${i}`, { lastMessageAt: 10 + i }),
			),
			1,
		);
		kit.threads = [th("Z", { archivedAt: 50, lastMessageAt: 100 })];
		mount(`/projects/${ORYN.id}`);
		const card = await screen.findByTestId("project-recent");
		await waitFor(() =>
			expect(
				within(card)
					.getAllByTestId("project-thread-row")
					.map((r) => r.dataset.thread),
			).toEqual(["Z", "L5", "L4", "L3", "L2"]),
		);
		expect(
			within(card).getByRole("link", { name: "查看全部" }),
		).toHaveAttribute("href", `/projects/${ORYN.id}/threads`);
	});

	it("没有线程：说明与新建线程；没有服务时没有服务卡", async () => {
		mount(`/projects/${ORYN.id}`);
		const card = await screen.findByTestId("project-recent");
		expect(card).toHaveTextContent(i18n.t("projectHome.noThreads"));
		expect(
			within(card).getByRole("button", { name: "新建线程" }),
		).toBeInTheDocument();
		expect(screen.queryByTestId("project-services")).toBeNull();
	});

	it("服务一行带状态与所属线程；别的项目的 orb 不算", async () => {
		indexStore
			.getState()
			.setAll([th("T-2", { title: "跑通 demo", kind: "work" })], 1);
		kit.orbs = [
			orb({
				id: "orb-1",
				threadIds: ["T-1", "T-2"],
				services: [
					{
						name: "demo",
						source: "dynamic",
						command: "python3 -m http.server",
						status: "running",
						restarts: 0,
						links: [],
						threadId: "T-2",
					},
				],
			}),
			orb({
				id: "orb-x",
				projectId: CORE.id,
				threadIds: ["T-x"],
				services: [
					{
						name: "other",
						source: "file",
						command: "x",
						status: "failed",
						restarts: 0,
						links: [],
					},
				],
			}),
		];
		mount(`/projects/${ORYN.id}`);
		const rows = await screen.findAllByTestId("project-service");
		expect(rows).toHaveLength(1);
		expect(rows[0]).toHaveTextContent("demo");
		expect(rows[0]).toHaveTextContent("运行中");
		expect(rows[0]).toHaveTextContent("跑通 demo");
		expect(within(rows[0] as HTMLElement).getByRole("link")).toHaveAttribute(
			"href",
			"/t/T-2",
		);
		// 只读：没有启停按钮
		expect(within(rows[0] as HTMLElement).queryByRole("button")).toBeNull();
	});
});
