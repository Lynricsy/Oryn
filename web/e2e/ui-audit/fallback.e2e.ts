/**
 * 退化路径：没有 Popover API 的 WebKit（tauri.conf.json 支持 iOS 15 / macOS 12，showPopover 到 Safari 17 才有）。
 * ui/Floating 在这里退回 fixed + z-60、留在原层叠树里；挑「对话框里」「隔离的托盘里」「手机下沉卡片里」三种最容易出事的位置验证。
 * 手机上首页模型选择走 Dialog、线程标题菜单不是 Floating，这两项只在桌面跑；手机改测下沉卡片平移时的执行位置下拉。
 */
import { expect, lastOverlay, PHASE, settle, surface } from "./audit";

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
		// 对话框面板进入时淡入 180ms：用户不可能在这段时间里点到胶囊，等它落定再点（问题记录 fallback #2）
		await page.getByRole("dialog").waitFor();
		await settle(page);
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
	only: "desktop",
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

/**
 * 手机：下沉卡片 `.sink-card` 用独立的 `translate` 平移，fixed 后代的包含块变成卡片。
 * 把卡片固定推开约 30% 抽屉宽，断言执行位置下拉仍贴着胶囊（竖直贴边、水平按锚点并夹在视口内）。
 */
const SHIFT_SINK = `${NO_POPOVER}
document.addEventListener("DOMContentLoaded", () => {
	const s = document.createElement("style");
	s.textContent = ".sink-card { --sink-p: 0.3 !important; transition: none !important; }";
	document.head.append(s);
});`;

surface({
	id: "fallback.sink-runner-picker",
	path: "/",
	ready: "textarea",
	only: "mobile",
	init: SHIFT_SINK,
	open: async (page) => {
		await page.getByRole("button", { name: /^local/ }).click();
		// 基线代码还没有 Floating（旧的 absolute 弹层），只截图记录、不做贴锚断言
		if (PHASE === "before") return lastOverlay(page);
		const overlay = page.locator("[data-floating]").last();
		await overlay.waitFor();
		// 进入动画带 scale/translate，量 getBoundingClientRect 前等它结束
		await settle(page);
		const gapToAnchor = await overlay.evaluate((f) => {
			const card = document.querySelector(".sink-card");
			const shifted = card ? card.getBoundingClientRect().left : 0;
			const a = (f.parentElement as HTMLElement).getBoundingClientRect();
			const r = f.getBoundingClientRect();
			const vw = document.documentElement.clientWidth;
			const left = Math.min(Math.max(a.left, 8), vw - 8 - r.width);
			const vertical =
				f.dataset.side === "top"
					? r.bottom - (a.top - 8)
					: r.top - (a.bottom + 4);
			return {
				shifted,
				dx: Math.abs(r.left - left),
				dy: Math.abs(vertical),
				a: [a.left, a.width],
				r: [r.left, r.width],
				vw,
				style: f.getAttribute("style"),
			};
		});
		// 卡片确实被推开了，弹层仍贴锚
		expect(gapToAnchor.shifted).toBeGreaterThan(50);
		expect(gapToAnchor.dx, JSON.stringify(gapToAnchor)).toBeLessThanOrEqual(2);
		expect(gapToAnchor.dy).toBeLessThanOrEqual(2);
		return overlay;
	},
});
