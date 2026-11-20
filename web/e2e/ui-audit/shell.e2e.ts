/**
 * 应用外壳：侧栏（各分区、分组、折叠态）、行上下文菜单、手机下沉侧栏、命令面板、通知铃铛与三类提示条。
 * 登录 / 设置 / 配对页（AuthCard、PairApproval）由 auth 区域（auth.e2e.ts）登记，这里不重复。
 * WindowControls / ChromeBar 只在 Tauri 壳里显示，由 host.e2e.ts 用假宿主登记。
 */
import type { Locator, Page, TestInfo } from "@playwright/test";
import { isMobile, lastOverlay, settle, surface } from "./audit";

const MAIN = "T-0193a1b2-0000-7000-8000-00000000a001";
const SIDEBAR_READY = "nav[aria-label=侧边栏]";

const sidebar = (page: Page) =>
	page.getByRole("navigation", { name: "侧边栏" }).first();

/** 让侧栏可见：桌面常驻；手机是下沉侧栏，点页头的侧栏开关等卡片移开（已打开时不再点）。 */
async function showSidebar(page: Page, info: TestInfo): Promise<Locator> {
	if (isMobile(info)) {
		const card = page.locator("[data-testid=sink-card][data-open]");
		if (!(await card.isVisible().catch(() => false))) {
			await page
				.getByRole("button", { name: "侧边栏", exact: true })
				.first()
				.click();
			await card.waitFor();
			await page.waitForTimeout(500);
			await settle(page);
		}
	}
	return sidebar(page);
}

/** 触屏长按（pointerdown 后 500ms 不动再抬起）。 */
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

/** 切到侧栏的某个分区（底栏胶囊）。 */
async function section(page: Page, info: TestInfo, name: string) {
	await showSidebar(page, info);
	await page
		.getByRole("navigation", { name: "主导航" })
		.getByRole("button", { name, exact: true })
		.click();
	await page.waitForTimeout(400);
	await settle(page);
	return sidebar(page);
}

/** 打开命令面板（侧栏搜索入口；手机首页就是下沉侧栏，同一个入口）。 */
async function palette(page: Page, info: TestInfo, query?: string) {
	await showSidebar(page, info);
	await page.getByTestId("sidebar-search").click();
	const dlg = page.getByRole("dialog").last();
	await dlg.waitFor();
	if (query !== undefined) {
		await page.keyboard.type(query);
		await page.waitForTimeout(500);
	}
	return dlg;
}

/** 在 vite 开发服务器里直接取应用的 store 模块（同一份实例），塞入提示条状态。 */
async function seedToasts(page: Page) {
	await page.evaluate(async (main) => {
		const load = (p: string) => import(/* @vite-ignore */ p);
		const n = await load("/src/state/notifications.ts");
		const store = n.notificationsStore;
		const now = Date.now();
		const toasts = [
			{
				id: "ntf_toast_1",
				kind: "input",
				threadId: main,
				title: "🐳 DOCKER: Docker provider 冻结后 orbd 重连",
				body: "允许执行 `docker exec orb-7f3c pkill -HUP -x orbd`？让冻结后恢复的容器里的 orbd 重新连上控制面。",
				url: `/t/${main}`,
				createdAt: now,
				deliveries: [],
			},
			{
				id: "ntf_toast_2",
				kind: "done",
				threadId: main,
				title: "补 M9 截图矩阵",
				body: "回合已完成",
				url: `/t/${main}`,
				createdAt: now,
				deliveries: [],
			},
			{
				id: "ntf_toast_3",
				kind: "budget",
				title: "Daily budget warning",
				body: "$8.16 of $10.00 spent, past the 80% warning threshold.",
				url: "/stats/usage",
				createdAt: now,
				deliveries: [],
			},
		];
		store.setState({ toasts });
		const t = await load("/src/state/toasts.ts");
		t.showToast({ id: "ua-busy", text: "正在导出「发布说明」…", busy: true });
		t.toastsStore.setState({
			toasts: [
				...t.toastsStore.getState().toasts,
				{
					id: "ua-undo",
					text: "已把「Q4 路线图（第三版，含评审意见与预算拆分）」移到最近删除",
					tone: "neutral",
					busy: false,
					action: { label: "撤销", run() {} },
				},
				{
					id: "ua-err",
					text: "恢复失败：版本 12 已被他人修改",
					tone: "err",
					busy: false,
				},
			],
		});
		const s = await load("/src/state/stores.ts");
		s.connectionStore.setState({
			errors: [
				{
					key: 9001,
					code: "not_found",
					message: "thread not found",
					threadId: main,
				},
				{ key: 9002, code: "internal", message: "subscribe failed: timeout" },
			],
		});
	}, MAIN);
	await page.waitForTimeout(300);
	await settle(page);
}

// ── 侧栏 ────────────────────────────────────────────────────────────────

surface({
	id: "shell.sidebar",
	path: "/",
	ready: SIDEBAR_READY,
	noReopen: true,
	open: (page, info) => showSidebar(page, info),
});

surface({
	id: "shell.sidebar.folds",
	path: "/",
	ready: SIDEBAR_READY,
	noReopen: true,
	open: async (page, info) => {
		const nav = await showSidebar(page, info);
		for (const name of [/^72 小时未活动/, /^稍后提醒/, /^已归档/]) {
			const btn = nav.getByRole("button", { name });
			if ((await btn.getAttribute("aria-expanded")) !== "true")
				await btn.click();
		}
		// 定时折叠行（nightly）也展开
		await nav.getByRole("button", { name: /^nightly/ }).click();
		await page.waitForTimeout(600);
		await settle(page);
		return nav;
	},
});

surface({
	id: "shell.sidebar.collapsed",
	path: `/t/${MAIN}`,
	ready: "[data-testid=transcript]",
	only: "desktop",
	open: async (page) => {
		await page.getByRole("button", { name: "收起侧边栏" }).click();
		await page.waitForTimeout(500);
		await settle(page);
		return undefined;
	},
});

surface({
	id: "shell.sidebar.assistant",
	path: "/",
	ready: SIDEBAR_READY,
	noReopen: true,
	open: (page, info) => section(page, info, "全局助手"),
});

surface({
	id: "shell.sidebar.projects",
	path: "/",
	ready: SIDEBAR_READY,
	noReopen: true,
	open: (page, info) => section(page, info, "项目"),
});

surface({
	id: "shell.sidebar.more",
	path: "/",
	ready: SIDEBAR_READY,
	noReopen: true,
	open: (page, info) => section(page, info, "更多"),
});

// 直接打开列表末尾的设置分区：「更多」侧栏要把高亮行滚进可视范围
surface({
	id: "shell.sidebar.more.deep",
	path: "/settings/about",
	ready: SIDEBAR_READY,
	noReopen: true,
	open: (page, info) => showSidebar(page, info),
});

surface({
	id: "shell.row-menu",
	path: "/",
	ready: SIDEBAR_READY,
	open: async (page, info) => {
		const row = (await showSidebar(page, info))
			.getByRole("link", { name: /^重构 WebSocket 重连/ })
			.first();
		if (isMobile(info)) await longPress(page, row);
		else await row.click({ button: "right" });
		return lastOverlay(page);
	},
});

surface({
	id: "shell.space-new",
	path: "/",
	ready: SIDEBAR_READY,
	open: async (page, info) => {
		await (await showSidebar(page, info))
			.getByRole("button", { name: "新建空间" })
			.click();
		return lastOverlay(page);
	},
});

surface({
	id: "shell.sink",
	path: `/t/${MAIN}`,
	ready: "[data-testid=transcript]",
	only: "mobile",
	noReopen: true,
	open: (page, info) => showSidebar(page, info),
});

// ── 通知与提示 ──────────────────────────────────────────────────────────

surface({
	id: "shell.notifications",
	path: "/",
	ready: SIDEBAR_READY,
	open: async (page, info) => {
		await (await showSidebar(page, info))
			.getByRole("button", { name: /^通知/ })
			.click();
		return lastOverlay(page);
	},
});

surface({
	id: "shell.notifications.empty",
	path: "/",
	ready: SIDEBAR_READY,
	noReopen: true,
	open: async (page, info) => {
		// 空态：直接清空应用里那份通知 store（路径是 vite 开发服务器上的模块 URL，只能运行时加载）
		await page.evaluate(async (url) => {
			const n = await import(/* @vite-ignore */ url);
			n.notificationsStore.setState({ items: [], unread: 0 });
		}, "/src/state/notifications.ts");
		await (await showSidebar(page, info))
			.getByRole("button", { name: /^通知/ })
			.click();
		return lastOverlay(page);
	},
});

surface({
	id: "shell.toasts",
	path: `/t/${MAIN}`,
	ready: "[data-testid=transcript]",
	noReopen: true,
	open: async (page) => {
		await seedToasts(page);
		// 区域本身 pointer-events:none，行间空隙透到页面：断言落在第一条（最长的提问）上
		return page
			.getByRole("region", { name: "通知提示" })
			.locator(":scope > *")
			.first();
	},
});

surface({
	id: "shell.toasts.actions",
	path: `/t/${MAIN}`,
	ready: "[data-testid=transcript]",
	noReopen: true,
	open: async (page) => {
		await seedToasts(page);
		// 提示区本身 pointer-events:none（行间空隙透到页面），断言落在最长的那一条上
		return page.getByTestId("action-toast").nth(1);
	},
});

surface({
	id: "shell.server-errors",
	path: `/t/${MAIN}`,
	ready: "[data-testid=transcript]",
	noReopen: true,
	open: async (page) => {
		await seedToasts(page);
		return page
			.getByRole("region", { name: "通知", exact: true })
			.getByRole("alert")
			.first();
	},
});

// ── 命令面板 ────────────────────────────────────────────────────────────

surface({
	id: "shell.palette",
	path: "/",
	ready: SIDEBAR_READY,
	open: (page, info) => palette(page, info),
});

surface({
	id: "shell.palette.results",
	path: "/",
	ready: SIDEBAR_READY,
	noReopen: true,
	open: (page, info) => palette(page, info, "重"),
});

surface({
	id: "shell.palette.empty",
	path: "/",
	ready: SIDEBAR_READY,
	noReopen: true,
	open: (page, info) => palette(page, info, "zzqqxx 不存在的东西"),
});
