/** 项目总览（M24）：按最近活动排序、活动曲线（14 天、折叠时隐藏）、空状态与「显示已归档项目」开关。 */
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../../api/client";
import i18n from "../../i18n";
import { indexStore } from "../../state/stores";
import {
	CORE,
	kit,
	LEGACY,
	mount,
	ORYN,
	resetKit,
	usageByProject,
	where,
} from "./projects-test-kit";

const thread = (id: string, projectId: string, at: number): Thread =>
	({
		id,
		projectId,
		title: id,
		kind: "work",
		state: "idle",
		createdAt: at,
		updatedAt: at,
		lastMessageAt: at,
	}) as unknown as Thread;

const rows = () =>
	screen.getAllByTestId("project-row").map((r) => r.dataset.project);

const curveName = (name: string) =>
	i18n.t("projectsOverview.activity", { name });

beforeEach(resetKit);

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
	indexStore.getState().setAll([], 0);
});

describe("ProjectsOverviewPage", () => {
	it("没有项目：空状态，主按钮去新建", async () => {
		kit.projects = [];
		mount("/projects");
		const empty = await screen.findByTestId("projects-empty");
		expect(empty).toHaveTextContent(i18n.t("projectsPage.empty"));
		fireEvent.click(within(empty).getByRole("button", { name: "新建项目" }));
		expect(where()).toHaveTextContent("/projects/new");
	});

	it("按最近活动排序：线程更新的项目在前；行上有描述 · 仓库、线程数与活跃时间", async () => {
		kit.projects = [{ ...ORYN, description: "控制面" }, CORE];
		indexStore
			.getState()
			.setAll(
				[
					thread("T-1", ORYN.id, 1_000),
					thread("T-2", CORE.id, 5_000),
					thread("T-3", CORE.id, 2_000),
				],
				1,
			);
		mount("/projects");
		await screen.findAllByTestId("project-row");
		expect(rows()).toEqual([CORE.id, ORYN.id]);
		const oryn = screen
			.getAllByTestId("project-row")
			.find((r) => r.dataset.project === ORYN.id) as HTMLElement;
		expect(oryn).toHaveAttribute("href", `/projects/${ORYN.id}`);
		expect(oryn).toHaveTextContent("控制面 · github.com/x/oryn");
		expect(oryn).toHaveTextContent("1 个线程");
		expect(oryn).toHaveTextContent("活跃");
		const core = screen
			.getAllByTestId("project-row")
			.find((r) => r.dataset.project === CORE.id) as HTMLElement;
		expect(core).toHaveTextContent(i18n.t("projects.noRepo"));
		expect(core).toHaveTextContent("2 个线程");
	});

	it("用量请求：按项目分组、按天序列、limit 500，窗口 14 个本地日", async () => {
		mount("/projects");
		await waitFor(() =>
			expect(kit.reads.some((r) => r.startsWith("/api/v1/stats/usage"))).toBe(
				true,
			),
		);
		const read = kit.reads.find((r) => r.startsWith("/api/v1/stats/usage"));
		const q = new URL(read ?? "", "http://x").searchParams;
		expect(q.get("groupBy")).toBe("project");
		expect(q.get("series")).toBe("day");
		expect(q.get("limit")).toBe("500");
		const days = (Number(q.get("to")) - Number(q.get("from"))) / 86_400_000;
		expect(Math.round(days)).toBe(14);
	});

	it("曲线：有数据时画出；不在分组里又没有 __other__ 的项目是全零平线", async () => {
		kit.usage = usageByProject({ [ORYN.id]: [0, 2, 5] });
		mount("/projects");
		expect(
			await screen.findByRole("img", { name: curveName("oryn") }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("img", { name: curveName("core") }),
		).toBeInTheDocument();
	});

	it("曲线：项目被折叠进 __other__ 时数值不可信，不画", async () => {
		kit.usage = usageByProject({ [ORYN.id]: [1, 2, 3], __other__: [1, 1, 1] });
		mount("/projects");
		// ORYN 的曲线出现 = 用量已返回；CORE 被折叠，没有曲线
		expect(
			await screen.findByRole("img", { name: curveName("oryn") }),
		).toBeInTheDocument();
		expect(screen.queryByRole("img", { name: curveName("core") })).toBeNull();
	});

	it("⋯ → 显示已归档项目：URL 带 ?archived=1，出现已归档区；再点一次收起", async () => {
		kit.archived = [LEGACY];
		mount("/projects");
		await screen.findAllByTestId("project-row");
		expect(
			screen.queryByRole("heading", { name: i18n.t("sidebar.archived") }),
		).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "更多操作" }));
		fireEvent.click(
			screen.getByRole("menuitemradio", { name: "显示已归档项目" }),
		);
		expect(where()).toHaveTextContent(/^\/projects\?archived=1$/);
		await waitFor(() => expect(rows()).toContain(LEGACY.id));
		const legacy = screen
			.getAllByTestId("project-row")
			.find((r) => r.dataset.project === LEGACY.id) as HTMLElement;
		expect(legacy).toHaveTextContent("已归档");
		expect(legacy).toHaveTextContent("旧官网");
		fireEvent.click(screen.getByRole("button", { name: "更多操作" }));
		expect(
			screen.getByRole("menuitemradio", { name: "显示已归档项目" }),
		).toHaveAttribute("aria-checked", "true");
		fireEvent.click(
			screen.getByRole("menuitemradio", { name: "显示已归档项目" }),
		);
		expect(where()).toHaveTextContent(/^\/projects$/);
		expect(rows()).not.toContain(LEGACY.id);
	});

	it("打开了归档开关但没有已归档项目：说明一句", async () => {
		mount("/projects?archived=1");
		expect(
			await screen.findByText(i18n.t("projectsOverview.noArchived")),
		).toBeInTheDocument();
	});
});
