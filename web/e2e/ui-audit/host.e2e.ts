/**
 * 宿主壳：Tauri 桌面 App 的自绘窗口栏（WindowControls / ChromeBar，三个平台、侧栏展开 / 收起、面板、全屏、最大化失焦、256 侧栏宽）、
 * 桌面与手机连接页（Launcher：连接、配对、错误）、手机壳（屏幕圆角 47 的下沉侧栏、贴边对话框、推送卡）。
 * 假宿主与截图脚本共用 scripts/fake-host.ts；窗口栏只在桌面视口跑，手机壳只在手机视口跑。
 */
import type { Page } from "@playwright/test";
import {
	chromeShell,
	fakeTauri,
	LAUNCHER_SERVER,
	mobileHandlers,
} from "../../scripts/fake-host";
import { lastOverlay, settle, surface } from "./audit";

const MAIN = "T-0193a1b2-0000-7000-8000-00000000a001";
const TRANSCRIPT = "[data-testid=transcript]";

/** 线程页右面板：要开就点「信息面板」，要关且开着就点掉。 */
async function panel(page: Page, open: boolean) {
	const toggle = page.getByRole("button", { name: "信息面板", exact: true });
	if (((await toggle.getAttribute("aria-pressed")) === "true") !== open)
		await toggle.click();
	await settle(page);
}

const PLATFORMS = [
	["mac", "macos"],
	["win", "windows"],
	["linux", "linux-left"],
] as const;

for (const [tag, platform] of PLATFORMS) {
	for (const collapsed of [false, true])
		for (const withPanel of [false, true])
			surface({
				id: `host.chrome-${tag}.thread${collapsed ? "-collapsed" : ""}${withPanel ? "-panel" : ""}`,
				path: `/t/${MAIN}`,
				ready: TRANSCRIPT,
				only: "desktop",
				init: chromeShell(platform, { collapsed }),
				open: async (page) => {
					await panel(page, withPanel);
					return undefined;
				},
			});
	surface({
		id: `host.chrome-${tag}.home`,
		path: "/",
		ready: "textarea",
		only: "desktop",
		init: chromeShell(platform),
	});
	surface({
		id: `host.chrome-${tag}.settings`,
		path: "/settings/appearance",
		ready: "main",
		only: "desktop",
		init: chromeShell(platform),
	});
	surface({
		id: `host.chrome-${tag}.launcher`,
		path: "/launcher.html",
		ready: "[data-testid=launcher-url]",
		only: "desktop",
		init: chromeShell(platform, { launcher: true }),
	});
	// 三栏最窄（侧栏 256px，840–1023 宽）
	surface({
		id: `host.chrome-${tag}.narrow`,
		path: `/t/${MAIN}`,
		ready: TRANSCRIPT,
		only: "desktop",
		init: chromeShell(platform),
		open: async (page) => {
			await page.setViewportSize({ width: 900, height: 860 });
			await panel(page, true);
			return undefined;
		},
	});
}

surface({
	id: "host.chrome-mac.fullscreen",
	path: `/t/${MAIN}`,
	ready: TRANSCRIPT,
	only: "desktop",
	init: chromeShell("macos", {
		window: { maximized: false, fullscreen: true, focused: true },
	}),
});

surface({
	id: "host.chrome-win.maximized-blur",
	path: `/t/${MAIN}`,
	ready: TRANSCRIPT,
	only: "desktop",
	init: chromeShell("windows", {
		window: { maximized: true, fullscreen: false, focused: false },
	}),
});

const PAIRING = {
	step: "pairing",
	serverUrl: LAUNCHER_SERVER,
	code: "K7QM-W3XP",
	verifyUrl: `${LAUNCHER_SERVER}/pair`,
	expiresAt: 0,
};
/**
 * 连接页各状态：[名字, 壳给的状态, 只在该状态出现的就绪标识]。不用通用的 h1：启动中的占位卡（正在连接）也有 h1。
 * 错误与更换确认两张卡在基线里没有专属 testid，并列一个按钮文字选择器，before / after 都能等到。
 */
const LAUNCHER_STATES: [string, Record<string, unknown>, string][] = [
	["connect", { step: "connect", serverUrl: "" }, "[data-testid=launcher-url]"],
	[
		"confirm-connect",
		{ step: "confirmConnect", serverUrl: LAUNCHER_SERVER },
		"[data-testid=launcher-confirm-host]",
	],
	["pairing", PAIRING, "[data-testid=launcher-code]"],
	[
		"error",
		{
			step: "error",
			serverUrl: LAUNCHER_SERVER,
			error: { code: "unreachable", message: "error sending request" },
		},
		'[data-testid=launcher-retry], button:has-text("重试")',
	],
	[
		"confirm-change",
		{ step: "confirmChange", serverUrl: LAUNCHER_SERVER },
		'[data-testid=launcher-change-confirm], button:has-text("移除并更换")',
	],
];

for (const [name, state, ready] of LAUNCHER_STATES) {
	surface({
		id: `host.launcher.${name}`,
		path: "/launcher.html",
		ready,
		only: "desktop",
		init: fakeTauri({
			launcher_state: {
				state,
				lang: "zh-CN",
				version: "0.1.0",
				tokenStore: "keyring",
			},
		}),
	});
	surface({
		id: `host.launcher-mobile.${name}`,
		path: "/launcher.html",
		ready,
		only: "mobile",
		init: fakeTauri(
			{
				launcher_state: {
					state,
					lang: "zh-CN",
					version: "0.1.0",
					tokenStore: "keystore",
				},
				host_set_chrome: null,
			},
			false,
			"android",
		),
	});
}

/** 手机壳（android，bridge 2，屏幕四角 47px）：下沉侧栏关 / 开、贴边对话框、推送卡。 */
const MOBILE = fakeTauri(mobileHandlers(47), "android");

surface({
	id: "host.mobile.sink-closed",
	path: `/t/${MAIN}`,
	ready: TRANSCRIPT,
	only: "mobile",
	init: MOBILE,
});

surface({
	id: "host.mobile.sink-open",
	path: `/t/${MAIN}`,
	ready: TRANSCRIPT,
	only: "mobile",
	init: MOBILE,
	open: async (page) => {
		await page
			.getByRole("button", { name: "侧边栏", exact: true })
			.first()
			.click();
		await page.locator("[data-testid=sink-card][data-open]").waitFor();
		await settle(page);
		return undefined;
	},
});

surface({
	id: "host.mobile.corners-dialog",
	path: `/t/${MAIN}`,
	ready: TRANSCRIPT,
	only: "mobile",
	init: MOBILE,
	open: async (page) => {
		await page.getByTestId("thread-title").click();
		await page.getByRole("menuitem", { name: "附件" }).click();
		return lastOverlay(page);
	},
});

surface({
	id: "host.mobile.push-card",
	path: "/settings/notifications",
	ready: "[data-testid=mobile-push-card]",
	only: "mobile",
	init: MOBILE,
});
