/**
 * 假宿主（Tauri 壳）的注入脚本：截图脚本（screenshots.ts）与 UI 审计（e2e/ui-audit/host.e2e.ts）共用。
 * 产出都是 `addInitScript` 用的脚本字符串，在页面脚本之前装好 `window.__TAURI__` 与 `__ORYN_HOST__`。
 */

/**
 * 假桌面 / 移动壳（M6 / M7）：`window.__TAURI__.core.invoke` 按命令表应答，`handlers` 是 `{命令: 应答}` 的 JSON；
 * `host` 非假时另注入 `__ORYN_HOST__`（远程页面里的宿主适配）：`true` / `"desktop"` = linux 桌面壳，
 * `"android"` = 移动壳（bridge 2）；连接页不需要它，`launcherPlatform` 只给连接页设平台。
 */
export function fakeTauri(
	handlers: Record<string, unknown>,
	host: boolean | "desktop" | "android" = false,
	launcherPlatform?: "android",
): string {
	const meta =
		host === "android"
			? `{ kind: "tauri", platform: "android", version: "0.1.0", bridge: 2 }`
			: host
				? `{ kind: "tauri", platform: "linux", version: "0.1.0", bridge: 1 }`
				: launcherPlatform
					? `{ kind: "tauri", platform: "${launcherPlatform}", version: "0.1.0", bridge: 2 }`
					: "";
	return `(() => {
		const replies = ${JSON.stringify(handlers)};
		${meta ? `window.__ORYN_HOST__ = ${meta};` : ""}
		// 配对码按截图时刻起算 9 分 30 秒后过期
		const state = replies.launcher_state && replies.launcher_state.state;
		if (state && state.step === "pairing") state.expiresAt = Date.now() + 570000;
		window.__TAURI__ = { core: { invoke: async (cmd) => {
			if (!(cmd in replies)) throw new Error("unexpected " + cmd);
			return replies[cmd];
		} } };
	})();`;
}

export const LAUNCHER_SERVER = "https://oryn.example.com";

/**
 * 无系统标题栏的假桌面壳（bridge 3）：`__ORYN_HOST__.chrome` 按平台给布局，`host_window` 应答窗口状态。
 * macOS 的红绿灯由系统画、浏览器里没有：这里在窗口坐标 (22, 24) 起画三个 12px 圆点占位（壳校准后的目标位置：按钮框 14×16、
 * 竖直中线 32、间距 20），只为看留白与对齐，不是页面的一部分。macOS 按 26 起的系统给 `radius: 26`（挂了 unified 工具栏的窗口外框），
 * 截图本身是直角的，看的是卡片按 26 − 8 同心后的圆角。
 * `collapsed`：侧栏收起（写 localStorage）；`launcher`：连接页（另应答 `launcher_state`）。
 */
export function chromeShell(
	platform: "macos" | "windows" | "linux-left",
	opts: {
		collapsed?: boolean;
		launcher?: boolean;
		window?: { maximized: boolean; fullscreen: boolean; focused: boolean };
	} = {},
): string {
	const chrome =
		platform === "macos"
			? {
					platform: "macos",
					controls: "native-left",
					buttons: [],
					inset: { top: 48, left: 84, right: 0 },
					radius: 26,
				}
			: {
					platform: platform === "windows" ? "windows" : "linux",
					controls: platform === "windows" ? "custom-right" : "custom-left",
					buttons:
						platform === "windows"
							? ["minimize", "maximize", "close"]
							: ["close", "minimize", "maximize"],
					inset: { top: 48, left: 0, right: 0 },
				};
	const meta = {
		kind: "tauri",
		platform: chrome.platform,
		version: "0.1.0",
		bridge: 3,
		chrome,
	};
	const replies = {
		host_ready: { bridge: 3, visible: true },
		host_set_status: null,
		host_set_language: null,
		host_notify: null,
		host_window: opts.window ?? {
			maximized: false,
			fullscreen: false,
			focused: true,
		},
		...(opts.launcher
			? {
					launcher_state: {
						state: { step: "connect", serverUrl: "" },
						lang: "zh-CN",
						version: "0.1.0",
						tokenStore: "keyring",
					},
				}
			: {}),
	};
	return `(() => {
		const replies = ${JSON.stringify(replies)};
		window.__ORYN_HOST__ = Object.freeze(${JSON.stringify(meta)});
		window.__TAURI__ = { core: { invoke: async (cmd) => {
			if (!(cmd in replies)) throw new Error("unexpected " + cmd);
			return replies[cmd];
		} } };
		${opts.collapsed ? `localStorage.setItem("oryn.sidebar.collapsed", "1");` : ""}
		${
			platform === "macos"
				? `document.addEventListener("DOMContentLoaded", () => {
			const lights = document.createElement("div");
			lights.setAttribute("aria-hidden", "true");
			lights.style.cssText = "position:fixed;left:22px;top:24px;z-index:2147483647;pointer-events:none;display:flex;gap:8px";
			for (const c of ["#ff5f57", "#febc2e", "#28c840"]) {
				const dot = document.createElement("span");
				// 全局 corner-shape 是超椭圆（index.css），这里是内联样式、不带 rounded-full，须显式 round 才是正圆
				dot.style.cssText = "width:12px;height:12px;margin:2px 0 2px 0;border-radius:50%;corner-shape:round;background:" + c + ";box-shadow:inset 0 0 0 0.5px rgba(0,0,0,.25)";
				lights.append(dot);
			}
			// 全屏时系统收起红绿灯：跟着壳的 window 事件显隐（逐帧动画脚本也靠它）
			const sync = (fs) => { lights.style.visibility = fs ? "hidden" : "visible"; };
			sync(replies.host_window.fullscreen);
			window.addEventListener("oryn:host", (e) => {
				const d = e.detail;
				if (d && d.type === "window" && typeof d.fullscreen === "boolean") sync(d.fullscreen);
			});
			document.body.append(lights);
		});`
				: ""
		}
	})();`;
}
/** 假移动壳的 `MobileInfo`：默认屏幕四角 40px、无 inset（自查屏幕圆角同心；`mobileScreen` 可改半径）。 */
const MOBILE_INFO = {
	version: "0.1.0",
	platform: "android",
	tokenStore: "keystore",
	webview: "146.0.7680.0",
	push: {
		state: "active",
		channel: "unifiedpush",
		distributor: "ntfy",
		endpointHost: "ntfy.sh",
	},
	cornerRadii: { tl: 40, tr: 40, bl: 40, br: 40 },
	insets: { top: 0, right: 0, bottom: 0, left: 0 },
};

/** 假移动壳（android，bridge 2）的命令表；`radius` = 屏幕四角半径。 */
export function mobileHandlers(radius: number) {
	return {
		host_ready: { bridge: 2, visible: true },
		host_set_status: null,
		host_set_language: null,
		host_set_chrome: null,
		host_mobile_info: {
			...MOBILE_INFO,
			cornerRadii: { tl: radius, tr: radius, bl: radius, br: radius },
		},
		host_notify: null,
	};
}
