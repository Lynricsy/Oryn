/** 项目分区：总览（含 ⋯ 菜单与已归档区）、新建、线程子页、项目页各卡片（设置、Agent 配置、GitHub 与触发规则、SSH、环境、快照与预热、服务、定时）及其下拉与对话框。 */
import type { Locator, Page } from "@playwright/test";
import { lastOverlay, settle, surface } from "./audit";

const PRJ = "prj_01JCZ0000000000000000ORYN";
const PAGE = `/projects/${PRJ}`;

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
surface({ id: "projects.page", path: PAGE });
surface({
	id: "projects.page.settings-bottom",
	path: PAGE,
	open: cardAt("setup 脚本"),
});
surface({
	id: "projects.page.agent-config",
	path: PAGE,
	open: cardAt("Agent 配置"),
});
surface({ id: "projects.page.github", path: PAGE, open: cardAt("GitHub") });
surface({ id: "projects.page.ssh", path: PAGE, open: cardAt("SSH 主机") });
surface({ id: "projects.page.accel", path: PAGE, open: cardAt("快照与预热") });
surface({
	id: "projects.page.schedules",
	path: PAGE,
	open: (page) =>
		scrollWithin(
			page,
			page.locator(`[data-testid=project-schedules-${PRJ}]`),
			true,
		),
});
surface({
	id: "projects.page.repo-picker",
	path: PAGE,
	open: async (page) => {
		await page.getByRole("button", { name: "从 GitHub 选择" }).click();
		return lastOverlay(page);
	},
});
surface({
	id: "projects.page.trigger-dialog",
	path: PAGE,
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
	id: "projects.page.trigger-dialog.model",
	path: PAGE,
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
	id: "projects.page.schedule-menu",
	path: PAGE,
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
	id: "projects.page.schedule-new",
	path: PAGE,
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
	id: "projects.page.schedule-edit",
	path: PAGE,
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
