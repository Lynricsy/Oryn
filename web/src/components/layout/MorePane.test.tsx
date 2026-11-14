/**
 * 「更多」分区侧栏（M9.6）：条目与 `MORE_ENTRIES` 一致（浏览器里没有「桌面」，桌面壳里有）、分两组、当前页高亮；
 * 连接行只在断线时出现（在第一行）；明暗切换；注册表里的每个 labelKey 在两种语言里都有。
 */
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { resources } from "../../i18n";
import { host } from "../../lib/host";
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

const h = host as { desktop: unknown };
const realDesktop = h.desktop;

beforeEach(() => {
	connectionStore.setState({ status: "open" });
	themeStore.getState().setMode("system");
});

afterEach(() => {
	h.desktop = realDesktop;
	window.innerWidth = 1024;
});

describe("MorePane", () => {
	it("条目与 MORE_ENTRIES 一致、按组排列；浏览器里没有「桌面」", () => {
		mount();
		const expected = MORE_ENTRIES.filter((e) => !e.desktopOnly);
		expect(entryLinks().map((a) => a.dataset.moreEntry)).toEqual(
			expected.map((e) => e.key),
		);
		expect(entryLinks().map((a) => a.getAttribute("href"))).toEqual(
			expected.map((e) => e.to),
		);
		expect(entryLinks().map((a) => a.textContent)).toEqual(
			expected.map((e) => lookup(resources["zh-CN"].translation, e.labelKey)),
		);
		const groups = Array.from(
			more().querySelectorAll<HTMLElement>("[data-more-group]"),
		);
		expect(groups.map((g) => g.firstElementChild?.textContent)).toEqual([
			"概览",
			"设置",
		]);
		for (const g of groups)
			for (const a of within(g).getAllByRole("link"))
				expect(
					MORE_ENTRIES.find((e) => e.key === a.dataset.moreEntry)?.group,
				).toBe(g.dataset.moreGroup);
	});

	it("桌面壳里出现桌面专属条目", () => {
		h.desktop = {};
		mount();
		expect(entryLinks().map((a) => a.dataset.moreEntry)).toEqual(
			MORE_ENTRIES.map((e) => e.key),
		);
	});

	it("当前页高亮（含子路径），点条目导航", () => {
		mount("/settings/secrets");
		const secrets = entryLinks().find((a) => a.dataset.moreEntry === "secrets");
		expect(secrets).toHaveAttribute("aria-current", "page");
		expect(
			entryLinks().filter((a) => a.getAttribute("aria-current") === "page"),
		).toHaveLength(1);
		const about = entryLinks().find((a) => a.dataset.moreEntry === "about");
		fireEvent.click(about as HTMLElement);
		expect(screen.getByTestId("where")).toHaveTextContent("/settings/about");
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
