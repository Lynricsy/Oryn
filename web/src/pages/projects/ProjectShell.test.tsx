/**
 * 项目页外框（M24）：ProjectGate 按 id 取项目（列表里没有时单取，含已归档），归档提示与取消归档，
 * 不存在 / 读取失败的一屏，面包屑。fetch 换成按路径记账的桩。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Project } from "../../api/client";
import { ProjectGate, ProjectShell } from "./ProjectShell";

const OLD: Project = {
	id: "prj_old",
	name: "legacy",
	shallowClone: false,
	snapshots: true,
	warmOrbs: 0,
	createdAt: 1,
	updatedAt: 1,
	archivedAt: 5,
};

let listed: Project[];
let listStatus: number;
let single: Record<string, Project>;
let writes: [string, string, unknown][];

async function handle(req: Request): Promise<Response> {
	const path = new URL(req.url, "http://x").pathname;
	const text = req.method === "GET" ? "" : await req.text();
	const body = text ? JSON.parse(text) : undefined;
	if (req.method !== "GET") writes.push([req.method, path, body]);
	if (path === "/api/v1/projects")
		return listStatus === 200
			? Response.json({ projects: listed })
			: Response.json(
					{ code: "internal", message: "boom" },
					{ status: listStatus },
				);
	const one = /^\/api\/v1\/projects\/([^/]+)$/.exec(path);
	const p = one ? single[one[1] ?? ""] : undefined;
	if (!p) return Response.json({ code: "not_found" }, { status: 404 });
	if (req.method === "PATCH") {
		const next: Project = {
			...p,
			archivedAt: body.archived === false ? undefined : p.archivedAt,
			updatedAt: p.updatedAt + 1,
		};
		if (body.archived === false) listed = [...listed, next];
		return Response.json(next);
	}
	return Response.json(p);
}

function mount(path: string) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter initialEntries={[path]}>
				<Routes>
					<Route
						path="/projects/:projectId"
						element={
							<ProjectGate>
								{(p) => (
									<ProjectShell project={p}>
										<p data-testid="body">{p.name}</p>
									</ProjectShell>
								)}
							</ProjectGate>
						}
					/>
					<Route
						path="/projects/:projectId/threads"
						element={
							<ProjectGate>
								{(p) => (
									<ProjectShell
										project={p}
										crumbs={[{ label: "设置", to: "/s" }, { label: "环境" }]}
									>
										<p />
									</ProjectShell>
								)}
							</ProjectGate>
						}
					/>
				</Routes>
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

beforeEach(() => {
	listed = [];
	listStatus = 200;
	single = { [OLD.id]: OLD };
	writes = [];
	vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) =>
		handle(
			input instanceof Request
				? input
				: new Request(new URL(String(input), "http://x"), init),
		),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("ProjectGate", () => {
	it("列表里没有：按 id 单取已归档项目，显示归档提示；取消归档发 PATCH", async () => {
		mount("/projects/prj_old");
		expect(await screen.findByTestId("project-archived")).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { level: 1, name: "legacy" }),
		).toBeInTheDocument();
		expect(screen.getByTestId("body")).toHaveTextContent("legacy");
		fireEvent.click(screen.getByRole("button", { name: "取消归档" }));
		await waitFor(() =>
			expect(writes).toEqual([
				["PATCH", "/api/v1/projects/prj_old", { archived: false }],
			]),
		);
		await waitFor(() =>
			expect(screen.queryByTestId("project-archived")).not.toBeInTheDocument(),
		);
	});

	it("未归档项目从列表直接取到，没有归档提示", async () => {
		listed = [{ ...OLD, archivedAt: undefined }];
		single = {};
		mount("/projects/prj_old");
		expect(await screen.findByTestId("body")).toHaveTextContent("legacy");
		expect(screen.queryByTestId("project-archived")).not.toBeInTheDocument();
	});

	it("404：项目不存在的一屏，不显示错误文字", async () => {
		mount("/projects/prj_gone");
		const box = await screen.findByTestId("project-not-found");
		expect(box).toHaveTextContent("项目不存在或已删除");
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
		expect(screen.getByRole("link", { name: "项目" })).toHaveAttribute(
			"href",
			"/projects",
		);
	});

	it("列表读取失败：项目不存在的一屏带错误", async () => {
		listStatus = 500;
		mount("/projects/prj_old");
		expect(await screen.findByTestId("project-not-found")).toBeInTheDocument();
		expect(screen.getByRole("alert")).toBeInTheDocument();
	});
});

describe("ProjectShell", () => {
	it("面包屑：项目 / <项目链接> / 中间段链接 / 末段是 h1", async () => {
		listed = [{ ...OLD, archivedAt: undefined }];
		mount("/projects/prj_old/threads");
		expect(
			await screen.findByRole("heading", { level: 1, name: "环境" }),
		).toBeInTheDocument();
		const nav = screen.getByRole("navigation", { name: "位置" });
		expect(nav).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "项目" })).toHaveAttribute(
			"href",
			"/projects",
		);
		expect(screen.getByRole("link", { name: /legacy/ })).toHaveAttribute(
			"href",
			"/projects/prj_old",
		);
		expect(screen.getByRole("link", { name: "设置" })).toHaveAttribute(
			"href",
			"/s",
		);
	});
});
