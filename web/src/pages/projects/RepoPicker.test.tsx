/** 项目表单的「从 GitHub 选择」（M14）：选中填入仓库地址与默认分支（已有默认分支不覆盖）；我的账号未连接时不出现。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { GitHubIdentity, GitHubStatus } from "../../api/github-queries";
import i18n from "../../i18n";
import {
	EMPTY_PROJECT,
	ProjectEditor,
	type ProjectForm,
} from "./ProjectEditor";
import type { GitHubRepo } from "./RepoPicker";

const T = (k: string) => i18n.t(k as "common.copy");

const OCTO: GitHubIdentity = {
	login: "octo",
	githubId: 1,
	avatarUrl: "",
	status: "ok",
	connectedAt: 1,
};

const STATUS: GitHubStatus = {
	oauthApp: { clientId: "Ov23liTEST" },
	identities: { user: OCTO },
	automationIdentity: "user",
	callbackUrl: "https://oryn.example/api/v1/public/github/oauth/callback",
	webhookBase: "https://oryn.example",
	webUrl: "https://github.com",
};

const REPOS: GitHubRepo[] = [
	{
		id: 42,
		fullName: "acme/widgets",
		private: true,
		defaultBranch: "trunk",
		pushedAt: 2,
	},
	{ id: 43, fullName: "acme/docs", private: false, defaultBranch: "main" },
];

let repoQueries: string[];

function renderEditor(status: GitHubStatus, initial: ProjectForm) {
	repoQueries = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: Request) => {
			const u = new URL(input.url);
			if (u.pathname === "/api/v1/github") return Response.json(status);
			if (u.pathname === "/api/v1/github/repos") {
				const q = u.searchParams.get("q") ?? "";
				repoQueries.push(q);
				return Response.json({
					repos: REPOS.filter((r) => r.fullName.includes(q)),
				});
			}
			if (u.pathname === "/api/v1/providers")
				return Response.json({ providers: [] });
			if (u.pathname === "/api/v1/browser-profiles")
				return Response.json({ profiles: [] });
			return Response.json({});
		}),
	);
	render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<ProjectEditor
				section="create"
				initial={initial}
				onSaved={() => undefined}
				onCancel={() => undefined}
			/>
		</QueryClientProvider>,
	);
}

const repoInput = () =>
	screen.getByPlaceholderText("https://github.com/…") as HTMLInputElement;
const branchInput = () =>
	screen.getByPlaceholderText("main") as HTMLInputElement;

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

describe("RepoPicker", () => {
	it("选中仓库：填入仓库地址与默认分支", async () => {
		renderEditor(STATUS, EMPTY_PROJECT);
		fireEvent.click(
			await screen.findByRole("button", {
				name: T("github.repoPicker.button"),
			}),
		);
		fireEvent.click(
			await screen.findByRole("button", { name: /acme\/widgets/ }),
		);
		expect(repoInput().value).toBe("https://github.com/acme/widgets");
		expect(branchInput().value).toBe("trunk");
		// 选中即关闭弹层
		expect(screen.queryByTestId("repo-picker")).toBeNull();
	});

	it("已有默认分支时不覆盖", async () => {
		renderEditor(STATUS, { ...EMPTY_PROJECT, baseBranch: "release" });
		fireEvent.click(
			await screen.findByRole("button", {
				name: T("github.repoPicker.button"),
			}),
		);
		fireEvent.click(await screen.findByRole("button", { name: /acme\/docs/ }));
		expect(repoInput().value).toBe("https://github.com/acme/docs");
		expect(branchInput().value).toBe("release");
	});

	it("搜索词防抖后作为 q 发出，私有仓库带私有徽标", async () => {
		renderEditor(STATUS, EMPTY_PROJECT);
		fireEvent.click(
			await screen.findByRole("button", {
				name: T("github.repoPicker.button"),
			}),
		);
		expect(
			await screen.findByRole("button", { name: /acme\/widgets/ }),
		).toHaveTextContent(T("github.repoPicker.private"));
		fireEvent.change(
			screen.getByRole("searchbox", { name: T("github.repoPicker.search") }),
			{ target: { value: "doc" } },
		);
		// 防抖 200ms 后才发 q=doc；结果到了 widgets 才会消失
		await waitFor(() =>
			expect(
				screen.queryByRole("button", { name: /acme\/widgets/ }),
			).toBeNull(),
		);
		expect(screen.getByRole("button", { name: /acme\/docs/ })).toBeVisible();
		expect(repoQueries).toEqual(["", "doc"]);
	});

	it("我的账号未连接：没有选择按钮", async () => {
		renderEditor({ ...STATUS, identities: {} }, EMPTY_PROJECT);
		// 等状态查询落定
		await act(async () => {
			await new Promise((r) => setTimeout(r, 50));
		});
		expect(
			screen.queryByRole("button", { name: T("github.repoPicker.button") }),
		).toBeNull();
	});

	it("我的账号需要重新授权：同样没有选择按钮", async () => {
		renderEditor(
			{
				...STATUS,
				identities: {
					user: { ...OCTO, status: "reauth" },
				},
			},
			EMPTY_PROJECT,
		);
		await act(async () => {
			await new Promise((r) => setTimeout(r, 50));
		});
		expect(
			screen.queryByRole("button", { name: T("github.repoPicker.button") }),
		).toBeNull();
	});
});
