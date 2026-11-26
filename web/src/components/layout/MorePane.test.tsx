/**
 * 「更多」分区侧栏（M9.6）：条目与 `MORE_ENTRIES` 一致；「设置」一行（无组标题）在前，「概览」组在后；当前页高亮；
 * 连接行只在断线时出现（在第一行）；明暗切换；注册表里的每个 labelKey 在两种语言里都有。
 */
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { resources } from "../../i18n";
import { connectionStore } from "../../state/stores";
import { themeStore } from "../../state/theme";
import { uiStore } from "../../state/ui";
import { MorePane } from "./MorePane";
import { MORE_ENTRIES } from "./more-entries";

function Where() {
	return <span data-testid="where">{useLocation().pathname}</span>;
}

function mount(path = "/settings/secrets") {
	return render(
		<MemoryRouter initialEntries={[path]}>
			<Routes>
				<Route
					path="*"
					element={
						<>
							<MorePane />
							<Where />
						</>
					}
				/>
			</Routes>
		</MemoryRouter>,
	);
}

const more = () => screen.getByTestId("sidebar-more");
const entryLinks = () =>
	Array.from(more().querySelectorAll<HTMLElement>("[data-more-entry]"));

function lookup(tree: unknown, key: string): unknown {
	return key
		.split(".")
		.reduce<unknown>(
			(node, part) =>
				node && typeof node === "object"
					? (node as Record<string, unknown>)[part]
					: undefined,
			tree,
		);
}

beforeEach(() => {
	connectionStore.setState({ status: "open" });
	themeStore.getState().setMode("system");
});

afterEach(() => {
	window.innerWidth = 1024;
});

describe("MorePane", () => {
	it("条目与 MORE_ENTRIES 一致、按组排列；「设置」只占一行且没有组标题", () => {
		mount();
		expect(entryLinks().map((a) => a.dataset.moreEntry)).toEqual(
			MORE_ENTRIES.map((e) => e.key),
		);
		expect(entryLinks().map((a) => a.getAttribute("href"))).toEqual(
			MORE_ENTRIES.map((e) => e.to),
		);
		expect(entryLinks().map((a) => a.textContent)).toEqual(
			MORE_ENTRIES.map((e) =>
				lookup(resources["zh-CN"].translation, e.labelKey),
			),
		);
		const groups = Array.from(
			more().querySelectorAll<HTMLElement>("[data-more-group]"),
		);
		expect(groups.map((g) => g.dataset.moreGroup)).toEqual([
			"settings",
			"overview",
		]);
		const [settings, overview] = groups as [HTMLElement, HTMLElement];
		expect(within(settings).getAllByRole("link")).toHaveLength(1);
		expect(settings.firstElementChild).toHaveAttribute(
			"data-more-entry",
			"settings",
		);
		expect(settings.firstElementChild).toHaveAttribute("href", "/settings");
		expect(overview.firstElementChild?.textContent).toBe("概览");
		for (const g of groups)
			for (const a of within(g).getAllByRole("link"))
				expect(
					MORE_ENTRIES.find((e) => e.key === a.dataset.moreEntry)?.group,
				).toBe(g.dataset.moreGroup);
	});

	it("当前页高亮（含子路径），点条目导航", () => {
		mount("/settings/secrets");
		const settings = entryLinks().find(
			(a) => a.dataset.moreEntry === "settings",
		);
		expect(settings).toHaveAttribute("aria-current", "page");
		expect(
			entryLinks().filter((a) => a.getAttribute("aria-current") === "page"),
		).toHaveLength(1);
		const boxes = entryLinks().find((a) => a.dataset.moreEntry === "boxes");
		fireEvent.click(boxes as HTMLElement);
		expect(screen.getByTestId("where")).toHaveTextContent("/boxes");
		expect(boxes).toHaveAttribute("aria-current", "page");
		expect(settings).not.toHaveAttribute("aria-current");
	});

	it("统计入口（M10）：在「概览」组里，链到 /stats，统计页的子路径下高亮", () => {
		mount("/stats/orbs");
		const stats = entryLinks().find((a) => a.dataset.moreEntry === "stats");
		expect(stats).toHaveAttribute("href", "/stats");
		expect(stats).toHaveTextContent("统计");
		expect(stats?.closest("[data-more-group]")).toHaveAttribute(
			"data-more-group",
			"overview",
		);
		expect(stats).toHaveAttribute("aria-current", "page");
	});

	it("连接行在 open 时不存在；connecting 时作为第一行出现并显示「正在连接…」", () => {
		mount();
		expect(screen.queryByTestId("sidebar-connection")).toBeNull();
		act(() => connectionStore.setState({ status: "connecting" }));
		const row = screen.getByTestId("sidebar-connection");
		expect(more().firstElementChild).toBe(row);
		expect(row).toHaveTextContent("正在连接…");
		act(() => connectionStore.setState({ status: "open" }));
		expect(screen.queryByTestId("sidebar-connection")).toBeNull();
	});

	it("明暗切换：点一下换到下一个模式，行尾显示当前模式", () => {
		mount();
		const toggle = within(more()).getByRole("button", { name: /切换明暗/ });
		expect(toggle).toHaveTextContent("跟随系统");
		fireEvent.click(toggle);
		expect(themeStore.getState().mode).not.toBe("system");
		expect(toggle).not.toHaveTextContent("跟随系统");
	});

	it("手机上点条目后收起下沉侧栏", () => {
		window.innerWidth = 390;
		uiStore.setState({ sidebarOpen: true });
		mount();
		fireEvent.click(entryLinks()[0] as HTMLElement);
		expect(uiStore.getState().sidebarOpen).toBe(false);
	});

	it("注册表里的每个 labelKey 在 zh-CN 与 en 里都有文案", () => {
		for (const e of MORE_ENTRIES) {
			expect(typeof lookup(resources["zh-CN"].translation, e.labelKey)).toBe(
				"string",
			);
			expect(typeof lookup(resources.en.translation, e.labelKey)).toBe(
				"string",
			);
		}
	});
});
