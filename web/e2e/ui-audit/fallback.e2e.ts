/**
 * 退化路径：没有 Popover API 的 WebKit（tauri.conf.json 支持 iOS 15 / macOS 12，showPopover 到 Safari 17 才有）。
 * ui/Floating 在这里退回 fixed + z-60、留在原层叠树里；挑「对话框里」「隔离的托盘里」「手机下沉卡片里」三种最容易出事的位置验证。
 */
import { lastOverlay, surface } from "./audit";

const NO_POPOVER = `delete HTMLElement.prototype.showPopover; delete HTMLElement.prototype.hidePopover; delete HTMLElement.prototype.togglePopover;`;
const MAIN_THREAD = "T-0193a1b2-0000-7000-8000-00000000a001";

surface({
	id: "fallback.schedule-role-picker",
	path: "/settings/automations",
	ready: "main",
	init: NO_POPOVER,
	open: async (page) => {
		await page
			.getByRole("button", { name: "创建", exact: true })
			.first()
			.click();
		await page
			.getByRole("dialog")
			.getByRole("button", { name: /^对象：/ })
			.click();
		return lastOverlay(page);
	},
	reopen: async (page) => {
		await page
			.getByRole("dialog")
			.getByRole("button", { name: /^对象：/ })
			.click();
		return lastOverlay(page);
	},
});

surface({
	id: "fallback.home-model-picker",
	path: "/",
	ready: "textarea",
	init: NO_POPOVER,
	open: async (page) => {
		await page.getByRole("button", { name: /^对象：/ }).click();
		await page.getByRole("button", { name: /指定模型/ }).click();
		return lastOverlay(page);
	},
});

surface({
	id: "fallback.thread-actions",
	path: `/t/${MAIN_THREAD}`,
	ready: "[data-testid=transcript]",
	init: NO_POPOVER,
	open: async (page) => {
		const more = page.getByRole("button", { name: "更多操作" }).first();
		if (await more.isVisible()) await more.click();
		else await page.getByTestId("thread-title").click();
		return lastOverlay(page);
	},
});
