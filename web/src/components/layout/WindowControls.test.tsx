/**
 * 自绘标题栏的窗口控件：各 `controls` 取值下按钮在哪一侧、有没有（标题行、页头两端、整页贴顶条），
 * 最大化 / 还原图标切换、全屏隐藏、失焦变淡，以及浏览器里完全不渲染（页头与侧栏不多任何东西）。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
} from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../../api/client";
import { qk } from "../../api/queries";
import {
	type ChromeControls,
	setWindowChrome,
	type WindowButton,
	type WindowChrome,
	type WindowFlags,
} from "../../lib/window-chrome";
import { getThreadStore } from "../../state/stores";
import { ThreadHeader } from "../thread/ThreadHeader";
import {
	ChromeBar,
	ChromeLead,
	SidebarChromeRow,
	WindowControls,
} from "./WindowControls";

/** 假的标题栏适配：`set` 模拟壳推来的 `window` 事件。 */
function install(
	controls: ChromeControls,
	buttons: WindowButton[] = controls.startsWith("custom")
		? ["minimize", "maximize", "close"]
		: [],
	flags: Partial<WindowFlags> = {},
) {
	let f: WindowFlags = {
		maximized: false,
		fullscreen: false,
		focused: true,
		...flags,
	};
	const subs = new Set<() => void>();
	const wc: WindowChrome = {
		chrome: {
			platform: controls === "native-left" ? "macos" : "linux",
			controls,
			buttons,
			inset: {
				top: 48,
				left: controls === "native-left" ? 84 : 0,
				right: 0,
			},
		},
		getFlags: () => f,
		subscribe: (cb) => {
			subs.add(cb);
			return () => subs.delete(cb);
		},
		act: vi.fn(),
	};
	act(() => setWindowChrome(wc));
	return {
		wc,
		set(next: Partial<WindowFlags>) {
			f = { ...f, ...next };
			act(() => {
				for (const cb of subs) cb();
			});
		},
	};
}

afterEach(() => {
	act(() => setWindowChrome(null));
});

const group = (side: "left" | "right") =>
	document.querySelector(`[data-window-controls=${side}]`);
const order = (side: "left" | "right") =>
	Array.from(
		group(side)?.querySelectorAll("[data-window-control]") ?? [],
		(b) => b.getAttribute("data-window-control"),
	);

function Shell() {
	return (
		<>
			<SidebarChromeRow />
			<header>
				<ChromeLead />
			</header>
			<ChromeBar />
		</>
	);
}

describe("各 controls 取值", () => {
	it("custom-right：右侧三个按钮（最小化 / 最大化 / 关闭），左侧与侧栏标题行都没有", () => {
		install("custom-right");
		render(<Shell />);
		expect(order("right")).toEqual(["minimize", "maximize", "close"]);
		expect(group("left")).toBeNull();
		expect(document.querySelector("[data-chrome-row=sidebar]")).toBeNull();
		expect(document.querySelector("[data-chrome-lead]")).toBeNull();
		// 贴顶条整条可拖动
		expect(document.querySelector("[data-chrome-row=bar]")).toHaveAttribute(
			"data-window-drag",
			"",
		);
	});

	it("custom-left：按 GTK 给的顺序排在左侧（侧栏标题行、页头左端、贴顶条），右侧没有", () => {
		install("custom-left", ["close", "minimize", "maximize"]);
		render(<Shell />);
		const groups = document.querySelectorAll("[data-window-controls=left]");
		expect(groups).toHaveLength(3);
		for (const g of groups)
			expect(
				Array.from(g.querySelectorAll("[data-window-control]"), (b) =>
					b.getAttribute("data-window-control"),
				),
			).toEqual(["close", "minimize", "maximize"]);
		expect(group("right")).toBeNull();
		const row = document.querySelector("[data-chrome-row=sidebar]");
		expect(row).toHaveAttribute("data-window-drag", "");
		expect(row?.querySelector("[data-window-controls=left]")).not.toBeNull();
	});

	it("native-left（macOS）：不画按钮；侧栏留出 48px 标题行，页头左端让出红绿灯的宽度", () => {
		install("native-left");
		render(<Shell />);
		expect(document.querySelector("[data-window-control]")).toBeNull();
		const row = document.querySelector("[data-chrome-row=sidebar]");
		expect(row).not.toBeNull();
		expect(row?.className).toContain("h-12");
		// jsdom 不做布局：页头左缘按 0 算，让出 inset.left 整段
		expect(document.querySelector("[data-chrome-lead=native]")).toHaveStyle({
			width: "84px",
		});
	});

	it("浏览器（没有标题栏适配）与 controls=none：什么都不渲染", () => {
		const { container } = render(<Shell />);
		expect(container.innerHTML).toBe("<header></header>");
		cleanup();
		install("none");
		const again = render(<Shell />);
		expect(again.container.innerHTML).toBe("<header></header>");
	});
});

describe("窗口状态", () => {
	it("最大化后图标与名称切到「还原」，点击仍发 toggleMaximize；其它按钮各发各的动作", () => {
		const { wc, set } = install("custom-right");
		render(<WindowControls side="right" />);
		const max = screen.getByRole("button", { name: "最大化" });
		fireEvent.click(max);
		expect(wc.act).toHaveBeenLastCalledWith("toggleMaximize");
		set({ maximized: true });
		expect(screen.queryByRole("button", { name: "最大化" })).toBeNull();
		const restore = screen.getByRole("button", { name: "还原" });
		expect(restore).toHaveAttribute("data-window-control", "maximize");
		fireEvent.click(restore);
		expect(wc.act).toHaveBeenLastCalledWith("toggleMaximize");
		fireEvent.click(screen.getByRole("button", { name: "最小化" }));
		expect(wc.act).toHaveBeenLastCalledWith("minimize");
		fireEvent.click(screen.getByRole("button", { name: "关闭窗口" }));
		expect(wc.act).toHaveBeenLastCalledWith("close");
		// 关闭悬停变 err 色，其余悬停出中性底色
		expect(
			screen.getByRole("button", { name: "关闭窗口" }).className,
		).toContain("hover:text-err");
	});

	it("全屏时按钮、侧栏标题行、页头让位都隐藏；退出全屏后恢复", () => {
		const right = install("custom-right");
		render(<Shell />);
		expect(group("right")).not.toBeNull();
		right.set({ fullscreen: true });
		expect(group("right")).toBeNull();
		expect(document.querySelector("[data-chrome-row]")).toBeNull();
		right.set({ fullscreen: false });
		expect(group("right")).not.toBeNull();

		const mac = install("native-left", [], { fullscreen: true });
		expect(document.querySelector("[data-chrome-row=sidebar]")).toBeNull();
		expect(document.querySelector("[data-chrome-lead]")).toBeNull();
		mac.set({ fullscreen: false });
		expect(document.querySelector("[data-chrome-row=sidebar]")).not.toBeNull();
	});

	it("窗口失焦时整组变淡，聚焦后恢复", () => {
		const { set } = install("custom-right");
		render(<WindowControls side="right" />);
		expect(group("right")).toHaveAttribute("data-focused");
		set({ focused: false });
		expect(group("right")).not.toHaveAttribute("data-focused");
		expect(group("right")?.className).toContain("opacity-50");
	});
});

describe("线程页头", () => {
	function renderHeader(props: {
		chromeLeft?: boolean;
		chromeRight?: boolean;
	}) {
		const id = "T-chrome";
		getThreadStore(id)
			.getState()
			.patchLocal({ thread: { id, title: "标题栏" } as Thread });
		const client = new QueryClient({
			defaultOptions: { queries: { retry: false, staleTime: Infinity } },
		});
		client.setQueryData(qk.projects, { projects: [] });
		render(
			<QueryClientProvider client={client}>
				<MemoryRouter>
					<ThreadHeader threadId={id} sidebarToggle {...props} />
				</MemoryRouter>
			</QueryClientProvider>,
		);
		return document.querySelector("header") as HTMLElement;
	}

	it("custom-right：按钮排在页头最右（「更多操作」之后），页头整条可拖动", () => {
		install("custom-right");
		const header = renderHeader({ chromeLeft: true, chromeRight: true });
		expect(header).toHaveAttribute("data-window-drag", "");
		expect(header.lastElementChild).toBe(group("right"));
		expect(group("left")).toBeNull();
	});

	it("custom-left：按钮排在页头最左（侧栏开关之前）", () => {
		install("custom-left", ["close", "minimize", "maximize"]);
		const header = renderHeader({ chromeLeft: true, chromeRight: true });
		const first = Array.from(header.children).find(
			(c) => !c.classList.contains("veil"),
		);
		expect(first).toBe(group("left"));
		expect(group("right")).toBeNull();
	});

	it("右面板展开时页头不承载按钮（chromeRight=false）；浏览器里页头与以前相同", () => {
		install("custom-right");
		renderHeader({ chromeLeft: true, chromeRight: false });
		expect(group("right")).toBeNull();
		cleanup();
		act(() => setWindowChrome(null));
		const header = renderHeader({ chromeLeft: true, chromeRight: true });
		expect(header.querySelector("[data-window-controls]")).toBeNull();
		expect(header.querySelector("[data-chrome-lead]")).toBeNull();
	});
});
