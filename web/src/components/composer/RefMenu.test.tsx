/**
 * 输入框 `#` 引用（M14）：`refToken` 的边界；已绑定项目的线程输入框里键入 `#lo` 弹出候选，Enter 选中后
 * `#lo` 从文本里删掉、附件条多一个 `.md` 附件且不发 prompt；未绑定项目不弹；Esc 只关菜单不清空输入。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas, Thread } from "../../api/client";
import { qk } from "../../api/queries";
import i18n from "../../i18n";
import { resetComposerDrafts } from "../../state/composer-drafts";
import { indexStore, resetThreadStores } from "../../state/stores";
import { Composer } from "./Composer";
import { refToken } from "./RefMenu";

const { cmd } = vi.hoisted(() => ({ cmd: vi.fn() }));
vi.mock("../../realtime/socket", () => ({
	realtime: {
		cmd: (...args: unknown[]) => cmd(...args),
		uiResponse: () => true,
		subscribe: () => () => undefined,
		focus: () => undefined,
	},
	OmpCommandError: class extends Error {},
}));

const BOUND = "prj_01JCZ8M3W4X5Y6Z7A8B9C0REF1";
const UNBOUND = "prj_01JCZ8M3W4X5Y6Z7A8B9C0REF2";
const T_BOUND = "T-0193a1b2-0000-7000-8000-0000000000r1";
const T_UNBOUND = "T-0193a1b2-0000-7000-8000-0000000000r2";

function project(id: string, bound: boolean): Schemas["Project"] {
	return {
		id,
		name: id,
		repoUrl: "https://github.com/acme/widgets",
		shallowClone: false,
		createdAt: 1,
		updatedAt: 1,
		snapshots: false,
		warmOrbs: 0,
		...(bound
			? {
					github: {
						repoId: 42,
						fullName: "acme/widgets",
						bound: true,
						listen: { reviews: true, comments: true, ci: true },
					},
				}
			: {}),
	} as Schemas["Project"];
}

function thread(id: string, projectId: string): Thread {
	return {
		id,
		title: "t",
		titleSource: "auto",
		state: "idle",
		stateChangedAt: 1,
		pinned: false,
		labels: [],
		unread: false,
		createdAt: 1,
		updatedAt: 1,
		projectId,
	} as Thread;
}

const REF: Schemas["GitHubRef"] = {
	number: 12,
	kind: "pull",
	title: "Fix login redirect loop",
	state: "open",
	url: "https://github.com/acme/widgets/pull/12",
	updatedAt: 1,
};

const ATTACHMENT: Schemas["Attachment"] = {
	id: "att_01JCZ8M3W4X5Y6Z7A8B9C0REF9",
	name: "widgets-pr-12.md",
	mime: "text/markdown",
	size: 120,
	sha256: "0".repeat(64),
	createdAt: 1,
};

let requests: { method: string; path: string; query: string }[];

function mount(threadId: string) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	qc.setQueryData(qk.projects, {
		projects: [project(BOUND, true), project(UNBOUND, false)],
	});
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter>
				<Composer threadId={threadId} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

function box(): HTMLTextAreaElement {
	return screen.getByRole("textbox", {
		name: i18n.t("composer.label"),
	}) as HTMLTextAreaElement;
}

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
	cmd.mockReset();
	resetThreadStores();
	resetComposerDrafts();
	indexStore.getState().upsertThread(thread(T_BOUND, BOUND));
	indexStore.getState().upsertThread(thread(T_UNBOUND, UNBOUND));
	requests = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (req: Request) => {
			const url = new URL(req.url);
			requests.push({
				method: req.method,
				path: url.pathname,
				query: url.search,
			});
			if (url.pathname === `/api/v1/projects/${BOUND}/github/refs`)
				return Response.json({ refs: [REF] });
			if (
				url.pathname === `/api/v1/projects/${BOUND}/github/refs/12/attachment`
			)
				return Response.json(ATTACHMENT, { status: 201 });
			return Response.json(
				{ code: "not_found", message: "x" },
				{ status: 404 },
			);
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

describe("refToken", () => {
	it("光标前的 #查询；# 须在行首或空白之后", () => {
		expect(refToken("see #12", 7)).toEqual({ start: 4, query: "12" });
		expect(refToken("a#12", 4)).toBeNull();
		expect(refToken("#", 1)).toEqual({ start: 0, query: "" });
		expect(refToken("x\n#log more", 6)).toEqual({ start: 2, query: "log" });
		// 只看光标之前：光标后面还有别的词时，片段不在光标处结束就不算
		expect(refToken("#log more", 9)).toBeNull();
	});
});

describe("Composer 的 # 菜单", () => {
	it("已绑定项目：#lo 出现候选，Enter 后输入框里没有 #lo、附件条多一个 .md，且没发 prompt", async () => {
		mount(T_BOUND);
		fireEvent.change(box(), { target: { value: "看看 #lo" } });
		const option = await screen.findByRole("option", { name: /#12/ });
		expect(option).toHaveTextContent("Fix login redirect loop");
		// 输入停 200ms 后才按 `lo` 查询
		await waitFor(() =>
			expect(
				requests.some(
					(r) =>
						r.path === `/api/v1/projects/${BOUND}/github/refs` &&
						new URLSearchParams(r.query).get("q") === "lo",
				),
			).toBe(true),
		);
		await act(async () => {
			fireEvent.keyDown(box(), { key: "Enter" });
		});
		expect(box().value).toBe("看看 ");
		expect(await screen.findByText("widgets-pr-12.md")).toBeInTheDocument();
		expect(screen.queryByTestId("ref-menu")).toBeNull();
		const prompts = cmd.mock.calls
			.map((c): Record<string, unknown> => c[1])
			.filter((f) => f.type === "prompt");
		expect(prompts).toHaveLength(0);
		expect(
			requests.some(
				(r) =>
					r.method === "POST" &&
					r.path === `/api/v1/projects/${BOUND}/github/refs/12/attachment`,
			),
		).toBe(true);
	});

	it("未绑定项目：键入 # 不出现菜单", async () => {
		mount(T_UNBOUND);
		fireEvent.change(box(), { target: { value: "#" } });
		await act(() => new Promise((r) => setTimeout(r, 300)));
		expect(screen.queryByTestId("ref-menu")).toBeNull();
		expect(requests.some((r) => r.path.includes("/github/refs"))).toBe(false);
	});

	it("菜单打开时 Esc 只关菜单不清空输入；再输入又会打开", async () => {
		mount(T_BOUND);
		fireEvent.change(box(), { target: { value: "#lo" } });
		await screen.findByRole("option", { name: /#12/ });
		fireEvent.keyDown(box(), { key: "Escape" });
		expect(screen.queryByTestId("ref-menu")).toBeNull();
		expect(box().value).toBe("#lo");
		fireEvent.change(box(), { target: { value: "#log" } });
		await waitFor(() =>
			expect(screen.getByTestId("ref-menu")).toBeInTheDocument(),
		);
	});
});
