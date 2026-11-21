/**
 * 「项目」分区侧栏（M24）：首行「总览」→ /projects（只在 /projects 选中），行尾 + → /projects/new；
 * 每个未归档项目一行，链到 /projects/<id>（子页里同样选中），副标题是线程数与最后活跃。数据走 mock 后端。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { threads as fixtureThreads, projects } from "../../mock/fixtures";
import { MockServer } from "../../mock/server";
import { indexStore } from "../../state/stores";
import { ProjectsPane } from "./ProjectsPane";

function Where() {
	const loc = useLocation();
	return <span data-testid="where">{loc.pathname}</span>;
}

function mount(path = "/projects") {
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={[path]}>
				<Routes>
					<Route
						path="*"
						element={
							<>
								<ProjectsPane />
								<Where />
							</>
						}
					/>
				</Routes>
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const where = () => screen.getByTestId("where").textContent;
const list = () => screen.getByTestId("sidebar-projects");
const live = projects.filter((p) => !p.archivedAt);
const legacy = projects.find((p) => p.archivedAt);
const overview = () => within(list()).getByRole("link", { name: "总览" });

beforeEach(() => {
	window.innerWidth = 1280;
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
	indexStore.getState().setAll(
		fixtureThreads.filter((t) => !t.archivedAt),
		1,
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	indexStore.getState().setAll([], 0);
	window.innerWidth = 1024;
});

describe("ProjectsPane", () => {
	it("首行是总览，链到 /projects 且只在 /projects 选中", async () => {
		const { unmount } = mount("/projects");
		expect(overview()).toHaveAttribute("href", "/projects");
		expect(overview()).toHaveAttribute("aria-current", "page");
		expect(list().firstElementChild).toContainElement(overview());
		unmount();
		mount(`/projects/${live[0]?.id}`);
		await within(list()).findAllByRole("link", { name: /个线程/ });
		expect(overview()).not.toHaveAttribute("aria-current");
	});

	it("行尾 + 去 /projects/new", () => {
		mount();
		fireEvent.click(within(list()).getByRole("button", { name: "新建项目" }));
		expect(where()).toBe("/projects/new");
	});

	it("未归档项目各一行，legacy-site 不在；子页里项目行仍选中", async () => {
		const target = live[0];
		mount(`/projects/${target?.id}/settings/overview`);
		await within(list()).findAllByRole("link", { name: /个线程/ });
		const rows = within(list())
			.getAllByRole("link")
			.filter((a) => a.getAttribute("href") !== "/projects");
		expect(rows.map((a) => a.getAttribute("href"))).toEqual(
			live.map((p) => `/projects/${p.id}`),
		);
		expect(rows.map((a) => a.getAttribute("href"))).not.toContain(
			`/projects/${legacy?.id}`,
		);
		expect(rows[0]).toHaveAttribute("aria-current", "page");
		expect(rows[0]).toHaveTextContent(target?.name ?? "");
		expect(overview()).not.toHaveAttribute("aria-current");
		fireEvent.click(rows[1] as HTMLElement);
		expect(where()).toBe(`/projects/${live[1]?.id}`);
	});

	it("副标题含『个线程 · 』与『活跃』", async () => {
		mount();
		const rows = await within(list()).findAllByRole("link", {
			name: /个线程/,
		});
		expect(rows).toHaveLength(live.length);
		for (const row of rows) {
			expect(row).toHaveTextContent("个线程 · ");
			expect(row).toHaveTextContent("活跃");
		}
	});
});
