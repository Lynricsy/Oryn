/**
 * 线程页右侧面板（信息 / 改动 / 文件 / 终端 / Portal / 桌面 / Artifacts）及其中的菜单、弹层、对话框。
 * 面板本身按「浮层」登记（根 = 右侧面板 aside）：这样会断言遮挡、玻璃，并把面板里的滚动区滚到底再截一张。
 * 宽屏点「信息面板」再点 `[data-panel-tab]`；手机经页头视图菜单 `thread-view-menu` → `[data-view]`。
 */
import type { Locator, Page } from "@playwright/test";
import { isMobile, lastOverlay, PHASE, settle, surface } from "./audit";

const MAIN = "T-0193a1b2-0000-7000-8000-00000000a001";
/** 选用的「GitHub 测试号」被另一台 orb 占用的线程；再切到同样被占用的「工作账号」会弹冲突对话框 */
const IN_USE = "T-0193a1b2-0000-7000-8000-00000000a003";
const THREAD = `/t/${MAIN}`;
const READY = "[data-testid=transcript]";

type Tab =
	| "info"
	| "changes"
	| "files"
	| "terminal"
	| "portal"
	| "desktop"
	| "artifacts";

const aside = (page: Page) =>
	page.getByRole("complementary", { name: "右侧面板" });

/** 切到右侧面板的指定标签，返回面板根。 */
async function openPanel(page: Page, tab: Tab): Promise<Locator> {
	const views = page.locator("[data-testid=thread-view-menu]").first();
	if (await views.isVisible().catch(() => false)) {
		await views.click();
		const item = page.locator(`[data-view=${tab}]`);
		await item.waitFor({ timeout: 15_000 });
		if ((await item.getAttribute("aria-checked")) === "true")
			await page.keyboard.press("Escape");
		else await item.click();
	} else {
		const btn = page.locator(`[data-panel-tab=${tab}]`);
		if (!(await btn.isVisible().catch(() => false)))
			await page.getByRole("button", { name: "信息面板", exact: true }).click();
		await btn.waitFor({ timeout: 15_000 });
		if ((await btn.getAttribute("aria-selected")) !== "true") await btn.click();
	}
	const root = aside(page);
	await root.waitFor({ timeout: 15_000 });
	await settle(page);
	return root;
}

/** 触屏长按（上下文菜单的触屏入口：pointerdown 后 500ms 不动再抬起）。 */
async function longPress(page: Page, target: Locator) {
	const fire = (type: string) =>
		target.evaluate((el, type) => {
			const r = el.getBoundingClientRect();
			el.dispatchEvent(
				new PointerEvent(type, {
					bubbles: true,
					cancelable: true,
					pointerType: "touch",
					pointerId: 7,
					isPrimary: true,
					clientX: r.left + Math.min(40, r.width / 2),
					clientY: r.top + r.height / 2,
				}),
			);
		}, type);
	await fire("pointerdown");
	await page.waitForTimeout(650);
	await fire("pointerup");
}

async function openDesktop(page: Page) {
	const root = await openPanel(page, "desktop");
	await page
		.locator("[data-testid=desktop-status][data-state=connected]")
		.waitFor({ timeout: 30_000 });
	await settle(page);
	return root;
}

// ── 信息 ────────────────────────────────────────────────────────────────

surface({
	id: "panels.info",
	path: THREAD,
	ready: READY,
	noReopen: true,
	open: (page) => openPanel(page, "info"),
});

// 900 宽（窄桌面窗口：侧栏 + 转录 + 面板三栏挤在一起）：入口行右侧的状态标签最容易放不下
surface({
	id: "panels.info.narrow",
	path: THREAD,
	ready: READY,
	only: "desktop",
	noReopen: true,
	open: async (page) => {
		await page.setViewportSize({ width: 900, height: 860 });
		await settle(page);
		return openPanel(page, "info");
	},
});

surface({
	id: "panels.info.service-logs",
	path: THREAD,
	ready: READY,
	open: async (page) => {
		const root = await openPanel(page, "info");
		await root.getByRole("button", { name: "日志", exact: true }).click();
		return lastOverlay(page);
	},
});

// ── 改动 ────────────────────────────────────────────────────────────────

surface({
	id: "panels.changes",
	path: THREAD,
	ready: READY,
	noReopen: true,
	open: (page) => openPanel(page, "changes"),
});

surface({
	id: "panels.changes.diff",
	path: THREAD,
	ready: READY,
	noReopen: true,
	open: async (page) => {
		const root = await openPanel(page, "changes");
		await root.getByRole("button", { name: /^M main\.ts/ }).click();
		await page.waitForTimeout(600);
		return root;
	},
});

surface({
	id: "panels.changes.sync-menu",
	path: THREAD,
	ready: READY,
	open: async (page) => {
		const root = await openPanel(page, "changes");
		await root.getByRole("button", { name: "同步", exact: true }).click();
		return lastOverlay(page);
	},
});

surface({
	id: "panels.changes.row-menu",
	path: THREAD,
	ready: READY,
	open: async (page, info) => {
		const root = await openPanel(page, "changes");
		const row = root.getByRole("button", { name: /^M README\.md/ });
		if (isMobile(info)) await longPress(page, row);
		else await row.click({ button: "right" });
		return lastOverlay(page);
	},
});

surface({
	id: "panels.changes.compare-branch",
	path: THREAD,
	ready: READY,
	noReopen: true,
	// 「与分支比较…」在面板头部就地展开分支名输入行，不是浮层
	open: async (page) => {
		const root = await openPanel(page, "changes");
		await root
			.getByRole("combobox", { name: "改动范围" })
			.selectOption({ label: "与分支比较…" });
		await page.waitForTimeout(400);
		return root;
	},
});

surface({
	id: "panels.pr-chip",
	path: THREAD,
	ready: READY,
	only: "desktop",
	open: async (page) => {
		await page
			.getByRole("button", { name: /^PR #\d+/ })
			.first()
			.click();
		return lastOverlay(page);
	},
});

// ── 文件 ────────────────────────────────────────────────────────────────

surface({
	id: "panels.files",
	path: THREAD,
	ready: READY,
	noReopen: true,
	open: (page) => openPanel(page, "files"),
});

surface({
	id: "panels.files.row-menu",
	path: THREAD,
	ready: READY,
	open: async (page) => {
		const root = await openPanel(page, "files");
		await root.getByRole("button", { name: "src 的操作" }).click();
		return lastOverlay(page);
	},
});

surface({
	id: "panels.files.viewer",
	path: THREAD,
	ready: READY,
	noReopen: true,
	open: async (page) => {
		const root = await openPanel(page, "files");
		await root.getByRole("treeitem", { name: "README.md" }).click();
		await page.waitForTimeout(800);
		return root;
	},
});

surface({
	id: "panels.files.home",
	path: THREAD,
	ready: READY,
	noReopen: true,
	open: async (page) => {
		const root = await openPanel(page, "files");
		await root.getByRole("tab", { name: "主目录" }).click();
		await page.waitForTimeout(800);
		return root;
	},
});

// ── 终端 ────────────────────────────────────────────────────────────────

surface({
	id: "panels.terminal",
	path: THREAD,
	ready: READY,
	noReopen: true,
	open: async (page) => {
		const root = await openPanel(page, "terminal");
		await page
			.locator("[data-testid=terminal-view]")
			.first()
			.waitFor({ timeout: 20_000 });
		return root;
	},
});

surface({
	id: "panels.terminal.tabs",
	path: THREAD,
	ready: READY,
	noReopen: true,
	open: async (page) => {
		const root = await openPanel(page, "terminal");
		const add = root.getByRole("button", { name: "新建终端", exact: true });
		for (let i = 0; i < 4; i++) {
			await add.click();
			await page.waitForTimeout(300);
		}
		await settle(page);
		return root;
	},
});

// ── Portal ──────────────────────────────────────────────────────────────

surface({
	id: "panels.portal",
	path: THREAD,
	ready: READY,
	noReopen: true,
	open: async (page) => {
		const root = await openPanel(page, "portal");
		await page
			.locator("[data-testid=portal-frame]")
			.waitFor({ timeout: 20_000 });
		return root;
	},
});

surface({
	id: "panels.portal.access",
	path: THREAD,
	ready: READY,
	open: async (page) => {
		const root = await openPanel(page, "portal");
		await root.getByRole("button", { name: "访问权限", exact: true }).click();
		return lastOverlay(page);
	},
});

// ── 桌面 ────────────────────────────────────────────────────────────────

surface({
	id: "panels.desktop",
	path: THREAD,
	ready: READY,
	noReopen: true,
	open: (page) => openDesktop(page),
});

surface({
	id: "panels.desktop.profile-menu",
	path: THREAD,
	ready: READY,
	open: async (page) => {
		await openDesktop(page);
		await page.getByTestId("desktop-profile-menu").click();
		return lastOverlay(page);
	},
});

surface({
	id: "panels.desktop.clipboard",
	path: THREAD,
	ready: READY,
	open: async (page) => {
		const root = await openDesktop(page);
		await root.getByRole("button", { name: "剪贴板", exact: true }).click();
		return lastOverlay(page);
	},
});

surface({
	id: "panels.desktop.more",
	path: THREAD,
	ready: READY,
	open: async (page) => {
		const root = await openDesktop(page);
		await root
			.getByRole("toolbar")
			.getByRole("button", { name: "更多" })
			.click();
		return lastOverlay(page);
	},
});

surface({
	id: "panels.desktop.keyboard",
	path: THREAD,
	ready: READY,
	only: "mobile",
	noReopen: true,
	open: async (page) => {
		const root = await openDesktop(page);
		await page.getByTestId("desktop-keyboard").click();
		await settle(page);
		return root;
	},
});

// 切换 Profile 前的确认：after 是应用内对话框；基线是原生 confirm（浏览器自带、不可审计），before 只截出页面并取消
surface({
	id: "panels.desktop.switch-confirm",
	path: `/t/${IN_USE}`,
	ready: READY,
	noReopen: true,
	open: async (page) => {
		await openDesktop(page);
		if (PHASE === "before") page.on("dialog", (d) => void d.dismiss());
		await page.getByTestId("desktop-profile-menu").click();
		await lastOverlay(page)
			.getByRole("menuitemradio", { name: /^工作账号/ })
			.click();
		if (PHASE === "before") return undefined;
		await page.getByTestId("profile-switch-confirm").waitFor();
		return lastOverlay(page);
	},
});

surface({
	id: "panels.desktop.conflict",
	path: `/t/${IN_USE}`,
	ready: READY,
	noReopen: true,
	open: async (page) => {
		await openDesktop(page);
		// 基线：切换前的确认是原生 confirm，接受；after：点应用内确认框的「切换」
		page.on("dialog", (d) => void d.accept());
		await page.getByTestId("desktop-profile-menu").click();
		await lastOverlay(page)
			.getByRole("menuitemradio", { name: /^工作账号/ })
			.click();
		if (PHASE === "after")
			await page.getByTestId("profile-switch-confirm").click();
		await page.getByTestId("profile-conflict").waitFor({ timeout: 15_000 });
		return lastOverlay(page);
	},
});

// ── Artifacts ───────────────────────────────────────────────────────────

surface({
	id: "panels.artifacts",
	path: THREAD,
	ready: READY,
	noReopen: true,
	open: async (page) => {
		const root = await openPanel(page, "artifacts");
		await settle(page);
		return root;
	},
});

surface({
	id: "panels.artifacts.switch",
	path: THREAD,
	ready: READY,
	open: async (page) => {
		const root = await openPanel(page, "artifacts");
		await root.getByTestId("artifact-switch").click();
		return lastOverlay(page);
	},
});

surface({
	id: "panels.artifacts.menu",
	path: THREAD,
	ready: READY,
	open: async (page) => {
		const root = await openPanel(page, "artifacts");
		await root.getByTestId("artifact-menu").first().click();
		return lastOverlay(page);
	},
});

surface({
	id: "panels.files.upload",
	path: THREAD,
	ready: READY,
	noReopen: true,
	open: async (page) => {
		const root = await openPanel(page, "files");
		// mock 只拦 fetch，上传走 XHR 会打到 vite 上回 401（被当成登出）：把上传请求挂住，队列停在「上传中」
		await page.route(
			(url) => url.pathname.endsWith("/fs/file"),
			async (route) => {
				if (route.request().method() !== "PUT") return route.fallback();
				await Promise.withResolvers<void>().promise;
			},
		);
		// 一批长短不一的文件名，看上传队列的截断、进度与滚动
		const names = [
			"截图 2026-11-20 下午 3.41.27（发布说明配图，最终版，勿删）.png",
			"a.txt",
			"orbd-crash-dump-7f3c-20261120T154127Z.tar.gz",
			"README.md",
			"设计稿-侧栏下沉-手机-v12.fig",
			"data.csv",
		];
		await page.getByTestId("file-upload-input").setInputFiles(
			names.map((name, i) => ({
				name,
				mimeType: "application/octet-stream",
				buffer: Buffer.alloc(2048 * (i + 1), 97),
			})),
		);
		await page.waitForTimeout(500);
		await settle(page);
		return root;
	},
});
