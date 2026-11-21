/**
 * 项目分区各页测试共用的桩（M24）：两个项目、setup 记录与 orb 工厂；按路径记账的 fetch 桩（项目、已归档项目、
 * 线程、用量统计、项目记忆开关、项目快照与 Agent 配置、仓库镜像状态……）；与 App 里项目路由一致的 `mount`。
 * 用例改 `kit` 上的字段准备数据，`resetKit()`（放在 beforeEach）还原默认值并装上 fetch 桩。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { vi } from "vitest";
import type { Orb, Project, Schemas, Thread } from "../../api/client";
import type { UsageStat, UsageStats } from "../../api/stats-queries";
import { ProjectCreatePage } from "./ProjectCreatePage";
import { ProjectHomePage } from "./ProjectHomePage";
import { ProjectsOverviewPage } from "./ProjectsOverviewPage";
import { ProjectThreadsPage } from "./ProjectThreadsPage";
import { BranchesPage } from "./repo/BranchesPage";
import { CommitsPage } from "./repo/CommitsPage";
import { FilesPage } from "./repo/FilesPage";
import { TagsPage } from "./repo/TagsPage";
import { ProjectSettingsPage } from "./settings/ProjectSettingsPage";

export interface Write {
	method: string;
	path: string;
	body: unknown;
}

export const ORYN: Project = {
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
export const CORE: Project = {
	id: "prj_b",
	name: "core",
	shallowClone: true,
	snapshots: true,
	warmOrbs: 0,
	createdAt: 2,
	updatedAt: 2,
};
export const LEGACY: Project = {
	id: "prj_old",
	name: "legacy",
	description: "旧官网",
	shallowClone: false,
	snapshots: true,
	warmOrbs: 0,
	createdAt: 0,
	updatedAt: 5,
	archivedAt: 5,
};

export const SETUP: Schemas["OrbSetupRun"] = {
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

export function orb(over: Partial<Orb>): Orb {
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

/** 用量统计的一项（只关心回合数，其余为 0）。 */
export function usageStat(key: string, turns = 0): UsageStat {
	return {
		key,
		requests: 0,
		requestErrors: 0,
		inputTokens: 0,
		outputTokens: 0,
		cacheReadTokens: 0,
		cacheWriteTokens: 0,
		costUsd: 0,
		toolCalls: 0,
		toolErrors: 0,
		turns,
	};
}

/** 按项目分组、带按天序列的用量：`byProject` 是项目 id → 每天的回合数（各项目等长）。 */
export function usageByProject(
	byProject: Record<string, readonly number[]>,
): UsageStats {
	const days = Math.max(0, ...Object.values(byProject).map((v) => v.length));
	const sum = (xs: readonly number[]) => xs.reduce((a, b) => a + b, 0);
	return {
		groupBy: "project",
		from: 0,
		to: days * 86_400_000,
		tz: "UTC",
		total: usageStat("total", sum(Object.values(byProject).flat())),
		groups: Object.entries(byProject).map(([k, v]) => usageStat(k, sum(v))),
		series: Array.from({ length: days }, (_, i) => ({
			key: `d${i}`,
			start: i * 86_400_000,
			total: usageStat("total"),
			groups: Object.entries(byProject)
				.filter(([, v]) => (v[i] ?? 0) > 0)
				.map(([k, v]) => usageStat(k, v[i])),
		})),
	};
}

const NO_REPO: Schemas["ProjectRepoStatus"] = {
	kind: "none",
	state: "absent",
	available: true,
	syncing: false,
	empty: false,
};

/** 桩的数据与记账；`resetKit` 还原。 */
export const kit = {
	/** 未归档项目（`GET /projects`） */
	projects: [] as Project[],
	/** 已归档项目（`GET /projects?archived=true`） */
	archived: [] as Project[],
	orbs: [] as Orb[],
	/** `GET /threads`：按 `archived`、`project` 过滤 */
	threads: [] as Thread[],
	snapshots: [] as Schemas["ProjectSnapshot"][],
	/** 项目级 Agent 配置：项目 id → 路径 → 内容 */
	agentFiles: {} as Record<string, Record<string, string>>,
	usage: usageByProject({}) as UsageStats,
	memoryProjects: [] as Schemas["MemoryProjectToggle"][],
	/** 非 GET 请求（Agent 配置的 PUT 体是原文） */
	writes: [] as Write[],
	/** 全部 GET 请求的 path + search */
	reads: [] as string[],
	/** `GET /projects/<id>/repo`（缺省：没有仓库，主页只显示一条说明） */
	repoStatus: NO_REPO as Schemas["ProjectRepoStatus"],
};

/** 项目级 Agent 配置（M12.2）：按项目存扁平文件；PUT 回带一条 possible_secret 警告。 */
async function projectAgentConfig(
	req: Request,
	id: string,
	rel: string | undefined,
): Promise<Response> {
	const files = kit.agentFiles[id] ?? {};
	kit.agentFiles[id] = files;
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
		kit.writes.push({ method: "PUT", path: new URL(req.url).pathname, body });
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

export async function handle(req: Request): Promise<Response> {
	const url = new URL(req.url, "http://x");
	const path = url.pathname;
	const q = url.searchParams;
	const cfg =
		/^\/api\/v1\/projects\/([^/]+)\/agent-config\/files(?:\/(.+))?$/.exec(path);
	if (cfg) return projectAgentConfig(req, cfg[1] ?? "", cfg[2]);
	const text = req.method === "GET" ? "" : await req.text();
	const body = text ? JSON.parse(text) : undefined;
	if (/^\/api\/v1\/projects\/[^/]+\/repo$/.test(path))
		return Response.json(kit.repoStatus);
	if (req.method === "GET") kit.reads.push(path + url.search);
	else kit.writes.push({ method: req.method, path, body });
	if (path === "/api/v1/projects") {
		if (req.method === "POST") {
			const p: Project = { ...body, id: "prj_new", createdAt: 9, updatedAt: 9 };
			kit.projects = [...kit.projects, p];
			return Response.json(p, { status: 201 });
		}
		return Response.json({
			projects: q.get("archived") === "true" ? kit.archived : kit.projects,
		});
	}
	const snaps = /^\/api\/v1\/projects\/([^/]+)\/snapshots$/.exec(path);
	if (snaps) {
		if (req.method !== "DELETE")
			return Response.json({ snapshots: kit.snapshots });
		kit.snapshots = kit.snapshots.map((s) =>
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
			kit.projects = kit.projects.filter((p) => p.id !== id);
			return new Response(null, { status: 204 });
		}
		const cur = [...kit.projects, ...kit.archived].find((p) => p.id === id);
		if (!cur) return Response.json({ code: "not_found" }, { status: 404 });
		if (req.method === "GET") return Response.json(cur);
		// PATCH：archived 是动作不是字段，换成 archivedAt 并在两个列表间挪动；其余字段照抄
		const { archived, ...patch } = body ?? {};
		const next: Project = { ...cur, ...patch, updatedAt: cur.updatedAt + 1 };
		if (archived === true) next.archivedAt = next.updatedAt;
		if (archived === false) delete next.archivedAt;
		const swap = (xs: Project[]) => xs.map((p) => (p.id === id ? next : p));
		const drop = (xs: Project[]) => xs.filter((p) => p.id !== id);
		if (archived === undefined) {
			kit.projects = swap(kit.projects);
			kit.archived = swap(kit.archived);
		} else if (next.archivedAt) {
			kit.projects = drop(kit.projects);
			kit.archived = [...drop(kit.archived), next];
		} else {
			kit.archived = drop(kit.archived);
			kit.projects = [...drop(kit.projects), next];
		}
		return Response.json(next);
	}
	if (path === "/api/v1/threads") {
		const archived = q.get("archived") === "true";
		const project = q.get("project");
		return Response.json({
			threads: kit.threads.filter(
				(t) =>
					Boolean(t.archivedAt) === archived &&
					(project === null ||
						(project === "none" ? !t.projectId : t.projectId === project)),
			),
		});
	}
	if (path === "/api/v1/stats/usage") return Response.json(kit.usage);
	if (path === "/api/v1/memory/projects")
		return Response.json({ items: kit.memoryProjects });
	const mem = /^\/api\/v1\/memory\/projects\/([^/]+)$/.exec(path);
	if (mem && req.method === "PUT") {
		const cur = kit.memoryProjects.find((m) => m.projectId === mem[1]);
		if (!cur) return Response.json({ code: "not_found" }, { status: 404 });
		const next = { ...cur, enabled: Boolean(body?.enabled), overridden: true };
		kit.memoryProjects = kit.memoryProjects.map((m) =>
			m.projectId === next.projectId ? next : m,
		);
		return Response.json(next);
	}
	if (path === "/api/v1/orbs") return Response.json({ orbs: kit.orbs });
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

/** 还原桩数据并装上 fetch 桩（beforeEach 里调用；afterEach 里 `vi.unstubAllGlobals()`）。 */
export function resetKit(): void {
	kit.projects = [ORYN, CORE];
	kit.archived = [];
	kit.orbs = [];
	kit.threads = [];
	kit.snapshots = [];
	kit.agentFiles = {};
	kit.usage = usageByProject({});
	kit.memoryProjects = [];
	kit.writes = [];
	kit.reads = [];
	kit.repoStatus = NO_REPO;
	vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) =>
		handle(
			input instanceof Request
				? input
				: new Request(new URL(String(input), "http://x"), init),
		),
	);
}

/** 当前地址（pathname + search）。 */
export function Where(): ReactNode {
	const loc = useLocation();
	return <span data-testid="where">{loc.pathname + loc.search}</span>;
}

/** 与 App 里项目分区一致的路由表。 */
export function mount(path: string) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter initialEntries={[path]}>
				<Routes>
					<Route path="/projects" element={<ProjectsOverviewPage />} />
					<Route path="/projects/new" element={<ProjectCreatePage />} />
					<Route path="/projects/:projectId" element={<ProjectHomePage />} />
					<Route
						path="/projects/:projectId/threads"
						element={<ProjectThreadsPage />}
					/>
					<Route
						path="/projects/:projectId/commits"
						element={<CommitsPage />}
					/>
					<Route
						path="/projects/:projectId/commits/:sha"
						element={<CommitsPage />}
					/>
					<Route
						path="/projects/:projectId/branches"
						element={<BranchesPage />}
					/>
					<Route path="/projects/:projectId/tags" element={<TagsPage />} />
					<Route path="/projects/:projectId/files/*" element={<FilesPage />} />
					<Route
						path="/projects/:projectId/settings"
						element={<ProjectSettingsPage />}
					/>
					<Route
						path="/projects/:projectId/settings/:tab"
						element={<ProjectSettingsPage />}
					/>
				</Routes>
				<Where />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

export const where = () => screen.getByTestId("where");
export const writesOnly = () =>
	kit.writes.map((w) => [w.method, w.path, w.body]);
