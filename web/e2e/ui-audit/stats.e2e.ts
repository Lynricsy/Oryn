/** 统计分区：用量 / orb / 产出三个标签与范围菜单、导出菜单、自定义范围与预算对话框、orb 抽屉、图表浮层。 */
import type { Page } from "@playwright/test";
import { isMobile, lastOverlay, settle, surface } from "./audit";

/** 把页内某个元素滚到统计页滚动区顶部（只滚滚动区；scrollIntoView 会连带推走 overflow:hidden 的外壳）。 */
async function scrollTo(page: Page, selector: string) {
	await page
		.locator(selector)
		.first()
		.evaluate((e) => {
			const s = document.querySelector("[data-testid=stats-scroll]");
			if (s)
				s.scrollTop =
					e.getBoundingClientRect().top -
					s.getBoundingClientRect().top +
					s.scrollTop -
					12;
		});
	await settle(page);
	return undefined;
}

/** 点主图倒数第二根柱子 / 某个点，取图表浮层。 */
async function chartTip(page: Page) {
	const bars = page.locator("[data-testid=stats-chart] g[data-bucket]");
	if ((await bars.count()) > 1)
		await bars.nth((await bars.count()) - 2).click();
	else await page.locator("[data-testid=stats-chart] svg").first().click();
	return page.locator("[data-testid=chart-tooltip]");
}

surface({ id: "stats.usage", path: "/stats/usage" });
surface({
	id: "stats.usage.tables",
	path: "/stats/usage",
	ready: "[data-testid=table-model]",
	open: (page) => scrollTo(page, "[data-testid=table-model]"),
});
surface({
	id: "stats.usage.tables-bottom",
	path: "/stats/usage",
	ready: "[data-testid=table-thread]",
	open: async (page) => {
		await page
			.locator("[data-testid=stats-scroll]")
			.evaluate((e) => e.scrollTo({ top: e.scrollHeight }));
		await settle(page);
		return undefined;
	},
});
surface({
	id: "stats.usage.range-menu",
	path: "/stats/usage",
	only: "mobile",
	open: async (page) => {
		await page.getByRole("button", { name: /近 30 天/ }).click();
		return lastOverlay(page);
	},
});
surface({
	id: "stats.usage.custom-range",
	path: "/stats/usage",
	open: async (page, info) => {
		if (isMobile(info)) {
			await page.getByRole("button", { name: /近 30 天/ }).click();
			await page.getByRole("menuitemradio", { name: /自定义/ }).click();
		} else await page.locator("[data-range=custom]").click();
		return page.getByRole("dialog");
	},
});
surface({
	id: "stats.usage.export",
	path: "/stats/usage",
	open: async (page) => {
		await page.getByTestId("stats-export").click();
		return lastOverlay(page);
	},
});
surface({
	id: "stats.usage.budget",
	path: "/stats/usage",
	open: async (page) => {
		await page.getByRole("button", { name: "设置预算" }).first().click();
		return page.getByRole("dialog", { name: "设置预算" });
	},
});
surface({
	id: "stats.usage.tooltip",
	path: "/stats/usage?range=7d&by=project",
	ready: "[data-testid=stats-chart]",
	open: chartTip,
	noReopen: true,
});
surface({ id: "stats.orbs", path: "/stats/orbs" });
surface({
	id: "stats.orbs.list",
	path: "/stats/orbs",
	ready: "[data-orb-row]",
	open: (page) => scrollTo(page, "[data-orb-row]"),
});
surface({
	id: "stats.orbs.drawer",
	path: "/stats/orbs",
	ready: "[data-orb-row]",
	open: async (page) => {
		await page.locator("[data-orb-row]").first().click();
		await page
			.locator("[data-testid=orb-drawer] [data-testid=timeline-strip]")
			.waitFor();
		return page.getByTestId("orb-drawer");
	},
});
surface({
	id: "stats.orbs.tooltip",
	path: "/stats/orbs",
	ready: "[data-testid=stats-chart]",
	open: chartTip,
	noReopen: true,
});
surface({ id: "stats.output", path: "/stats/output" });
surface({
	id: "stats.output.tables",
	path: "/stats/output",
	open: async (page) => {
		await page
			.locator("[data-testid=stats-scroll]")
			.evaluate((e) => e.scrollTo({ top: e.scrollHeight }));
		await settle(page);
		return undefined;
	},
});
surface({
	id: "stats.output.tooltip",
	path: "/stats/output",
	ready: "[data-testid=stats-chart]",
	open: chartTip,
	noReopen: true,
});
