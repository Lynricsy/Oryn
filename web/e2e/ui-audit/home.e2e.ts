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
		await page.getByRole("button", { name: /指定模型/ }).click();
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
