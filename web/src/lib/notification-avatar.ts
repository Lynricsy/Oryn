/**
 * 通知头像解析与移动端 PNG 栅格化：
 * - 普通线程：取线程标题卡图标与线程色；
 * - 全局助手主线（动态）与助手对话线程：取全局助手头像（图片 / 字形 / 默认 sparkle）；
 * - 无线程系统通知：/settings/assistant 归属全局助手，其余为 system。
 *
 * 移动端（iOS / Android）原生通知在需要显示全局助手自定义图片或 Nerd Font 私有区字形时，
 * 由前端利用已加载的 Symbols Nerd Font / Canvas 预先栅格化为 128×128 PNG base64 随通知一并传给壳。
 */
import { getCachedAssistantAvatar } from "../api/identity-queries";
import type { AppNotification } from "../state/notifications";
import { indexStore } from "../state/stores";
import type { HostNotificationAvatar, HostPlatform } from "./host";
import { rasterizeAvatar } from "./native-tab-bar";
import { isNerdGlyph, parseTitleCard } from "./title";

export const NOTIFICATION_AVATAR_PX = 128;

const NERD_FONT_FAMILY = '"Symbols Nerd Font Mono"';
const PNG_PREFIX = "data:image/png;base64,";

/** 与 Android / iOS 原生完全一致的浅底 + 深字配色表（线程 8 色 + 默认中性 + 全局助手强调色）。 */
const THREAD_PALETTE: Record<string, { bg: string; fg: string }> = {
	red: { bg: "#FCE8E6", fg: "#B3261E" },
	orange: { bg: "#FEF0E1", fg: "#B45309" },
	yellow: { bg: "#FEF7DC", fg: "#9A6B00" },
	green: { bg: "#E6F4EA", fg: "#18794E" },
	teal: { bg: "#E3F5F5", fg: "#0F766E" },
	blue: { bg: "#E8EFFC", fg: "#2B5CE6" },
	purple: { bg: "#F2E8FC", fg: "#7C3AED" },
	pink: { bg: "#FCE7F3", fg: "#BE185D" },
};

const NEUTRAL_PALETTE = { bg: "#EEF0F4", fg: "#1F2430" };
const ASSISTANT_PALETTE = { bg: "#E8EFFC", fg: "#2B5CE6" };

const renderedCache = new Map<string, Promise<string | null>>();

function isAssistantUrl(url: string): boolean {
	return (
		url === "/settings/assistant" ||
		url.startsWith("/settings/assistant/") ||
		url === "/assistant" ||
		url.startsWith("/assistant/")
	);
}

/** 解析一条通知的基础头像元数据（同步，不触发网络或 Canvas）。 */
export function resolveNotificationAvatar(
	n: Pick<AppNotification, "threadId" | "title" | "url" | "avatar">,
): HostNotificationAvatar {
	const cachedAssistant = getCachedAssistantAvatar();
	if (n.avatar) {
		const out: HostNotificationAvatar = { kind: n.avatar.kind };
		if (n.avatar.icon) out.icon = n.avatar.icon;
		if (n.avatar.color) out.color = n.avatar.color;
		if (n.avatar.hash) out.hash = n.avatar.hash;
		if (
			out.kind === "assistant" &&
			!out.icon &&
			cachedAssistant?.avatarIcon &&
			!cachedAssistant.avatarImage
		) {
			out.icon = cachedAssistant.avatarIcon;
		}
		return out;
	}
	// 兼容旧服务端（尚未下发 n.avatar）：按本地线程索引或路径兜底推导
	if (n.threadId) {
		const th = indexStore.getState().threads[n.threadId];
		if (th?.kind === "assistant") {
			const out: HostNotificationAvatar = { kind: "assistant" };
			if (cachedAssistant?.avatarIcon && !cachedAssistant.avatarImage) {
				out.icon = cachedAssistant.avatarIcon;
			}
			return out;
		}
		const card = parseTitleCard(th?.title || n.title);
		const out: HostNotificationAvatar = { kind: "thread" };
		if (card.icon) out.icon = card.icon;
		if (th?.color) out.color = th.color;
		return out;
	}
	if (isAssistantUrl(n.url)) {
		const out: HostNotificationAvatar = { kind: "assistant" };
		if (cachedAssistant?.avatarIcon && !cachedAssistant.avatarImage) {
			out.icon = cachedAssistant.avatarIcon;
		}
		return out;
	}
	return { kind: "system" };
}

async function renderNerdAvatarPng(
	glyph: string,
	bg: string,
	fg: string,
	px = NOTIFICATION_AVATAR_PX,
): Promise<string | null> {
	if (typeof document === "undefined" || !("fonts" in document)) return null;
	const fontSize = Math.round(px * 0.56);
	const font = `${fontSize}px ${NERD_FONT_FAMILY}`;
	if ((await document.fonts.load(font, glyph)).length === 0) return null;
	const canvas = document.createElement("canvas");
	canvas.width = px;
	canvas.height = px;
	const ctx = canvas.getContext("2d");
	if (!ctx) return null;
	ctx.fillStyle = bg;
	ctx.beginPath();
	ctx.arc(px / 2, px / 2, px / 2, 0, Math.PI * 2);
	ctx.fill();
	ctx.font = font;
	ctx.fillStyle = fg;
	const m = ctx.measureText(glyph);
	const w = m.actualBoundingBoxLeft + m.actualBoundingBoxRight;
	const h = m.actualBoundingBoxAscent + m.actualBoundingBoxDescent;
	if (!(w > 0 && h > 0)) return null;
	ctx.fillText(
		glyph,
		(px - w) / 2 + m.actualBoundingBoxLeft,
		(px - h) / 2 + m.actualBoundingBoxAscent,
	);
	const url = canvas.toDataURL("image/png");
	return url.startsWith(PNG_PREFIX) ? url.slice(PNG_PREFIX.length) : null;
}

function cachedNerdPng(
	glyph: string,
	bg: string,
	fg: string,
): Promise<string | null> {
	const key = `nerd:${glyph}:${bg}:${fg}`;
	let p = renderedCache.get(key);
	if (!p) {
		p = renderNerdAvatarPng(glyph, bg, fg).catch(() => null);
		renderedCache.set(key, p);
	}
	return p;
}

function cachedImagePng(src: string): Promise<string | null> {
	const key = `img:${src}`;
	let p = renderedCache.get(key);
	if (!p) {
		p = rasterizeAvatar(src, NOTIFICATION_AVATAR_PX).catch(() => null);
		renderedCache.set(key, p);
	}
	return p;
}

/**
 * 移动端（android / ios）按需补齐预栅格化 PNG（全局助手自定义图片或 Nerd Font 字形）。
 * 不需要异步栅格化时同步返回原对象，保证绝大多数场景无微任务延迟。
 */
export function prepareNotificationAvatar(
	avatar: HostNotificationAvatar,
	platform: HostPlatform | null,
): HostNotificationAvatar | Promise<HostNotificationAvatar> {
	if (platform !== "android" && platform !== "ios") return avatar;
	if (avatar.kind === "assistant") {
		const cached = getCachedAssistantAvatar();
		if (cached?.avatarImage) {
			return cachedImagePng(cached.avatarImage).then((png) =>
				png ? { ...avatar, image: png } : avatar,
			);
		}
		if (avatar.icon && isNerdGlyph(avatar.icon)) {
			return cachedNerdPng(
				avatar.icon,
				ASSISTANT_PALETTE.bg,
				ASSISTANT_PALETTE.fg,
			).then((png) => (png ? { ...avatar, image: png } : avatar));
		}
		return avatar;
	}
	if (avatar.kind === "thread" && avatar.icon && isNerdGlyph(avatar.icon)) {
		const pal =
			(avatar.color && THREAD_PALETTE[avatar.color]) || NEUTRAL_PALETTE;
		return cachedNerdPng(avatar.icon, pal.bg, pal.fg).then((png) =>
			png ? { ...avatar, image: png } : avatar,
		);
	}
	return avatar;
}

/** 仅测试用：清理内存缓存。 */
export function clearNotificationAvatarCache(): void {
	renderedCache.clear();
}
