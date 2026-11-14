/**
 * 定时的显示与表单换算（M9）：表达式摘要 `describeSchedule`、cron 预设、`every` 时长串的拆分与拼接、
 * 定义 → 表单输入（`inputOfSchedule`）。只做显示，校验以服务端（`internal/schedule/spec.go`）为准。
 */
import type { TFunction } from "i18next";
import type { Schemas } from "../api/client";
import { formatDateTime } from "./format";

type ScheduleLike = Pick<
	Schemas["Schedule"],
	"kind" | "cron" | "every" | "at" | "timezone"
>;

/** 对话框里的四个 cron 预设（同时是摘要能翻成短语的四种表达式）。 */
export const CRON_PRESETS = [
	{ key: "daily9", cron: "0 9 * * *" },
	{ key: "weekdays9", cron: "0 9 * * 1-5" },
	{ key: "hourly", cron: "0 * * * *" },
	{ key: "monday9", cron: "0 9 * * 1" },
] as const;

export type CronPresetKey = (typeof CRON_PRESETS)[number]["key"];

/** 可用占位符（投递时由服务端替换）。 */
export const SCHEDULE_PLACEHOLDERS = [
	"{{now}}",
	"{{date}}",
	"{{scheduled_at}}",
	"{{run_number}}",
	"{{last_run_at}}",
] as const;

export function cronPreset(cron: string | undefined): CronPresetKey | null {
	const norm = (cron ?? "").trim().replace(/\s+/g, " ");
	return CRON_PRESETS.find((p) => p.cron === norm)?.key ?? null;
}

/** Go 时长串（`30m0s`、`1h30m0s`、`90s`、`2h`）→ 秒；无法解析为 null。 */
export function parseGoDuration(s: string | undefined): number | null {
	if (!s) return null;
	const re = /(\d+(?:\.\d+)?)(h|m|s)/g;
	let total = 0;
	let consumed = 0;
	for (const m of s.matchAll(re)) {
		const n = Number(m[1]);
		total += n * (m[2] === "h" ? 3600 : m[2] === "m" ? 60 : 1);
		consumed += m[0].length;
	}
	return consumed === s.length && consumed > 0 ? total : null;
}

export type EveryUnit = "m" | "h" | "d";

/** 秒 → 表单里的「数值 + 单位」（取能整除的最大单位）。 */
export function splitEvery(seconds: number): {
	value: number;
	unit: EveryUnit;
} {
	if (seconds > 0 && seconds % 86_400 === 0)
		return { value: seconds / 86_400, unit: "d" };
	if (seconds > 0 && seconds % 3600 === 0)
		return { value: seconds / 3600, unit: "h" };
	return { value: Math.max(1, Math.round(seconds / 60)), unit: "m" };
}

/** 表单的「数值 + 单位」→ 服务端接受的时长串（天换算成小时：Go 不认 `d`）。 */
export function joinEvery(value: number, unit: EveryUnit): string {
	const n = Math.max(1, Math.floor(value));
	if (unit === "d") return `${n * 24}h`;
	return `${n}${unit}`;
}

function browserZone(): string {
	try {
		return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
	} catch {
		return "UTC";
	}
}

/** 间隔短语：整天 / 整小时 / 小时加分钟 / 分钟。 */
function describeEvery(seconds: number, t: TFunction): string {
	if (seconds % 86_400 === 0)
		return t("schedule.describe.everyDays", { n: seconds / 86_400 });
	if (seconds % 3600 === 0)
		return t("schedule.describe.everyHours", { n: seconds / 3600 });
	if (seconds >= 3600 && seconds % 60 === 0)
		return t("schedule.describe.everyHm", {
			h: Math.floor(seconds / 3600),
			m: (seconds % 3600) / 60,
		});
	return t("schedule.describe.everyMinutes", {
		n: Math.max(1, Math.round(seconds / 60)),
	});
}

/**
 * 表达式摘要：`every` →「每 30 分钟」；`once` → 本地化时间；`cron` 命中四个预设时给短语
 * （时区与浏览器不同时补上时区），否则原样显示表达式 + 时区。
 */
export function describeSchedule(s: ScheduleLike, t: TFunction): string {
	if (s.kind === "every") {
		const sec = parseGoDuration(s.every);
		return sec === null ? (s.every ?? "") : describeEvery(sec, t);
	}
	if (s.kind === "once")
		return s.at === undefined
			? t("schedule.describe.onceUnset")
			: t("schedule.describe.once", { time: formatDateTime(s.at) });
	const preset = cronPreset(s.cron);
	const zone = s.timezone || "UTC";
	if (preset) {
		const phrase = t(`schedule.preset.${preset}`);
		return zone === browserZone() ? phrase : `${phrase} · ${zone}`;
	}
	return `${(s.cron ?? "").trim()} · ${zone}`;
}

/** 已有定义 → 表单输入（去掉只读字段）。 */
export function inputOfSchedule(
	s: Schemas["Schedule"],
): Schemas["ScheduleInput"] {
	return {
		mode: s.mode,
		...(s.threadId ? { threadId: s.threadId } : {}),
		name: s.name,
		kind: s.kind,
		...(s.cron !== undefined ? { cron: s.cron } : {}),
		...(s.every !== undefined ? { every: s.every } : {}),
		...(s.at !== undefined ? { at: s.at } : {}),
		timezone: s.timezone,
		...(s.endsAt !== undefined ? { endsAt: s.endsAt } : {}),
		prompt: s.prompt,
		enabled: s.enabled,
		...(s.projectId ? { projectId: s.projectId } : {}),
		...(s.spaceId ? { spaceId: s.spaceId } : {}),
		...(s.model ? { model: s.model } : {}),
		...(s.thinkingLevel ? { thinkingLevel: s.thinkingLevel } : {}),
		...(s.provider ? { provider: s.provider } : {}),
		...(s.orbType ? { orbType: s.orbType } : {}),
		overlap: s.overlap,
		keepLast: s.keepLast,
	};
}

/** 侧栏 / 列表里给定时的显示名：有名字用名字，线程内定时无名时取兜底文案。 */
export function scheduleLabel(
	s: Pick<Schemas["Schedule"], "name"> | undefined,
	fallback: string,
): string {
	return s?.name.trim() || fallback;
}
