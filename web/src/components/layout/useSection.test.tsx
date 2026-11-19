/**
 * 分区（M9.6）的快捷键与路径记忆：Ctrl/⌘+1…4 导航到各分区记住的路径（修饰键组合不对或 5 不触发）、Ctrl/⌘+/ 回上次的助手对话、
 * Ctrl/⌘+K 先回线程分区再聚焦搜索；追踪器不让协调对话的重定向污染线程分区的记忆，记忆里的坏路径落到缺省路径。
 * 挂完整的应用壳（1280 宽），线程页用一个按协调对话重定向的替身（同 ThreadPage）。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import {
	MemoryRouter,
	Navigate,
	type NavigateFunction,
	Route,
	Routes,
	useLocation,
	useNavigate,
	useParams,
} from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../../api/client";
import { qk } from "../../api/queries";
import { indexStore, useIndex } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { AppShell } from "./AppShell";

vi.mock("../../realtime/socket", () => ({
	realtime: {
		connect: vi.fn(),
		close: vi.fn(),
		send: vi.fn(),
		onSchedulesChanged: vi.fn(() => () => undefined),
		onGitHubChanged: vi.fn(() => () => undefined),
		onSshChanged: vi.fn(() => () => undefined),
		onArtifactsChanged: vi.fn(() => () => undefined),
		onArtifactConsent: vi.fn(() => () => undefined),
		onArtifactWatchesChanged: vi.fn(() => () => undefined),
	},
}));

function th(id: string, p: Partial<Thread> = {}): Thread {
	return {
		id,
		title: id,
		titleSource: "auto",
		state: "idle",
		stateChangedAt: 1,
		pinned: false,
		labels: [],
		unread: false,
		createdAt: 1,
		updatedAt: 1,
		...p,
	};
}

let go: NavigateFunction = () => undefined;

function Where() {
	const loc = useLocation();
	go = useNavigate();
	return <p data-testid="where">{loc.pathname + loc.search}</p>;
}

/** 线程页替身：`A…` 是助手对话（不管索引里有没有），重定向到助手分区，同 ThreadPage 的协调对话重定向。 */
function FakeThread() {
	const { id = "" } = useParams();
	if (id.startsWith("A")) return <Navigate to={`/assistant/${id}`} replace />;
	return <Where />;
}

/** 线程页替身：只按索引里的种类重定向（同 ThreadPage），索引没到时照常显示。 */
function IndexedThread() {
	const { id = "" } = useParams();
	const kind = useIndex((s) => s.threads[id]?.kind);
	if (kind === "assistant") return <Navigate to={`/assistant/${id}`} replace />;
	return <Where />;
}

function mount(path: string, thread = <FakeThread />) {
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false, staleTime: Infinity } },
	});
	qc.setQueryData(qk.me, {
		displayName: "Ada",
		createdAt: 1,
		auth: { kind: "web" },
	});
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={[path]}>
				<Routes>
					<Route element={<AppShell />}>
						<Route path="t/:id" element={thread} />
						<Route path="*" element={<Where />} />
					</Route>
				</Routes>
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const where = () => screen.getByTestId("where").textContent;
/** 让追踪器延后的那个宏任务跑完（它在本任务之前排队）。 */
const settle = () =>
	act(async () => {
		await new Promise((r) => setTimeout(r, 0));
	});
const press = (init: KeyboardEventInit, target: Window | Element = window) =>
	act(() => {
		fireEvent.keyDown(target, init);
	});
const digit = (n: number, extra: KeyboardEventInit = {}) =>
	press({ key: String(n), code: `Digit${n}`, ctrlKey: true, ...extra });

beforeEach(() => {
	window.innerWidth = 1280;
	localStorage.clear();
	sessionStorage.clear();
	uiStore.setState({
		sidebarCollapsed: false,
		sidebarOpen: false,
		sectionPaths: {},
		projectFilter: undefined,
	});
	indexStore.getState().setAll([], 1);
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL) => {
			const url = new URL(
				input instanceof Request ? input.url : String(input),
				"http://x",
			);
			if (url.pathname === "/api/v1/projects")
				return Response.json({ projects: [] });
			if (url.pathname === "/api/v1/threads")
				return Response.json({ threads: [] });
			return Response.json({});
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	indexStore.getState().setAll([], 0);
	window.innerWidth = 1024;
});

describe("Ctrl/⌘+1…4", () => {
	it("各自导航到该分区记住的路径；先在 /settings/secrets 停留、去线程、Ctrl+4 回到 /settings/secrets", async () => {
		uiStore.setState({
			sectionPaths: { assistant: "/assistant/T-a", projects: "/projects/p1" },
		});
		mount("/settings/secrets");
		await settle();
		digit(1);
		expect(where()).toBe("/");
		await settle();
		digit(4);
		expect(where()).toBe("/settings/secrets");
		digit(2);
		expect(where()).toBe("/assistant/T-a");
		press({ key: "3", code: "Digit3", metaKey: true });
		expect(where()).toBe("/projects/p1");
		digit(1);
		expect(where()).toBe("/");
		expect(uiStore.getState().sectionPaths).toMatchObject({
			threads: "/",
			more: "/settings/secrets",
		});
	});

	it("Ctrl+Shift+1、Ctrl+Alt+1、Ctrl+5、不带修饰键的 1 都不触发", async () => {
		mount("/settings/secrets");
		await settle();
		digit(1, { shiftKey: true, key: "!" });
		digit(1, { altKey: true });
		digit(5);
		press({ key: "1", code: "Digit1" });
		expect(where()).toBe("/settings/secrets");
	});

	it("焦点在输入框里同样生效；侧栏收起时不展开", async () => {
		uiStore.setState({ sidebarCollapsed: true });
		mount("/t/T-1");
		await settle();
		const input = document.createElement("input");
		document.body.append(input);
		input.focus();
		press({ key: "4", code: "Digit4", ctrlKey: true }, input);
		expect(where()).toBe("/settings/automations");
		expect(uiStore.getState().sidebarCollapsed).toBe(true);
		input.remove();
	});

	it("已在该分区时不导航（停在当前子页面上）", async () => {
		uiStore.setState({ sectionPaths: { more: "/settings/about" } });
		mount("/settings/secrets");
		await settle();
		digit(4);
		expect(where()).toBe("/settings/secrets");
	});
});

describe("Ctrl/⌘+/ 与 Ctrl/⌘+K", () => {
	it("Ctrl+/ 回上次的助手对话；没有记忆时落到 /assistant", async () => {
		mount("/t/T-1");
		await settle();
		press({ key: "/", code: "Slash", ctrlKey: true });
		expect(where()).toBe("/assistant");
		act(() => {
			go("/assistant/T-b");
		});
		await settle();
		digit(1);
		expect(where()).toBe("/t/T-1");
		press({ key: "/", code: "Slash", metaKey: true });
		expect(where()).toBe("/assistant/T-b");
	});

	it("Ctrl+K 开关命令面板：不改路由，输入框里同样生效，关闭后焦点回到打开前的元素", async () => {
		mount(
			"/t/T-1",
			<>
				<FakeThread />
				<input aria-label="草稿" />
			</>,
		);
		await settle();
		const field = screen.getByRole("textbox", { name: "草稿" });
		act(() => field.focus());
		press({ key: "k", code: "KeyK", ctrlKey: true }, field);
		// 命令面板是按需加载的分块：全量并行跑时加载可能超过默认的 1 秒
		const input = await screen.findByRole("combobox", {}, { timeout: 5000 });
		expect(where()).toBe("/t/T-1");
		expect(document.activeElement).toBe(input);
		press({ key: "k", code: "KeyK", metaKey: true }, input);
		expect(screen.queryByRole("combobox")).toBeNull();
		expect(document.activeElement).toBe(field);
	});

	it("侧栏的搜索入口不只在线程分区：在设置分区点它同样打开命令面板", async () => {
		mount("/settings");
		await settle();
		fireEvent.click(screen.getByRole("button", { name: "搜索与命令" }));
		expect(
			await screen.findByRole("combobox", {}, { timeout: 5000 }),
		).toBeInTheDocument();
		expect(where()).toBe("/settings");
	});
});

describe("追踪器", () => {
	it("/t/<助手对话>（索引里已知）被重定向后，线程分区的记忆没有变成它", async () => {
		indexStore.getState().setAll([th("A1", { kind: "assistant" })], 1);
		mount("/t/T-1");
		await settle();
		act(() => {
			go("/t/A1");
		});
		await settle();
		expect(where()).toBe("/assistant/A1");
		expect(uiStore.getState().sectionPaths).toEqual({
			threads: "/t/T-1",
			assistant: "/assistant/A1",
		});
	});

	it("索引里已知是协调对话的 /t/<id>（重定向还没发生）也不记", async () => {
		indexStore
			.getState()
			.setAll(
				[th("K1", { kind: "assistant" }), th("S1", { kind: "space" })],
				1,
			);
		mount("/t/T-1");
		await settle();
		// 替身不重定向 K / S 开头的 id：模拟重定向晚于追踪器的那一拍
		act(() => {
			go("/t/K1");
		});
		await settle();
		act(() => {
			go("/t/S1");
		});
		await settle();
		expect(where()).toBe("/t/S1");
		expect(uiStore.getState().sectionPaths.threads).toBe("/t/T-1");
	});

	it("冷启动（索引里还没有该对话）：同一拍里的重定向不留痕", async () => {
		mount("/t/T-1");
		await settle();
		act(() => {
			go("/t/A2");
		});
		await settle();
		expect(where()).toBe("/assistant/A2");
		expect(uiStore.getState().sectionPaths.threads).toBe("/t/T-1");
	});

	it("冷启动、索引异步到达：判定前不覆盖线程记忆；到达后是助手对话则重定向，切回线程仍是原工作线程", async () => {
		indexStore.setState({ threads: {}, loaded: false });
		uiStore.setState({ sectionPaths: { threads: "/t/W1" } });
		mount("/t/C1", <IndexedThread />);
		await settle();
		expect(where()).toBe("/t/C1");
		expect(uiStore.getState().sectionPaths.threads).toBe("/t/W1");
		act(() => {
			indexStore
				.getState()
				.setAll([th("C1", { kind: "assistant" }), th("W1")], 2);
		});
		await settle();
		expect(where()).toBe("/assistant/C1");
		expect(uiStore.getState().sectionPaths.threads).toBe("/t/W1");
		digit(1);
		expect(where()).toBe("/t/W1");
	});

	it("冷启动、索引到达后确认是工作线程：这时才记住", async () => {
		indexStore.setState({ threads: {}, loaded: false });
		uiStore.setState({ sectionPaths: { threads: "/t/W1" } });
		mount("/t/W2", <IndexedThread />);
		await settle();
		expect(uiStore.getState().sectionPaths.threads).toBe("/t/W1");
		act(() => {
			indexStore.getState().setAll([th("W2", { kind: "work" })], 2);
		});
		expect(uiStore.getState().sectionPaths.threads).toBe("/t/W2");
	});

	it("记忆里是 /t/<助手对话>（冷启动竞态留下的）时，切到线程落到 /", async () => {
		indexStore.getState().setAll([th("A1", { kind: "assistant" })], 1);
		uiStore.setState({ sectionPaths: { threads: "/t/A1" } });
		mount("/settings/devices");
		await settle();
		digit(1);
		expect(where()).toBe("/");
	});

	it("记忆里的路径属于别的分区或以 // 开头时用缺省路径", async () => {
		uiStore.setState({ sectionPaths: { threads: "/settings/secrets" } });
		mount("/settings/devices");
		await settle();
		digit(1);
		expect(where()).toBe("/");
		uiStore.setState({
			sectionPaths: {
				...uiStore.getState().sectionPaths,
				more: "//evil.example/x",
			},
		});
		digit(4);
		expect(where()).toBe("/settings/automations");
	});
});
