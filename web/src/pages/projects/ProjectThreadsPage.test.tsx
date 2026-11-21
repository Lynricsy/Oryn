/** 项目的线程子页（M24）：索引 + 本项目归档线程，协调对话不列；状态筛选与搜索写进 URL，筛空时可一键清除。 */
import {
	act,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../../api/client";
import { indexStore } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { CORE, kit, mount, ORYN, resetKit, where } from "./projects-test-kit";

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
		...p,
	};
}

const T1 = th("T1", { projectId: ORYN.id, labels: ["bug"], lastMessageAt: 5 });
const T2 = th("T2", { projectId: ORYN.id, shippedAt: 4, lastMessageAt: 4 });
const SPACE = th("SP", { projectId: ORYN.id, kind: "space" });
const OTHER = th("T9", { projectId: CORE.id });
const T3 = th("T3", { projectId: ORYN.id, archivedAt: 3, lastMessageAt: 3 });

const rows = () =>
	screen.getAllByTestId("project-thread-row").map((r) => r.dataset.thread);

beforeEach(() => {
	resetKit();
	kit.threads = [T3];
	indexStore.getState().setAll([T1, T2, SPACE, OTHER], 1);
});

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
	indexStore.getState().setAll([], 0);
});

describe("项目线程子页", () => {
	it("列出本项目的实时与归档线程，按状态筛选、搜索，筛空后清除", async () => {
		vi.useFakeTimers({ shouldAdvanceTime: true });
		mount(`/projects/${ORYN.id}/threads`);
		await waitFor(() => expect(rows()).toEqual(["T1", "T2", "T3"]));
		expect(kit.reads).toContain(
			`/api/v1/threads?archived=true&project=${ORYN.id}`,
		);
		expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("线程");

		fireEvent.click(screen.getByRole("button", { name: "已归档" }));
		expect(where().textContent).toBe(
			`/projects/${ORYN.id}/threads?status=archived`,
		);
		expect(rows()).toEqual(["T3"]);

		fireEvent.change(
			screen.getByRole("searchbox", { name: "搜索标题与标签" }),
			{
				target: { value: "bug" },
			},
		);
		await act(() => vi.advanceTimersByTimeAsync(200));
		expect(where().textContent).toBe(
			`/projects/${ORYN.id}/threads?status=archived&q=bug`,
		);
		expect(screen.getByText("没有符合条件的线程。")).toBeTruthy();

		fireEvent.click(screen.getByRole("button", { name: "清除筛选" }));
		expect(where().textContent).toBe(`/projects/${ORYN.id}/threads`);
		expect(rows()).toEqual(["T1", "T2", "T3"]);
		expect((screen.getByRole("searchbox") as HTMLInputElement).value).toBe("");
		// 清除之后防抖到期也不会把旧搜索词写回去
		await act(() => vi.advanceTimersByTimeAsync(300));
		expect(where().textContent).toBe(`/projects/${ORYN.id}/threads`);
	});

	it("URL 里的筛选在打开时生效，非法状态按全部处理", async () => {
		mount(`/projects/${ORYN.id}/threads?status=bogus&q=BUG`);
		await waitFor(() => expect(rows()).toEqual(["T1"]));
		expect(
			screen.getByRole("button", { name: "全部" }).getAttribute("aria-pressed"),
		).toBe("true");
		expect((screen.getByRole("searchbox") as HTMLInputElement).value).toBe(
			"BUG",
		);
	});

	it("已交付的线程带徽标", async () => {
		mount(`/projects/${ORYN.id}/threads?status=shipped`);
		await waitFor(() => expect(rows()).toEqual(["T2"]));
		const row = screen.getByTestId("project-thread-row");
		expect(within(row).getByText("已交付")).toBeTruthy();
	});

	it("项目没有线程时是空状态；新建线程把项目交给首页", async () => {
		indexStore.getState().setAll([], 1);
		mount(`/projects/${CORE.id}/threads`);
		await waitFor(() =>
			expect(screen.getByText("这个项目还没有线程。")).toBeTruthy(),
		);
		expect(screen.queryByTestId("project-thread-row")).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "新建线程" }));
		expect(uiStore.getState().homeSeed).toEqual({
			text: "",
			projectId: CORE.id,
		});
		expect(where().textContent).toBe("/");
	});
});
