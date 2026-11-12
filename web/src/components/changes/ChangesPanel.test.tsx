import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Orb } from "../../api/client";
import i18n from "../../i18n";
import { handleOrbFetch, resetOrbFs } from "../../mock/orbfs";
import { getThreadStore } from "../../state/stores";
import ChangesPanel, { POLL_MS } from "./ChangesPanel";

vi.mock("../../state/terminals", () => ({
	terminalHub: { onExit: vi.fn(() => () => {}) },
}));

const t = (k: string, o: Record<string, unknown> = {}) =>
	i18n.t(k as "common.save", o);

const ORB = "orb-chg000000000000000000001";
let requests: {
	method: string;
	path: string;
	query: URLSearchParams;
	paths?: string[];
}[];
let override: ((url: URL) => Response | null) | null;

beforeEach(() => {
	resetOrbFs();
	localStorage.clear();
	requests = [];
	override = null;
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const url = new URL(req.url);
			const text = req.method === "POST" ? await req.clone().text() : "";
			requests.push({
				method: req.method,
				path: url.pathname,
				query: url.searchParams,
				paths: text
					? (JSON.parse(text) as { paths?: string[] }).paths
					: undefined,
			});
			const o = override?.(url);
			if (o) return o;
			return (await handleOrbFetch(req)) ?? new Response(null, { status: 404 });
		}),
	);
});

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

const statusReqs = () => requests.filter((r) => r.path.endsWith("/git/status"));
/** 首次加载经 fetch → 渲染；负载高时给足等待时间（不是固定 sleep，元素出现即返回）。 */
const filesList = () =>
	screen.findByRole("list", { name: t("changes.files") }, { timeout: 5000 });

/** 该文件所在行的暂存勾选框（按路径列的 title 定位，不依赖文案）。 */
function box(path: string): HTMLInputElement {
	const row = screen.getAllByTitle(path)[0]?.closest("li");
	const input = row?.querySelector('input[type="checkbox"]');
	if (!(input instanceof HTMLInputElement))
		throw new Error(`no checkbox for ${path}`);
	return input;
}

describe("ChangesPanel", () => {
	it("全部改动：按目录分组、状态字母与 +/−、重命名显示 old → new、三态暂存勾选", async () => {
		render(<ChangesPanel threadId="T-c1" orbId={ORB} />);
		const list = await filesList();
		expect(
			within(list).getByText("docs/old-guide.md → docs/guide.md"),
		).toBeInTheDocument();
		expect(within(list).getByText("src")).toBeInTheDocument();
		expect(
			screen.getByRole("option", {
				name: t("changes.scope.allBase", { base: "main" }),
			}),
		).toBeInTheDocument();
		expect(box("README.md").checked).toBe(true);
		expect(box("src/main.ts").indeterminate).toBe(true);
		expect(box("src/main.ts").checked).toBe(false);
		expect(box("src/util.ts").checked).toBe(false);
		expect(box("src/util.ts").indeterminate).toBe(false);
		// scope=all 把基线之后的提交也算进来
		expect(within(list).getByText("files.ts")).toBeInTheDocument();
		expect(statusReqs()[0]?.query.get("scope")).toBe("all");
		expect(statusReqs()[0]?.query.has("bg")).toBe(false);
	});

	it("勾选未暂存文件 → stage；重命名把新旧路径一起提交；勾掉已暂存 → unstage", async () => {
		render(<ChangesPanel threadId="T-c2" orbId={ORB} />);
		await filesList();
		fireEvent.click(box("src/util.ts"));
		await waitFor(() => expect(box("src/util.ts").checked).toBe(true));
		fireEvent.click(box("docs/guide.md"));
		await waitFor(() => expect(box("docs/guide.md").checked).toBe(true));
		fireEvent.click(box("README.md"));
		await waitFor(() => expect(box("README.md").checked).toBe(false));
		const posts = requests.filter((r) => r.method === "POST");
		expect(posts.map((p) => [p.path.split("/").pop(), p.paths])).toEqual([
			["stage", ["src/util.ts"]],
			["stage", ["docs/old-guide.md", "docs/guide.md"]],
			["unstage", ["README.md"]],
		]);
	});

	it("仅未提交 / 单个 commit：请求带对应 scope；commit 范围不显示暂存勾选", async () => {
		render(<ChangesPanel threadId="T-c3" orbId={ORB} />);
		await filesList();
		const picker = screen.getByRole("combobox", {
			name: t("changes.scope.label"),
		});
		fireEvent.change(picker, { target: { value: "uncommitted" } });
		await waitFor(() => expect(screen.queryByText("files.ts")).toBeNull());
		expect(statusReqs().at(-1)?.query.get("scope")).toBe("uncommitted");
		expect(box("src/util.ts")).toBeInTheDocument();

		const sha = "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b";
		await waitFor(() =>
			expect(
				screen.getByRole("option", { name: /^1a2b3c4 docs: 更新 README/ }),
			).toBeInTheDocument(),
		);
		fireEvent.change(picker, { target: { value: `commit:${sha}` } });
		await waitFor(() => expect(screen.queryByText("util.ts")).toBeNull());
		expect(statusReqs().at(-1)?.query.get("scope")).toBe("commit");
		expect(statusReqs().at(-1)?.query.get("commit")).toBe(sha);
		expect(screen.getByText("README.md")).toBeInTheDocument();
		expect(screen.queryByRole("checkbox")).toBeNull();
		expect(
			screen.queryByRole("button", { name: t("changes.stageAll") }),
		).toBeNull();
	});

	it("点文件显示 git patch：增删行着色，文件头不当作增删", async () => {
		const { container } = render(<ChangesPanel threadId="T-c4" orbId={ORB} />);
		await filesList();
		fireEvent.click(screen.getByText("util.ts"));
		await waitFor(() =>
			expect(container.querySelector(".tv-diff-row--add")).not.toBeNull(),
		);
		const rows = [...container.querySelectorAll(".tv-diff-row")];
		const plusHeader = rows.find((r) => r.textContent?.startsWith("+++ "));
		expect(plusHeader?.className).toContain("tv-diff-row--hunk");
		const added = rows
			.filter((r) => r.className.includes("--add"))
			.map((r) => r.textContent);
		expect(added).toContain("+export function greet(name: string): string {");
		const diffReq = requests.find((r) => r.path.endsWith("/git/diff"));
		expect(diffReq?.query.get("path")).toBe("src/util.ts");
	});

	it("二进制文件显示说明行而不是 patch", async () => {
		render(<ChangesPanel threadId="T-c5" orbId={ORB} />);
		await filesList();
		fireEvent.click(screen.getByText("data.bin"));
		expect(
			await screen.findByText(t("changes.diff.binary")),
		).toBeInTheDocument();
	});

	it("repo:false：显示提示与候选仓库，所选 root 记入 localStorage 并用于后续请求", async () => {
		const orb = "orb-chg0000000000000000000e8";
		render(<ChangesPanel threadId="T-c6" orbId={orb} />);
		expect(await screen.findByText(t("changes.notRepo"))).toBeInTheDocument();
		fireEvent.click(
			screen.getByRole("button", { name: "/home/user/workspace/repo" }),
		);
		expect(await filesList()).toBeInTheDocument();
		expect(localStorage.getItem("oryn.changes.root.T-c6")).toBe(
			"/home/user/workspace/repo",
		);
		expect(statusReqs().at(-1)?.query.get("root")).toBe(
			"/home/user/workspace/repo",
		);
	});

	it("旧版 orbd：显示「旧版本」提示", async () => {
		render(
			<ChangesPanel threadId="T-c7" orbId="orb-old00000000000000000000e4" />,
		);
		expect(await screen.findByText(t("changes.outdated"))).toBeInTheDocument();
	});

	it("orb ready 且文档可见时每 10s 以 bg=1 轮询；orb_not_ready 时保留已有列表", async () => {
		render(<ChangesPanel threadId="T-c8" orbId={ORB} />);
		await filesList();
		vi.useFakeTimers();
		act(() => {
			getThreadStore("T-c8")
				.getState()
				.patchLocal({
					orb: { id: ORB, state: "ready" } as Orb,
				});
		});
		const before = statusReqs().length;
		await act(async () => {
			await vi.advanceTimersByTimeAsync(POLL_MS);
		});
		expect(statusReqs().length).toBe(before + 1);
		expect(statusReqs().at(-1)?.query.get("bg")).toBe("true");
		// 轮询期间文件列表没变：不重拉 diff
		expect(requests.some((r) => r.path.endsWith("/git/diff"))).toBe(false);

		override = (url) =>
			url.pathname.endsWith("/git/status")
				? Response.json(
						{ code: "orb_not_ready", message: "frozen" },
						{ status: 409 },
					)
				: null;
		await act(async () => {
			await vi.advanceTimersByTimeAsync(POLL_MS);
		});
		expect(statusReqs().length).toBe(before + 2);
		expect(
			screen.getByRole("list", { name: t("changes.files") }),
		).toBeInTheDocument();
		expect(screen.queryByRole("alert")).toBeNull();
	});

	it("orb 不是 ready 时不轮询", async () => {
		render(<ChangesPanel threadId="T-c9" orbId={ORB} />);
		await filesList();
		vi.useFakeTimers();
		act(() => {
			getThreadStore("T-c9")
				.getState()
				.patchLocal({
					orb: { id: ORB, state: "frozen" } as Orb,
				});
		});
		const before = statusReqs().length;
		await act(async () => {
			await vi.advanceTimersByTimeAsync(POLL_MS * 3);
		});
		expect(statusReqs().length).toBe(before);
	});
});

describe("ChangesPanel 后台刷新", () => {
	it("打开的文件在后台刷新时以 bg=1 重拉 diff；用户点开文件时不带 bg", async () => {
		render(<ChangesPanel threadId="T-c10" orbId={ORB} />);
		await filesList();
		fireEvent.click(screen.getByText("util.ts"));
		await waitFor(() =>
			expect(requests.filter((r) => r.path.endsWith("/git/diff"))).toHaveLength(
				1,
			),
		);
		const diffs = () => requests.filter((r) => r.path.endsWith("/git/diff"));
		expect(diffs()[0]?.query.has("bg")).toBe(false);
		vi.useFakeTimers();
		act(() => {
			getThreadStore("T-c10")
				.getState()
				.patchLocal({ orb: { id: ORB, state: "ready" } as Orb });
		});
		// 轮询之间暂存状态变了（相当于 agent 在终端里 git add）：下一次轮询看到列表变化
		await act(async () => {
			await fetch(`/api/v1/orbs/${ORB}/git/stage`, {
				method: "POST",
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify({ paths: ["legacy.txt"] }),
			});
			await vi.advanceTimersByTimeAsync(POLL_MS);
		});
		expect(diffs()).toHaveLength(2);
		expect(diffs()[1]?.query.get("bg")).toBe("true");
		expect(diffs()[1]?.query.get("path")).toBe("src/util.ts");
	});
});
