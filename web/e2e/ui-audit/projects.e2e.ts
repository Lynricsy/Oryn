/** 项目分区：总览（含 ⋯ 菜单与已归档区）、新建、线程子页、项目主页、项目设置各分组（概览与危险操作、环境与快照预热、Agent、GitHub 与触发规则、定时、SSH）及其下拉与对话框。 */
import type { Locator, Page } from "@playwright/test";
import { lastOverlay, settle, surface } from "./audit";

const PRJ = "prj_01JCZ0000000000000000ORYN";
const PAGE = `/projects/${PRJ}`;
const SETTINGS = `${PAGE}/settings`;

/**
 * 只滚元素最近的滚动容器（scrollIntoView 会连带滚动 overflow:hidden 的外壳，把整个应用框推出视口，用户滚不到那种状态）。
 * `end` 时滚到底。
 */
async function scrollWithin(page: Page, el: Locator, end = false) {
	await el.evaluate((e, end) => {
		let s = e.parentElement;
		while (s && !/(auto|scroll)/.test(getComputedStyle(s).overflowY))
			s = s.parentElement;
		if (!s) return;
		s.scrollTop = end
			? s.scrollHeight
			: e.getBoundingClientRect().top -
				s.getBoundingClientRect().top +
				s.scrollTop -
				12;
	}, end);
	await settle(page);
	return undefined;
}

/** 把某张卡片（按标题文字）滚到滚动区顶部。 */
function cardAt(title: string | RegExp) {
	return (page: Page) =>
		scrollWithin(
			page,
			page
				.locator("main")
				.getByText(title, { exact: typeof title === "string" })
				.first(),
		);
}

surface({ id: "projects.list", path: "/projects" });
surface({
	id: "projects.list.menu",
	path: "/projects",
	ready: "[data-testid=project-row]",
	open: async (page) => {
		await page.getByRole("button", { name: "更多操作" }).click();
		return lastOverlay(page);
	},
});
surface({
	id: "projects.list.archived",
	path: "/projects?archived=1",
	ready:
		'[data-testid=project-row][data-project="prj_01JCZ000000000000000LEGACY"]',
});
surface({ id: "projects.new", path: "/projects/new" });
surface({
	id: "projects.threads",
	path: `${PAGE}/threads`,
	ready: "[data-testid=project-thread-row]",
});
surface({
	id: "projects.threads.archived",
	path: `${PAGE}/threads?status=archived`,
	ready: "[data-testid=project-thread-row]",
});
surface({
	id: "projects.home",
	path: PAGE,
	ready: "[data-testid=project-home] [data-testid=project-thread-row]",
});
surface({
	id: "projects.home.bottom",
	path: PAGE,
	ready: "[data-testid=project-home] [data-testid=project-thread-row]",
	open: (page) => scrollWithin(page, page.getByTestId("project-home"), true),
});
surface({
	id: "projects.archived",
	path: "/projects/prj_01JCZ000000000000000LEGACY",
	ready: "[data-testid=project-archived]",
});
surface({
	id: "projects.settings.nav",
	path: SETTINGS,
	ready: "[data-testid=project-settings-list]",
	only: "mobile",
});
surface({ id: "projects.settings.overview", path: `${SETTINGS}/overview` });
surface({
	id: "projects.settings.overview.danger",
	path: `${SETTINGS}/overview`,
	open: cardAt("危险操作"),
});
surface({
	id: "projects.settings.repo-picker",
	path: `${SETTINGS}/overview`,
	open: async (page) => {
		await page.getByRole("button", { name: "从 GitHub 选择" }).click();
		return lastOverlay(page);
	},
});
surface({
	id: "projects.settings.environment",
	path: `${SETTINGS}/environment`,
});
surface({
	id: "projects.settings.accel",
	path: `${SETTINGS}/environment`,
	open: cardAt("快照与预热"),
});
surface({ id: "projects.settings.agent", path: `${SETTINGS}/agent` });
surface({ id: "projects.settings.github", path: `${SETTINGS}/github` });
surface({
	id: "projects.settings.trigger-dialog",
	path: `${SETTINGS}/github`,
	ready: `[data-testid=github-triggers-${PRJ}]`,
	open: async (page) => {
		await page
			.getByTestId(`github-triggers-${PRJ}`)
			.getByRole("button", { name: "新建" })
			.click();
		return page.getByRole("dialog");
	},
});
surface({
	id: "projects.settings.trigger-dialog.model",
	path: `${SETTINGS}/github`,
	ready: `[data-testid=github-triggers-${PRJ}]`,
	open: async (page) => {
		await page
			.getByTestId(`github-triggers-${PRJ}`)
			.getByRole("button", { name: "新建" })
			.click();
		// 等对话框自己的进入动画结束：用户不会在它淡入的 200ms 里点到「添加模型」
		await settle(page);
		const dialog = page.getByTestId("github-trigger-dialog");
		await dialog.getByRole("button", { name: "添加模型" }).click();
		return lastOverlay(page);
	},
	close: async (page) => {
		await page
			.getByTestId("github-trigger-dialog")
			.getByRole("button", { name: "添加模型" })
			.click();
	},
	reopen: async (page) => {
		await page
			.getByTestId("github-trigger-dialog")
			.getByRole("button", { name: "添加模型" })
			.click();
		return lastOverlay(page);
	},
});
surface({
	id: "projects.settings.schedules",
	path: `${SETTINGS}/schedules`,
	ready: `[data-testid=project-schedules-${PRJ}] [data-schedule]`,
});
surface({
	id: "projects.settings.schedule-menu",
	path: `${SETTINGS}/schedules`,
	ready: `[data-testid=project-schedules-${PRJ}] [data-schedule]`,
	open: async (page) => {
		await page
			.locator(`[data-testid=project-schedules-${PRJ}]`)
			.getByRole("button", { name: /的操作$/ })
			.first()
			.click();
		return lastOverlay(page);
	},
});
surface({
	id: "projects.settings.schedule-new",
	path: `${SETTINGS}/schedules`,
	ready: `[data-testid=project-schedules-${PRJ}]`,
	open: async (page) => {
		await page.getByRole("button", { name: "新建定时任务" }).click();
		await page
			.getByRole("dialog")
			.locator("[data-testid=schedule-preview] li")
			.first()
			.waitFor();
		return page.getByRole("dialog");
	},
});
surface({
	id: "projects.settings.schedule-edit",
	path: `${SETTINGS}/schedules`,
	ready: `[data-testid=project-schedules-${PRJ}] [data-schedule]`,
	open: async (page) => {
		await page
			.locator(`[data-testid=project-schedules-${PRJ}]`)
			.getByRole("button", { name: /的操作$/ })
			.first()
			.click();
		await page.getByRole("menuitem", { name: "编辑" }).click();
		await page.getByRole("dialog").getByRole("textbox").first().waitFor();
		await settle(page);
		return page.getByRole("dialog");
	},
});
surface({ id: "projects.settings.ssh", path: `${SETTINGS}/ssh` });
