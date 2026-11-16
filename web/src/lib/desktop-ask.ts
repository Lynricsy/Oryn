/**
 * 桌面请求（`desktop_ask_user`）与桌面控制状态的前端约定（docs/protocol.md「桌面控制状态」）。
 *
 * agent 的 `desktop_ask_user` 工具发出一条普通的 select 请求：标题「需要你操作桌面（<事项>）：<原因>」
 * （other 不带括号），选项恰为「完成」「跳过」。orbd（internal/orbdrt/deskcontrol.go）、扩展（oryn-orb.ts）
 * 与这里按同一格式识别，改动须三处同步。界面据此把它画成桌面专用的卡片与 Desktop 面板横幅，作答值仍是原文选项。
 */
import type { Schemas } from "../api/client";

export type DesktopAskKind = Schemas["OrbDesktopAskKind"];
export type DesktopControl = Schemas["OrbDesktopControl"];
export type DesktopAsk = Schemas["OrbDesktopAsk"];

/** 作答值（原文选项）。 */
export const DESKTOP_ASK_DONE = "完成";
export const DESKTOP_ASK_SKIP = "跳过";

const TITLE_RE =
	/^需要你操作桌面(?:（(登录|输入验证码|人工验证)）)?：([\s\S]+)$/;

const KIND_BY_LABEL: Record<string, DesktopAskKind> = {
	登录: "sign_in",
	输入验证码: "code",
	人工验证: "check",
};

const LABEL_BY_KIND: Record<DesktopAskKind, string> = {
	sign_in: "登录",
	code: "输入验证码",
	check: "人工验证",
	other: "",
};

/** 识别桌面请求：标题与选项都符合约定时返回事项与原因，否则 null（按普通 select 处理）。 */
export function parseDesktopAsk(
	title: string,
	options: readonly string[],
): { kind: DesktopAskKind; reason: string } | null {
	if (
		options.length !== 2 ||
		options[0] !== DESKTOP_ASK_DONE ||
		options[1] !== DESKTOP_ASK_SKIP
	)
		return null;
	const m = TITLE_RE.exec(title);
	if (!m?.[2]) return null;
	return {
		kind: m[1] ? (KIND_BY_LABEL[m[1]] ?? "other") : "other",
		reason: m[2],
	};
}

/** 按约定拼标题（mock 与测试用；真实请求由扩展拼）。 */
export function desktopAskTitle(kind: DesktopAskKind, reason: string): string {
	const label = LABEL_BY_KIND[kind];
	return `需要你操作桌面${label ? `（${label}）` : ""}：${reason}`;
}

/** Desktop 面板的控制视图。 */
export interface DesktopControlView {
	state: Schemas["OrbDesktopControlState"];
	/** 用户持有控制（可「交还」） */
	userHeld: boolean;
	/** agent 正在操作：面板强制只读观看，只能「接管」 */
	locked: boolean;
	ask?: DesktopAsk;
}

/**
 * 由 `Orb.desktop.control` 推出面板该怎么画。没有上报（旧 orbd、orb 不在运行）按 idle；
 * `ask` 状态缺请求详情时退回 `user` / `idle`（不画没法作答的横幅）。
 */
export function deriveDesktopControl(
	control: DesktopControl | undefined,
): DesktopControlView {
	if (!control) return { state: "idle", userHeld: false, locked: false };
	const userHeld = control.userHeld;
	if (control.state === "ask" && control.ask)
		return { state: "ask", userHeld, locked: false, ask: control.ask };
	if (control.state === "agent" && !userHeld)
		return { state: "agent", userHeld, locked: true };
	if (userHeld || control.state === "user")
		return { state: "user", userHeld: true, locked: false };
	return { state: "idle", userHeld: false, locked: false };
}

/** 用户持有控制时，有输入至多每这么久续期一次（orbd 10 分钟无续期自动交还）。 */
export const CONTROL_RENEW_MS = 30_000;
