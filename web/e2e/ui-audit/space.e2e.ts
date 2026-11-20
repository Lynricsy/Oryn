/** 空间分区：Space 页（聊天、进度、定时、线程卡片）与它的菜单、对话框。 */
import type { Page, TestInfo } from "@playwright/test";
import { isMobile, lastOverlay, settle, surface } from "./audit";

const SPACE = `/space/spc_01JD0000000000000000SPC001`;
const READY = "[data-testid=transcript]";

/** 手机上右列在「线程」标签里（已经切过去就不再点：重开时标签栏可能正被关闭中的抽屉盖着）。 */
async function side(page: Page, info: TestInfo) {
	if (!isMobile(info)) return;
	const progress = page.locator("[data-testid=space-progress]");
	if (!(await progress.isVisible())) {
		await page
			.locator("[data-space-tab=threads]")
			.filter({ visible: true })
			.first()
			.click();
		await progress.waitFor();
	}
	await settle(page);
}

surface({ id: "space.page", path: SPACE, ready: READY });
surface({
	id: "space.side",
	path: SPACE,
	ready: READY,
	open: async (page, info) => {
		await side(page, info);
		return undefined;
	},
});
surface({
	id: "space.menu",
	path: SPACE,
	ready: READY,
	open: async (page) => {
		await page.getByRole("button", { name: "空间操作" }).click();
		return lastOverlay(page);
	},
});
surface({
	id: "space.edit-dialog",
	path: SPACE,
	ready: READY,
	open: async (page) => {
		await page.getByRole("button", { name: "空间操作" }).click();
		// 基线手机上这份菜单一直在挪动（Playwright 判为 not stable，强制点也落空），直接派发 click；after 上菜单进顶层后稳定
		await lastOverlay(page)
			.getByText("编辑空间", { exact: true })
			.dispatchEvent("click");
		return page.getByRole("dialog");
	},
	noReopen: true,
});
surface({
	id: "space.thread-menu",
	path: SPACE,
	ready: READY,
	open: async (page, info) => {
		await side(page, info);
		await page.getByRole("button", { name: "线程操作" }).first().click();
		return lastOverlay(page);
	},
	noReopen: true,
});
surface({
	id: "space.add-thread",
	path: SPACE,
	ready: READY,
	open: async (page, info) => {
		await side(page, info);
		await page.getByRole("button", { name: "加入线程" }).click();
		await settle(page);
		return undefined;
	},
});
surface({
	id: "space.schedule-dialog",
	path: SPACE,
	ready: READY,
	open: async (page, info) => {
		await side(page, info);
		await page
			.getByRole("button", { name: /的操作$/ })
			.first()
			.click();
		await page.getByRole("menuitem", { name: "编辑" }).click();
		await page.getByRole("dialog").getByRole("textbox").first().waitFor();
		await settle(page);
		return page.getByRole("dialog");
	},
});
surface({
	id: "space.schedule-menu",
	path: SPACE,
	ready: READY,
	open: async (page, info) => {
		await side(page, info);
		await page
			.getByRole("button", { name: /的操作$/ })
			.first()
			.click();
		return lastOverlay(page);
	},
});
