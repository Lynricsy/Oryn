/** 设置页「GitHub」（M14）：未连接只有创建表单；已连接四张卡片；Agent 未连接时自动化单选禁用；回调错误码显示文案。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { GitHubIdentity, GitHubStatus } from "../../api/github-queries";
import i18n from "../../i18n";
import GitHubSettings, { callbackNotice } from "./GitHubSettings";

const T = (k: string) => i18n.t(k as "common.copy");

const NOW = Date.now();

const OCTO: GitHubIdentity = {
	login: "octo",
	githubId: 1,
	avatarUrl: "",
	status: "ok",
	connectedAt: NOW - 60_000,
};

const EMPTY: GitHubStatus = {
	identities: {},
	installations: [],
	automationIdentity: "user",
	webhookUrl: "https://oryn.example/api/v1/public/github/webhook",
};

const CONNECTED: GitHubStatus = {
	app: {
		appId: 7,
		slug: "oryn-test",
		name: "Oryn test",
		htmlUrl: "https://github.com/apps/oryn-test",
		ownerLogin: "octo",
		public: false,
		installUrl: "https://github.com/apps/oryn-test/installations/new",
	},
	identities: { user: OCTO },
	installations: [
		{
			id: 11,
			accountLogin: "octo",
			accountType: "User",
			repositorySelection: "all",
			suspended: false,
		},
	],
	automationIdentity: "user",
	webhookUrl: "https://oryn.example/api/v1/public/github/webhook",
	lastDeliveryAt: NOW - 120_000,
	lastDeliverySource: "poll",
};

let calls: string[];

function Where() {
	const loc = useLocation();
	return <span data-testid="where">{loc.pathname + loc.search}</span>;
}

function renderAt(status: GitHubStatus, path = "/settings/github") {
	calls = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: Request) => {
			const u = new URL(input.url);
			calls.push(`${input.method} ${u.pathname}`);
			if (u.pathname === "/api/v1/github") return Response.json(status);
			return Response.json(status);
		}),
	);
	render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<MemoryRouter initialEntries={[path]}>
				<Routes>
					<Route
						path="/settings/github"
						element={
							<>
								<GitHubSettings />
								<Where />
							</>
						}
					/>
				</Routes>
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("GitHubSettings", () => {
	it("未连接：只有创建表单，没有安装、账号、自动化卡片", async () => {
		renderAt(EMPTY);
		expect(
			await screen.findByRole("button", { name: T("github.app.create") }),
		).toBeInTheDocument();
		expect(screen.getByTestId("github-app-create")).toBeInTheDocument();
		// webhook 占位符取自状态里的默认地址
		expect(screen.getByPlaceholderText(EMPTY.webhookUrl)).toBeInTheDocument();
		expect(screen.queryByText(T("github.installations.title"))).toBeNull();
		expect(screen.queryByText(T("github.identity.title"))).toBeNull();
		expect(screen.queryByText(T("github.automation.title"))).toBeNull();
	});

	it("已连接：四张卡片都在，显示 App 名称、安装、账号与投递来源", async () => {
		renderAt(CONNECTED);
		expect(await screen.findByTestId("github-app")).toBeInTheDocument();
		expect(screen.getByText(T("github.app.title"))).toBeInTheDocument();
		expect(
			screen.getByText(T("github.installations.title")),
		).toBeInTheDocument();
		expect(screen.getByText(T("github.identity.title"))).toBeInTheDocument();
		expect(screen.getByText(T("github.automation.title"))).toBeInTheDocument();
		expect(screen.getByRole("link", { name: /Oryn test/ })).toHaveAttribute(
			"href",
			CONNECTED.app?.htmlUrl,
		);
		expect(screen.getAllByTestId("github-installation")).toHaveLength(1);
		expect(screen.getByText(T("github.installations.all"))).toBeInTheDocument();
		expect(screen.getByText(/轮询/)).toBeInTheDocument();
		expect(screen.getByTestId("github-identity-user")).toHaveTextContent(
			"@octo",
		);
		expect(screen.getByTestId("github-identity-agent")).toHaveTextContent(
			T("github.identity.notConnected"),
		);
		expect(screen.queryByTestId("github-app-create")).toBeNull();
	});

	it("Agent 未连接：自动化单选的 Agent 项禁用，我的账号选中", async () => {
		renderAt(CONNECTED);
		const agent = await screen.findByRole("radio", {
			name: T("github.identity.agent"),
		});
		expect(agent).toBeDisabled();
		const user = screen.getByRole("radio", { name: T("github.identity.user") });
		expect(user).toBeChecked();
		expect(user).toBeEnabled();
	});

	it("Agent 已连接：Agent 项可选；令牌失效的槽位显示「需要重新授权」", async () => {
		renderAt({
			...CONNECTED,
			identities: {
				user: { ...OCTO, status: "reauth" },
				agent: {
					login: "octo-bot",
					githubId: 2,
					avatarUrl: "",
					status: "ok",
					connectedAt: NOW,
				},
			},
		});
		const agent = await screen.findByRole("radio", {
			name: T("github.identity.agent"),
		});
		expect(agent).toBeEnabled();
		expect(screen.getByTestId("github-identity-user")).toHaveTextContent(
			T("github.identity.reauth"),
		);
		expect(
			screen.getByRole("button", { name: T("github.identity.reconnect") }),
		).toBeInTheDocument();
	});

	it("带 ?error=same_account 进入：显示对应错误文案，并清掉 query", async () => {
		renderAt(CONNECTED, "/settings/github?error=same_account");
		expect(
			await screen.findByText("不能与另一个槽位是同一个账号"),
		).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent(
				/^\/settings\/github$/,
			),
		);
		// 提示在清掉 query 之后仍在
		expect(
			screen.getByText("不能与另一个槽位是同一个账号"),
		).toBeInTheDocument();
	});

	it("带 installation_id 进入：同步一次安装", async () => {
		renderAt(
			CONNECTED,
			"/settings/github?installation_id=11&setup_action=install",
		);
		await waitFor(() =>
			expect(calls).toContain("POST /api/v1/github/installations/sync"),
		);
		expect(
			calls.filter((c) => c === "POST /api/v1/github/installations/sync"),
		).toHaveLength(1);
	});
});

describe("callbackNotice", () => {
	it("错误码：已知的用专门文案，未知的用通用文案；成功类按参数区分", () => {
		expect(callbackNotice(new URLSearchParams("error=conflict"))).toEqual({
			tone: "err",
			code: "conflict",
		});
		expect(
			callbackNotice(new URLSearchParams("error=github_upstream")),
		).toEqual({ tone: "err", code: "unknown" });
		expect(callbackNotice(new URLSearchParams("created=1"))).toEqual({
			tone: "ok",
			kind: "created",
		});
		expect(callbackNotice(new URLSearchParams("connected=agent"))).toEqual({
			tone: "ok",
			kind: "connected",
			slot: "agent",
		});
		expect(callbackNotice(new URLSearchParams(""))).toBeNull();
	});
});
