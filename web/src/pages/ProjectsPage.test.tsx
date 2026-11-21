/**
 * 项目分区的主区（M9.6）：索引页的跳转与空状态、项目设置表单（新建 / 编辑 / 取消 / 删除）、环境配置卡、
 * 服务汇总卡与定时任务卡。fetch 换成按路径记账的桩。
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
import { answerConfirm } from "../test/confirm";
import { ProjectPage, ProjectsIndexPage } from "./ProjectsPage";

// 编辑器换成 textarea：这里测 Agent 配置卡片的接线，CodeMirror 本身另有测试
vi.mock("../components/editor/CodeEditor", () => ({
	default: ({
		value,
		path,
		onChange,
	}: {
		value: string;
		path: string;
		onChange?(v: string): void;
	}) => (
		<textarea
			aria-label={`editor:${path}`}
			value={value}
			onChange={(e) => onChange?.(e.target.value)}
		/>
	),
}));

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
	snapshots: true,
	warmOrbs: 0,
	icon: "\u{f07d3}",
	createdAt: 1,
	updatedAt: 1,
};
const CORE: Project = {
	id: "prj_b",
	name: "core",
	shallowClone: true,
	snapshots: true,
	warmOrbs: 0,
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
let snapshotList: Schemas["ProjectSnapshot"][];

let projectFiles: Record<string, Record<string, string>>;

/** 项目级 Agent 配置（M12.2）：按项目存扁平文件；PUT 回带一条 possible_secret 警告。 */
async function projectAgentConfig(
	req: Request,
	id: string,
	rel: string | undefined,
): Promise<Response> {
	const files = projectFiles[id] ?? {};
	projectFiles[id] = files;
	if (rel === undefined)
		return Response.json({
			files: Object.entries(files).map(([path, c]) => ({
				path,
				size: c.length,
				mode: 420,
				sha256: "0".repeat(64),
				updatedAt: 1,
			})),
		});
	const path = decodeURIComponent(rel);
	if (req.method === "PUT") {
		const body = await req.text();
		writes.push({ method: "PUT", path: new URL(req.url).pathname, body });
		files[path] = body;
		return Response.json({
			file: {
				path,
				size: body.length,
				mode: 420,
				sha256: "0".repeat(64),
				updatedAt: 2,
			},
			extractedSecrets: [],
			warnings: [
				{ path, line: 2, kind: "possible_secret", message: "looks like a key" },
			],
		});
	}
	const c = files[path];
	return c === undefined
		? Response.json({ code: "not_found", message: path }, { status: 404 })
		: new Response(c);
}

async function handle(req: Request): Promise<Response> {
	const url = new URL(req.url, "http://x");
	const path = url.pathname;
	const cfg =
		/^\/api\/v1\/projects\/([^/]+)\/agent-config\/files(?:\/(.+))?$/.exec(path);
	if (cfg) return projectAgentConfig(req, cfg[1] ?? "", cfg[2]);
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
	const snaps = /^\/api\/v1\/projects\/([^/]+)\/snapshots$/.exec(path);
	if (snaps) {
		if (req.method !== "DELETE")
			return Response.json({ snapshots: snapshotList });
		snapshotList = snapshotList.map((s) =>
			s.state === "ready"
				? { ...s, state: "retired", retireReason: "manual" }
				: s,
		);
		return new Response(null, { status: 204 });
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
	snapshotList = [];
	projectFiles = {};
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
					snapshots: true,
					warmOrbs: 0,
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
			[
				"POST",
				"/api/v1/projects",
				{ name: "fresh", shallowClone: false, snapshots: true, warmOrbs: 0 },
			],
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
		mount(`/projects/${ORYN.id}`);
		await screen.findByRole("heading", { level: 1, name: "oryn" });
		fireEvent.click(screen.getByRole("button", { name: "删除" }));
		expect((await answerConfirm(true)).title).toBe("删除项目 oryn？");
		await waitFor(() =>
			expect(where()).toHaveTextContent(`/projects/${CORE.id}`),
		);
		expect(writesOnly()).toEqual([
			["DELETE", `/api/v1/projects/${ORYN.id}`, undefined],
		]);
	});

	it("删除时取消确认：不发请求", async () => {
		mount(`/projects/${ORYN.id}`);
		await screen.findByRole("heading", { level: 1, name: "oryn" });
		fireEvent.click(screen.getByRole("button", { name: "删除" }));
		await answerConfirm(false);
		expect(writes).toEqual([]);
		expect(where()).toHaveTextContent(`/projects/${ORYN.id}`);
	});

	it("不存在的 id：提示并给回项目列表的链接", async () => {
		mount("/projects/prj_gone");
		const box = await screen.findByTestId("project-not-found");
		expect(box).toHaveTextContent(i18n.t("projectsPage.notFound"));
		expect(within(box).getByRole("link")).toHaveAttribute("href", "/projects");
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

const SNAP = (
	over: Partial<Schemas["ProjectSnapshot"]> &
		Pick<Schemas["ProjectSnapshot"], "id" | "state">,
): Schemas["ProjectSnapshot"] => ({
	providerId: "prov_1",
	providerName: "local",
	type: "medium",
	configHash: "h",
	depth: 0,
	lineageAt: Date.now() - 2 * 3_600_000,
	createdAt: 1,
	useCount: 0,
	...over,
});

describe("ProjectPage 项目快照与预热（M11a）", () => {
	it("快照开关：关掉后保存，PATCH 带 snapshots=false", async () => {
		mount(`/projects/${ORYN.id}`);
		const toggle = await screen.findByRole("checkbox", {
			name: i18n.t("accel.snapshotsToggle"),
		});
		expect(toggle).toBeChecked();
		fireEvent.click(toggle);
		expect(toggle).not.toBeChecked();
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(writes).toHaveLength(1));
		expect(writes[0]?.body).toEqual(
			expect.objectContaining({ snapshots: false, warmOrbs: 0 }),
		);
	});

	it("预热步进器：0–3，到上限后「增加」禁用，保存带 warmOrbs", async () => {
		projectList = [{ ...ORYN, warmOrbs: 2 }, CORE];
		mount(`/projects/${ORYN.id}`);
		const value = await screen.findByTestId("warm-orbs-value");
		const inc = screen.getByRole("button", { name: i18n.t("accel.warmInc") });
		const dec = screen.getByRole("button", { name: i18n.t("accel.warmDec") });
		expect(value).toHaveTextContent("2");
		// 下限：减到 0 后「减少」禁用，再点不变
		for (let i = 0; i < 3; i++) fireEvent.click(dec);
		expect(value).toHaveTextContent("0");
		expect(dec).toBeDisabled();
		// 上限：加到 3 后「增加」禁用，再点不变
		for (let i = 0; i < 3; i++) fireEvent.click(inc);
		expect(value).toHaveTextContent("3");
		expect(inc).toBeDisabled();
		fireEvent.click(inc);
		expect(value).toHaveTextContent("3");
		// 说明文字写明全局上限与常驻占用内存
		const hint = screen.getByText(i18n.t("accel.warmHint"));
		expect(hint).toHaveTextContent("pool.max_total");
		expect(hint).toHaveTextContent("常驻占用内存");
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(writes).toHaveLength(1));
		expect(writes[0]?.body).toEqual(expect.objectContaining({ warmOrbs: 3 }));
	});

	it("快照表：三种状态、提交前 7 位、使用次数与退役原因", async () => {
		snapshotList = [
			SNAP({ id: "s-b", state: "building", type: "large" }),
			SNAP({
				id: "s-r",
				state: "ready",
				baseCommit: "abc1234def5678",
				useCount: 7,
				sizeBytes: 1_240_000_000,
			}),
			SNAP({
				id: "s-x",
				state: "retired",
				type: "small",
				retireReason: "superseded",
			}),
		];
		mount(`/projects/${ORYN.id}`);
		const table = await screen.findByTestId("snapshot-list");
		const row = (id: string) =>
			table.querySelector(`[data-snapshot="${id}"]`) as HTMLElement;
		expect(row("s-b")).toHaveTextContent("建立中");
		expect(row("s-r")).toHaveTextContent("可用");
		expect(row("s-r")).toHaveTextContent("abc1234");
		expect(row("s-r")).not.toHaveTextContent("abc1234d");
		expect(row("s-r")).toHaveTextContent("1.2 GB");
		expect(row("s-r")).toHaveTextContent("7");
		expect(row("s-r")).toHaveTextContent("2h 0m");
		expect(row("s-x")).toHaveTextContent("已退役");
		expect(row("s-x")).toHaveTextContent("已被新快照取代");
	});

	it("清除快照：取消确认不发请求；确认后 DELETE 并刷新列表", async () => {
		snapshotList = [
			SNAP({ id: "s-r", state: "ready", baseCommit: "abc1234def" }),
		];
		mount(`/projects/${ORYN.id}`);
		const clear = await screen.findByRole("button", {
			name: i18n.t("accel.clear"),
		});
		fireEvent.click(clear);
		expect((await answerConfirm(false)).title).toBe(
			i18n.t("accel.clearConfirm", { name: ORYN.name }),
		);
		expect(writes).toEqual([]);
		fireEvent.click(clear);
		await answerConfirm(true);
		await waitFor(() => expect(writes).toHaveLength(1));
		expect(writesOnly()).toEqual([
			["DELETE", `/api/v1/projects/${ORYN.id}/snapshots`, undefined],
		]);
		// 没有可用快照了：按钮消失，行变成「已退役 · 手动清除」
		await waitFor(() =>
			expect(
				screen.queryByRole("button", { name: i18n.t("accel.clear") }),
			).toBeNull(),
		);
		expect(screen.getByTestId("snapshot-list")).toHaveTextContent("手动清除");
	});

	it("预热 orb 列表：池 orb 带「预热」徽标与 Docker 徽标；非池 orb 不列", async () => {
		orbList = [
			orb({
				id: "orb-pool",
				threadIds: [],
				pool: "idle",
				state: "frozen",
				nestedDocker: true,
			}),
			orb({ id: "orb-work" }),
		];
		mount(`/projects/${ORYN.id}`);
		const card = await screen.findByTestId("project-accel");
		await waitFor(() =>
			expect(card.querySelector('[data-pool-orb="orb-pool"]')).not.toBeNull(),
		);
		const row = card.querySelector('[data-pool-orb="orb-pool"]') as HTMLElement;
		expect(row).toHaveTextContent(i18n.t("accel.warm"));
		expect(row).toHaveTextContent("Docker");
		expect(row).toHaveTextContent(i18n.t("accel.pool.idle"));
		expect(card.querySelector('[data-pool-orb="orb-work"]')).toBeNull();
	});

	it("没有预热 orb：开了预热时说明暂不满足条件", async () => {
		projectList = [{ ...ORYN, warmOrbs: 1 }, CORE];
		mount(`/projects/${ORYN.id}`);
		expect(await screen.findByTestId("pool-empty")).toHaveTextContent(
			i18n.t("accel.pool.pending"),
		);
		expect(await screen.findByTestId("snapshots-empty")).toBeInTheDocument();
	});
});

describe("ProjectPage Agent 配置（M12.2）", () => {
	const agentCard = async () =>
		(
			await screen.findByRole("heading", {
				name: i18n.t("projectsPage.agentConfig.title"),
			})
		).closest("section") as HTMLElement;

	it("列出该项目的配置；编辑保存走项目接口，写入警告逐条显示在卡片下", async () => {
		projectFiles = {
			[ORYN.id]: { "mcp.json": "{}\n", "rules/a.md": "a\n" },
			[CORE.id]: { "AGENTS.md": "core\n" },
		};
		mount(`/projects/${ORYN.id}`);
		const card = await agentCard();
		expect(card).toHaveTextContent(i18n.t("projectsPage.agentConfig.hint"));
		expect(card).toHaveTextContent(
			i18n.t("projectsPage.agentConfig.secretHint"),
		);
		expect(card).toHaveTextContent(i18n.t("projectsPage.agentConfig.pushHint"));
		const scope = within(card);
		expect(
			await scope.findByRole("tab", {
				name: i18n.t("files.roots.projectAgentConfig"),
			}),
		).toBeInTheDocument();
		expect(await scope.findByText("rules")).toBeInTheDocument();
		expect(scope.queryByText("AGENTS.md")).toBeNull();
		fireEvent.click(scope.getByText("mcp.json"));
		const editor = await scope.findByLabelText("editor:mcp.json");
		expect(editor).toHaveValue("{}\n");
		fireEvent.change(editor, {
			target: { value: '{\n  "token": "Bearer ghp_x"\n}\n' },
		});
		fireEvent.click(scope.getByRole("button", { name: "保存" }));
		expect(
			await scope.findByText("mcp.json:2 — looks like a key"),
		).toBeInTheDocument();
		expect(writesOnly()).toEqual([
			[
				"PUT",
				`/api/v1/projects/${ORYN.id}/agent-config/files/mcp.json`,
				'{\n  "token": "Bearer ghp_x"\n}\n',
			],
		]);
	});

	it("新建项目页没有 Agent 配置卡片", () => {
		mount("/projects/new");
		expect(
			screen.queryByRole("heading", {
				name: i18n.t("projectsPage.agentConfig.title"),
			}),
		).toBeNull();
	});
});
