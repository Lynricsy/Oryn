/** 路由：鉴权跳转；项目分区（M9.6）的路由与 `/settings/projects` 的重定向；渲染异常由顶层错误边界接住。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import type * as ReactRouter from "react-router";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";

vi.mock("./realtime/socket", () => ({
	realtime: {
		connect: vi.fn(),
		close: vi.fn(),
		send: vi.fn(),
		onSchedulesChanged: vi.fn(() => () => undefined),
		onGitHubChanged: vi.fn(() => () => undefined),
		onSshChanged: vi.fn(() => () => undefined),
		onBoxesChanged: vi.fn(() => () => undefined),
		onArtifactsChanged: vi.fn(() => () => undefined),
		onArtifactConsent: vi.fn(() => () => undefined),
		onArtifactWatchesChanged: vi.fn(() => () => undefined),
	},
}));

// 各页面本身另有测试；这里只看路由落到哪个页面、带什么参数
vi.mock("./pages/projects/ProjectsOverviewPage", () => ({
	ProjectsOverviewPage: () => <p data-testid="page">INDEX</p>,
}));
vi.mock("./pages/projects/ProjectCreatePage", () => ({
	ProjectCreatePage: () => <p data-testid="page">CREATE</p>,
}));
vi.mock("./pages/projects/ProjectThreadsPage", async () => {
	const { useParams } =
		await vi.importActual<typeof ReactRouter>("react-router");
	return {
		ProjectThreadsPage: () => (
			<p data-testid="page">{`THREADS ${useParams().projectId}`}</p>
		),
	};
});
vi.mock("./pages/projects/ProjectHomePage", async () => {
	const { useParams } =
		await vi.importActual<typeof ReactRouter>("react-router");
	return {
		ProjectHomePage: () => {
			const { projectId } = useParams();
			// 模拟页面渲染期抛错（M12.1 走查问题 1：确认条卸载时 TypeError 整页白屏）
			if (projectId === "boom") throw new TypeError("l is not a function");
			return <p data-testid="page">{`HOME ${projectId}`}</p>;
		},
	};
});
vi.mock("./pages/projects/settings/ProjectSettingsPage", async () => {
	const { useParams } =
		await vi.importActual<typeof ReactRouter>("react-router");
	return {
		ProjectSettingsPage: () => {
			const { projectId, tab } = useParams();
			return <p data-testid="page">{`SETTINGS ${projectId} ${tab ?? "-"}`}</p>;
		},
	};
});

function Where() {
	const loc = useLocation();
	return <span data-testid="where">{loc.pathname}</span>;
}

function renderAt(path: string) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter initialEntries={[path]}>
				<App />
				<Routes>
					<Route path="*" element={<Where />} />
				</Routes>
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

/** 已登录：`me` 有身份，其余接口给空列表 / 空对象。 */
function signedIn() {
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL) => {
			const url = new URL(
				input instanceof Request ? input.url : String(input),
				"http://x",
			);
			if (url.pathname === "/api/v1/me")
				return Response.json({
					displayName: "Ada",
					createdAt: 1,
					auth: { kind: "web" },
				});
			if (url.pathname === "/api/v1/projects")
				return Response.json({ projects: [] });
			if (url.pathname === "/api/v1/threads")
				return Response.json({ threads: [] });
			return Response.json({});
		}),
	);
}

function signedOut() {
	vi.stubGlobal(
		"fetch",
		vi.fn(async () =>
			Response.json(
				{ code: "unauthorized", message: "no session" },
				{ status: 401 },
			),
		),
	);
}

describe("路由与鉴权", () => {
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("未登录访问首页时跳转登录页", async () => {
		signedOut();
		renderAt("/");
		expect(
			await screen.findByRole("heading", { name: "登录" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "使用 Passkey 登录" }),
		).toBeInTheDocument();
	});

	it("配对页要求先登录", async () => {
		signedOut();
		renderAt("/pair");
		expect(await screen.findByText("请先登录再批准设备。")).toBeInTheDocument();
	});
});

describe("项目分区的路由（M9.6）", () => {
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("/settings/projects 重定向到 /projects", async () => {
		signedIn();
		renderAt("/settings/projects");
		expect(await screen.findByTestId("page")).toHaveTextContent("INDEX");
		expect(screen.getByTestId("where")).toHaveTextContent(/^\/projects$/);
	});

	it("/projects/new 是新建页，不被 /projects/:projectId 抢走", async () => {
		signedIn();
		renderAt("/projects/new");
		expect(await screen.findByTestId("page")).toHaveTextContent("CREATE");
	});

	it("/projects/<id> 是项目主页并带上 id", async () => {
		signedIn();
		renderAt("/projects/prj_1");
		expect(await screen.findByTestId("page")).toHaveTextContent("HOME prj_1");
	});

	it("/projects/<id>/settings[/<分组>] 是项目设置并带上分组", async () => {
		signedIn();
		renderAt("/projects/prj_1/settings");
		expect(await screen.findByTestId("page")).toHaveTextContent(
			"SETTINGS prj_1 -",
		);
	});

	it("/projects/<id>/settings/github 带上分组", async () => {
		signedIn();
		renderAt("/projects/prj_1/settings/github");
		expect(await screen.findByTestId("page")).toHaveTextContent(
			"SETTINGS prj_1 github",
		);
	});

	it("/projects/<id>/threads 是线程子页并带上 id", async () => {
		signedIn();
		renderAt("/projects/prj_1/threads");
		expect(await screen.findByTestId("page")).toHaveTextContent(
			"THREADS prj_1",
		);
	});

	it("/settings 仍重定向到设备页", async () => {
		signedIn();
		renderAt("/settings");
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent(
				"/settings/devices",
			),
		);
	});
});

describe("顶层错误边界", () => {
	afterEach(() => {
		vi.unstubAllGlobals();
		vi.restoreAllMocks();
	});

	it("页面渲染抛错：不白屏，显示简短说明、错误原文与「重新加载」", async () => {
		// React 会把被边界接住的错误打到控制台；这里不让它刷屏
		vi.spyOn(console, "error").mockImplementation(() => undefined);
		signedIn();
		renderAt("/projects/boom");
		const alert = await screen.findByRole("alert");
		expect(screen.getByRole("heading", { name: "界面出错了" })).toBeVisible();
		expect(alert).toHaveTextContent("重新加载即可恢复");
		expect(alert).toHaveTextContent("l is not a function");
		expect(
			screen.getByRole("button", { name: "重新加载" }),
		).toBeInTheDocument();
	});
});
