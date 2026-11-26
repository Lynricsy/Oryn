/**
 * 设置 → Git 与 GitHub（M14 / M15）：未配置 OAuth App 时只有「提交者」与 OAuth App 一行；配置后再有 GitHub 账号
 * （含自动化用哪个账号）、仓库 webhook、审计。OAuth App 对话框的回调地址（取服务端给的值）与保存 / 删除请求；
 * 账号的 scope 与缺失项；hook 状态与申请批准链接；审计表格的过滤与分页；回调错误码显示文案。
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
import { answerConfirm } from "../../test/confirm";
import { callbackNotice } from "./GitHubSettings";
import GitSettings from "./GitSettings";

const T = (k: string) => i18n.t(k as "common.copy");

const NOW = Date.now();

const OCTO: GitHubIdentity = {
	login: "octo",
	githubId: 1,
	avatarUrl: "",
	status: "ok",
	connectedAt: NOW - 60_000,
};

/** 未配置 OAuth App：回调地址与 webhook 基址仍由服务端给出。 */
const EMPTY: GitHubStatus = {
	identities: {},
	automationIdentity: "user",
	callbackUrl: "https://public.example/api/v1/public/github/oauth/callback",
	webhookBase: "https://oryn.example",
	webUrl: "https://github.com",
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

/** OAuth App 已配置、我的账号已连接、Agent 账号未连接。 */
const CONNECTED: GitHubStatus = {
	...EMPTY,
	oauthApp: { clientId: "Ov23liTEST" },
	identities: { user: OCTO },
	lastDeliveryAt: NOW - 120_000,
	lastDeliverySource: "poll",
};

/** OAuth App 已配置、两个身份都已连接（Agent 缺两个 scope）。 */
const OAUTH: GitHubStatus = {
	...CONNECTED,
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

function renderAt(status: GitHubStatus, path = "/settings/git") {
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
						path="/settings/git"
						element={
							<>
								<GitSettings />
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

const region = (name: string) => screen.getByRole("region", { name });

/** OAuth App 分组（等状态加载完、行出现后再取：加载中的占位分组随后会被换掉）。 */
async function oauthGroup(): Promise<HTMLElement> {
	await screen.findByTestId("github-oauth-row");
	return region(T("github.oauth.title"));
}

/** 打开 OAuth App 对话框（未配置时是「配置」，已配置时是「编辑」）。 */
async function openOAuthDialog(button: string): Promise<HTMLElement> {
	const group = await oauthGroup();
	fireEvent.click(within(group).getByRole("button", { name: button }));
	return screen.findByTestId("github-oauth-app");
}

describe("Git 与 GitHub", () => {
	it("未配置 OAuth App：提交者 + OAuth App 一行「配置」，不请求 hook 与审计", async () => {
		renderAt(EMPTY);
		const oauth = await oauthGroup();
		expect(oauth).toHaveTextContent(T("github.oauth.notConfigured"));
		expect(
			within(oauth).getByRole("button", { name: T("github.oauth.setup") }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("region", { name: T("gitIdentity.title") }),
		).toBeInTheDocument();
		expect(screen.queryByText(T("github.identity.title"))).toBeNull();
		expect(screen.queryByText(T("github.hooks.title"))).toBeNull();
		expect(screen.queryByText(T("github.audit.title"))).toBeNull();
		expect(calls.some((c) => c.includes("/github/hooks"))).toBe(false);
		expect(calls.some((c) => c.includes("/github/audit"))).toBe(false);
	});

	it("已配置：提交者、OAuth App、GitHub 账号（含自动化）、webhook、审计各一组，标题不再用「身份」", async () => {
		renderAt(CONNECTED);
		const accounts = await screen.findByRole("region", {
			name: T("github.identity.title"),
		});
		const titles = screen
			.getAllByRole("region")
			.map((r) => r.querySelector("h3")?.textContent);
		expect(titles).toEqual([
			"提交者",
			"OAuth App",
			"GitHub 账号",
			"仓库 webhook",
			"审计",
		]);
		expect(region("OAuth App")).toHaveTextContent("Ov23liTEST");
		// 自动化是账号分组里的一行
		expect(
			within(accounts).getByRole("combobox", {
				name: T("github.automation.title"),
			}),
		).toBeInTheDocument();
		expect(screen.getByTestId("github-last-delivery")).toHaveTextContent(
			/轮询/,
		);
		expect(screen.getByTestId("github-identity-user")).toHaveTextContent(
			"@octo",
		);
		expect(
			screen.getByTestId("github-identity-agent").closest("li"),
		).toHaveTextContent(T("github.identity.notConnected"));
	});

	it("Agent 未连接：自动化下拉里 Agent 账号不可选，当前为我的账号", async () => {
		renderAt(CONNECTED);
		const select = await screen.findByRole("combobox", {
			name: T("github.automation.title"),
		});
		expect(select).toHaveTextContent(T("github.identity.user"));
		expect(select.closest("li")).toHaveTextContent(
			T("github.automation.agentMissing"),
		);
		fireEvent.click(select);
		expect(
			screen.getByRole("option", { name: T("github.identity.agent") }),
		).toHaveAttribute("aria-disabled", "true");
	});

	it("Agent 已连接：选 Agent 账号发 PUT；令牌失效的槽位显示「需要重新授权」", async () => {
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
		const select = await screen.findByRole("combobox", {
			name: T("github.automation.title"),
		});
		fireEvent.click(select);
		fireEvent.click(
			screen.getByRole("option", { name: T("github.identity.agent") }),
		);
		await waitFor(() => expect(sent).toHaveLength(1));
		expect(sent[0]?.method).toBe("PUT");
		expect(screen.getByTestId("github-identity-user")).toHaveTextContent(
			T("github.identity.reauth"),
		);
		expect(
			screen.getByRole("button", { name: T("github.identity.reconnect") }),
		).toBeInTheDocument();
	});

	it("账号 ⋯ → 断开：确认后发 DELETE", async () => {
		renderAt(CONNECTED);
		fireEvent.click(
			await screen.findByRole("button", { name: "我的账号的更多操作" }),
		);
		fireEvent.click(
			await screen.findByRole("menuitem", { name: "断开我的账号" }),
		);
		expect((await answerConfirm(true)).title).toBe("断开我的账号 @octo？");
		await waitFor(() =>
			expect(sent.some((s) => s.method === "DELETE")).toBe(true),
		);
	});

	it("带 ?error=same_account 进入：显示对应错误文案，并清掉 query", async () => {
		renderAt(CONNECTED, "/settings/git?error=same_account");
		expect(
			await screen.findByText("不能与另一个槽位是同一个账号"),
		).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent(
				/^\/settings\/git$/,
			),
		);
		// 提示在清掉 query 之后仍在
		expect(
			screen.getByText("不能与另一个槽位是同一个账号"),
		).toBeInTheDocument();
	});

	it("带 ?connected=agent 进入：显示连接成功", async () => {
		renderAt(CONNECTED, "/settings/git?connected=agent");
		expect(await screen.findByTestId("github-notice")).toHaveTextContent(
			"Agent 账号已连接",
		);
	});
});

describe("Git 与 GitHub · OAuth App（M15）", () => {
	it("首次配置：回调地址取服务端给的值；Secret 必填；保存发 PUT 全部字段后关闭对话框", async () => {
		renderAt(EMPTY);
		const form = await openOAuthDialog(T("github.oauth.setup"));
		const dialog = screen.getByRole("dialog", {
			name: T("github.oauth.setupTitle"),
		});
		expect(screen.getByTestId("github-oauth-callback")).toHaveTextContent(
			EMPTY.callbackUrl,
		);
		expect(
			within(form).getByRole("link", { name: T("github.oauth.register") }),
		).toHaveAttribute("href", "https://github.com/settings/applications/new");
		expect(
			within(form).getByRole("button", {
				name: T("github.oauth.copyCallback"),
			}),
		).toBeInTheDocument();
		// webhook 基址默认填好
		expect(
			within(form).getByRole("textbox", { name: /Webhook 基址/ }),
		).toHaveValue("https://oryn.example");
		const save = within(dialog).getByRole("button", { name: T("common.save") });
		expect(save).toBeDisabled();
		fireEvent.change(within(form).getByRole("textbox", { name: "Client ID" }), {
			target: { value: " Ov23liNEW " },
		});
		// 只有 Client ID 还不够：首次保存 Secret 必填
		expect(save).toBeDisabled();
		fireEvent.change(within(form).getByLabelText("Client Secret"), {
			target: { value: "s3cret" },
		});
		expect(save).toBeEnabled();
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
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	it("已配置：Secret 留空时不发送；回调地址取服务端的值", async () => {
		renderAt(OAUTH);
		const form = await openOAuthDialog(T("common.edit"));
		expect(screen.getByTestId("github-oauth-callback")).toHaveTextContent(
			OAUTH.callbackUrl,
		);
		expect(within(form).getByText(T("github.oauth.secretKeep"))).toBeVisible();
		const save = within(screen.getByRole("dialog")).getByRole("button", {
			name: T("common.save"),
		});
		expect(save).toBeEnabled();
		fireEvent.click(save);
		await waitFor(() => expect(sent).toHaveLength(1));
		expect(sent[0]?.body).toEqual({
			clientId: "Ov23liTEST",
			webhookBase: "https://hooks.example",
		});
	});

	it("⋯ → 删除配置：确认文案说明会删 hook、断开账号；取消不发请求，确认后发 DELETE", async () => {
		renderAt(OAUTH);
		const group = await oauthGroup();
		const remove = async () => {
			fireEvent.click(
				await within(group).findByRole("button", {
					name: T("github.oauth.more"),
				}),
			);
			fireEvent.click(
				await screen.findByRole("menuitem", { name: T("github.oauth.delete") }),
			);
		};
		await remove();
		const request = await answerConfirm(false);
		expect(request.title).toBe(T("github.oauth.deleteConfirm"));
		expect(request.body).toMatch(/webhook/);
		expect(sent).toHaveLength(0);
		await remove();
		await answerConfirm(true);
		await waitFor(() =>
			expect(sent).toEqual([
				{ method: "DELETE", path: "/api/v1/github/oauth-app", body: null },
			]),
		);
	});

	it("账号：显示授予的 scope；缺 scope 时警告并给出重新连接", async () => {
		renderAt(OAUTH);
		const user = (await screen.findByTestId("github-identity-user")).closest(
			"li",
		) as HTMLElement;
		expect(within(user).getByTestId("github-scopes-user")).toHaveTextContent(
			"admin:repo_hook",
		);
		expect(within(user).queryByRole("alert")).toBeNull();
		expect(
			within(user).queryByRole("button", {
				name: T("github.identity.reconnect"),
			}),
		).toBeNull();
		const agent = screen
			.getByTestId("github-identity-agent")
			.closest("li") as HTMLElement;
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

	it("hook：按仓库列出状态与最近投递；非正常状态展开说明，组织限制给出申请批准链接", async () => {
		renderAt(OAUTH);
		const rows = (await screen.findAllByTestId("github-hook")).map(
			(r) => r.closest("li") as HTMLElement,
		);
		expect(rows).toHaveLength(2);
		const [active, restricted] = rows as [HTMLElement, HTMLElement];
		expect(active).toHaveTextContent("acme/widgets");
		expect(active).toHaveTextContent(T("github.hooks.status.active"));
		expect(active).toHaveTextContent(/实时/);
		expect(active).not.toHaveTextContent(T("github.hooks.explain.active"));
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
	it("错误码：已知的用专门文案，未知的用通用文案；连接成功带槽位；其余 query 忽略", () => {
		expect(
			callbackNotice(new URLSearchParams("error=github_not_configured")),
		).toEqual({ tone: "err", code: "github_not_configured" });
		expect(
			callbackNotice(new URLSearchParams("error=github_upstream")),
		).toEqual({ tone: "err", code: "unknown" });
		expect(callbackNotice(new URLSearchParams("connected=agent"))).toEqual({
			tone: "ok",
			slot: "agent",
		});
		// M14 的 App 回调参数不再有意义
		expect(callbackNotice(new URLSearchParams("created=1"))).toBeNull();
		expect(
			callbackNotice(new URLSearchParams("installation_id=11")),
		).toBeNull();
		expect(callbackNotice(new URLSearchParams(""))).toBeNull();
	});
});
