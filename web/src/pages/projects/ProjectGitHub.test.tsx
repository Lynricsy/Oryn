/**
 * 项目页 GitHub 卡片（M14 / M15）：非 GitHub 仓库不渲染；未绑定不显示开关；切换开关发 PATCH `{ githubListen }`；
 * 未绑定原因（读不到 / 组织限制带申请批准链接 / SSO）、未配置 OAuth App 或我的账号未连接时的提示、已绑定时的 hook 状态徽标。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Project } from "../../api/client";
import type { GitHubStatus } from "../../api/github-queries";
import i18n from "../../i18n";
import { ProjectGitHubCard } from "./ProjectGitHub";

const T = (k: string) => i18n.t(k as "common.copy");

/** 未配置 OAuth App。 */
const STATUS: GitHubStatus = {
	identities: {},
	automationIdentity: "user",
	callbackUrl: "https://oryn.example/api/v1/public/github/oauth/callback",
	webhookBase: "https://oryn.example",
	webUrl: "https://github.com",
};

const BOUND: Project = {
	id: "prj_1",
	name: "widgets",
	repoUrl: "https://github.com/acme/widgets",
	shallowClone: false,
	snapshots: true,
	warmOrbs: 0,
	createdAt: 1,
	updatedAt: 1,
	github: {
		repoId: 42,
		fullName: "acme/widgets",
		bound: true,
		listen: { reviews: true, comments: true, ci: true },
	},
};

let patches: unknown[];

function renderCard(project: Project, status: GitHubStatus = STATUS) {
	patches = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: Request) => {
			const u = new URL(input.url);
			if (input.method === "PATCH") {
				const body = await input.json();
				patches.push(body);
				return Response.json({ ...project, github: project.github });
			}
			if (u.pathname === "/api/v1/github") return Response.json(status);
			return Response.json({});
		}),
	);
	return render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<MemoryRouter>
				<ProjectGitHubCard project={project} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("ProjectGitHubCard", () => {
	it("已绑定：显示仓库与三个开关；关掉评审回流发出 PATCH { githubListen }", async () => {
		renderCard(BOUND);
		expect(screen.getByTestId("project-github")).toHaveTextContent(
			"acme/widgets",
		);
		const reviews = screen.getByRole("checkbox", {
			name: T("github.project.listen.reviews"),
		});
		expect(reviews).toBeChecked();
		expect(
			screen.getByRole("checkbox", {
				name: T("github.project.listen.comments"),
			}),
		).toBeChecked();
		expect(
			screen.getByRole("checkbox", { name: T("github.project.listen.ci") }),
		).toBeChecked();
		fireEvent.click(reviews);
		await waitFor(() => expect(patches).toHaveLength(1));
		expect(patches[0]).toEqual({
			githubListen: { reviews: false, comments: true, ci: true },
		});
	});

	it("未绑定、还没配置 OAuth App：不显示三个开关，提示并链接到设置页", async () => {
		renderCard({
			...BOUND,
			github: {
				...(BOUND.github as NonNullable<Project["github"]>),
				bound: false,
			},
		});
		expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
		expect(
			await screen.findByText(T("github.project.unbound")),
		).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: T("github.project.connect") }),
		).toHaveAttribute("href", "/settings/github");
	});

	it("GitHub 地址但从未解析出仓库：同样链接到设置页", async () => {
		const { github: _g, ...plain } = BOUND;
		renderCard(plain);
		expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
		expect(
			await screen.findByRole("link", { name: T("github.project.connect") }),
		).toHaveAttribute("href", "/settings/github");
	});

	it("仓库地址不是 GitHub：不渲染", () => {
		const { github: _g, ...plain } = BOUND;
		const { container } = renderCard({
			...plain,
			repoUrl: "https://gitlab.com/acme/widgets",
		});
		expect(container).toBeEmptyDOMElement();
	});

	describe("M15", () => {
		const OAUTH: GitHubStatus = {
			...STATUS,
			oauthApp: { clientId: "Ov23liTEST" },
		};
		const unbound = (
			bindStatus?: NonNullable<Project["github"]>["bindStatus"],
		): Project => ({
			...BOUND,
			github: {
				...(BOUND.github as NonNullable<Project["github"]>),
				bound: false,
				...(bindStatus ? { bindStatus } : {}),
			},
		});

		it("组织限制：写明原因，并链接到 OAuth App 的组织批准页", async () => {
			renderCard(unbound("org_restricted"), OAUTH);
			const box = screen.getByTestId("project-github-unbound");
			expect(box).toHaveAttribute("data-reason", "org_restricted");
			expect(box).toHaveTextContent("acme/widgets");
			expect(
				await screen.findByRole("link", {
					name: T("github.hooks.requestApproval"),
				}),
			).toHaveAttribute(
				"href",
				"https://github.com/settings/connections/applications/Ov23liTEST",
			);
			expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
		});

		it("组织批准链接取 GitHub 网页基址（webUrl）", async () => {
			renderCard(unbound("org_restricted"), {
				...OAUTH,
				webUrl: "https://ghe.example/",
			});
			expect(
				await screen.findByRole("link", {
					name: T("github.hooks.requestApproval"),
				}),
			).toHaveAttribute(
				"href",
				"https://ghe.example/settings/connections/applications/Ov23liTEST",
			);
		});

		it("读不到 / 需要 SSO：只有原因，没有批准链接", async () => {
			renderCard(unbound("not_found"), OAUTH);
			expect(screen.getByTestId("project-github-unbound")).toHaveTextContent(
				T("github.project.bindStatus.not_found").replace(
					"{{repo}}",
					"acme/widgets",
				),
			);
			await screen.findByTestId("project-github");
			expect(screen.queryByRole("link")).toBeNull();
		});

		it("SSO：原因文案", () => {
			renderCard(unbound("sso_required"), OAUTH);
			expect(screen.getByTestId("project-github-unbound")).toHaveAttribute(
				"data-reason",
				"sso_required",
			);
		});

		it("配置了 OAuth App、我的账号未连接：提示连接并链接到设置页", async () => {
			renderCard(unbound(), OAUTH);
			expect(
				await screen.findByText(T("github.project.unboundNoUser")),
			).toBeInTheDocument();
			expect(
				screen.getByRole("link", { name: T("github.project.connect") }),
			).toHaveAttribute("href", "/settings/github");
		});

		it("已绑定：显示 hook 状态徽标；不是 active 时附说明", () => {
			renderCard({
				...BOUND,
				github: {
					...(BOUND.github as NonNullable<Project["github"]>),
					hookStatus: "no_admin",
				},
			});
			const hook = screen.getByTestId("project-github-hook");
			expect(hook).toHaveAttribute("data-status", "no_admin");
			expect(hook).toHaveTextContent(T("github.hooks.status.no_admin"));
			expect(
				screen.getByText(T("github.hooks.explain.no_admin")),
			).toBeInTheDocument();
		});

		it("已绑定、hook 正常：只有徽标，没有额外说明", () => {
			renderCard({
				...BOUND,
				github: {
					...(BOUND.github as NonNullable<Project["github"]>),
					hookStatus: "active",
				},
			});
			expect(screen.getByTestId("project-github-hook")).toHaveTextContent(
				T("github.hooks.status.active"),
			);
			expect(screen.queryByText(T("github.hooks.explain.active"))).toBeNull();
		});
	});
});
