/**
 * 连接页与壳之间的桥（M6 计划 3.3 / 3.5 / 4）：`invoke` = `window.__TAURI__.core.invoke`；
 * 壳经 `oryn:host` 事件（`detail.type === "launcher"`）推送新的 `LauncherState`。类型与壳的 `session.rs` 一致（字段 camelCase）。
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
	tokenStore: "keyring" | "file";
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
