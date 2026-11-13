/** 应用壳：桌面端侧栏收起 / 展开（按钮、页头开关、Ctrl+B）与持久化。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { qk } from "../../api/queries";
import { SIDEBAR_COLLAPSED_KEY, uiStore } from "../../state/ui";
import { ThreadHeader } from "../thread/ThreadHeader";
import { AppShell } from "./AppShell";

vi.mock("../../realtime/socket", () => ({
	realtime: { connect: vi.fn(), close: vi.fn(), send: vi.fn() },
}));

function setup() {
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false, staleTime: Infinity } },
	});
	qc.setQueryData(qk.me, {
		displayName: "Ada",
		createdAt: 1,
		auth: { kind: "web" },
	});
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={["/t/T-1"]}>
				<Routes>
					<Route element={<AppShell />}>
						<Route
							path="/t/:id"
							element={<ThreadHeader threadId="T-1" sidebarToggle={false} />}
						/>
					</Route>
				</Routes>
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const column = () =>
	document.querySelector<HTMLElement>("[data-sidebar]") as HTMLElement;
const headerToggle = () => screen.queryByRole("button", { name: "侧边栏" });

beforeEach(() => {
	window.innerWidth = 1280;
	localStorage.clear();
	uiStore.setState({ sidebarCollapsed: false, sidebarOpen: false });
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL) => {
			const url = new URL(
				input instanceof Request ? input.url : String(input),
				"http://x",
			);
			if (url.pathname === "/api/v1/projects")
				return Response.json({ projects: [] });
			if (url.pathname === "/api/v1/threads")
				return Response.json({ threads: [] });
			return Response.json({});
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	window.innerWidth = 1024;
});

describe("侧栏收起（expanded）", () => {
	it("点收起按钮：侧栏列变 inert，页头出现展开开关；点开关再展开", async () => {
		setup();
		expect(column()).not.toHaveAttribute("inert");
		expect(headerToggle()).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "收起侧边栏" }));
		expect(column()).toHaveAttribute("inert");
		expect(column().dataset.sidebar).toBe("collapsed");
		expect(localStorage.getItem(SIDEBAR_COLLAPSED_KEY)).toBe("1");
		const toggle = await waitFor(() => {
			const b = headerToggle();
			expect(b).not.toBeNull();
			return b as HTMLElement;
		});
		fireEvent.click(toggle);
		expect(column()).not.toHaveAttribute("inert");
		expect(headerToggle()).toBeNull();
		expect(localStorage.getItem(SIDEBAR_COLLAPSED_KEY)).toBe("0");
	});

	it("Ctrl+B 切换；焦点在输入框里时不抢", () => {
		setup();
		fireEvent.keyDown(window, { key: "b", ctrlKey: true });
		expect(column()).toHaveAttribute("inert");
		fireEvent.keyDown(window, { key: "B", metaKey: true });
		expect(column()).not.toHaveAttribute("inert");
		const search = screen.getByRole("textbox", { name: "搜索线程" });
		fireEvent.keyDown(search, { key: "b", ctrlKey: true });
		expect(column()).not.toHaveAttribute("inert");
	});

	it("收起状态在重新挂载后保持", () => {
		const first = setup();
		fireEvent.click(screen.getByRole("button", { name: "收起侧边栏" }));
		first.unmount();
		setup();
		expect(column()).toHaveAttribute("inert");
		expect(headerToggle()).not.toBeNull();
	});
});
