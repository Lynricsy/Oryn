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
 */

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
	desktop: DesktopHost | null;
}

/** 本页面实现的宿主协议版本（与壳 `host_ready` 返回的 `bridge` 对应）。 */
export const BRIDGE = 1;
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
		desktop: null,
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

function tauriHost(win: Window, invoke: Invoke): Host {
	const meta = win.__ORYN_HOST__;
	const platform = PLATFORMS.find((p) => p === meta?.platform) ?? null;
	/** 壳报告的窗口可见性；`host_ready` 返回前按可见处理。 */
	let visible = true;
	let reauthRequested = false;
	const foreground = listeners<[]>();
	const navigate = listeners<[string]>();
	const update = listeners<[]>();

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
		} | null;
		if (!d || typeof d !== "object") return;
		if (d.type === "visibility" && typeof d.visible === "boolean")
			setVisible(d.visible);
		else if (d.type === "navigate" && typeof d.path === "string")
			navigate.emit(d.path);
		else if (d.type === "update") update.emit();
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
		desktop: {
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
