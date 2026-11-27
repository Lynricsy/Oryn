/** 首页（新建线程）：输入卡与它的四个胶囊弹层。 */
import { lastOverlay, surface } from "./audit";

surface({ id: "home", path: "/", ready: "textarea" });

surface({
	id: "home.role-picker",
	path: "/",
	ready: "textarea",
	open: async (page) => {
		await page.getByRole("button", { name: /^对象：/ }).click();
		return lastOverlay(page);
	},
});

surface({
	id: "home.model-picker",
	path: "/",
	ready: "textarea",
	open: async (page) => {
		await page.getByRole("button", { name: /^对象：/ }).click();
		// 与 home.role-picker 同一个选择器：这一面看输入筛选后的状态（分组收窄、检视栏跟随高亮）
		await page.locator("[data-model-picker] input").fill("claude");
		return lastOverlay(page);
	},
});

surface({
	id: "home.runner-picker",
	path: "/",
	ready: "textarea",
	open: async (page) => {
		await page.getByRole("button", { name: /^local/ }).click();
		return lastOverlay(page);
	},
});

surface({
	id: "home.profile-picker",
	path: "/",
	ready: "textarea",
	open: async (page) => {
		await page.getByRole("button", { name: /^浏览器 Profile/ }).click();
		return lastOverlay(page);
	},
});

surface({
	id: "home.project-picker",
	path: "/",
	ready: "textarea",
	open: async (page) => {
		await page
			.getByTestId("home-pills")
			.getByRole("button", { name: "无项目" })
			.click();
		return lastOverlay(page);
	},
});
