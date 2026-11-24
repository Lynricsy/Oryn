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
 * 移动壳 bridge ≥ 8 且 platform 为 ios：`mobile.sink` / `mobile.tabBar` / `mobile.header` 非空。下沉侧栏的卡片开合是 WKWebView
 * 根滚动视图的横向滚动（`native-sink.ts`），原生按 `contentOffset` 跟随：原生底栏（系统 UITabBar 浮在 WebView 之上、右缘裁到
 * 卡片左缘，`native-tab-bar.ts`）与原生页头（`native-header.ts`）的位置都由原生自己算，网页只发 spec、遮挡与透明度。
 * 另有 `tabbar` 与 `header` 事件。壳在本页 `host_sink_set(true)` 生效前拒绝非空的底栏 / 页头 spec；
 * bridge ≤ 7 的壳（页头靠跨进程对时跟随）一律用网页页头与网页底栏。
 */

import type { Attachment } from "./attachments";

export type HostPlatform = "linux" | "macos" | "windows" | "android" | "ios";

export interface HostNotificationAvatar {
	kind: "thread" | "assistant" | "system";
	/** 单个字形（普通线程标题卡图标，或全局助手自定义字形）。 */
	icon?: string;
	/** 线程色（red / orange / yellow / green / teal / blue / purple / pink）。 */
	color?: string;
	/** 预栅格化好的 128×128 头像 PNG base64（不带 data: 前缀）。 */
	image?: string;
	/** 全局助手自定义头像内容的 8 位 SHA-256 摘要。 */
	hash?: string;
}

export interface HostNotification {
	id: string;
	title: string;
	body: string;
	/** 点击后打开的站内路径（如 `/t/<id>`）。 */
	path: string;
	/** 通知创建时间（毫秒），供移动端 MessagingStyle 显示。 */
	timestamp?: number;
	/** 通知发送者头像元数据。 */
	avatar?: HostNotificationAvatar;
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

/**
 * 推送状态（壳归并后的视图，M7 计划 2.7）。bridge ≥ 9 的壳不再报 `unsupported`；旧壳仍可能报，页面的默认分支兜底。
 */
export type MobilePushState =
	| "disabled"
	| "permission_denied"
	| "no_distributor"
	| "registering"
	| "active"
	| "error";

/** 推送通道：UnifiedPush，或经官方推送网关的 FCM（Android gms 包）/ APNs（iOS）。 */
export type MobilePushChannel = "unifiedpush" | "fcm" | "apns";

/** `enablePush` 的通道选择（bridge ≥ 9）：`default` = 本包的默认通道；`unifiedpush` = 手动改用 UnifiedPush。 */
export type MobilePushTransport = "default" | "unifiedpush";

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
		channel?: MobilePushChannel | null;
		/** 可切换到的另一通道（只有 gms 包有）。 */
		alt?: "unifiedpush" | "fcm" | null;
		distributor?: string | null;
		endpointHost?: string | null;
		/** 错误码（`gateway_*` 见 docs/protocol.md「官方推送网关」）或原生 / orynd 原样透传的错误。 */
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

/** 原生底栏的一项（iOS 26+，见 `native-tab-bar.ts`）。 */
export interface NativeTabBarItem {
	id: string;
	label: string;
	/** SF Symbol 名 / 单个字形 / PNG 的 base64（不带 data: 前缀）。 */
	icon: { symbol: string } | { glyph: string } | { image: string };
	badge?: "accent" | "warn" | "err";
}

/** 原生底栏的整份状态；只在内容变了时发。 */
export interface NativeTabBarSpec {
	items: NativeTabBarItem[];
	selected: string;
	/** 网页胶囊在布局视口里的矩形（CSS px）；原生底栏按它摆（左右对齐，高度取系统的）。 */
	frame: { x: number; y: number; width: number; height: number };
	dark: boolean;
	/** 选中项颜色 `#rrggbb`；null = 系统强调色。 */
	accent: string | null;
}

export interface NativeTabBarHost {
	/** 拒绝 = 原生底栏不可用，调用方退回网页底栏。 */
	set(spec: NativeTabBarSpec | null): Promise<void>;
	/** 网页内容（菜单、弹层、提示、软键盘）压在底栏位置上：原生隐藏、不接触摸，网页胶囊顶上。 */
	cover(covered: boolean): Promise<void>;
}

/** 下沉侧栏的根滚动跟随（iOS 且 bridge ≥ 8）。 */
export interface NativeSinkHost {
	/** `true`：原生按根滚动的 `contentOffset` 跟随，之后原生底栏 / 页头才允许接管；拒绝 = 不可用。 */
	set(on: boolean): Promise<void>;
	/** 原生根滚动视图开合：`animated` 时逐帧推进，否则一步落位。 */
	show(open: boolean, animated: boolean): Promise<void>;
}

/** orb 状态珠的画法（`OrbBead` 的各档 + `err` = 7px 错误色圆点）。 */
export type NativeOrb =
	| "booting"
	| "ready"
	| "working"
	| "freezing"
	| "frozen"
	| "stopped"
	| "deleted"
	| "failed"
	| "err";

/**
 * 五选一：SF Symbol / 字形方块 / 模板图方块（`bg`、`fg` 只配这两种）/ 纯色圆点 / orb 状态珠。颜色一律 `#rrggbb`。
 * `mask` 是白色字形、透明底的 PNG（base64，不带 data: 前缀），给系统字体画不了的 Nerd 字形用（见 `nerd-mask.ts`）。
 */
export interface NativeIcon {
	symbol?: string;
	glyph?: string;
	mask?: string;
	bg?: string;
	fg?: string;
	dot?: string;
	orb?: NativeOrb;
}

/** 一段「图形 + 文字」：标题胶囊第二行的一段、菜单行的说明行。 */
export interface NativeSeg {
	text: string;
	icon?: NativeIcon;
	tone?: "err";
}

export interface NativeMark {
	symbol: string;
	dim?: boolean;
}

export interface NativeMenuItem {
	/** 菜单内唯一；子项是 `<父 id>/<key>`。原生点击原样回传。 */
	id: string;
	label: string;
	icon?: NativeIcon;
	sub?: NativeSeg;
	hint?: string;
	/** 文字后的提醒点（视图菜单）。 */
	dot?: "accent" | "warn";
	pulse?: boolean;
	/** 给出即单选项；true 时右侧打勾。 */
	checked?: boolean;
	danger?: boolean;
	disabled?: boolean;
	/** 选中时原生把这段文字写进剪贴板（网页不再执行该项的 onSelect）。 */
	copy?: string;
	/** 一级子页（子项不能再有 children）。 */
	children?: NativeMenuItem[];
}

export interface NativeMenu {
	/** 读屏用的菜单名。 */
	label: string;
	/** 打开时是否压暗页面（标题菜单 true，视图菜单 false）。 */
	dim: boolean;
	/** 顶部宫格（常用操作）。 */
	quick: NativeMenuItem[];
	/** 浅底状态块。 */
	status: NativeMenuItem[];
	/** 其余分组，组间一条细线。 */
	groups: NativeMenuItem[][];
}

export interface NativeFrame {
	x: number;
	y: number;
	width: number;
	height: number;
}

/** 原生页头的整份内容（iOS 26+，见 `native-header.ts`）；只在内容变了时发。 */
export interface NativeHeaderSpec {
	/** 线程 id（只有侧栏钮的页是页名，如 `home`）：变了原生收起菜单；事件原样带回。 */
	key: string;
	dark: boolean;
	palette: {
		accent: string;
		brand: string;
		err: string;
		warn: string;
		good: string;
		frost: string;
	};
	/** 可调参数放在网页（改它不用发版）：遮罩不透明度、面板底色着色强度，均 0–1。 */
	tune: { scrim: number; tint: number };
	/** 读屏文案：子页的返回行、关闭区。 */
	text: { back: string; close: string };
	lead: { frame: NativeFrame; symbol: string; label: string };
	/** 没有标题胶囊的页（首页：只有侧栏钮）为 `null`（不省略）。 */
	title: {
		frame: NativeFrame;
		text: string;
		icon?: NativeIcon;
		/** 有值时第二行只显示它（错误色）。 */
		error?: string;
		meta: NativeSeg[];
		marks: NativeMark[];
		/** 读屏读的整句。 */
		label: string;
		menu: NativeMenu;
	} | null;
	trail: {
		frame: NativeFrame;
		symbol: string;
		label: string;
		dot?: "accent" | "warn";
		menu: NativeMenu;
	} | null;
}

/** 页头控件层的透明度（水平位移与是否接触摸由原生按根滚动自己算）。 */
export interface NativeHeaderShow {
	alpha: number;
	/** 淡入淡出时长（整数毫秒）；0 = 立即。 */
	ms: number;
}

export type NativeMenuKind = "title" | "trail";

export interface NativeHeaderMenu {
	open: NativeMenuKind | null;
	/** 直接停在某个子页（父项 id）。 */
	page?: string;
}

export type NativeHeaderEvent =
	| { op: "lead"; key: string }
	| { op: "menu"; key: string; menu: NativeMenuKind | null }
	| { op: "select"; key: string; menu: NativeMenuKind; id: string };

export interface NativeHeaderHost {
	/** 应答 = 原生页头已经挂到窗口上；拒绝 = 不可用，调用方继续用网页页头。 */
	set(spec: NativeHeaderSpec | null): Promise<void>;
	show(show: NativeHeaderShow): Promise<void>;
	menu(menu: NativeHeaderMenu): Promise<void>;
}

/** 可替换图标的 id：`default` 与壳带的其它图标（见壳的 `mobile_logic::APP_ICONS`）。 */
export interface AppIconState {
	/** 系统是否允许换图标（iOS 总是 true，留给以后的平台）。 */
	supported: boolean;
	current: string;
	/** 本壳带的全部 id，默认在前；页面只显示自己认识的。 */
	options: string[];
}

export interface AppIconHost {
	get(): Promise<AppIconState>;
	/** 系统改完（并弹出系统提示）才应答；拒绝时图标没变。 */
	set(id: string): Promise<void>;
}

export interface MobileHost {
	info(): Promise<MobileInfo>;
	/** 启用推送；`transport` 只在 `pushTransport` 为真（bridge ≥ 9）时传给壳，旧壳一律按缺省通道。 */
	enablePush(transport?: MobilePushTransport): Promise<MobileInfo>;
	disablePush(): Promise<MobileInfo>;
	openExternal(url: string): void;
	shareAck(id: string): void;
	setChrome(c: { dark: boolean; color: string }): void;
	openNotificationSettings(): void;
	changeServer(): void;
	/** 壳能按 `enablePush(transport)` 切换推送通道（bridge ≥ 9）。 */
	pushTransport: boolean;
	/** 下沉侧栏的根滚动跟随（iOS 且 bridge ≥ 8）；其它情况为 null，卡片用网页手势。 */
	sink: NativeSinkHost | null;
	/** 原生底栏（iOS 且 bridge ≥ 8）；其它情况为 null，用网页底栏。 */
	tabBar: NativeTabBarHost | null;
	/** 原生页头（iOS 且 bridge ≥ 8）；其它情况为 null，用网页页头。 */
	header: NativeHeaderHost | null;
	/** 可替换图标（iOS 且 bridge ≥ 6）；其它情况为 null，设置里不显示这一项。 */
	appIcon: AppIconHost | null;
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
	/** 用户点了原生底栏的一项（仅 iOS 移动壳；点已选中项同样上报）。 */
	onTabBar(cb: (id: string) => void): () => void;
	/** 原生页头上的点击（仅 iOS 移动壳；见 `native-header.ts`）。 */
	onHeader(cb: (e: NativeHeaderEvent) => void): () => void;
	desktop: DesktopHost | null;
	mobile: MobileHost | null;
}

/** 本页面实现的宿主协议版本（与壳 `host_ready` 返回的 `bridge` 对应；3 = 桌面自绘标题栏；4 = iOS 原生底栏；5 = iOS 原生页头；6 = iOS 可替换图标；7 = 原生底栏改系统 UITabBar；8 = 下沉侧栏改由根滚动驱动、原生按 contentOffset 跟随，原生底栏浮在 WebView 之上、右缘裁到卡片左缘，页头可只有侧栏钮；9 = 官方推送网关：`host_push_enable` 可带 `transport`，推送状态带 `alt`、通道含 `apns`、不再报 `unsupported`）。 */
export const BRIDGE = 9;
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
		onTabBar: () => noop,
		onHeader: () => noop,
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

function menuKind(v: unknown): NativeMenuKind | null {
	return v === "title" || v === "trail" ? v : null;
}

/** 解析 `header` 事件：`key` 必须是字符串；`select` 要有菜单种类与字符串 `id`，否则整条丢弃。 */
export function parseHeaderEvent(v: unknown): NativeHeaderEvent | null {
	if (!v || typeof v !== "object") return null;
	const o = v as Record<string, unknown>;
	const key = o.key;
	if (typeof key !== "string") return null;
	if (o.op === "lead") return { op: "lead", key };
	if (o.op === "menu") return { op: "menu", key, menu: menuKind(o.menu) };
	if (o.op === "select") {
		const menu = menuKind(o.menu);
		return menu && typeof o.id === "string"
			? { op: "select", key, menu, id: o.id }
			: null;
	}
	return null;
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
	native: boolean,
	appIcon: boolean,
	pushTransport: boolean,
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
		enablePush: (transport) =>
			pushTransport && transport
				? invoke<MobileInfo>("host_push_enable", { transport })
				: invoke<MobileInfo>("host_push_enable"),
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
		pushTransport,
		// 用 invoke 不用 call：拒绝要交给调用方退回网页手势 / 底栏 / 页头
		sink: native
			? {
					set: (on) => invoke<void>("host_sink_set", { on }),
					show: (open, animated) =>
						invoke<void>("host_sink_show", { open, animated }),
				}
			: null,
		tabBar: native
			? {
					set: (spec) => invoke<void>("host_tab_bar_set", { spec }),
					cover: (covered) => invoke<void>("host_tab_bar_cover", { covered }),
				}
			: null,
		header: native
			? {
					set: (spec) => invoke<void>("host_header_set", { spec }),
					show: (show) => invoke<void>("host_header_show", { show }),
					menu: (menu) => invoke<void>("host_header_menu", { menu }),
				}
			: null,
		appIcon: appIcon
			? {
					get: () => invoke<AppIconState>("host_app_icon_get"),
					set: (id) => invoke<void>("host_app_icon_set", { id }),
				}
			: null,
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
	const tabBar = listeners<[string]>();
	const header = listeners<[NativeHeaderEvent]>();
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
			id?: unknown;
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
		} else if (isMobile && d.type === "tabbar" && typeof d.id === "string")
			tabBar.emit(d.id);
		else if (isMobile && d.type === "header") {
			const e = parseHeaderEvent(d);
			if (e) header.emit(e);
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
		onTabBar: (cb) => tabBar.add(cb),
		onHeader: (cb) => header.add(cb),
		mobile: isMobile
			? mobileHost(
					win,
					invoke,
					call,
					platform === "ios" && (meta?.bridge ?? 0) >= 8,
					platform === "ios" && (meta?.bridge ?? 0) >= 6,
					(meta?.bridge ?? 0) >= 9,
				)
			: null,
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
