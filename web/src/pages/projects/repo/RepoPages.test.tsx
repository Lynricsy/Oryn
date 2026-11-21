/**
 * 仓库子页（M24 阶段 ②）：对 mock 后端渲染。提交页宽屏左列表右详情、窄屏只有详情；详情里展开文件出 diff；
 * 分支页带「默认」；文件页列目录、文件用只读的 FileViewer；改 ref 写进 `?ref=`；镜像不可读时只有一条说明。
 * Git LFS：能预览的带说明、超过预览上限的只能下载（download=1）、取对象失败的有原因与重试。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { projects } from "../../../mock/fixtures";
import { MockServer } from "../../../mock/server";
import { Where } from "../projects-test-kit";
import { BranchesPage } from "./BranchesPage";
import { CommitsPage } from "./CommitsPage";
import { FilesPage } from "./FilesPage";
import { TagsPage } from "./TagsPage";

const download = vi.hoisted(() => vi.fn());
vi.mock("../../../lib/artifacts/download", () => ({ downloadUrl: download }));

const ORYN = projects[0]?.id ?? "";
const CORE = projects[1]?.id ?? "";

let server: MockServer;
/** 发给 mock 后端的请求地址 */
let urls: URL[];

beforeEach(() => {
	window.innerWidth = 1280;
	server = new MockServer();
	urls = [];
	download.mockReset();
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			urls.push(new URL(req.url));
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

function mount(path: string) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={[path]}>
				<Routes>
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
				</Routes>
				<Where />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

/** mock 仓库基线上的提交（新到旧）。 */
async function mainCommits(): Promise<{ sha: string; subject: string }[]> {
	const res = await server.handleFetch(
		new Request(
			new URL(`/api/v1/projects/${ORYN}/repo/commits`, location.origin),
		),
	);
	const body: { commits: { sha: string; subject: string }[] } =
		await res?.json();
	return body.commits;
}

describe("提交子页", () => {
	it("宽屏：列表与详情同时在，选中的行高亮", async () => {
		const [head] = await mainCommits();
		mount(`/projects/${ORYN}/commits/${head?.sha}`);
		const detail = await screen.findByTestId("repo-commit-detail");
		expect(
			await within(detail).findByRole("heading", { name: head?.subject }),
		).toBeInTheDocument();
		const list = await screen.findByTestId("repo-commit-list");
		expect(within(list).getAllByTestId("repo-commit-row")).toHaveLength(7);
		expect(
			within(list).getByRole("link", { current: "page" }),
		).toHaveTextContent(head?.subject ?? "");
		expect(
			within(detail).queryByRole("button", { name: "返回" }),
		).not.toBeInTheDocument();
	});

	it("宽屏没选提交时右侧是提示；点行进详情，链接保留 ?ref=", async () => {
		mount(`/projects/${ORYN}/commits?ref=feature%2Frepo-browser`);
		expect(await screen.findByText("选择一个提交查看改动")).toBeInTheDocument();
		const rows = await screen.findAllByTestId("repo-commit-row");
		expect(rows).toHaveLength(8);
		const first = rows[0] as HTMLElement;
		fireEvent.click(within(first).getAllByRole("link")[0] as HTMLElement);
		await waitFor(() =>
			expect(screen.getByTestId("where").textContent).toBe(
				`/projects/${ORYN}/commits/${first.dataset.sha}?ref=feature%2Frepo-browser`,
			),
		);
		expect(await screen.findByTestId("repo-commit-detail")).toBeInTheDocument();
	});

	it("窄屏只有详情，返回钮回到列表", async () => {
		window.innerWidth = 390;
		const [head] = await mainCommits();
		mount(`/projects/${ORYN}/commits/${head?.sha}`);
		const detail = await screen.findByTestId("repo-commit-detail");
		expect(screen.queryByTestId("repo-commit-list")).not.toBeInTheDocument();
		fireEvent.click(within(detail).getByRole("button", { name: "返回" }));
		await waitFor(() =>
			expect(screen.getByTestId("where").textContent).toBe(
				`/projects/${ORYN}/commits`,
			),
		);
		expect(await screen.findByTestId("repo-commit-list")).toBeInTheDocument();
		expect(screen.queryByTestId("repo-commit-detail")).not.toBeInTheDocument();
	});

	it("详情：正文、关联线程、父提交；文件默认展开出 diff，点标题收起", async () => {
		const commits = await mainCommits();
		const at = commits.findIndex(
			(x) => x.subject === "lib: greet world when the name is empty",
		);
		const c = commits[at];
		mount(`/projects/${ORYN}/commits/${c?.sha}`);
		const detail = await screen.findByTestId("repo-commit-detail");
		expect(
			await within(detail).findByText(/顺手 trim 掉首尾空白/),
		).toBeInTheDocument();
		expect(within(detail).getByTestId("repo-commit-thread")).toHaveTextContent(
			"由该线程推送",
		);
		const parent = commits[at + 1];
		expect(
			within(detail).getByRole("link", { name: parent?.sha.slice(0, 7) }),
		).toHaveAttribute("href", `/projects/${ORYN}/commits/${parent?.sha}`);
		expect(within(detail).getByText("1 个文件变更")).toBeInTheDocument();
		expect(await within(detail).findByTestId("diff-rows")).toBeInTheDocument();
		fireEvent.click(
			within(detail).getByRole("button", { expanded: true }) as HTMLElement,
		);
		expect(within(detail).queryByTestId("diff-rows")).not.toBeInTheDocument();
	});

	it("ref 选择写进 ?ref=", async () => {
		mount(`/projects/${ORYN}/commits`);
		fireEvent.click(await screen.findByTestId("repo-ref-picker"));
		fireEvent.click(await screen.findByRole("menuitem", { name: /标签/ }));
		fireEvent.click(
			await screen.findByRole("menuitemradio", { name: "v0.1.0" }),
		);
		await waitFor(() =>
			expect(screen.getByTestId("where").textContent).toBe(
				`/projects/${ORYN}/commits?ref=v0.1.0`,
			),
		);
		expect(await screen.findAllByTestId("repo-commit-row")).toHaveLength(6);
	});
});

describe("分支与标签子页", () => {
	it("分支：默认分支第一并带「默认」，文件入口带 ?ref=", async () => {
		mount(`/projects/${ORYN}/branches`);
		const rows = await screen.findAllByTestId("repo-ref-row");
		expect(rows.map((r) => r.dataset.ref)).toEqual([
			"main",
			"feature/repo-browser",
		]);
		expect(
			within(rows[0] as HTMLElement).getByText("默认"),
		).toBeInTheDocument();
		expect(
			within(rows[1] as HTMLElement).queryByText("默认"),
		).not.toBeInTheDocument();
		expect(
			within(rows[1] as HTMLElement).getByRole("link", { name: "文件" }),
		).toHaveAttribute(
			"href",
			`/projects/${ORYN}/files?ref=feature%2Frepo-browser`,
		);
		expect(
			within(screen.getByTestId("repo-nav")).getByRole("link", {
				name: "分支",
				current: "page",
			}),
		).toBeInTheDocument();
	});

	it("标签：列出 v0.1.0", async () => {
		mount(`/projects/${ORYN}/tags`);
		const rows = await screen.findAllByTestId("repo-ref-row");
		expect(rows.map((r) => r.dataset.ref)).toEqual(["v0.1.0"]);
	});

	it("镜像不可读时只有一条说明", async () => {
		mount(`/projects/${CORE}/branches`);
		expect(await screen.findByTestId("repo-unavailable")).toHaveTextContent(
			"凭据被拒绝",
		);
		expect(screen.queryByTestId("repo-nav")).not.toBeInTheDocument();
	});
});

describe("文件子页", () => {
	it("目录：列出子项，路径导航各段可点", async () => {
		mount(`/projects/${ORYN}/files/docs?ref=main`);
		const entries = await screen.findAllByTestId("repo-entry");
		expect(entries.map((e) => e.dataset.path)).toContain("docs/guide.md");
		const nav = screen.getByTestId("repo-path");
		expect(within(nav).getByRole("link", { name: "Oryn" })).toHaveAttribute(
			"href",
			`/projects/${ORYN}/files?ref=main`,
		);
		expect(
			within(nav).getByRole("link", { name: "docs", current: "page" }),
		).toBeInTheDocument();
	});

	it("文件：只读的 FileViewer，没有保存按钮；Markdown 的相对链接与图片按文件所在目录解析", async () => {
		mount(`/projects/${ORYN}/files/README.md`);
		const viewer = await screen.findByTestId("file-viewer");
		expect(await within(viewer).findByText("README.md")).toBeInTheDocument();
		expect(
			within(viewer).queryByRole("button", { name: /保存/ }),
		).not.toBeInTheDocument();
		expect(
			await within(viewer).findByRole("link", { name: "指南" }),
		).toHaveAttribute("href", `/projects/${ORYN}/files/docs/guide.md?ref=main`);
		expect(within(viewer).getByRole("img", { name: "架构图" })).toHaveAttribute(
			"src",
			`/api/v1/projects/${ORYN}/repo/blob?ref=main&path=docs%2Fdiagram.svg`,
		);
	});

	it("改 ref 留在同一路径", async () => {
		mount(`/projects/${ORYN}/files/src`);
		await screen.findAllByTestId("repo-entry");
		fireEvent.click(screen.getByTestId("repo-ref-picker"));
		fireEvent.click(await screen.findByRole("menuitem", { name: /分支/ }));
		fireEvent.click(
			await screen.findByRole("menuitemradio", {
				name: "feature/repo-browser",
			}),
		);
		await waitFor(() =>
			expect(screen.getByTestId("where").textContent).toBe(
				`/projects/${ORYN}/files/src?ref=feature%2Frepo-browser`,
			),
		);
	});
});

describe("文件子页 · Git LFS", () => {
	it("目录：docs 下有 media，media 里的 LFS 文件带「LFS」徽标", async () => {
		mount(`/projects/${ORYN}/files/docs`);
		const entries = await screen.findAllByTestId("repo-entry");
		expect(entries.map((e) => e.dataset.path)).toContain("docs/media");
		cleanup();
		mount(`/projects/${ORYN}/files/docs/media`);
		const media = await screen.findAllByTestId("repo-entry");
		expect(media.map((e) => e.dataset.path)).toEqual([
			"docs/media/banner.svg",
			"docs/media/missing.psd",
			"docs/media/walkthrough.mp4",
		]);
		for (const e of media)
			expect(within(e).getByTitle("由 Git LFS 存储")).toHaveTextContent("LFS");
	});

	it("能预览的 LFS 文件：上方写明由 Git LFS 存储，下面照常预览", async () => {
		mount(`/projects/${ORYN}/files/docs/media/banner.svg`);
		expect(await screen.findByTestId("repo-lfs-note")).toHaveTextContent(
			/由 Git LFS 存储 · \d/,
		);
		expect(screen.getByTestId("file-viewer")).toBeInTheDocument();
	});

	it("超过预览上限：只有下载按钮，地址带 download=1", async () => {
		mount(`/projects/${ORYN}/files/docs/media/walkthrough.mp4`);
		const large = await screen.findByTestId("repo-lfs-large");
		expect(large).toHaveTextContent("文件过大，不在浏览器中预览");
		expect(screen.queryByTestId("file-viewer")).not.toBeInTheDocument();
		fireEvent.click(within(large).getByRole("button", { name: /下载（/ }));
		expect(download).toHaveBeenCalledTimes(1);
		expect(download.mock.calls[0]?.[0]).toMatch(/download=1$/);
		expect(download.mock.calls[0]?.[1]).toBe("walkthrough.mp4");
	});

	it("取对象失败：写原因，重试会重新请求目录树", async () => {
		mount(`/projects/${ORYN}/files/docs/media/missing.psd`);
		const failed = await screen.findByTestId("repo-lfs-error");
		expect(failed).toHaveTextContent("找不到这个 LFS 对象");
		expect(screen.queryByTestId("file-viewer")).not.toBeInTheDocument();
		const trees = () =>
			urls.filter((u) => u.pathname.endsWith("/repo/tree")).length;
		const before = trees();
		fireEvent.click(within(failed).getByRole("button", { name: "重试" }));
		await waitFor(() => expect(trees()).toBe(before + 1));
	});
});
