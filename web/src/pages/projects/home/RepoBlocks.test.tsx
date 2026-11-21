/**
 * 项目主页的仓库区块（M24 阶段 ②）：对 mock 后端渲染整页。Oryn 镜像就绪 → 「提交到 main」5 行、根目录条目、
 * README 的相对链接与图片；oryn-core 克隆失败 → 只有一条说明；控制面没有 git → 只有一条说明；刷新钮发同步请求。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { projects } from "../../../mock/fixtures";
import { MockServer } from "../../../mock/server";
import { ProjectHomePage } from "../ProjectHomePage";
import { resolveRepoPath } from "../repo/paths";
import { RepoEntryList } from "./RepoEntryList";

const ORYN = projects[0]?.id ?? "";
const CORE = projects[1]?.id ?? "";

let requests: Request[];
/** 非 null 时 `GET …/repo` 返回它（覆盖 mock 后端的状态） */
let statusOverride: Record<string, unknown> | null;

beforeEach(() => {
	window.innerWidth = 1280;
	requests = [];
	statusOverride = null;
	const server = new MockServer();
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			requests.push(req.clone());
			if (statusOverride && new URL(req.url).pathname.endsWith("/repo"))
				return Response.json(statusOverride);
			return (
				(await server.handleFetch(req)) ?? new Response(null, { status: 404 })
			);
		},
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	window.innerWidth = 1024;
});

function mount(id: string) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={[`/projects/${id}`]}>
				<Routes>
					<Route path="/projects/:projectId" element={<ProjectHomePage />} />
				</Routes>
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

describe("项目主页的仓库区块", () => {
	it("镜像就绪：提交到 main 5 行、根目录条目、最新提交条与同步状态", async () => {
		mount(ORYN);
		const commits = await screen.findByTestId("repo-commits-card");
		expect(within(commits).getByText("提交到 main")).toBeInTheDocument();
		expect(
			await within(commits).findAllByTestId("repo-commit-row"),
		).toHaveLength(5);
		const entries = await screen.findAllByTestId("repo-entry");
		expect(entries.map((e) => e.dataset.path)).toEqual(
			expect.arrayContaining(["docs", "src", "README.md", ".gitignore"]),
		);
		expect(
			within(entries.find((e) => e.dataset.path === "docs") as HTMLElement)
				.getByRole("link")
				.getAttribute("href"),
		).toBe(`/projects/${ORYN}/files/docs?ref=main`);
		expect(await screen.findByTestId("repo-head-commit")).toHaveTextContent(
			"lib: greet world when the name is empty",
		);
		expect(screen.getByTestId("repo-sync-status")).toHaveTextContent("已同步");
		expect(screen.queryByTestId("repo-unavailable")).toBeNull();
	});

	it("README：相对链接进文件页（带 ref），相对图片走 blob 地址", async () => {
		mount(ORYN);
		const readme = await screen.findByTestId("project-readme");
		const guide = await within(readme).findByRole("link", { name: "指南" });
		expect(guide.getAttribute("href")).toBe(
			`/projects/${ORYN}/files/docs/guide.md?ref=main`,
		);
		expect(guide.getAttribute("target")).toBeNull();
		const img = within(readme).getByRole("img", { name: "架构图" });
		expect(img.getAttribute("src")).toBe(
			`/api/v1/projects/${ORYN}/repo/blob?ref=main&path=docs%2Fdiagram.svg`,
		);
	});

	it("克隆失败：仓库行写原因，提交 / 文件 / README 换成一条说明", async () => {
		mount(CORE);
		expect(await screen.findByTestId("repo-sync-status")).toHaveTextContent(
			"同步失败：凭据被拒绝",
		);
		const card = screen.getByTestId("repo-unavailable");
		expect(card).toHaveTextContent("仓库镜像不可用");
		expect(card).toHaveTextContent("凭据被拒绝");
		expect(screen.queryByText(/^提交到/)).toBeNull();
		expect(screen.queryByTestId("project-files")).toBeNull();
		// 「最近线程」照常
		expect(screen.getByTestId("project-recent")).toBeInTheDocument();
	});

	it("控制面没有 git：只有一条说明，没有提交与同步按钮", async () => {
		statusOverride = {
			kind: "mirror",
			state: "absent",
			available: false,
			unavailableReason: "git_missing",
			syncing: false,
			empty: false,
		};
		mount(ORYN);
		const card = await screen.findByTestId("repo-unavailable");
		expect(card).toHaveTextContent("控制面未安装 git");
		expect(screen.getAllByTestId("repo-unavailable")).toHaveLength(1);
		expect(screen.queryByText(/^提交到/)).toBeNull();
		expect(screen.queryByRole("button", { name: "立即同步" })).toBeNull();
	});

	it("仓库行的刷新钮发出同步请求", async () => {
		mount(ORYN);
		const status = await screen.findByTestId("repo-sync-status");
		fireEvent.click(within(status).getByRole("button", { name: "立即同步" }));
		await vi.waitFor(() =>
			expect(
				requests.some(
					(r) =>
						r.method === "POST" &&
						new URL(r.url).pathname === `/api/v1/projects/${ORYN}/repo/sync`,
				),
			).toBe(true),
		);
		expect(await screen.findByText("同步中…")).toBeInTheDocument();
	});
});

describe("RepoEntryList", () => {
	it("Git LFS 条目带「LFS」徽标，普通文件没有", () => {
		const file = (name: string, lfs?: { oid: string }) => ({
			name,
			path: name,
			type: "file" as const,
			mode: 420,
			size: 10,
			oid: "b".repeat(40),
			...(lfs ? { lfs } : {}),
		});
		render(
			<MemoryRouter>
				<RepoEntryList
					projectId={ORYN}
					refName="main"
					tree={{
						ref: "main",
						commit: "c".repeat(40),
						path: "",
						self: {
							name: "",
							path: "",
							type: "dir",
							mode: 493,
							size: 0,
							oid: "t".repeat(40),
						},
						entries: [file("big.bin", { oid: "a".repeat(64) }), file("a.txt")],
						truncated: false,
						lastCommitsComplete: true,
					}}
				/>
			</MemoryRouter>,
		);
		const [big, plain] = screen.getAllByTestId("repo-entry");
		expect(
			within(big as HTMLElement).getByTitle("由 Git LFS 存储"),
		).toHaveTextContent("LFS");
		expect(
			within(plain as HTMLElement).queryByTitle("由 Git LFS 存储"),
		).toBeNull();
	});
});

describe("resolveRepoPath", () => {
	it.each([
		["", "docs/guide.md", { path: "docs/guide.md", hash: "" }],
		["docs", "../README.md#top", { path: "README.md", hash: "#top" }],
		["docs", "./a%20b.md?raw=1", { path: "docs/a b.md", hash: "" }],
		["docs/x", "/src/main.go", { path: "src/main.go", hash: "" }],
		["", "../outside.md", null],
		["", "bad%zz", null],
	])("%s + %s", (base, rel, want) => {
		expect(resolveRepoPath(base, rel)).toEqual(want);
	});
});
