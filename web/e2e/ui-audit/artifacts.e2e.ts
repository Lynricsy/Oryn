/** 产物分区：总览（筛选 / 显示 / 导入 / 卡片菜单）、最近删除、查看器（更多菜单、版本、评论、分享、演示、画布）、打开页。 */
import type { Page, TestInfo } from "@playwright/test";
import { isMobile, lastOverlay, settle, surface } from "./audit";

const ART = {
	web: "art_01JDNAWEB0000000000000000A",
	document: "art_01JDNADOC0000000000000000B",
	slides: "art_01JDNASLIDES00000000000000",
	canvas: "art_01JDNACANVAS00000000000000",
};
const CARD = "[data-testid=artifact-card]";
/** 查看器就绪 = 内容帧已显现（帧加载完或超过显现延迟才去掉 opacity-0）：只等查看器外壳会截到转圈。 */
const VIEWER = "[data-testid=artifact-frame]:not(.opacity-0)";

/** 经 mock 接口给页面夹具建两条评论（一条锚在标题上，一条整页），再打开评论侧栏。 */
async function openComments(page: Page, info: TestInfo) {
	await page.evaluate(async (id) => {
		const post = (body: unknown) =>
			fetch(`/api/v1/artifacts/${id}/comments`, {
				method: "POST",
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify(body),
			});
		const anchor = {
			version: 3,
			path: "index.html",
			page: null,
			missing: false,
		};
		await post({
			anchor: { ...anchor, offset: null, tag: "h1", text: "本周习惯打卡" },
			body: "标题再醒目一点，换成品牌色；副标题的灰色在暗色主题下对比度不够，读起来很吃力，顺手一起调一下。",
		});
		await post({
			anchor: { ...anchor, offset: null, tag: null, text: "" },
			body: "整体留白偏少，卡片之间拉开一些。",
		});
	}, ART.web);
	await page.getByTestId("artifact-version").click();
	if (isMobile(info))
		await page.getByRole("tab", { name: /^评论/ }).first().click();
	else await page.getByTestId("artifact-side-comments").click();
	await page
		.locator("[data-testid=artifact-side]")
		.getByText("整体留白偏少", { exact: false })
		.waitFor();
	return page.getByTestId("artifact-side");
}

surface({ id: "artifacts.list", path: "/artifacts", ready: CARD });
surface({
	id: "artifacts.list.filter",
	path: "/artifacts",
	ready: CARD,
	only: "desktop",
	open: async (page) => {
		await page.getByTestId("artifacts-filter").click();
		return lastOverlay(page);
	},
});
surface({
	id: "artifacts.list.display",
	path: "/artifacts",
	ready: CARD,
	only: "desktop",
	open: async (page) => {
		await page.getByTestId("artifacts-display").click();
		return lastOverlay(page);
	},
});
surface({
	id: "artifacts.list.import",
	path: "/artifacts",
	ready: CARD,
	only: "desktop",
	open: async (page) => {
		await page.getByTestId("artifacts-import").click();
		return lastOverlay(page);
	},
});
surface({
	id: "artifacts.list.options",
	path: "/artifacts",
	ready: CARD,
	only: "mobile",
	open: async (page) => {
		await page.getByTestId("artifacts-options").click();
		return lastOverlay(page);
	},
});
surface({
	id: "artifacts.list.card-menu",
	path: "/artifacts",
	ready: CARD,
	open: async (page) => {
		const card = page.locator(`li:has(${CARD})`).first();
		await card.hover();
		await card.getByTestId("artifact-menu").click();
		return lastOverlay(page);
	},
});
surface({
	id: "artifacts.list.search",
	path: "/artifacts",
	ready: CARD,
	open: async (page) => {
		const search = page.getByTestId("artifacts-search");
		if (await search.isVisible()) await search.click();
		await page.getByRole("searchbox").fill("习惯");
		await settle(page);
		return undefined;
	},
});
surface({
	id: "artifacts.list.bottom",
	path: "/artifacts",
	ready: CARD,
	open: async (page) => {
		await page
			.getByTestId("artifacts-scroll")
			.evaluate((e) => e.scrollTo({ top: e.scrollHeight }));
		await settle(page);
		return undefined;
	},
});
surface({ id: "artifacts.trash", path: "/artifacts/trash", ready: CARD });
surface({
	id: "artifacts.trash.card-menu",
	path: "/artifacts/trash",
	ready: CARD,
	open: async (page) => {
		const card = page.locator(`li:has(${CARD})`).first();
		await card.hover();
		await card.getByTestId("artifact-menu").click();
		return lastOverlay(page);
	},
});
surface({ id: "artifacts.web", path: `/artifacts/${ART.web}`, ready: VIEWER });
surface({
	id: "artifacts.web.menu",
	path: `/artifacts/${ART.web}`,
	ready: VIEWER,
	open: async (page) => {
		await page
			.getByTestId("artifact-viewer")
			.getByTestId("artifact-menu")
			.first()
			.click();
		return lastOverlay(page);
	},
});
surface({
	id: "artifacts.web.history",
	path: `/artifacts/${ART.web}?history=1`,
	ready: "[data-testid=artifact-side]",
	open: async (page) => page.getByTestId("artifact-side"),
	noReopen: true,
});
surface({
	id: "artifacts.web.comments",
	path: `/artifacts/${ART.web}`,
	ready: VIEWER,
	open: openComments,
	noReopen: true,
});
surface({
	id: "artifacts.web.comment-mode",
	path: `/artifacts/${ART.web}`,
	ready: VIEWER,
	only: "desktop",
	open: async (page) => {
		await page.getByTestId("artifact-mode-comment").click();
		await settle(page);
		return undefined;
	},
});
surface({
	id: "artifacts.document",
	path: `/artifacts/${ART.document}`,
	ready: VIEWER,
});
surface({
	id: "artifacts.document.share",
	path: `/artifacts/${ART.document}`,
	ready: VIEWER,
	open: async (page) => {
		await page.getByTestId("artifact-share").click();
		await page.getByTestId("share-url").first().waitFor();
		return lastOverlay(page);
	},
});
surface({
	id: "artifacts.web.share",
	path: `/artifacts/${ART.web}`,
	ready: VIEWER,
	open: async (page) => {
		await page.getByTestId("artifact-share").click();
		await page.getByTestId("share-publish").first().waitFor();
		return lastOverlay(page);
	},
});
surface({
	id: "artifacts.slides",
	path: `/artifacts/${ART.slides}`,
	ready: VIEWER,
});
surface({
	id: "artifacts.web.presenter",
	path: `/artifacts/${ART.web}`,
	ready: VIEWER,
	open: async (page) => {
		await page.getByTestId("artifact-present").click();
		return page.getByTestId("artifact-presenter");
	},
});
surface({
	id: "artifacts.canvas",
	path: `/artifacts/${ART.canvas}`,
	ready: "[data-testid=canvas-editor]",
});
surface({
	id: "artifacts.canvas.zoom-menu",
	// 手机上画布只读，工具条换成视图条（CompactBar：只有适合全部、评论，多页时才有页面菜单）
	only: "desktop",
	path: `/artifacts/${ART.canvas}`,
	ready: "[data-testid=canvas-editor]",
	open: async (page) => {
		await page.getByTestId("canvas-zoom-menu").click();
		return lastOverlay(page);
	},
});
surface({
	id: "artifacts.canvas.sheet-menu",
	only: "desktop",
	path: `/artifacts/${ART.canvas}`,
	ready: "[data-testid=canvas-editor]",
	open: async (page) => {
		await page.getByTestId("canvas-sheet-menu").click();
		return lastOverlay(page);
	},
});
surface({
	id: "artifacts.canvas.layers",
	only: "desktop",
	path: `/artifacts/${ART.canvas}`,
	ready: "[data-testid=canvas-editor]",
	open: async (page) => {
		await page.getByTestId("canvas-layers-toggle").click();
		await page.getByTestId("canvas-layers").waitFor();
		await settle(page);
		return undefined;
	},
});
surface({
	id: "artifacts.open.gone",
	path: "/artifacts/open/zzzzzzzzzzzzzzzzzzzzzzzzzz",
	ready: "text=这个页面已不在线上",
});
