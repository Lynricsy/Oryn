/** 显示格式化（随当前界面语言）。 */
import i18n from "../i18n";

export function formatCost(usd: number): string {
	if (!Number.isFinite(usd) || usd === 0) return "$0";
	if (usd < 0.01) return `$${usd.toFixed(4)}`;
	return `$${usd.toFixed(2)}`;
}

export function formatTokens(n: number): string {
	if (!Number.isFinite(n)) return "0";
	if (n < 1000) return String(Math.round(n));
	if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k`;
	return `${(n / 1_000_000).toFixed(1)}M`;
}

export function formatDuration(ms: number): string {
	if (!Number.isFinite(ms) || ms < 0) return "";
	if (ms < 1000) return `${Math.round(ms)}ms`;
	const s = ms / 1000;
	if (s < 60) return `${s.toFixed(s < 10 ? 1 : 0)}s`;
	const m = Math.floor(s / 60);
	if (m < 60) return `${m}m ${Math.round(s % 60)}s`;
	return `${Math.floor(m / 60)}h ${m % 60}m`;
}

const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
	["year", 365 * 24 * 3600_000],
	["month", 30 * 24 * 3600_000],
	["week", 7 * 24 * 3600_000],
	["day", 24 * 3600_000],
	["hour", 3600_000],
	["minute", 60_000],
];

export function formatRelative(ts: number, now = Date.now()): string {
	const diff = ts - now;
	const rtf = new Intl.RelativeTimeFormat(i18n.language, {
		numeric: "auto",
		style: "narrow",
	});
	for (const [unit, ms] of UNITS) {
		if (Math.abs(diff) >= ms) return rtf.format(Math.round(diff / ms), unit);
	}
	return rtf.format(0, "minute");
}

export function formatDateTime(ts: number): string {
	return new Intl.DateTimeFormat(i18n.language, {
		dateStyle: "medium",
		timeStyle: "short",
	}).format(ts);
}
