/**
 * 项目的定时任务（M9，M24 起在项目设置的「定时任务」分组 `/projects/<id>/settings/schedules`）：直接列出该项目的「每次新建线程」定时；
 * 设置 → 关于：协调宿主一栏列出协调 orb 并标「协调」。数据走 mock 后端（夹具同截图）。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { M9_IDS, projects } from "../mock/fixtures";
import { MockServer } from "../mock/server";
import { ProjectSettingsPage } from "./projects/settings/ProjectSettingsPage";
import { SettingsPage } from "./SettingsPage";
import type { SettingsSection } from "./settings/registry";

let server: MockServer;

beforeEach(() => {
	server = new MockServer();
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
});

function setup(section: SettingsSection) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<SettingsPage section={section} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

describe("项目页的定时任务（M9）", () => {
	it("「定时任务」卡直接列出该项目的定时，别的项目的不在里面", async () => {
		const [oryn, core] = projects;
		if (!oryn || !core) throw new Error("夹具缺项目");
		const client = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		render(
			<QueryClientProvider client={client}>
				<MemoryRouter
					initialEntries={[`/projects/${oryn.id}/settings/schedules`]}
				>
					<Routes>
						<Route
							path="/projects/:projectId/settings/:tab"
							element={<ProjectSettingsPage />}
						/>
					</Routes>
				</MemoryRouter>
			</QueryClientProvider>,
		);
		const box = await screen.findByTestId(`project-schedules-${oryn.id}`);
		await waitFor(() => expect(within(box).getByText("nightly")).toBeVisible());
		expect(
			box.querySelector(`[data-schedule="${M9_IDS.spaceSchedule}"]`),
		).not.toBeNull();
		expect(box.querySelectorAll("[data-schedule]")).toHaveLength(2);
		expect(screen.queryByTestId(`project-schedules-${core.id}`)).toBeNull();
	});
});

describe("设置 → 关于：协调宿主（M9）", () => {
	it("可用性、隔离摘要，并列出协调 orb 带「协调」标注", async () => {
		setup("about");
		const about = await screen.findByTestId("coordinator-about");
		await waitFor(() => expect(about).toHaveTextContent("可用"));
		expect(about).toHaveTextContent("网络：独立命名空间");
		expect(about).toHaveTextContent("Landlock ABI 7");
		expect(about).toHaveTextContent("运行用户 uid 65534");
		const orb = await within(about).findByTestId("coordinator-orb");
		expect(orb).toHaveTextContent(M9_IDS.coordOrb);
		expect(orb).toHaveTextContent("协调");
	});

	it("不可用时显示原因原文", async () => {
		localStorage.setItem("oryn.mock.coordinator", "down");
		try {
			setup("about");
			const about = await screen.findByTestId("coordinator-about");
			await waitFor(() => expect(about).toHaveTextContent("不可用"));
			expect(about).toHaveTextContent("does not match core/source.json");
		} finally {
			localStorage.removeItem("oryn.mock.coordinator");
		}
	});
});
