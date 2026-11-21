/**
 * 分区轨道（M9.6）：四个 pane 同时在 DOM 里，非当前的 inert + aria-hidden；`--section-i` 随路由变化；
 * 切走再切回，线程 pane 是同一个 DOM 节点，滚动位置与分组的收起状态都在。动效规则（jsdom 不排版，直接读 index.css 原文）：
 * 轨道 200ms 滑动；减少动态时全局规则把过渡压成 0s，当前 pane 改为 150ms 淡入，且这条规则在分层里（分层的 !important
 * 优先于全局规则里未分层的 !important）。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import {
	MemoryRouter,
	type NavigateFunction,
	Route,
	Routes,
	useNavigate,
} from "react-router";
import {
	afterEach,
	beforeAll,
	beforeEach,
	describe,
	expect,
	it,
	vi,
} from "vitest";
import { threads as fixtureThreads } from "../../mock/fixtures";
import { MockServer } from "../../mock/server";
import { connectionStore, indexStore } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { SectionPanes } from "./SectionPanes";

let go: NavigateFunction = () => undefined;

function Nav() {
	go = useNavigate();
	return null;
}

function mount(path = "/") {
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={[path]}>
				<Routes>
					<Route
						path="*"
						element={
							<>
								<Nav />
								<SectionPanes />
							</>
						}
					/>
				</Routes>
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const track = () => document.querySelector<HTMLElement>(".section-track");
const pane = (id: string) =>
	document.querySelector<HTMLElement>(
		`[data-testid="section-pane"][data-section="${id}"]`,
	) as HTMLElement;

beforeEach(() => {
	window.innerWidth = 1280;
	const server = new MockServer();
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			return (
				(await server.handleFetch(req)) ?? new Response(null, { status: 404 })
			);
		},
	);
	indexStore.getState().setAll(
		fixtureThreads.filter((t) => !t.archivedAt),
		1,
	);
	connectionStore.setState({ status: "open" });
	uiStore.setState({ sectionPaths: {} });
});

afterEach(() => {
	vi.unstubAllGlobals();
	indexStore.getState().setAll([], 0);
	window.innerWidth = 1024;
});

describe("SectionPanes", () => {
	it("四个 pane 同时在 DOM 里，按底栏顺序；只有当前的可交互", () => {
		mount("/");
		const all = document.querySelectorAll<HTMLElement>(
			'[data-testid="section-pane"]',
		);
		expect(Array.from(all).map((p) => p.dataset.section)).toEqual([
			"threads",
			"assistant",
			"projects",
			"more",
		]);
		// 读屏只看得到当前分区
		expect(
			screen.getAllByRole("region").map((r) => r.getAttribute("aria-label")),
		).toEqual(["线程"]);
		expect(pane("threads")).not.toHaveAttribute("inert");
		expect(pane("threads")).toHaveAttribute("data-active");
		for (const id of ["assistant", "projects", "more"]) {
			expect(pane(id)).toHaveAttribute("inert");
			expect(pane(id)).toHaveAttribute("aria-hidden", "true");
			expect(pane(id)).not.toHaveAttribute("data-active");
		}
	});

	it("--section-i 随路由变化", () => {
		mount("/t/T-1");
		const index = () => track()?.style.getPropertyValue("--section-i");
		expect(index()).toBe("0");
		act(() => {
			go("/settings/about");
		});
		expect(index()).toBe("3");
		expect(pane("more")).not.toHaveAttribute("inert");
		expect(pane("threads")).toHaveAttribute("inert");
		act(() => {
			go("/projects/p1");
		});
		expect(index()).toBe("2");
		act(() => {
			go("/assistant/T-a");
		});
		expect(index()).toBe("1");
		act(() => {
			go("/space/spc");
		});
		expect(index()).toBe("0");
	});

	it("切到「更多」再回来：同一个 DOM 节点，滚动位置与分组收起状态都在", async () => {
		mount("/");
		const scroller = screen.getByTestId("threads-scroll");
		const pinned = await waitFor(() => {
			const b = screen
				.getAllByRole("button", { expanded: true })
				.find((el) => el.textContent?.includes("已置顶"));
			expect(b).toBeDefined();
			return b as HTMLElement;
		});
		fireEvent.click(pinned);
		expect(pinned).toHaveAttribute("aria-expanded", "false");
		scroller.scrollTop = 200;
		act(() => {
			go("/settings/about");
		});
		expect(pane("threads")).toHaveAttribute("inert");
		act(() => {
			go("/");
		});
		expect(screen.getByTestId("threads-scroll")).toBe(scroller);
		expect(scroller.scrollTop).toBe(200);
		expect(pinned.isConnected).toBe(true);
		expect(pinned).toHaveAttribute("aria-expanded", "false");
	});
});

/** `needle` 所在位置外层的各级块的前导（选择器 / at-rule），由外到内。 */
function enclosing(text: string, needle: string): string[] {
	const at = text.indexOf(needle);
	expect(at).toBeGreaterThanOrEqual(0);
	const stack: string[] = [];
	let start = 0;
	for (let i = 0; i < at; i++) {
		const c = text[i];
		if (c === "{") {
			stack.push(
				text
					.slice(start, i)
					.replace(/\/\*[\s\S]*?\*\//g, "")
					.trim(),
			);
			start = i + 1;
		} else if (c === "}") {
			stack.pop();
			start = i + 1;
		} else if (c === ";") start = i + 1;
	}
	return stack;
}

/** 从 `selector {` 起到配对的 `}` 为止的块正文。 */
function block(text: string, from: number): string {
	const open = text.indexOf("{", from);
	let depth = 0;
	for (let i = open; i < text.length; i++) {
		if (text[i] === "{") depth++;
		else if (text[i] === "}" && --depth === 0) return text.slice(open + 1, i);
	}
	return "";
}

describe("分区轨道的动效规则（index.css）", () => {
	let css = "";
	beforeAll(async () => {
		// vitest 会把 CSS 导入（含 ?raw）处理成空串，这里直接读源文件（工作目录是 web/）；
		// 前端 tsconfig 不带 Node 类型，只声明用到的函数（同 squircle-geometry.test）
		const fs = (await import("node:fs" as string)) as {
			readFileSync(path: string, encoding: "utf8"): string;
		};
		css = fs.readFileSync("src/index.css", "utf8");
	});

	it("轨道按 --section-i 平移、200ms --ease 过渡，四格各占 25%", () => {
		const track = block(css, css.indexOf(".section-track {"));
		expect(track).toContain("width: 400%");
		expect(track).toContain("translate: calc(var(--section-i, 0) * -25%) 0");
		expect(track).toContain("transition: translate 200ms var(--ease)");
		expect(block(css, css.indexOf(".section-pane {"))).toContain("width: 25%");
	});

	it("减少动态：当前 pane 150ms 淡入，规则在分层里，压得过全局未分层的 1ms / 0s", () => {
		const at = css.indexOf(".section-pane[data-active] {");
		const rule = block(css, at);
		expect(rule).toContain("animation: section-fade 150ms");
		expect(rule).toContain("animation-duration: 150ms !important");
		const chain = enclosing(css, ".section-pane[data-active] {");
		expect(chain[0]).toBe("@layer components");
		expect(chain).toContain("@media (prefers-reduced-motion: reduce)");
		expect(block(css, css.indexOf("@keyframes section-fade"))).toContain(
			"opacity: 0",
		);
		// 全局规则：未分层、过渡归零（轨道因此不滑）
		const global = enclosing(css, "transition-duration: 0s !important");
		expect(global[0]).toBe("@media (prefers-reduced-motion: reduce)");
		expect(global.some((p) => p.startsWith("@layer"))).toBe(false);
	});
});
