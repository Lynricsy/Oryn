/**
 * 宿主适配（M6 计划 6.1）：同一套 Web UI 跑在浏览器里（web）或桌面壳的主窗口里（tauri，加载服务器自己的 Web UI）。
 * - 判定：壳的 initialization_script 写入 `window.__ORYN_HOST__ = {kind: "tauri", platform, version, bridge}`，
 *   且能取到 Tauri 的 `invoke`（`withGlobalTauri` 的 `__TAURI__.core.invoke`，退而取 `__TAURI_INTERNALS__.invoke`）。
 *   不依赖 `@tauri-apps/api`。
 * - web：前台 = 标签页可见；其余全部空操作，`desktop = null`——纯 Web 的行为与以前完全相同。
 * - tauri：壳经 `window.dispatchEvent(new CustomEvent("oryn:host", {detail}))` 推送事件（`visibility` / `navigate` /
 *   `update`；`launcher` 只给连接页）；前台 = 标签页可见且壳报告窗口可见（未隐藏 / 最小化）。
 *   发出即忘的命令（`ready` / `notify` / `setStatus` / `setLanguage` / `reauth` / `changeServer`）吞掉 invoke 的拒绝并
 *   `console.warn`，宿主异常不能把页面弄崩；`desktop.*` 里有返回值的方法把拒绝交给调用方（设置页显示错误）。
 * `reauth` 每个页面只发一次：壳收到后总会把主窗口整页导航走（换取成功去 `/auth/app`，失败回连接页），
 * 同一次 401 经多个入口（请求钩子、`me` 查询、WS）报上来也只算一次，不会误触壳的「60 秒内 3 次」循环保护。
 * 兼容：以后新增宿主命令就把 `BRIDGE` 加一，调用新命令前比较 `__ORYN_HOST__.bridge`。
 * 移动壳（M7 计划 4.1，bridge ≥ 2 且 platform 为 android / ios）：`mobile` 非空、`desktop = null`；
 * 另有 `share`（系统分享已上传为附件）、`mobile`（推送状态变了）、`insets`（屏幕圆角与原生 inset 变了）三个事件；
 * 页面里的外链（`target="_blank"` 或跨源 http(s)）与 `window.open` 一律改经 `host_open_external` 交给系统浏览器。
 * 桌面壳 bridge ≥ 3 去掉了系统标题栏：`__ORYN_HOST__.chrome` 声明按钮布局，窗口操作与 `window` 事件见 `window-chrome.ts`。
 */

import type { Attachment } from "./attachments";

export type HostPlatform = "linux" | "macos" | "windows" | "android" | "ios";

export interface HostNotification {
	id: string;
	title: string;
	body: string;
	/** 点击后打开的站内路径（如 `/t/<id>`）。 */
	path: string;
}

export interface HostStatusItem {
	threadId: string;
	title: string;
	kind: "awaiting" | "unread";
}

export interface HostStatus {
	awaiting: number;
	unread: number;
	/** ≤5 条，先待处理后未读。 */
	items: HostStatusItem[];
}

export interface DesktopSettings {
	shortcut: string;
	closeToTray: boolean;
	autoUpdate: boolean;
}

export type UpdateState =
	| "none"
	| "available"
	| "downloading"
	| "ready"
	| "error";

export interface DesktopInfo {
	version: string;
	settings: DesktopSettings;
	/** 快捷键注册失败的原因（解析失败、被占用、Wayland 下不可用）；成功时缺省或 null。 */
	shortcutError?: string | null;
	tokenStore: "keyring" | "file";
	update: {
		state: UpdateState;
		version?: string | null;
		error?: string | null;
	};
}

export interface DesktopHost {
	info(): Promise<DesktopInfo>;
	setSettings(patch: Partial<DesktopSettings>): Promise<DesktopInfo>;
	checkUpdate(): Promise<{ version: string; notes?: string | null } | null>;
	installUpdate(): Promise<void>;
	changeServer(): void;
}

/** 推送状态（壳归并后的视图，M7 计划 2.7）。 */
export type MobilePushState =
	| "unsupported"
	| "disabled"
	| "permission_denied"
	| "no_distributor"
	| "registering"
	| "active"
	| "error";

/** 四角半径（CSS px，取不到为 0）。 */
export interface CornerRadii {
	tl: number;
	tr: number;
	bl: number;
	br: number;
}

/** 原生容器加在 WebView 四周的 padding（系统栏 + 刘海 + 软键盘，CSS px）。 */
export interface EdgeInsets {
	top: number;
	right: number;
	bottom: number;
	left: number;
}

/** 屏幕几何：物理屏幕圆角与 WebView 外的 inset（`insets` 事件与 `MobileInfo` 同形）。 */
export interface DeviceGeometry {
	cornerRadii: CornerRadii;
	insets: EdgeInsets;
}

export interface MobileInfo extends Partial<DeviceGeometry> {
	version: string;
	platform: "android" | "ios";
	tokenStore: "keystore" | "keychain";
	/** 系统 WebView 版本（取不到为空串）。 */
	webview: string;
	push: {
		state: MobilePushState;
		/** 当前推送通道；未启用时缺省。 */
		channel?: "unifiedpush" | "fcm" | null;
		distributor?: string | null;
		endpointHost?: string | null;
		error?: string | null;
	};
}

/** 系统分享进来的内容：壳已把文件上传为附件（M7 计划 2.8）。 */
export interface ShareIntake {
	id: string;
	text: string | null;
	attachments: Attachment[];
	failed: { name: string; reason: string }[];
}

export interface MobileHost {
	info(): Promise<MobileInfo>;
	enablePush(): Promise<MobileInfo>;
	disablePush(): Promise<MobileInfo>;
	openExternal(url: string): void;
	shareAck(id: string): void;
	setChrome(c: { dark: boolean; color: string }): void;
	openNotificationSettings(): void;
	changeServer(): void;
}

export interface Host {
	readonly kind: "web" | "tauri";
	readonly platform: HostPlatform | null;
	/** 页面是否正被人看着：web = 标签页可见；tauri = 标签页可见且窗口未隐藏 / 最小化。 */
	isForeground(): boolean;
	onForegroundChange(cb: () => void): () => void;
	onNavigate(cb: (path: string) => void): () => void;
	onUpdate(cb: () => void): () => void;
	/** 启动握手；返回壳里待开的路径（没有则 null）。web 下恒为 null。 */
	ready(lang: string, path: string): Promise<string | null>;
	notify(n: HostNotification): void;
	setStatus(s: HostStatus): void;
	setLanguage(lang: string): void;
	reauth(path: string): void;
	/** 系统分享进来的内容（仅移动壳会触发）。 */
	onShare(cb: (s: ShareIntake) => void): () => void;
	/** 推送状态变了，调用方重取 `mobile.info()`（仅移动壳）。 */
	onMobile(cb: () => void): () => void;
	/** 屏幕圆角 / inset 变了（旋转、折叠、软键盘；仅移动壳）。 */
	onInsets(cb: (g: DeviceGeometry) => void): () => void;
	desktop: DesktopHost | null;
	mobile: MobileHost | null;
}

/** 本页面实现的宿主协议版本（与壳 `host_ready` 返回的 `bridge` 对应；3 = 桌面自绘标题栏）。 */
export const BRIDGE = 3;
/** 壳向页面推送事件用的 DOM 事件名。 */
export const HOST_EVENT = "oryn:host";

export type Invoke = <T>(
	cmd: string,
	args?: Record<string, unknown>,
) => Promise<T>;

declare global {
	interface Window {
		__ORYN_HOST__?: {
			kind?: string;
			platform?: string;
			version?: string;
			bridge?: number;
			/** 桌面壳的标题栏布局（bridge ≥ 3，见 `window-chrome.ts`）。 */
			chrome?: unknown;
		};
		__TAURI__?: { core?: { invoke?: Invoke } };
		__TAURI_INTERNALS__?: { invoke?: Invoke };
	}
}

/** 取 Tauri 的 invoke；不在壳里时为 null。 */
export function findInvoke(win: Window): Invoke | null {
	return win.__TAURI__?.core?.invoke ?? win.__TAURI_INTERNALS__?.invoke ?? null;
}

const PLATFORMS: readonly HostPlatform[] = [
	"linux",
	"macos",
	"windows",
	"android",
	"ios",
];

function tabVisible(win: Window): boolean {
	const doc = win.document as Document | undefined;
	return !doc || doc.visibilityState === "visible";
}

function onTabVisibility(win: Window, cb: () => void): () => void {
	const doc = win.document as Document | undefined;
	if (!doc) return () => {};
	doc.addEventListener("visibilitychange", cb);
	return () => doc.removeEventListener("visibilitychange", cb);
}

const noop = () => {};

function webHost(win: Window): Host {
	return {
		kind: "web",
		platform: null,
		isForeground: () => tabVisible(win),
		onForegroundChange: (cb) => onTabVisibility(win, cb),
		onNavigate: () => noop,
		onUpdate: () => noop,
		ready: async () => null,
		notify: noop,
		setStatus: noop,
		setLanguage: noop,
		reauth: noop,
		onShare: () => noop,
		onMobile: () => noop,
		onInsets: () => noop,
		desktop: null,
		mobile: null,
	};
}

/** 一组回调；逐个调用，单个回调抛错不影响其它。 */
function listeners<A extends unknown[]>() {
	const set = new Set<(...args: A) => void>();
	return {
		add(cb: (...args: A) => void): () => void {
			set.add(cb);
			return () => set.delete(cb);
		},
		emit(...args: A) {
			for (const cb of [...set]) {
				try {
					cb(...args);
				} catch (e) {
					console.warn("[host] listener failed", e);
				}
			}
		},
	};
}

function num(v: unknown): number {
	return typeof v === "number" && Number.isFinite(v) && v > 0 ? v : 0;
}

/** 解析 `insets` 事件 / `MobileInfo` 里的屏幕几何；两项都缺时为 null，单项缺省按 0。 */
export function parseGeometry(v: unknown): DeviceGeometry | null {
	if (!v || typeof v !== "object") return null;
	const o = v as { cornerRadii?: unknown; insets?: unknown };
	const r = (o.cornerRadii ?? null) as Record<string, unknown> | null;
	const i = (o.insets ?? null) as Record<string, unknown> | null;
	if ((!r || typeof r !== "object") && (!i || typeof i !== "object"))
		return null;
	return {
		cornerRadii: {
			tl: num(r?.tl),
			tr: num(r?.tr),
			bl: num(r?.bl),
			br: num(r?.br),
		},
		insets: {
			top: num(i?.top),
			right: num(i?.right),
			bottom: num(i?.bottom),
			left: num(i?.left),
		},
	};
}

function parseShare(v: unknown): ShareIntake | null {
	if (!v || typeof v !== "object") return null;
	const o = v as Record<string, unknown>;
	if (typeof o.id !== "string" || !o.id) return null;
	const failed = Array.isArray(o.failed)
		? o.failed.flatMap((f) =>
				f && typeof f === "object" && typeof f.name === "string"
					? [{ name: f.name as string, reason: String(f.reason ?? "") }]
					: [],
			)
		: [];
	return {
		id: o.id,
		text: typeof o.text === "string" && o.text ? o.text : null,
		attachments: Array.isArray(o.attachments)
			? (o.attachments.filter(
					(a) => a && typeof a === "object" && typeof a.id === "string",
				) as Attachment[])
			: [],
		failed,
	};
}

/** 链接是否该交给系统浏览器：新窗口目标，或 http(s) 且跨源。 */
export function externalHref(a: HTMLAnchorElement, win: Window): string | null {
	const raw = a.getAttribute("href");
	if (!raw || raw.startsWith("#")) return null;
	let url: URL;
	try {
		url = new URL(raw, win.location.href);
	} catch {
		return null;
	}
	const web = url.protocol === "http:" || url.protocol === "https:";
	if (!web) return null;
	if (a.target === "_blank" || url.origin !== win.location.origin)
		return url.href;
	return null;
}

/**
 * 移动壳的宿主命令；构造时接管外链：捕获阶段的 `click`（`a[href]` 且新窗口或跨源 → 系统浏览器）与 `window.open`。
 * 同源普通链接与 `download` 链接不拦（后者由原生下载监听处理）。
 */
function mobileHost(
	win: Window,
	invoke: Invoke,
	call: <T>(
		cmd: string,
		args?: Record<string, unknown>,
	) => Promise<T | undefined>,
): MobileHost {
	const openExternal = (url: string) => {
		void call("host_open_external", { url });
	};
	win.document?.addEventListener(
		"click",
		(ev) => {
			const e = ev as MouseEvent;
			if (e.defaultPrevented || e.button !== 0) return;
			const t = e.target as Element | null;
			const a = t?.closest?.("a[href]") as HTMLAnchorElement | null;
			if (!a || a.hasAttribute("download")) return;
			const href = externalHref(a, win);
			if (!href) return;
			e.preventDefault();
			openExternal(href);
		},
		true,
	);
	win.open = ((u?: string | URL) => {
		if (u !== undefined && u !== "") {
			try {
				openExternal(new URL(String(u), win.location.href).href);
			} catch {
				// 非法地址：忽略
			}
		}
		return null;
	}) as Window["open"];
	return {
		info: () => invoke<MobileInfo>("host_mobile_info"),
		enablePush: () => invoke<MobileInfo>("host_push_enable"),
		disablePush: () => invoke<MobileInfo>("host_push_disable"),
		openExternal,
		shareAck: (id) => {
			void call("host_share_ack", { id });
		},
		setChrome: ({ dark, color }) => {
			void call("host_set_chrome", { dark, color });
		},
		openNotificationSettings: () => {
			void call("host_open_notification_settings");
		},
		changeServer: () => {
			void call("host_change_server");
		},
	};
}

function tauriHost(win: Window, invoke: Invoke): Host {
	const meta = win.__ORYN_HOST__;
	const platform = PLATFORMS.find((p) => p === meta?.platform) ?? null;
	/** 壳报告的窗口可见性；`host_ready` 返回前按可见处理。 */
	let visible = true;
	let reauthRequested = false;
	const foreground = listeners<[]>();
	const navigate = listeners<[string]>();
	const update = listeners<[]>();
	const share = listeners<[ShareIntake]>();
	const mobileChanged = listeners<[]>();
	const insets = listeners<[DeviceGeometry]>();
	const isMobile =
		(platform === "android" || platform === "ios") && (meta?.bridge ?? 0) >= 2;

	const setVisible = (v: boolean) => {
		if (v === visible) return;
		visible = v;
		foreground.emit();
	};

	win.addEventListener(HOST_EVENT, (ev) => {
		const d = (ev as CustomEvent<unknown>).detail as {
			type?: unknown;
			visible?: unknown;
			path?: unknown;
			share?: unknown;
			cornerRadii?: unknown;
			insets?: unknown;
		} | null;
		if (!d || typeof d !== "object") return;
		if (d.type === "visibility" && typeof d.visible === "boolean")
			setVisible(d.visible);
		else if (d.type === "navigate" && typeof d.path === "string")
			navigate.emit(d.path);
		else if (d.type === "update") update.emit();
		else if (isMobile && d.type === "share") {
			const s = parseShare(d.share);
			if (s) share.emit(s);
		} else if (isMobile && d.type === "mobile") mobileChanged.emit();
		else if (isMobile && d.type === "insets") {
			const g = parseGeometry(d);
			if (g) insets.emit(g);
		}
	});

	/** 发出即忘：拒绝只记日志。 */
	const call = async <T>(
		cmd: string,
		args?: Record<string, unknown>,
	): Promise<T | undefined> => {
		try {
			return await invoke<T>(cmd, args);
		} catch (e) {
			console.warn(`[host] ${cmd} failed`, e);
			return undefined;
		}
	};

	return {
		kind: "tauri",
		platform,
		isForeground: () => tabVisible(win) && visible,
		onForegroundChange: (cb) => {
			const offTab = onTabVisibility(win, cb);
			const offWin = foreground.add(cb);
			return () => {
				offTab();
				offWin();
			};
		},
		onNavigate: (cb) => navigate.add(cb),
		onUpdate: (cb) => update.add(cb),
		ready: async (lang, path) => {
			const r = await call<{
				bridge?: number;
				visible?: boolean;
				pendingPath?: string | null;
			}>("host_ready", { bridge: BRIDGE, lang, path });
			if (typeof r?.visible === "boolean") setVisible(r.visible);
			return r?.pendingPath || null;
		},
		notify: (notification) => {
			void call("host_notify", { notification });
		},
		setStatus: (status) => {
			void call("host_set_status", { status });
		},
		setLanguage: (lang) => {
			void call("host_set_language", { lang });
		},
		reauth: (path) => {
			if (reauthRequested) return;
			reauthRequested = true;
			void call("host_reauth", { path });
		},
		onShare: (cb) => share.add(cb),
		onMobile: (cb) => mobileChanged.add(cb),
		onInsets: (cb) => insets.add(cb),
		mobile: isMobile ? mobileHost(win, invoke, call) : null,
		desktop: isMobile
			? null
			: {
					info: () => invoke<DesktopInfo>("host_info"),
					setSettings: (patch) =>
						invoke<DesktopInfo>("host_set_settings", { patch }),
					checkUpdate: async () =>
						(await invoke<{ version: string; notes?: string | null } | null>(
							"host_check_update",
						)) ?? null,
					installUpdate: async () => {
						await invoke<unknown>("host_install_update");
					},
					changeServer: () => {
						void call("host_change_server");
					},
				},
	};
}

/** 按注入的 window 判定宿主（测试可传假 window）。 */
export function createHost(win: Window): Host {
	const invoke = findInvoke(win);
	if (win.__ORYN_HOST__?.kind === "tauri" && invoke)
		return tauriHost(win, invoke);
	return webHost(win);
}

/** 当前页面的宿主（非浏览器环境下按 web 处理）。 */
export const host: Host = createHost(
	typeof window === "undefined" ? ({} as Window) : window,
);
