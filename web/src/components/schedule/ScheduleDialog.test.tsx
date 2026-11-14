/**
 * 定时对话框（M9）：两种模式的请求体、预览防抖、名称冲突、运行列表。走 mock 后端（与 orynd 同格式），
 * 记录发往 `/api/v1/schedules*` 与 `/threads/{id}/schedule` 的请求。
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
import { ScheduleDialog, type ScheduleTarget } from "./ScheduleDialog";

interface Call {
	method: string;
	path: string;
	body: Record<string, unknown> | null;
}
let calls: Call[];
let server: MockServer;

beforeEach(() => {
	calls = [];
	server = new MockServer();
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			const path = new URL(req.url).pathname;
			if (/\/schedules?\b/.test(path)) {
				const text = req.method === "GET" ? "" : await req.clone().text();
				calls.push({
					method: req.method,
					path,
					body: text ? (JSON.parse(text) as Record<string, unknown>) : null,
				});
			}
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
	indexStore.getState().setAll([], 0);
});

function mount(target: ScheduleTarget, scheduleId?: string) {
	const onClose = vi.fn();
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	render(
		<QueryClientProvider client={qc}>
			<MemoryRouter>
				<ScheduleDialog
					target={target}
					scheduleId={scheduleId}
					onClose={onClose}
				/>
			</MemoryRouter>
		</QueryClientProvider>,
	);
	return onClose;
}

const dialog = () => screen.getByRole("dialog");
const save = () => within(dialog()).getByRole("button", { name: "保存" });
/** `Field` 的控件：标签首行文字恰为 `label`（提示文字也在 label 里，不能按子串匹配）。 */
const field = (label: string) => {
	const host = Array.from(dialog().querySelectorAll("label")).find(
		(l) => l.firstElementChild?.textContent === label,
	);
	const control = host?.querySelector<HTMLElement>("input,textarea,select");
	if (!control) throw new Error(`没有「${label}」字段`);
	return control;
};
/** 等表单（而不是加载中的占位对话框）出现。 */
const ready = () => waitFor(() => field("提示词"));
const writes = () =>
	calls.filter((c) => c.method !== "GET" && !c.path.endsWith("/preview"));
const previews = () => calls.filter((c) => c.path.endsWith("/preview"));

describe("ScheduleDialog：线程内定时", () => {
	it("新建：PUT threads/{id}/schedule，请求体只有时间字段与提示词（不带 mode / new_thread 字段）", async () => {
		const onClose = mount({ mode: "thread", threadId: M9_IDS.spaceIdle });
		await ready();
		expect(dialog()).toHaveAccessibleName("定时");
		// 名称可选
		expect(field("名称（可选）")).toHaveValue("");
		fireEvent.click(within(dialog()).getByRole("radio", { name: "每隔" }));
		fireEvent.change(within(dialog()).getByLabelText("间隔数值"), {
			target: { value: "2" },
		});
		fireEvent.change(within(dialog()).getByLabelText("间隔单位"), {
			target: { value: "h" },
		});
		expect(save()).toBeDisabled();
		fireEvent.change(field("提示词"), { target: { value: "看一眼 CI" } });
		// 线程内定时没有项目 / 模型 / 保留数
		expect(dialog().textContent).not.toContain("保留最近几次");
		fireEvent.click(save());
		await waitFor(() => expect(onClose).toHaveBeenCalled());
		expect(writes()).toHaveLength(1);
		const [put] = writes();
		expect(put?.method).toBe("PUT");
		expect(put?.path).toBe(`/api/v1/threads/${M9_IDS.spaceIdle}/schedule`);
		expect(put?.body).toMatchObject({
			kind: "every",
			every: "2h",
			prompt: "看一眼 CI",
			enabled: true,
		});
		for (const k of ["mode", "threadId", "projectId", "keepLast", "overlap"])
			expect(put?.body).not.toHaveProperty(k);
		expect(
			server.automation.schedules.some((s) => s.threadId === M9_IDS.spaceIdle),
		).toBe(true);
	});

	it("已有：带出原定义，保存仍走线程的 PUT", async () => {
		const onClose = mount({ mode: "thread", threadId: M9_IDS.scheduledThread });
		await ready();
		expect(dialog()).toHaveAccessibleName("编辑定时");
		expect(field("cron 表达式")).toHaveValue("0 9 * * *");
		fireEvent.click(
			within(dialog()).getByRole("button", { name: "工作日 9:00" }),
		);
		fireEvent.click(save());
		await waitFor(() => expect(onClose).toHaveBeenCalled());
		expect(writes()[0]).toMatchObject({
			method: "PUT",
			path: `/api/v1/threads/${M9_IDS.scheduledThread}/schedule`,
			body: { kind: "cron", cron: "0 9 * * 1-5" },
		});
	});
});

describe("ScheduleDialog：每次新建线程", () => {
	it("新建：名称必填；POST schedules 带 mode / 项目 / Space / 重叠策略 / 保留数", async () => {
		const oryn = projects[0]?.id ?? "";
		const onClose = mount({
			mode: "new_thread",
			projectId: oryn,
			spaceId: M9_IDS.space,
		});
		await ready();
		expect(dialog()).toHaveAccessibleName("新建定时任务");
		// 目标（项目、Space）紧跟名称，在时间字段与提示词之前
		const labels = Array.from(dialog().querySelectorAll("label")).map(
			(l) => l.firstElementChild?.textContent,
		);
		expect(labels.slice(0, 3)).toEqual(["名称", "项目", "空间"]);
		expect(labels.indexOf("项目")).toBeLessThan(labels.indexOf("提示词"));
		await waitFor(() => expect(field("空间")).toHaveValue("发布 M9"));
		expect(field("空间")).toHaveAttribute("readonly");
		fireEvent.change(field("提示词"), { target: { value: "跑测试" } });
		expect(save()).toBeDisabled();
		fireEvent.change(field("名称"), { target: { value: "smoke" } });
		fireEvent.change(field("上一次还没结束时"), { target: { value: "allow" } });
		fireEvent.change(field("保留最近几次"), { target: { value: "80" } });
		await waitFor(() => expect(field("项目")).toHaveValue(oryn));
		fireEvent.click(save());
		await waitFor(() => expect(onClose).toHaveBeenCalled());
		const [post] = writes();
		expect(post).toMatchObject({
			method: "POST",
			path: "/api/v1/schedules",
			body: {
				mode: "new_thread",
				name: "smoke",
				projectId: oryn,
				spaceId: M9_IDS.space,
				kind: "cron",
				overlap: "allow",
				// 保留数夹到 1–50
				keepLast: 50,
				prompt: "跑测试",
			},
		});
	});

	it("lockProject：项目只读", async () => {
		mount({
			mode: "new_thread",
			projectId: projects[1]?.id,
			lockProject: true,
		});
		await ready();
		expect(field("项目")).toBeDisabled();
		// 项目列表到了之后才有对应的选项
		await waitFor(() => expect(field("项目")).toHaveValue(projects[1]?.id));
	});

	it("同项目重名：409 schedule_name_taken 标在名称框上，对话框不关", async () => {
		const onClose = mount({ mode: "new_thread", projectId: projects[0]?.id });
		await ready();
		fireEvent.change(field("名称"), { target: { value: "nightly" } });
		fireEvent.change(field("提示词"), { target: { value: "x" } });
		fireEvent.click(save());
		const alert = await within(dialog()).findByRole("alert");
		expect(alert).toHaveTextContent("这个项目里已有同名的定时任务");
		expect(field("名称")).toHaveAttribute("aria-invalid", "true");
		expect(onClose).not.toHaveBeenCalled();
	});

	it("编辑：带出名称与运行记录（新建线程的运行带线程链接，失败行显示错误）", async () => {
		mount(
			{ mode: "new_thread", projectId: projects[0]?.id },
			M9_IDS.projectSchedule,
		);
		await ready();
		expect(dialog()).toHaveAccessibleName("编辑定时");
		expect(field("名称")).toHaveValue("nightly");
		expect(field("项目")).toBeDisabled();
		const runs = await within(dialog()).findByTestId("schedule-runs");
		const rows = within(runs).getAllByRole("listitem");
		expect(rows.map((r) => r.getAttribute("data-status"))).toEqual([
			"running",
			"completed",
			"completed",
		]);
		expect(within(rows[0] as HTMLElement).getByRole("link")).toHaveAttribute(
			"href",
			`/t/${M9_IDS.run3}`,
		);
		expect(within(rows[0] as HTMLElement).getByRole("link")).toHaveTextContent(
			"nightly · 11-14 09:00",
		);
	});

	it("连续失败自动暂停：顶部提示，运行行显示错误原文", async () => {
		mount({ mode: "new_thread" }, M9_IDS.failingSchedule);
		await ready();
		expect(within(dialog()).getByText("连续失败，已自动暂停")).toBeVisible();
		const runs = await within(dialog()).findByTestId("schedule-runs");
		expect(
			within(runs).getAllByText('create thread: provider "nope" not found'),
		).toHaveLength(3);
	});
});

describe("ScheduleDialog：预览", () => {
	it("防抖：连续改动只在停下后请求一次（用最后的表达式），显示接下来的时间", async () => {
		mount({ mode: "thread", threadId: M9_IDS.spaceIdle });
		await ready();
		const cron = field("cron 表达式");
		fireEvent.change(cron, { target: { value: "0 1 * * *" } });
		fireEvent.change(cron, { target: { value: "0 2 * * *" } });
		fireEvent.change(cron, { target: { value: "15 3 * * *" } });
		const box = within(dialog()).getByTestId("schedule-preview");
		await waitFor(() =>
			expect(within(box).getAllByRole("listitem").length).toBeGreaterThan(0),
		);
		expect(previews()).toHaveLength(1);
		expect(previews()[0]?.body).toMatchObject({
			kind: "cron",
			cron: "15 3 * * *",
		});
	});

	it("不合法的表达式：显示服务端的错误", async () => {
		mount({ mode: "thread", threadId: M9_IDS.spaceIdle });
		await ready();
		fireEvent.change(field("cron 表达式"), { target: { value: "bad" } });
		const box = within(dialog()).getByTestId("schedule-preview");
		await within(box).findByRole("alert");
	});
});
