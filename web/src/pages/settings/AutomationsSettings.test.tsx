/**
 * 设置 → 自动化（M9）的两张卡片与 `?schedule=` / `?project=` 直达，以及项目设置里展开的「定时任务」的增删与启停。
 * 走 mock 后端（夹具同截图）。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	threads as fixtureThreads,
	M9_IDS,
	projects,
} from "../../mock/fixtures";
import { MockServer } from "../../mock/server";
import { indexStore } from "../../state/stores";
import AutomationsSettings from "./AutomationsSettings";
import { ProjectSchedules } from "./ProjectSchedules";

let server: MockServer;
let requests: string[];

beforeEach(() => {
	server = new MockServer();
	requests = [];
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			const u = new URL(req.url);
			requests.push(`${req.method} ${u.pathname}${u.search}`);
			return (
				(await server.handleFetch(req)) ?? new Response(null, { status: 404 })
			);
		},
	);
	indexStore.getState().setAll(
		fixtureThreads.filter((t) => !t.archivedAt),
		1,
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
	indexStore.getState().setAll([], 0);
});

function mount(node: React.ReactNode, path = "/settings/automations") {
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={[path]}>{node}</MemoryRouter>
		</QueryClientProvider>,
	);
}

const rowNames = (section: HTMLElement) =>
	Array.from(section.querySelectorAll("[data-schedule]")).map(
		(li) => li.getAttribute("data-schedule") as string,
	);

describe("设置 → 自动化：定时任务", () => {
	it("分「项目定时」「线程内定时」两段；agent 建的带徽标，线程内定时用线程标题", async () => {
		mount(<AutomationsSettings />);
		const projectSec = screen.getByTestId("automation-project-schedules");
		await waitFor(() =>
			expect(rowNames(projectSec)).toEqual([
				M9_IDS.projectSchedule,
				M9_IDS.spaceSchedule,
				M9_IDS.failingSchedule,
			]),
		);
		const threadSec = screen.getByTestId("automation-thread-schedules");
		expect(rowNames(threadSec)).toEqual([M9_IDS.threadSchedule]);
		expect(threadSec).toHaveTextContent("每天早上同步上游并跑测试");
		const patrol = projectSec.querySelector(
			`[data-schedule="${M9_IDS.spaceSchedule}"]`,
		) as HTMLElement;
		expect(patrol).toHaveTextContent("由 agent 创建");
		expect(patrol).toHaveTextContent("工作日 9:00");
		const failing = projectSec.querySelector(
			`[data-schedule="${M9_IDS.failingSchedule}"]`,
		) as HTMLElement;
		expect(failing).toHaveTextContent("失败");
		expect(within(failing).getByRole("checkbox")).not.toBeChecked();
	});

	it("?schedule=<id> 直接打开该定时的对话框", async () => {
		mount(
			<AutomationsSettings />,
			`/settings/automations?schedule=${M9_IDS.projectSchedule}`,
		);
		const dialog = await screen.findByRole("dialog", { name: "编辑定时" });
		await waitFor(() =>
			expect(within(dialog).getByDisplayValue("nightly")).toBeVisible(),
		);
	});

	it("?project=<id> 只列该项目的项目定时，不显示线程内定时", async () => {
		const core = projects[1];
		mount(<AutomationsSettings />, `/settings/automations?project=${core?.id}`);
		const projectSec = screen.getByTestId("automation-project-schedules");
		await waitFor(() =>
			expect(rowNames(projectSec)).toEqual([M9_IDS.failingSchedule]),
		);
		expect(screen.queryByTestId("automation-thread-schedules")).toBeNull();
		expect(await screen.findByText(`只看项目「${core?.name}」`)).toBeVisible();
	});
});

describe("设置 → 自动化：操作记录", () => {
	it("每行 = 时间、发起方、动作、目标线程；自动回合带「自动」，失败带错误码", async () => {
		mount(<AutomationsSettings />);
		const list = screen.getByTestId("automation-actions");
		await waitFor(() =>
			expect(list.querySelectorAll("[data-action]")).toHaveLength(5),
		);
		const row = (id: string) =>
			list.querySelector(`[data-action="${id}"]`) as HTMLElement;
		const auto = row("cac_01JD0000000000000000CAC004");
		expect(auto).toHaveTextContent("归档线程");
		expect(auto).toHaveTextContent("自动");
		expect(within(auto).getByRole("link", { name: /助手/ })).toHaveAttribute(
			"href",
			`/assistant/${M9_IDS.assistant}`,
		);
		const failed = row("cac_01JD0000000000000000CAC003");
		expect(failed).toHaveTextContent("forbidden");
		expect(failed).not.toHaveTextContent("自动");
		const space = row("cac_01JD0000000000000000CAC001");
		await waitFor(() =>
			expect(
				within(space).getByRole("link", { name: "空间「发布 M9」" }),
			).toHaveAttribute("href", `/space/${M9_IDS.space}`),
		);
		expect(
			within(space).getByRole("link", { name: "侧栏按定时折叠运行线程" }),
		).toHaveAttribute("href", `/t/${M9_IDS.spaceNeedsYou}`);
		// 不满一页时没有「加载更多」
		expect(screen.queryByRole("button", { name: "加载更多" })).toBeNull();
	});

	it("「加载更多」每次再取 50 条", async () => {
		const base = server.automation.actions[0];
		if (!base) throw new Error("夹具缺操作记录");
		for (let i = 0; i < 60; i++)
			server.automation.actions.push({
				...base,
				id: `cac_01JD00000000000000MORE${String(i).padStart(2, "0")}`,
			});
		mount(<AutomationsSettings />);
		const more = await screen.findByRole("button", { name: "加载更多" });
		expect(
			screen
				.getByTestId("automation-actions")
				.querySelectorAll("[data-action]"),
		).toHaveLength(50);
		fireEvent.click(more);
		await waitFor(() =>
			expect(
				screen
					.getByTestId("automation-actions")
					.querySelectorAll("[data-action]"),
			).toHaveLength(65),
		);
		expect(requests).toContain("GET /api/v1/automation/actions?limit=100");
		expect(screen.queryByRole("button", { name: "加载更多" })).toBeNull();
	});
});

describe("项目设置里的定时任务", () => {
	const oryn = projects[0];

	it("列出该项目的「每次新建线程」定时；启停走 pause / resume", async () => {
		if (!oryn) throw new Error("夹具缺项目");
		mount(<ProjectSchedules project={oryn} />);
		const box = screen.getByTestId(`project-schedules-${oryn.id}`);
		await waitFor(() =>
			expect(rowNames(box)).toEqual([
				M9_IDS.projectSchedule,
				M9_IDS.spaceSchedule,
			]),
		);
		const nightly = box.querySelector(
			`[data-schedule="${M9_IDS.projectSchedule}"]`,
		) as HTMLElement;
		expect(nightly).toHaveTextContent("每 6 小时");
		fireEvent.click(within(nightly).getByRole("checkbox"));
		await waitFor(() =>
			expect(within(nightly).getByRole("checkbox")).not.toBeChecked(),
		);
		expect(requests).toContain(
			`POST /api/v1/schedules/${M9_IDS.projectSchedule}/pause`,
		);
	});

	it("新建（项目锁定为本项目）后出现在列表里；删除后消失", async () => {
		if (!oryn) throw new Error("夹具缺项目");
		vi.spyOn(window, "confirm").mockReturnValue(true);
		mount(<ProjectSchedules project={oryn} />);
		const box = screen.getByTestId(`project-schedules-${oryn.id}`);
		await waitFor(() => expect(rowNames(box)).toHaveLength(2));
		fireEvent.click(within(box).getByRole("button", { name: "新建定时任务" }));
		const dialog = await screen.findByRole("dialog", { name: "新建定时任务" });
		const select = dialog.querySelector("select:disabled") as HTMLSelectElement;
		await waitFor(() => expect(select.value).toBe(oryn.id));
		const control = (label: string) =>
			Array.from(dialog.querySelectorAll("label"))
				.find((l) => l.firstElementChild?.textContent === label)
				?.querySelector("input,textarea") as HTMLElement;
		fireEvent.change(control("名称"), { target: { value: "lint" } });
		fireEvent.change(control("提示词"), { target: { value: "跑 lint" } });
		fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
		await waitFor(() => expect(rowNames(box)).toHaveLength(3));
		const created = server.automation.schedules.find((s) => s.name === "lint");
		expect(created).toMatchObject({ mode: "new_thread", projectId: oryn.id });

		const row = box.querySelector(
			`[data-schedule="${created?.id}"]`,
		) as HTMLElement;
		fireEvent.click(
			within(row).getByRole("button", { name: "「lint」的操作" }),
		);
		fireEvent.click(await screen.findByRole("menuitem", { name: "删除" }));
		await waitFor(() => expect(rowNames(box)).toHaveLength(2));
	});
});
