/**
 * 连接页与壳之间的桥（M6 计划 3.3 / 3.5 / 4）：`invoke` = `window.__TAURI__.core.invoke`；
 * 壳经 `oryn:host` 事件（`detail.type === "launcher"`）推送新的 `LauncherState`。类型与壳的 `session.rs` 一致（字段 camelCase）。
 * 移动壳（M7 计划 4.8）复用同一连接页：平台取 `window.__ORYN_HOST__.platform`，扫码走 barcode-scanner 插件的命令。
 */
import { findInvoke, HOST_EVENT } from "../lib/host";

/** 壳给出的错误码（`session.rs` 的 `LauncherError.code`）。 */
export const LAUNCHER_ERROR_CODES = [
	"invalid_url",
	"insecure_url",
	"unreachable",
	"not_oryn",
	"server_too_old",
	"pair_expired",
	"pair_failed",
	"revoked",
	"keyring",
	"session_loop",
	"web_timeout",
] as const;

export type LauncherErrorCode = (typeof LAUNCHER_ERROR_CODES)[number];

export interface LauncherError {
	code: string;
	message: string;
}

export type LauncherState =
	| { step: "connect"; serverUrl: string; error?: LauncherError | null }
	| {
			step: "pairing";
			serverUrl: string;
			code: string;
			verifyUrl: string;
			/** 配对码过期时刻（UTC 毫秒，与 `pair/start` 的 `expiresAt` 相同） */
			expiresAt: number;
	  }
	| { step: "connecting"; serverUrl: string }
	| { step: "error"; serverUrl: string; error: LauncherError }
	| { step: "confirmChange"; serverUrl: string }
	| { step: "connected"; serverUrl: string };

/** `launcher_state` 的返回。 */
export interface LauncherInfo {
	state: LauncherState;
	lang: string;
	version: string;
	/** 桌面：keyring / file；移动：keystore（Android）/ keychain（iOS）。 */
	tokenStore: "keyring" | "file" | "keystore" | "keychain";
}

export function invoke<T>(
	cmd: string,
	args?: Record<string, unknown>,
): Promise<T> {
	const fn = findInvoke(window);
	if (!fn) return Promise.reject(new Error("Tauri IPC is not available"));
	return fn<T>(cmd, args);
}

/** 订阅壳推来的状态变化；返回取消订阅。 */
export function onState(cb: (s: LauncherState) => void): () => void {
	const listener = (ev: Event) => {
		const d = (ev as CustomEvent<unknown>).detail as {
			type?: unknown;
			state?: LauncherState;
		} | null;
		if (d?.type === "launcher" && d.state) cb(d.state);
	};
	window.addEventListener(HOST_EVENT, listener);
	return () => window.removeEventListener(HOST_EVENT, listener);
}

/** 是否跑在移动壳里（壳注入的 `__ORYN_HOST__.platform` 为 android / ios）。 */
export function isMobilePlatform(win: Window = window): boolean {
	const p = win.__ORYN_HOST__?.platform;
	return p === "android" || p === "ios";
}

/** 能否扫码：移动平台，且有 barcode-scanner 的全局对象或能取到 invoke（桌面上没有该插件）。 */
export function canScan(win: Window = window): boolean {
	if (!isMobilePlatform(win)) return false;
	const g = win.__TAURI__;
	return (!!g && "barcodeScanner" in g) || findInvoke(win) !== null;
}

/** 插件的权限状态：可能直接是字符串，也可能是 `{camera: "..."}`。 */
function permissionOf(v: unknown): string {
	if (typeof v === "string") return v;
	if (v && typeof v === "object" && "camera" in v) return String(v.camera);
	return "";
}

export type ScanResult =
	| { kind: "ok"; content: string }
	| { kind: "cancelled" }
	| { kind: "denied" }
	| { kind: "failed"; error: unknown };

/** 插件 reject 的字符串里表示「用户取消」的。 */
function isCancel(e: unknown): boolean {
	const s = typeof e === "string" ? e : e instanceof Error ? e.message : "";
	return /cancel/i.test(s);
}

/** 壳的返回键监听（`withGlobalTauri` 的 `__TAURI__.app.onBackButtonPress`）注册后的句柄。 */
interface BackListener {
	unregister(): Promise<void>;
}
type OnBackButtonPress = (handler: () => void) => Promise<BackListener>;

/** 取 `__TAURI__.app.onBackButtonPress`；旧壳或桌面没有时 null。 */
function backButton(win: Window): OnBackButtonPress | null {
	const g = win.__TAURI__;
	const app = g && "app" in g ? g.app : null;
	if (!app || typeof app !== "object" || !("onBackButtonPress" in app))
		return null;
	const fn = app.onBackButtonPress;
	// 运行时只能确认是函数，签名以 @tauri-apps/api 的 app.onBackButtonPress 为准
	return typeof fn === "function" ? (fn as OnBackButtonPress) : null;
}

/**
 * 全屏扫码期间接管返回键：只取消扫码（插件随即以 cancelled 拒绝 scan），App 不退出。
 * 有监听时壳的 AppPlugin 不再走默认的「后退或退出」；注册失败就照常扫码。
 */
async function holdBackButton(win: Window): Promise<BackListener | null> {
	const on = backButton(win);
	if (!on) return null;
	try {
		return await on(() => {
			invoke("plugin:barcode-scanner|cancel").catch((e) =>
				console.warn("[launcher] scan cancel failed", e),
			);
		});
	} catch (e) {
		console.warn("[launcher] back button listener failed", e);
		return null;
	}
}

/**
 * 扫一个二维码：先查权限，非 granted 就申请（未授权时 scan 会直接拒绝），仍拒绝 → denied；
 * 授权后全屏扫描只认 QR_CODE。用户取消（含扫码中按返回键）→ cancelled，其它拒绝 → failed。
 * 扫码期间返回键只取消扫码；结束（成功 / 取消 / 出错）后解除，返回键恢复原行为。
 */
export async function scanQr(win: Window = window): Promise<ScanResult> {
	try {
		let perm = permissionOf(
			await invoke<unknown>("plugin:barcode-scanner|check_permissions"),
		);
		if (perm !== "granted")
			perm = permissionOf(
				await invoke<unknown>("plugin:barcode-scanner|request_permissions"),
			);
		if (perm !== "granted") return { kind: "denied" };
	} catch (error) {
		return { kind: "failed", error };
	}
	const back = await holdBackButton(win);
	try {
		const r = await invoke<{ content?: unknown } | null>(
			"plugin:barcode-scanner|scan",
			{ windowed: false, formats: ["QR_CODE"] },
		);
		const content = typeof r?.content === "string" ? r.content.trim() : "";
		return content ? { kind: "ok", content } : { kind: "cancelled" };
	} catch (error) {
		return isCancel(error) ? { kind: "cancelled" } : { kind: "failed", error };
	} finally {
		await back
			?.unregister()
			.catch((e) =>
				console.warn("[launcher] back button unregister failed", e),
			);
	}
}
