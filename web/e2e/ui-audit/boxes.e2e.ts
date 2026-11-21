/** Boxes 分区（M23）：介绍页、总览、新建对话框、box 页（app 行菜单、运行时段、密钥、悬停预览）、首屏执行位置的 Boxes 分组。 */
import type { Page } from "@playwright/test";
import { expect, lastOverlay, settle, surface } from "./audit";

/** mock 种子里一直运行的那台 box。 */
const WORKBENCH = "/boxes/box_01JDBX00000000000WORKBENCH";
const APP_ROW = "[data-testid=box-app-row]";
/** `bug-tracker` 公开、有预览；预览帧的地址由 mock 种子固定。 */
const PREVIEW_URL = "https://bug-tracker.apps.oryn.test/**";
const PREVIEW_TEXT = "Bug tracker · 12 open issues";
const PREVIEW_HTML = `<!doctype html><html><head><meta charset="utf-8"><meta name="oryn-preview" content="v1; enabled; iframe"><title>Bug tracker</title></head><body style="margin:0;font:16px system-ui;padding:24px;background:#f6f7f9;color:#111"><h1 style="font-size:22px;margin:0 0 12px">${PREVIEW_TEXT}</h1><p>Login page flickers on Safari · #482</p><p>Export to CSV drops the last row · #479</p></body></html>`;

async function openAppMenu(page: Page) {
	await page.getByRole("button", { name: "bug-tracker 的操作" }).click();
	return lastOverlay(page);
}

/** 指针停在 `bug-tracker` 的徽标上，等预览帧里的页面真的渲染出来。 */
async function hoverPreview(page: Page) {
	await page.getByRole("button", { name: "预览 bug-tracker" }).hover();
	await expect(
		page
			.frameLocator("[data-testid=app-preview-frame]")
			.getByText(PREVIEW_TEXT),
	).toBeVisible({ timeout: 15_000 });
	return page.locator("[data-floating]").filter({ visible: true }).last();
}

surface({
	id: "boxes.empty",
	path: "/boxes",
	ready: "[data-testid=boxes-hero]",
	init: `localStorage.setItem("oryn.mock.boxes", "empty");`,
});
surface({
	id: "boxes.list",
	path: "/boxes",
	ready: "[data-testid=boxes-list]",
});
surface({
	id: "boxes.new-dialog",
	path: "/boxes",
	ready: "[data-testid=boxes-list]",
	open: async (page) => {
		await page.getByRole("button", { name: "新建 Box" }).first().click();
		await page.getByTestId("new-box-dialog-body").waitFor();
		return lastOverlay(page);
	},
});
surface({ id: "boxes.detail", path: WORKBENCH, ready: APP_ROW });
surface({
	id: "boxes.detail.app-menu",
	path: WORKBENCH,
	ready: APP_ROW,
	open: openAppMenu,
});
surface({
	id: "boxes.detail.schedule",
	path: WORKBENCH,
	ready: APP_ROW,
	open: async (page) => {
		await page.getByRole("button", { name: "Box 操作" }).click();
		await page.getByRole("menuitem", { name: "运行时段…" }).click();
		return page.getByRole("dialog", { name: "运行时段" });
	},
});
surface({
	id: "boxes.detail.secrets",
	path: WORKBENCH,
	ready: APP_ROW,
	open: async (page) => {
		await openAppMenu(page);
		await page.getByRole("menuitem", { name: "密钥…" }).click();
		return page.getByRole("dialog", { name: /bug-tracker/ });
	},
});
surface({
	id: "boxes.detail.preview",
	path: WORKBENCH,
	ready: APP_ROW,
	// 触屏没有悬停，预览不启用
	only: "desktop",
	open: async (page) => {
		// mock 的 fetch 替身管不到 iframe 的导航：预览地址由 Playwright 给一页固定 HTML
		await page.route(PREVIEW_URL, (route) =>
			route.fulfill({ contentType: "text/html", body: PREVIEW_HTML }),
		);
		return hoverPreview(page);
	},
	// Escape 关掉之后指针还停在徽标上，再悬停没有新的进入事件：先移到角落、等浮层消失
	reopen: async (page) => {
		await page.mouse.move(1, 1);
		await expect(page.getByTestId("app-preview-frame")).toHaveCount(0);
		await settle(page);
		return hoverPreview(page);
	},
});
surface({
	id: "boxes.home-executor",
	path: "/",
	ready: "textarea",
	open: async (page) => {
		// Boxes 分组在弹层底部：先滚到底，首次打开的截图里就有 box 与 app 行
		await page.getByRole("button", { name: /^local/ }).click();
		await page
			.getByTestId("executor-options-body")
			.evaluate((e) => e.scrollTo({ top: e.scrollHeight }));
		return lastOverlay(page);
	},
});
