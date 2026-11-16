/**
 * 收件箱页：分节顺序与行内容取实时索引；分组方式切换并存 localStorage；空状态。项目名走 mock 后端。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../api/client";
import { projects } from "../mock/fixtures";
import { resetNotifyMock } from "../mock/notify";
import { MockServer } from "../mock/server";
import {
	type AppNotification,
	notificationsStore,
} from "../state/notifications";
import { indexStore } from "../state/stores";
import { toastsStore } from "../state/toasts";
import { InboxPage } from "./InboxPage";

const NOW = Date.now();

function th(id: string, p: Partial<Thread> = {}): Thread {
	return {
		id,
		title: id,
		titleSource: "auto",
		state: "idle",
		stateChangedAt: NOW,
		pinned: false,
		labels: [],
		unread: false,
		createdAt: NOW,
		updatedAt: NOW,
		lastMessageAt: NOW,
		...p,
	};
}

const sections = () =>
	[...document.querySelectorAll<HTMLElement>("[data-inbox-section]")].map(
		(s) => s.dataset.inboxSection,
	);

describe("收件箱页", () => {
	beforeEach(() => {
		const server = new MockServer();
		vi.stubGlobal(
			"fetch",
			async (input: RequestInfo | URL, init?: RequestInit) => {
				const req = input instanceof Request ? input : new Request(input, init);
				return (
					(await server.handleFetch(req)) ?? new Response(null, { status: 404 })
				);
			},
		);
	});

	afterEach(() => {
		vi.unstubAllGlobals();
		indexStore.getState().setAll([], 0);
		notificationsStore.setState({ items: [] });
		localStorage.clear();
		resetNotifyMock();
	});

	function mount() {
		const qc = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		return render(
			<QueryClientProvider client={qc}>
				<MemoryRouter>
					<InboxPage />
				</MemoryRouter>
			</QueryClientProvider>,
		);
	}

	it("按状态分节，行带意图行与派生来源，点行进线程；切到按项目并记住", async () => {
		const oryn = projects[0];
		indexStore.getState().setAll(
			[
				th("parent", { title: "🔌 SOCK: 父线程", projectId: oryn?.id }),
				th("run", {
					state: "working",
					activity: "读 socket.ts",
					projectId: oryn?.id,
				}),
				th("wait", {
					state: "awaiting_input",
					parentThreadId: "parent",
					projectId: oryn?.id,
				}),
				th("done", { unread: true, lastMessageAt: NOW - 60_000 }),
				th("err", { state: "error", archivedAt: NOW }),
			],
			1,
		);
		mount();
		expect(sections()).toEqual(["you", "ready", "running"]);
		const wait = document.querySelector(
			'[data-inbox-row="wait"]',
		) as HTMLElement;
		expect(wait).toHaveAttribute("href", "/t/wait");
		expect(wait).toHaveTextContent("派生自「父线程」");
		await waitFor(() => expect(wait).toHaveTextContent(oryn?.name ?? ""));
		const run = document.querySelector('[data-inbox-row="run"]') as HTMLElement;
		expect(
			within(run).getByText("读 socket.ts", {
				selector: "[data-inbox-activity]",
			}),
		).toBeVisible();
		// 空闲已读的父线程、已归档的出错线程都不列
		expect(document.querySelector('[data-inbox-row="parent"]')).toBeNull();
		expect(document.querySelector('[data-inbox-row="err"]')).toBeNull();

		fireEvent.click(screen.getByRole("button", { name: "按项目" }));
		expect(screen.getByRole("button", { name: "按项目" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(sections()).toEqual([oryn?.id, "_none"]);
		expect(localStorage.getItem("oryn.inbox.groupBy")).toBe("project");
	});

	it("索引变化实时反映；没有条目时是空状态", () => {
		indexStore.getState().setAll([th("quiet")], 1);
		mount();
		expect(screen.getByTestId("inbox-empty")).toHaveTextContent(
			"没有需要你处理的线程",
		);
		act(() =>
			indexStore
				.getState()
				.upsertThread(th("quiet", { state: "awaiting_input" })),
		);
		expect(screen.queryByTestId("inbox-empty")).toBeNull();
		expect(sections()).toEqual(["you"]);
	});

	it("行内预览取该线程未作答的提问与失败原因；状态筛选只留那一类，那一类清空后回到全部", async () => {
		const note = (
			id: string,
			threadId: string,
			p: Partial<AppNotification>,
		): AppNotification => ({
			id,
			kind: "input",
			threadId,
			title: threadId,
			body: "",
			url: `/t/${threadId}`,
			createdAt: NOW,
			deliveries: [],
			...p,
		});
		// 新的在前：已作答的那条较新，预览要跳过它取未作答的
		notificationsStore.setState({
			items: [
				note("n2", "wait", { body: "已经答过的旧问题", resolvedAt: NOW }),
				note("n1", "wait", { body: "允许执行 `rm -rf dist`？" }),
			],
		});
		indexStore.getState().setAll(
			[
				th("wait", { state: "awaiting_input" }),
				th("err", {
					state: "error",
					error: { kind: "agent_failed", reason: "omp exited with status 1" },
				}),
				th("pull", {
					state: "error",
					error: {
						kind: "orb_failed",
						reason: "docker run: exit status 125: pull access denied for x:1",
						cause: "image_pull_denied",
						detail: "x:1",
					},
				}),
				th("run", { state: "working", activity: "跑测试" }),
			],
			1,
		);
		mount();
		const preview = (id: string) =>
			document.querySelector(`[data-inbox-row="${id}"] [data-inbox-preview]`);
		// 提问原文纯文本：反引号不当标记解析
		expect(preview("wait")).toHaveTextContent("允许执行 `rm -rf dist`？");
		expect(preview("wait")).not.toHaveTextContent("已经答过");
		// 认不出原因（没有 cause）：原文；认得出：一句人话 + 抽出的镜像名，原文仍在
		expect(preview("err")).toHaveTextContent("omp exited with status 1");
		expect(preview("pull")).toHaveTextContent(
			"拉取环境镜像被拒绝：镜像不存在或没有权限x:1docker run",
		);

		fireEvent.click(
			document.querySelector("[data-inbox-filter=stopped]") as HTMLElement,
		);
		expect(sections()).toEqual(["stopped"]);
		act(() => indexStore.getState().upsertThread(th("err", { state: "idle" })));
		expect(sections()).toEqual(["stopped"]);
		act(() =>
			indexStore.getState().upsertThread(th("pull", { state: "idle" })),
		);
		// 离开的行先播完收起动画（LEAVE_MS）才从原分节里消失
		await waitFor(() => expect(sections()).toEqual(["you", "running"]));
	});

	it("行内归档走归档入口：带 worktree 的线程先弹出是否一并移除工作树", () => {
		indexStore.getState().setAll(
			[
				th("wt", {
					state: "error",
					worktree: { repo: "/r", dir: "/r/.wt/x", branch: "oryn/x" },
				}),
			],
			1,
		);
		mount();
		const row = document.querySelector('[data-inbox-row="wt"]')
			?.parentElement as HTMLElement;
		fireEvent.click(within(row).getByRole("button", { name: "归档" }));
		expect(screen.getByTestId("archive-dialog")).toHaveTextContent("/r/.wt/x");
		// 对话框确认前不归档
		expect(indexStore.getState().threads.wt?.archivedAt).toBeUndefined();
	});

	it("可一触作答：审批原文纯文本显示；作答后按钮消失；409 时提示并换成同线程更早的未决提问；WS 解决事件同样撤掉", async () => {
		// mock 后端里两条带 quickAnswer 的 input 通知：…04（Docker 审批）与 …05（侧栏折叠）
		const ask = (
			id: string,
			createdAt: number,
			question: string,
		): AppNotification => ({
			id,
			kind: "input",
			threadId: "wait",
			title: "wait",
			body: question,
			url: "/t/wait",
			createdAt,
			deliveries: [],
			quickAnswer: {
				question,
				options: [
					{ i: 0, label: "允许" },
					{ i: 1, label: "拒绝" },
				],
			},
		});
		const q2 = "Command: echo `rm -rf /tmp/x`\n允许？";
		// 线程状态在通知之后才变（agent 断线重放同一请求）：通知仍有效
		notificationsStore.setState({
			items: [
				ask("ntf_01JD0000000000000000000004", NOW - 60_000, q2),
				ask("ntf_01JD0000000000000000000005", NOW - 120_000, "更早的提问？"),
			],
		});
		indexStore
			.getState()
			.setAll(
				[th("wait", { state: "awaiting_input", stateChangedAt: NOW })],
				1,
			);
		mount();
		const quick = () =>
			document.querySelector("[data-inbox-quick]") as HTMLElement | null;
		// 反引号原样保留：显示的就是会执行的命令
		expect(quick()?.querySelector("[data-inbox-question]")?.textContent).toBe(
			q2,
		);
		fireEvent.click(
			within(quick() as HTMLElement).getByRole("button", { name: "允许" }),
		);
		// 作答成功：这条记为已解决，换成更早的未决提问
		await waitFor(() => expect(quick()).toHaveTextContent("更早的提问？"));
		expect(
			notificationsStore
				.getState()
				.items.find((n) => n.id === "ntf_01JD0000000000000000000004")
				?.resolvedAt,
		).toBeDefined();

		// 本地还当它未决、后端已解决：409 → 提示并撤掉，露出下一条
		act(() =>
			notificationsStore.setState({
				items: [
					ask("ntf_01JD0000000000000000000004", NOW - 60_000, q2),
					ask("ntf_01JD0000000000000000000005", NOW - 120_000, "更早的提问？"),
				],
			}),
		);
		fireEvent.click(
			within(quick() as HTMLElement).getByRole("button", { name: "拒绝" }),
		);
		await waitFor(() => expect(quick()).toHaveTextContent("更早的提问？"));
		expect(toastsStore.getState().toasts.map((x) => x.text)).toContain(
			"已在别处作答",
		);

		// WS notifications.resolved：最后一条也撤掉
		act(() =>
			notificationsStore
				.getState()
				.onResolved(["ntf_01JD0000000000000000000005"], NOW),
		);
		expect(quick()).toBeNull();
	});

	it("进出动效：同一线程回到某节时再次淡入；快速来回切换不留重复的离场行", async () => {
		const pause = (ms: number) =>
			act(
				() =>
					new Promise<void>((resolve) => {
						setTimeout(resolve, ms);
					}),
			);
		const errors = vi.spyOn(console, "error").mockImplementation(() => {});
		const set = (state: Thread["state"]) =>
			act(() => indexStore.getState().upsertThread(th("x", { state })));
		const rowsIn = (section: string) =>
			document.querySelectorAll(
				`[data-inbox-section="${section}"] [data-inbox-row="x"]`,
			);
		const li = (section: string) => rowsIn(section)[0]?.closest("li");
		indexStore.getState().setAll([th("x", { state: "working" })], 1);
		mount();
		await pause(850); // 首次出场结束

		set("awaiting_input");
		expect(li("you")).toHaveClass("inbox-arrive");
		await pause(400);
		set("working");
		await pause(400);
		// 再次进入「等你处理」：仍是新到的一行
		set("awaiting_input");
		expect(li("you")).toHaveClass("inbox-arrive");

		// 180ms 内来回切换：同键只留一条离场记录
		set("working");
		set("awaiting_input");
		set("working");
		set("awaiting_input");
		expect(rowsIn("running").length).toBeLessThanOrEqual(1);
		expect(
			errors.mock.calls.some((c) => String(c[0]).includes("same key")),
		).toBe(false);
		await waitFor(() => expect(rowsIn("running")).toHaveLength(0));
		errors.mockRestore();
	});
});
