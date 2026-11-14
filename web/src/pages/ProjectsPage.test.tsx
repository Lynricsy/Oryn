/**
 * 项目分区的主区（M9.6）：索引页的跳转与空状态、项目设置表单（新建 / 编辑 / 取消 / 删除）、环境配置卡、
 * 服务汇总卡、定时任务卡与「查看线程」。fetch 换成按路径记账的桩。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Orb, Project, Schemas, Thread } from "../api/client";
import i18n from "../i18n";
import { indexStore } from "../state/stores";
import { uiStore } from "../state/ui";
import { ProjectPage, ProjectsIndexPage } from "./ProjectsPage";

interface Write {
	method: string;
	path: string;
	body: unknown;
}

const ORYN: Project = {
	id: "prj_a",
	name: "oryn",
	repoUrl: "https://github.com/x/oryn",
	shallowClone: false,
	icon: "\u{f07d3}",
	createdAt: 1,
	updatedAt: 1,
};
const CORE: Project = {
	id: "prj_b",
	name: "core",
	shallowClone: true,
	createdAt: 2,
	updatedAt: 2,
};

const SETUP: Schemas["OrbSetupRun"] = {
	id: "run_1",
	orbId: "orb-1",
	phase: "setup",
	trigger: "auto",
	startedAt: 1_000,
	finishedAt: 61_000,
	ok: true,
	setupRan: true,
	durationMs: 60_000,
	hasRepoScript: true,
	hasProjectScript: false,
};

function orb(over: Partial<Orb>): Orb {
	return {
		id: "orb-1",
		providerId: "prov_1",
		type: "medium",
		cpus: 4,
		memoryMB: 8192,
		image: "oryn-orb:dev",
		state: "ready",
		stateChangedAt: 10,
		createdAt: 1,
		threadIds: ["T-1"],
		projectId: ORYN.id,
		...over,
	};
}

let projectList: Project[];
let orbList: Orb[];
let writes: Write[];

async function handle(req: Request): Promise<Response> {
	const url = new URL(req.url, "http://x");
	const path = url.pathname;
	const text = req.method === "GET" ? "" : await req.text();
	const body = text ? JSON.parse(text) : undefined;
	if (req.method !== "GET") writes.push({ method: req.method, path, body });
	if (path === "/api/v1/projects") {
		if (req.method === "POST") {
			const p: Project = { ...body, id: "prj_new", createdAt: 9, updatedAt: 9 };
			projectList = [...projectList, p];
			return Response.json(p, { status: 201 });
		}
		return Response.json({ projects: projectList });
	}
	const one = /^\/api\/v1\/projects\/([^/]+)$/.exec(path);
	if (one) {
		const id = one[1];
		if (req.method === "DELETE") {
			projectList = projectList.filter((p) => p.id !== id);
			return new Response(null, { status: 204 });
		}
		const cur = projectList.find((p) => p.id === id);
		if (!cur) return Response.json({ code: "not_found" }, { status: 404 });
		const next = { ...cur, ...body, updatedAt: cur.updatedAt + 1 };
		projectList = projectList.map((p) => (p.id === id ? next : p));
		return Response.json(next);
	}
	if (path === "/api/v1/orbs") return Response.json({ orbs: orbList });
	if (path === "/api/v1/schedules") return Response.json({ schedules: [] });
	if (path === "/api/v1/providers") return Response.json({ providers: [] });
	if (path === "/api/v1/browser-profiles")
		return Response.json({
			profiles: [
				{
					id: "bpf_A",
					name: "工作账号",
					revision: 1,
					sizeBytes: 1,
					createdAt: 1,
					updatedAt: 1,
				},
			],
		});
	return Response.json({ code: "not_found", message: path }, { status: 404 });
}

function Where() {
	return <span data-testid="where">{useLocation().pathname}</span>;
}

function mount(path: string) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter initialEntries={[path]}>
				<Routes>
					<Route path="/" element={<p>THREADS</p>} />
					<Route path="/projects" element={<ProjectsIndexPage />} />
					<Route path="/projects/new" element={<ProjectPage create />} />
					<Route path="/projects/:projectId" element={<ProjectPage />} />
				</Routes>
				<Where />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const where = () => screen.getByTestId("where");
const writesOnly = () => writes.map((w) => [w.method, w.path, w.body]);

beforeEach(() => {
	projectList = [ORYN, CORE];
	orbList = [];
	writes = [];
	uiStore.getState().setProjectFilter(undefined);
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
	vi.restoreAllMocks();
	indexStore.getState().setAll([], 0);
});

describe("ProjectsIndexPage", () => {
	it("有未归档项目：跳到第一个", async () => {
		projectList = [{ ...ORYN, archivedAt: 5 }, CORE];
		mount("/projects");
		await waitFor(() =>
			expect(where()).toHaveTextContent(`/projects/${CORE.id}`),
		);
		expect(
			await screen.findByRole("heading", { level: 1, name: "core" }),
		).toBeInTheDocument();
	});

	it("没有项目：空状态，主按钮去新建", async () => {
		projectList = [];
		mount("/projects");
		const empty = await screen.findByTestId("projects-empty");
		expect(empty).toHaveTextContent(i18n.t("projectsPage.empty"));
		fireEvent.click(within(empty).getByRole("button", { name: "新建项目" }));
		expect(where()).toHaveTextContent("/projects/new");
	});
});

describe("ProjectPage 项目设置", () => {
	it("编辑后保存：PATCH 体照旧（空串 = 清除），留在本页", async () => {
		mount(`/projects/${ORYN.id}`);
		const name = await screen.findByLabelText("名称");
		fireEvent.change(name, { target: { value: "oryn2" } });
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(writes).toHaveLength(1));
		expect(writesOnly()).toEqual([
			[
				"PATCH",
				`/api/v1/projects/${ORYN.id}`,
				{
					name: "oryn2",
					repoUrl: "https://github.com/x/oryn",
					baseBranch: "",
					defaultProvider: "",
					defaultOrbType: "",
					setupScript: "",
					resumeScript: "",
					shallowClone: false,
					color: "",
					icon: "\u{f07d3}",
					gitAuthorName: "",
					gitAuthorEmail: "",
					defaultBrowserProfileId: "",
				},
			],
		]);
		expect(where()).toHaveTextContent(`/projects/${ORYN.id}`);
		expect(
			await screen.findByRole("heading", { level: 1, name: "oryn2" }),
		).toBeInTheDocument();
	});

	it("编辑时取消：表单还原为服务端值，不发请求", async () => {
		mount(`/projects/${ORYN.id}`);
		const name = await screen.findByLabelText("名称");
		fireEvent.change(name, { target: { value: "scratch" } });
		fireEvent.click(screen.getByRole("button", { name: "取消" }));
		expect(screen.getByLabelText("名称")).toHaveValue("oryn");
		expect(where()).toHaveTextContent(`/projects/${ORYN.id}`);
		expect(writes).toEqual([]);
	});

	it("新建成功后跳到新项目；只有设置卡", async () => {
		mount("/projects/new");
		expect(screen.queryByText("环境配置")).toBeNull();
		fireEvent.change(screen.getByLabelText("名称"), {
			target: { value: "fresh" },
		});
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(where()).toHaveTextContent("/projects/prj_new"));
		expect(writesOnly()).toEqual([
			["POST", "/api/v1/projects", { name: "fresh", shallowClone: false }],
		]);
		expect(
			await screen.findByRole("heading", { level: 1, name: "fresh" }),
		).toBeInTheDocument();
	});

	it("新建时取消：回 /projects", async () => {
		mount("/projects/new");
		fireEvent.click(screen.getByRole("button", { name: "取消" }));
		await waitFor(() =>
			expect(where()).toHaveTextContent(`/projects/${ORYN.id}`),
		);
		expect(writes).toEqual([]);
	});

	it("选字形后保存发送该字形；选「自动」发送空串清除", async () => {
		mount(`/projects/${ORYN.id}`);
		const grid = await screen.findByRole("group", {
			name: i18n.t("projects.icon"),
		});
		expect(
			within(grid).getByRole("button", { name: "language go" }),
		).toHaveAttribute("aria-pressed", "true");
		fireEvent.click(
			within(grid).getByRole("button", { name: "language rust" }),
		);
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(writes).toHaveLength(1));
		fireEvent.click(
			await screen.findByRole("button", { name: i18n.t("projects.iconAuto") }),
		);
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(writes).toHaveLength(2));
		expect(writes.map((w) => w.body)).toEqual([
			expect.objectContaining({ icon: "\u{f1617}" }),
			expect.objectContaining({ icon: "" }),
		]);
	});

	it("默认浏览器 Profile：选中后保存带它的 id", async () => {
		mount(`/projects/${ORYN.id}`);
		await screen.findByRole("option", { name: "工作账号" });
		fireEvent.change(
			screen.getByRole("combobox", {
				name: new RegExp(`^${i18n.t("projects.defaultBrowserProfile")}`),
			}),
			{ target: { value: "bpf_A" } },
		);
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(writes).toHaveLength(1));
		expect(writes[0]?.body).toEqual(
			expect.objectContaining({ defaultBrowserProfileId: "bpf_A" }),
		);
	});

	it("删除：确认后 DELETE 并回 /projects（再落到剩下的项目）", async () => {
		vi.spyOn(window, "confirm").mockReturnValue(true);
		mount(`/projects/${ORYN.id}`);
		await screen.findByRole("heading", { level: 1, name: "oryn" });
		fireEvent.click(screen.getByRole("button", { name: "删除" }));
		await waitFor(() =>
			expect(where()).toHaveTextContent(`/projects/${CORE.id}`),
		);
		expect(window.confirm).toHaveBeenCalledWith("删除项目 oryn？");
		expect(writesOnly()).toEqual([
			["DELETE", `/api/v1/projects/${ORYN.id}`, undefined],
		]);
	});

	it("删除时取消确认：不发请求", async () => {
		vi.spyOn(window, "confirm").mockReturnValue(false);
		mount(`/projects/${ORYN.id}`);
		await screen.findByRole("heading", { level: 1, name: "oryn" });
		fireEvent.click(screen.getByRole("button", { name: "删除" }));
		expect(writes).toEqual([]);
		expect(where()).toHaveTextContent(`/projects/${ORYN.id}`);
	});

	it("不存在的 id：提示并给回项目列表的链接", async () => {
		mount("/projects/prj_gone");
		const box = await screen.findByTestId("project-not-found");
		expect(box).toHaveTextContent(i18n.t("projectsPage.notFound"));
		expect(within(box).getByRole("link")).toHaveAttribute("href", "/projects");
	});

	it("「查看线程」设侧栏过滤并回线程分区", async () => {
		mount(`/projects/${ORYN.id}`);
		fireEvent.click(await screen.findByRole("button", { name: "查看线程" }));
		expect(uiStore.getState().projectFilter).toBe(ORYN.id);
		expect(where()).toHaveTextContent(/^\/$/);
		expect(screen.getByText("THREADS")).toBeInTheDocument();
	});

	it("定时任务卡：渲染该项目的定时列表", async () => {
		mount(`/projects/${ORYN.id}`);
		const box = await screen.findByTestId(`project-schedules-${ORYN.id}`);
		expect(
			await within(box).findByText(i18n.t("projects.noSchedules")),
		).toBeInTheDocument();
	});
});

describe("ProjectPage 环境配置与服务", () => {
	it("没有记录：两张卡都是空状态", async () => {
		mount(`/projects/${ORYN.id}`);
		expect(await screen.findByTestId("project-env-empty")).toHaveTextContent(
			i18n.t("projectsPage.noSetupRun"),
		);
		expect(screen.getByTestId("project-services-empty")).toHaveTextContent(
			i18n.t("projectsPage.noServices"),
		);
	});

	it("最近一次 setup：结果、耗时、触发方式、仓库内脚本与打开线程；服务一行带状态与所属线程", async () => {
		indexStore.getState().setAll(
			[
				{
					id: "T-2",
					title: "跑通 demo",
					kind: "work",
				} as unknown as Thread,
			],
			1,
		);
		orbList = [
			orb({
				id: "orb-old",
				threadIds: ["T-9"],
				lastSetup: { ...SETUP, id: "run_0", finishedAt: 5, ok: false },
			}),
			orb({
				id: "orb-1",
				threadIds: ["T-1", "T-2"],
				lastSetup: SETUP,
				services: [
					{
						name: "demo",
						source: "dynamic",
						command: "python3 -m http.server",
						status: "running",
						restarts: 0,
						links: [],
						threadId: "T-2",
					},
				],
			}),
			// 别的项目的 orb 不算（它的 setup 更晚、还失败了，服务也不该出现）
			orb({
				id: "orb-x",
				projectId: CORE.id,
				threadIds: ["T-x"],
				lastSetup: { ...SETUP, id: "run_x", finishedAt: 99_000, ok: false },
				services: [
					{
						name: "other",
						source: "file",
						command: "x",
						status: "failed",
						restarts: 0,
						links: [],
					},
				],
			}),
		];
		mount(`/projects/${ORYN.id}`);
		const env = await screen.findByTestId("project-env");
		expect(env).toHaveTextContent("成功");
		expect(env).toHaveTextContent("自动");
		expect(env).toHaveTextContent(
			i18n.t("env.repoScript", { value: i18n.t("env.repoScriptYes") }),
		);
		expect(within(env).getByRole("link", { name: "打开线程" })).toHaveAttribute(
			"href",
			"/t/T-1",
		);
		const rows = await screen.findAllByTestId("project-service");
		expect(rows).toHaveLength(1);
		expect(rows[0]).toHaveTextContent("demo");
		expect(rows[0]).toHaveTextContent("运行中");
		expect(within(rows[0] as HTMLElement).getByRole("link")).toHaveAttribute(
			"href",
			"/t/T-2",
		);
		expect(rows[0]).toHaveTextContent("跑通 demo");
		// 只读：没有启停按钮
		expect(within(rows[0] as HTMLElement).queryByRole("button")).toBeNull();
	});
});
