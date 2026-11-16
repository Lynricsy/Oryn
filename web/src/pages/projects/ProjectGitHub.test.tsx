/** 项目页 GitHub 卡片（M14）：非 GitHub 仓库不渲染；未绑定不显示开关；切换开关发 PATCH `{ githubListen }`。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Project } from "../../api/client";
import type { GitHubStatus } from "../../api/github-queries";
import i18n from "../../i18n";
import { ProjectGitHubCard } from "./ProjectGitHub";

const T = (k: string) => i18n.t(k as "common.copy");

const STATUS: GitHubStatus = {
	app: {
		appId: 7,
		slug: "oryn-test",
		name: "Oryn test",
		htmlUrl: "https://github.com/apps/oryn-test",
		ownerLogin: "octo",
		public: false,
		installUrl: "https://github.com/apps/oryn-test/installations/new",
	},
	identities: {},
	installations: [],
	automationIdentity: "user",
	webhookUrl: "https://oryn.example/api/v1/public/github/webhook",
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

	it("未绑定：不显示三个开关，给出安装 App 的链接", async () => {
		renderCard({
			...BOUND,
			github: {
				...(BOUND.github as NonNullable<Project["github"]>),
				bound: false,
			},
		});
		expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
		expect(screen.getByText(T("github.project.unbound"))).toBeInTheDocument();
		expect(
			await screen.findByRole("link", { name: T("github.project.install") }),
		).toHaveAttribute("href", STATUS.app?.installUrl);
	});

	it("GitHub 地址但从未解析出仓库、也还没有 App：链接到设置页", async () => {
		const { github: _g, ...plain } = BOUND;
		renderCard(plain, { ...STATUS, app: undefined });
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
});
