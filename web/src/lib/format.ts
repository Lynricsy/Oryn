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

/** 已运行时长：只取最大单位（47 分钟 / 3 小时 / 2 天），单位文字交给 Intl 按界面语言给出。 */
export function formatUptime(ms: number, lang: string): string {
	const min = Math.max(1, Math.floor(ms / 60_000));
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

/** 字节数：<1KiB 为 B，<1MiB 为 KB，<1GiB 为 MB（一位小数），否则 GB。 */
export function formatBytes(n: number): string {
	if (n < 1024) return `${n} B`;
	if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
	if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
	return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

/** 百分比取整；非有限数显示「—」。 */
export function formatPercent(n: number): string {
	return Number.isFinite(n) ? `${Math.round(n)}%` : "—";
}

/** 内存（MB 输入）：<1024 显示 MB，否则 GB 至多一位小数并去掉 `.0`。 */
export function formatMemoryMB(mb: number): string {
	if (mb < 1024) return `${Math.round(mb)} MB`;
	return `${Number((mb / 1024).toFixed(1))} GB`;
}

/** 核数（可为小数）：整数原样，否则至多一位小数。 */
export function formatCpus(n: number): string {
	return Number.isInteger(n) ? String(n) : Number(n.toFixed(1)).toString();
}

/** orb 规格的核数：0 表示不限（不限资源的 docker 类型，或 runner / 协调 orb）。 */
export function formatOrbCpus(cpus: number): string {
	return cpus > 0
		? i18n.t("panel.cores", { n: formatCpus(cpus) })
		: i18n.t("panel.cpuUnlimited");
}

/** orb 规格的内存（MiB）：0 表示不限。 */
export function formatOrbMemory(mb: number): string {
	return mb > 0 ? formatMemoryMB(mb) : i18n.t("panel.memoryUnlimited");
}

/** orb 规格的紧凑写法 `4C / 8 GB`：为 0 的一项写「不限」，两项都为 0 时合成一句。 */
export function formatOrbSpec(cpus: number, memoryMB: number): string {
	if (cpus <= 0 && memoryMB <= 0) return i18n.t("panel.resourcesUnlimited");
	const cpu = cpus > 0 ? `${formatCpus(cpus)}C` : i18n.t("panel.cpuUnlimited");
	return `${cpu} / ${formatOrbMemory(memoryMB)}`;
}

/** 配额读数后补「· N 台不限」：不限资源的 orb 没有配额可加，单独点明台数。 */
export function withUnlimitedCount(text: string, n: number): string {
	return n > 0
		? i18n.t("providers.overview.node.withUnlimited", { text, n })
		: text;
}

/** 计数按界面语言分组（如 48,200）。 */
export function formatCount(n: number): string {
	return new Intl.NumberFormat(i18n.language).format(n);
}
