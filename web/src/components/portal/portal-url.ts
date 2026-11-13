/**
 * Portal 地址（计划 M4 5.2）。`template` 是线程的 `portalUrlTemplate`：线程短码已代入、保留 `{port}`，
 * 如 `https://t-3f9a1c0b7e2d-p{port}.orb.example.com/`。模板不是 http(s) 时（mock 的 `data:` 页面）只代入端口。
 */
import type { Schemas } from "../../api/client";

type PortalLink = Schemas["OrbPortalLink"];

function isHttp(url: string): boolean {
	return /^https?:\/\//i.test(url);
}

/** 规范化路径：去掉空白，补前导 `/`。 */
export function normalizePath(path: string): string {
	const p = path.trim();
	if (!p) return "/";
	return p.startsWith("/") ? p : `/${p}`;
}

/** 代入端口后的根地址（不带结尾 `/`）。 */
function portalRoot(template: string, port: number): string {
	return template.replaceAll("{port}", String(port)).replace(/\/+$/, "");
}

/** 公开地址：根地址 + 路径。复制给别人的就是它（不带 `/__oryn/login`）。 */
export function portalUrl(
	template: string,
	port: number,
	path: string,
): string {
	if (!isHttp(template)) return template.replaceAll("{port}", String(port));
	return `${portalRoot(template, port)}${normalizePath(path)}`;
}

/**
 * iframe 与「新窗口」用的地址：先到 portal 主机的 `/__oryn/login`，已是所有者直接 302 到 `next`，
 * 否则走主域名授权握手（公开中的 portal 也先认出所有者）。
 */
export function portalFrameSrc(
	template: string,
	port: number,
	path: string,
): string {
	if (!isHttp(template)) return template.replaceAll("{port}", String(port));
	return `${portalRoot(template, port)}/__oryn/login?next=${encodeURIComponent(normalizePath(path))}`;
}

/** URL 胶囊里显示的主机名（含非默认端口）；非 http(s) 模板显示 `:<端口>`。 */
export function portalHost(template: string, port: number): string {
	if (!isHttp(template)) return `:${port}`;
	try {
		return new URL(portalRoot(template, port)).host;
	} catch {
		return `:${port}`;
	}
}

/** portal 的首个链接路径（无链接时为根路径）。 */
export function firstLinkPath(portal: {
	links: readonly PortalLink[];
}): string {
	return normalizePath(portal.links[0]?.path ?? "/");
}

/** 剩余时间（取最大单位，向下取整；不足 1 分钟按 1 分钟）：分钟 → 小时 → 天，单位文字随界面语言。 */
export function formatRemaining(ms: number, lang: string): string {
	const min = Math.max(1, Math.ceil(ms / 60_000));
	const [n, unit] =
		min < 60
			? [min, "minute"]
			: min < 1440
				? [Math.floor(min / 60), "hour"]
				: [Math.floor(min / 1440), "day"];
	return new Intl.NumberFormat(lang, {
		style: "unit",
		unit,
		unitDisplay: "long",
	}).format(n);
}
