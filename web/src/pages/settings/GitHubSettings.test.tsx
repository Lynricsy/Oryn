/**
 * 设置页「GitHub」（M14 / M15）：未连接只有 OAuth App 与 App 创建表单；App 已连接时四张卡片；Agent 未连接时自动化单选禁用；
 * 回调错误码显示文案。M15：OAuth App 卡片的回调地址与保存 / 删除请求，配置后隐藏 App 卡片；身份的 scope 与缺失项；
 * hook 状态与申请批准链接；审计表格的过滤与分页。
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
import { afterEach, describe, expect, it, vi } from "vitest";
import type {
	GitHubAuditEntry,
	GitHubHook,
	GitHubIdentity,
	GitHubStatus,
} from "../../api/github-queries";
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
	webhookBase: "https://oryn.example",
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

const SCOPES = [
	"repo",
	"workflow",
	"read:org",
	"admin:repo_hook",
	"gist",
	"write:packages",
	"user:email",
];

/** OAuth App 已配置（旧 App 字段仍在：后端 8.1 之前两者并存）。 */
const OAUTH: GitHubStatus = {
	...CONNECTED,
	oauthApp: {
		clientId: "Ov23liTEST",
		callbackUrl: "https://oryn.example/api/v1/public/github/oauth/callback",
	},
	webhookBase: "https://hooks.example",
	identities: {
		user: { ...OCTO, scopes: SCOPES, missingScopes: [] },
		agent: {
			login: "octo-bot",
			githubId: 2,
			avatarUrl: "",
			status: "ok",
			connectedAt: NOW,
			scopes: SCOPES.slice(0, 4),
			missingScopes: ["gist", "write:packages"],
		},
	},
};

const HOOKS: GitHubHook[] = [
	{
		repoId: 42,
		fullName: "acme/widgets",
		hookId: 9,
		status: "active",
		lastDeliveryAt: NOW - 60_000,
		lastDeliverySource: "webhook",
		updatedAt: NOW,
	},
	{
		repoId: 43,
		fullName: "bigcorp/locked",
		status: "org_restricted",
		updatedAt: NOW,
	},
];

function auditEntry(id: number, repo = "acme/widgets"): GitHubAuditEntry {
	return {
		id,
		at: NOW - id * 60_000,
		threadId: "T-1",
		slot: "agent",
		method: "POST",
		op: "/repos/{owner}/{repo}/issues/{issue_number}/comments",
		repo,
		status: id === 120 ? 403 : 201,
		...(id === 120 ? { gate: "denied: no prepare record" } : {}),
	};
}

let calls: string[];
let sent: { method: string; path: string; body: unknown }[];

function Where() {
	const loc = useLocation();
	return <span data-testid="where">{loc.pathname + loc.search}</span>;
}

function renderAt(status: GitHubStatus, path = "/settings/github") {
	calls = [];
	sent = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: Request) => {
			const u = new URL(input.url);
			calls.push(`${input.method} ${u.pathname}${u.search}`);
			if (input.method === "PUT" || input.method === "DELETE") {
				const body = input.method === "PUT" ? await input.json() : null;
				sent.push({ method: input.method, path: u.pathname, body });
				return input.method === "DELETE"
					? new Response(null, { status: 204 })
					: Response.json(status);
			}
			if (u.pathname === "/api/v1/github/hooks")
				return Response.json({ hooks: HOOKS });
			if (u.pathname === "/api/v1/github/audit") {
				const before = u.searchParams.get("before");
				const repo = u.searchParams.get("repo");
				if (repo) return Response.json({ entries: [auditEntry(7, repo)] });
				return before
					? Response.json({ entries: [auditEntry(99), auditEntry(98)] })
					: Response.json({
							entries: [auditEntry(120), auditEntry(100)],
							nextBefore: 100,
						});
			}
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
	it("未连接：OAuth App 卡片与 App 创建表单，没有安装、账号、自动化卡片", async () => {
		renderAt(EMPTY);
		expect(
			await screen.findByRole("button", { name: T("github.app.create") }),
		).toBeInTheDocument();
		expect(screen.getByTestId("github-app-create")).toBeInTheDocument();
		expect(screen.getByTestId("github-oauth-app")).toBeInTheDocument();
		// webhook 占位符取自状态里的默认地址
		expect(screen.getByPlaceholderText(EMPTY.webhookUrl)).toBeInTheDocument();
		expect(screen.queryByText(T("github.installations.title"))).toBeNull();
		expect(screen.queryByText(T("github.identity.title"))).toBeNull();
		expect(screen.queryByText(T("github.automation.title"))).toBeNull();
		expect(screen.queryByText(T("github.hooks.title"))).toBeNull();
		expect(screen.queryByText(T("github.audit.title"))).toBeNull();
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
		expect(
			within(screen.getByTestId("github-app")).getByText(/轮询/),
		).toBeInTheDocument();
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

describe("GitHubSettings · OAuth App（M15）", () => {
	it("首次配置：回调地址由 webhook 基址推出；Secret 必填；保存发 PUT 全部字段", async () => {
		renderAt(EMPTY);
		const card = await screen.findByTestId("github-oauth-app");
		expect(screen.getByTestId("github-oauth-callback")).toHaveTextContent(
			"https://oryn.example/api/v1/public/github/oauth/callback",
		);
		expect(
			within(card).getByRole("button", {
				name: T("github.oauth.copyCallback"),
			}),
		).toBeInTheDocument();
		// webhook 基址默认填好
		expect(
			within(card).getByRole("textbox", { name: /Webhook 基址/ }),
		).toHaveValue("https://oryn.example");
		const save = within(card).getByRole("button", { name: T("common.save") });
		expect(save).toBeDisabled();
		fireEvent.change(within(card).getByRole("textbox", { name: "Client ID" }), {
			target: { value: " Ov23liNEW " },
		});
		// 只有 Client ID 还不够：首次保存 Secret 必填
		expect(save).toBeDisabled();
		fireEvent.change(within(card).getByLabelText("Client Secret"), {
			target: { value: "s3cret" },
		});
		expect(save).toBeEnabled();
		// 首次配置没有删除按钮
		expect(
			within(card).queryByRole("button", { name: T("github.oauth.delete") }),
		).toBeNull();
		fireEvent.click(save);
		await waitFor(() => expect(sent).toHaveLength(1));
		expect(sent[0]).toEqual({
			method: "PUT",
			path: "/api/v1/github/oauth-app",
			body: {
				clientId: "Ov23liNEW",
				clientSecret: "s3cret",
				webhookBase: "https://oryn.example",
			},
		});
		expect(await within(card).findByRole("status")).toHaveTextContent(
			T("github.oauth.saved"),
		);
	});

	it("已配置：隐藏 App 创建 / 安装卡片；Secret 留空时不发送；回调地址取服务端的值", async () => {
		renderAt(OAUTH);
		const card = await screen.findByTestId("github-oauth-app");
		expect(screen.queryByTestId("github-app")).toBeNull();
		expect(screen.queryByTestId("github-app-create")).toBeNull();
		expect(screen.queryByText(T("github.app.title"))).toBeNull();
		expect(screen.queryByText(T("github.installations.title"))).toBeNull();
		expect(screen.getByText(T("github.identity.title"))).toBeInTheDocument();
		expect(screen.getByText(T("github.automation.title"))).toBeInTheDocument();
		expect(screen.getByText(T("github.hooks.title"))).toBeInTheDocument();
		expect(screen.getByText(T("github.audit.title"))).toBeInTheDocument();
		expect(screen.getByTestId("github-oauth-callback")).toHaveTextContent(
			OAUTH.oauthApp?.callbackUrl ?? "",
		);
		expect(within(card).getByText(T("github.oauth.secretKeep"))).toBeVisible();
		const save = within(card).getByRole("button", { name: T("common.save") });
		expect(save).toBeEnabled();
		fireEvent.click(save);
		await waitFor(() => expect(sent).toHaveLength(1));
		expect(sent[0]?.body).toEqual({
			clientId: "Ov23liTEST",
			webhookBase: "https://hooks.example",
		});
	});

	it("删除：确认文案说明会删 hook、断开身份；取消不发请求，确认后发 DELETE", async () => {
		renderAt(OAUTH);
		const card = await screen.findByTestId("github-oauth-app");
		const confirm = vi.fn(() => false);
		vi.stubGlobal("confirm", confirm);
		const del = within(card).getByRole("button", {
			name: T("github.oauth.delete"),
		});
		fireEvent.click(del);
		expect(confirm).toHaveBeenCalledWith(T("github.oauth.deleteConfirm"));
		expect(T("github.oauth.deleteConfirm")).toMatch(/webhook/);
		expect(sent).toHaveLength(0);
		confirm.mockReturnValue(true);
		fireEvent.click(del);
		await waitFor(() =>
			expect(sent).toEqual([
				{ method: "DELETE", path: "/api/v1/github/oauth-app", body: null },
			]),
		);
	});

	it("身份：显示授予的 scope；缺 scope 时警告并给出重新连接", async () => {
		renderAt(OAUTH);
		const user = await screen.findByTestId("github-identity-user");
		expect(within(user).getByTestId("github-scopes")).toHaveTextContent(
			"admin:repo_hook",
		);
		expect(within(user).queryByRole("alert")).toBeNull();
		expect(
			within(user).queryByRole("button", {
				name: T("github.identity.reconnect"),
			}),
		).toBeNull();
		const agent = screen.getByTestId("github-identity-agent");
		expect(agent).toHaveTextContent(T("github.identity.scopesMissing"));
		expect(within(agent).getByRole("alert")).toHaveTextContent(
			"gist write:packages",
		);
		expect(
			within(agent).getByRole("button", {
				name: T("github.identity.reconnect"),
			}),
		).toBeInTheDocument();
	});

	it("hook：按仓库列出状态徽标与说明；组织限制给出申请批准链接", async () => {
		renderAt(OAUTH);
		const rows = await screen.findAllByTestId("github-hook");
		expect(rows).toHaveLength(2);
		const [active, restricted] = rows as [HTMLElement, HTMLElement];
		expect(active).toHaveTextContent("acme/widgets");
		expect(active).toHaveTextContent(T("github.hooks.status.active"));
		expect(active).toHaveTextContent(/实时/);
		expect(restricted).toHaveTextContent(
			T("github.hooks.status.org_restricted"),
		);
		expect(restricted).toHaveTextContent(
			T("github.hooks.explain.org_restricted"),
		);
		expect(
			within(restricted).getByRole("link", {
				name: T("github.hooks.requestApproval"),
			}),
		).toHaveAttribute(
			"href",
			"https://github.com/settings/connections/applications/Ov23liTEST",
		);
	});

	it("审计：表格列出记录；加载更多带 before；按仓库过滤带 repo", async () => {
		renderAt(OAUTH);
		await waitFor(() =>
			expect(screen.getAllByTestId("github-audit-row")).toHaveLength(2),
		);
		const table = screen.getByTestId("github-audit");
		expect(table).toHaveTextContent("denied: no prepare record");
		expect(within(table).getAllByRole("link")[0]).toHaveAttribute(
			"href",
			"/t/T-1",
		);
		fireEvent.click(
			screen.getByRole("button", { name: T("github.audit.more") }),
		);
		await waitFor(() =>
			expect(screen.getAllByTestId("github-audit-row")).toHaveLength(4),
		);
		expect(calls).toContain("GET /api/v1/github/audit?limit=50&before=100");
		// 最后一页没有 nextBefore：「加载更多」消失
		expect(
			screen.queryByRole("button", { name: T("github.audit.more") }),
		).toBeNull();
		fireEvent.change(
			screen.getByRole("combobox", { name: T("github.audit.repoFilter") }),
			{ target: { value: "bigcorp/locked" } },
		);
		fireEvent.click(
			screen.getByRole("button", { name: T("github.audit.filter") }),
		);
		await waitFor(() =>
			expect(screen.getAllByTestId("github-audit-row")).toHaveLength(1),
		);
		expect(calls).toContain(
			"GET /api/v1/github/audit?limit=50&repo=bigcorp%2Flocked",
		);
		expect(
			screen.getByRole("button", { name: T("github.audit.clearFilter") }),
		).toBeInTheDocument();
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
