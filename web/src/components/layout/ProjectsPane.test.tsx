/**
 * 「项目」分区侧栏（M9.6）：首行「新建项目」→ /projects/new；项目行链到 /projects/<id>；行尾按钮设过滤并回线程分区
 * （回到线程分区记住的路径）；「无项目的线程」= 只看无项目线程。数据走 mock 后端。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { threads as fixtureThreads, projects } from "../../mock/fixtures";
import { MockServer } from "../../mock/server";
import { indexStore } from "../../state/stores";
import { uiStore } from "../../state/ui";
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
	uiStore.setState({ projectFilter: undefined, sectionPaths: {} });
});

afterEach(() => {
	vi.unstubAllGlobals();
	indexStore.getState().setAll([], 0);
	uiStore.setState({ projectFilter: undefined });
	window.innerWidth = 1024;
});

describe("ProjectsPane", () => {
	it("没有分区标题行：第一行是「新建项目」，点了去 /projects/new", () => {
		mount();
		const first = list().firstElementChild as HTMLElement;
		expect(first).toHaveTextContent("新建项目");
		fireEvent.click(first);
		expect(where()).toBe("/projects/new");
	});

	it("未归档的项目各一行，链到项目页；当前项目是选中行", async () => {
		const target = live[0];
		mount(`/projects/${target?.id}`);
		const links = await within(list()).findAllByRole("link");
		expect(links.map((a) => a.getAttribute("href"))).toEqual(
			live.map((p) => `/projects/${p.id}`),
		);
		expect(links[0]).toHaveAttribute("aria-current", "page");
		expect(links[0]).toHaveTextContent(target?.name ?? "");
		fireEvent.click(links[1] as HTMLElement);
		expect(where()).toBe(`/projects/${live[1]?.id}`);
	});

	it("行尾按钮：设过滤并回到线程分区记住的路径", async () => {
		const p = live[0];
		uiStore.setState({ sectionPaths: { threads: "/t/T-remembered" } });
		mount(`/projects/${live[1]?.id}`);
		fireEvent.click(
			await within(list()).findByRole("button", {
				name: `只看「${p?.name}」的线程`,
			}),
		);
		expect(uiStore.getState().projectFilter).toBe(p?.id);
		expect(where()).toBe("/t/T-remembered");
	});

	it("「无项目的线程」：只看无项目线程并回线程分区", () => {
		mount();
		fireEvent.click(
			within(list()).getByRole("button", { name: /^无项目的线程/ }),
		);
		expect(uiStore.getState().projectFilter).toBeNull();
		expect(where()).toBe("/");
	});
});
