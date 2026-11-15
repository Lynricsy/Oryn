/**
 * 文件库面板（M13 4.7）：默认列本对话 + 库内共享的文件，可切到全部；行上显示来源、范围标记、所属对话 / 来源线程；
 * 切换共享走 `PATCH` 且行内范围标记随之更新；删除先确认；预览复用 `FileViewer`（只读）。数据走 mock 后端。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AssistantFile } from "../../api/library-queries";
import { threads as fixtureThreads, M9_IDS } from "../../mock/fixtures";
import { MockServer } from "../../mock/server";
import { indexStore } from "../../state/stores";
import { LibraryPanel } from "./LibraryPanel";

let server: MockServer;
let requests: { method: string; path: string; search: string; body: string }[];

beforeEach(() => {
	server = new MockServer();
	requests = [];
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			// rawFetch 用相对地址（预览读内容），先按任意源补全
			const req =
				input instanceof Request
					? input
					: new Request(new URL(input, "http://x"), init);
			const u = new URL(req.url, "http://x");
			requests.push({
				method: req.method,
				path: u.pathname,
				search: u.search,
				body: req.method === "PATCH" ? await req.clone().text() : "",
			});
			return (
				(await server.handleFetch(req)) ?? new Response(null, { status: 404 })
			);
		},
	);
	indexStore.getState().setAll(
		fixtureThreads.filter((t) => !t.archivedAt),
		1,
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
	indexStore.getState().setAll([], 0);
});

function mount(threadId: string = M9_IDS.assistant) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter>
				<LibraryPanel threadId={threadId} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const row = (name: string) => screen.getByRole("listitem", { name });
const scopeOf = (name: string) => within(row(name)).getByTestId("scope");

/** 一个属于别的助手对话的文件：上传后随那边的消息发送才进入列表。 */
async function fileOfOtherConversation(name: string) {
	const res = await server.handleFetch(
		new Request(
			`http://x/api/v1/assistant/files?name=${encodeURIComponent(name)}`,
			{ method: "POST", body: "x" },
		),
	);
	const file: AssistantFile = await res?.json();
	expect(server.library.deliver("T-other-conversation", [file.id])).toEqual([
		`- ~/library/threads/T-other-conversation/${file.id}/${name} (text/plain; charset=utf-8, 1 bytes)`,
	]);
}

describe("LibraryPanel", () => {
	it("列出本对话与共享的文件：来源、范围标记、所属对话与来源线程", async () => {
		mount();
		await screen.findByRole("listitem", { name: "周报-11月第2周.md" });
		expect(requests[0]?.search).toBe(`?threadId=${M9_IDS.assistant}`);
		expect(
			screen
				.getAllByRole("listitem")
				.map((li) => li.getAttribute("aria-label")),
		).toEqual(["shots-index.json", "等待中的线程.csv", "周报-11月第2周.md"]);
		const upload = row("周报-11月第2周.md");
		expect(within(upload).getByText("上传")).toBeVisible();
		expect(scopeOf("周报-11月第2周.md")).toHaveTextContent("仅此对话");
		expect(within(upload).getByText("本对话")).toBeVisible();
		expect(within(row("等待中的线程.csv")).getByText("助手保存")).toBeVisible();
		const orb = row("shots-index.json");
		expect(within(orb).getByText("orb")).toBeVisible();
		expect(scopeOf("shots-index.json")).toHaveTextContent("库内共享");
		expect(within(orb).getByRole("link")).toHaveAttribute(
			"href",
			`/t/${M9_IDS.spaceIdle}`,
		);
	});

	it("切换共享调用 PATCH，行内范围标记随之更新", async () => {
		mount();
		await screen.findByRole("listitem", { name: "周报-11月第2周.md" });
		fireEvent.click(
			within(row("周报-11月第2周.md")).getByRole("button", {
				name: "设为库内共享",
			}),
		);
		await waitFor(() =>
			expect(scopeOf("周报-11月第2周.md")).toHaveTextContent("库内共享"),
		);
		const patch = requests.find((q) => q.method === "PATCH");
		expect(patch?.path).toBe(
			"/api/v1/assistant/files/afl_01JDM000000000000000LIB001",
		);
		expect(JSON.parse(patch?.body ?? "{}")).toEqual({ scope: "shared" });
		const back = within(row("周报-11月第2周.md")).getByRole("button", {
			name: "改回仅所属对话",
		});
		expect(back).toHaveAttribute("aria-pressed", "true");
		fireEvent.click(back);
		await waitFor(() =>
			expect(scopeOf("周报-11月第2周.md")).toHaveTextContent("仅此对话"),
		);
		expect(
			requests
				.filter((q) => q.method === "PATCH")
				.map((q) => JSON.parse(q.body)),
		).toEqual([{ scope: "shared" }, { scope: "conversation" }]);
	});

	it("「全部」列出别的对话的文件，并链到它所属的对话", async () => {
		await fileOfOtherConversation("别处的笔记.txt");
		mount();
		await screen.findByRole("listitem", { name: "周报-11月第2周.md" });
		expect(
			screen.queryByRole("listitem", { name: "别处的笔记.txt" }),
		).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "全部" }));
		const other = await screen.findByRole("listitem", {
			name: "别处的笔记.txt",
		});
		expect(requests.at(-1)?.search).toBe("");
		expect(within(other).getByRole("link")).toHaveAttribute(
			"href",
			"/assistant/T-other-conversation",
		);
	});

	it("删除先确认：取消不发请求，确认后行消失", async () => {
		const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
		mount();
		await screen.findByRole("listitem", { name: "等待中的线程.csv" });
		const del = () =>
			fireEvent.click(
				within(row("等待中的线程.csv")).getByRole("button", { name: "删除" }),
			);
		del();
		expect(confirm).toHaveBeenCalledWith(
			"删除「等待中的线程.csv」？已复制给其它线程的附件不受影响。",
		);
		expect(requests.some((q) => q.method === "DELETE")).toBe(false);
		confirm.mockReturnValue(true);
		del();
		await waitFor(() =>
			expect(
				screen.queryByRole("listitem", { name: "等待中的线程.csv" }),
			).toBeNull(),
		);
		expect(requests.find((q) => q.method === "DELETE")?.path).toBe(
			"/api/v1/assistant/files/afl_01JDM000000000000000LIB002",
		);
	});

	it("预览用只读的 FileViewer：读取内容，没有保存按钮，可返回列表", async () => {
		mount();
		await screen.findByRole("listitem", { name: "周报-11月第2周.md" });
		fireEvent.click(
			within(row("周报-11月第2周.md")).getByRole("button", { name: "预览" }),
		);
		// Markdown 读完后出现「源码 / 预览」切换
		fireEvent.click(
			await screen.findByRole("button", { name: "预览", pressed: false }),
		);
		expect(await screen.findByRole("heading", { name: "周报" })).toBeVisible();
		expect(screen.queryByRole("button", { name: "保存" })).toBeNull();
		expect(
			requests.some((q) =>
				q.path.endsWith("/afl_01JDM000000000000000LIB001/content"),
			),
		).toBe(true);
		fireEvent.click(screen.getByRole("button", { name: "返回列表" }));
		expect(
			await screen.findByRole("listitem", { name: "周报-11月第2周.md" }),
		).toBeVisible();
	});
});
