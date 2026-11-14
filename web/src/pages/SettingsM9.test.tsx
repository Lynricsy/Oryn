/**
 * 设置 → 项目（M9）：每个项目行尾的时钟按钮带「每次新建线程」定时的数量角标，点开在行下展开该项目的定时列表；
 * 设置 → 关于：协调宿主一栏列出协调 orb 并标「协调」。数据走 mock 后端（夹具同截图）。
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
import { M9_IDS, projects } from "../mock/fixtures";
import { MockServer } from "../mock/server";
import { SettingsPage, type SettingsSection } from "./SettingsPage";

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

describe("设置 → 项目的定时任务（M9）", () => {
	it("时钟按钮带数量角标；点开展开该项目的定时列表，再点收起", async () => {
		const [oryn, core] = projects;
		if (!oryn || !core) throw new Error("夹具缺项目");
		setup("projects");
		await waitFor(() =>
			expect(
				screen.getByTestId(`project-schedule-count-${oryn.id}`),
			).toHaveTextContent("2"),
		);
		expect(
			screen.getByTestId(`project-schedule-count-${core.id}`),
		).toHaveTextContent("1");
		const toggles = screen.getAllByRole("button", { name: "定时任务" });
		expect(toggles[0]).toHaveAttribute("aria-expanded", "false");
		fireEvent.click(toggles[0] as HTMLElement);
		expect(toggles[0]).toHaveAttribute("aria-expanded", "true");
		const box = await screen.findByTestId(`project-schedules-${oryn.id}`);
		await waitFor(() => expect(within(box).getByText("nightly")).toBeVisible());
		expect(
			box.querySelector(`[data-schedule="${M9_IDS.spaceSchedule}"]`),
		).not.toBeNull();
		fireEvent.click(toggles[0] as HTMLElement);
		expect(screen.queryByTestId(`project-schedules-${oryn.id}`)).toBeNull();
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
