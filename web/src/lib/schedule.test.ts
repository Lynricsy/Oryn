/** 定时的显示换算（M9）：表达式摘要、时长串拆分与拼接、cron 预设识别。 */
import { describe, expect, it } from "vitest";
import i18n from "../i18n";
import {
	cronPreset,
	describeSchedule,
	joinEvery,
	parseGoDuration,
	splitEvery,
} from "./schedule";

const t = i18n.t.bind(i18n);
const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;

describe("describeSchedule", () => {
	it("every：按能整除的最大单位给短语", () => {
		expect(
			describeSchedule({ kind: "every", every: "30m0s", timezone: zone }, t),
		).toBe("每 30 分钟");
		expect(
			describeSchedule({ kind: "every", every: "6h0m0s", timezone: zone }, t),
		).toBe("每 6 小时");
		expect(
			describeSchedule({ kind: "every", every: "1h30m0s", timezone: zone }, t),
		).toBe("每 1 小时 30 分钟");
		expect(
			describeSchedule({ kind: "every", every: "48h0m0s", timezone: zone }, t),
		).toBe("每 2 天");
	});

	it("every 无法解析时原样显示", () => {
		expect(
			describeSchedule({ kind: "every", every: "abc", timezone: zone }, t),
		).toBe("abc");
	});

	it("cron 命中预设给短语；时区与浏览器不同时补上时区", () => {
		expect(
			describeSchedule(
				{ kind: "cron", cron: "0 9 * * 1-5", timezone: zone },
				t,
			),
		).toBe("工作日 9:00");
		const other = zone === "Pacific/Fiji" ? "Asia/Tokyo" : "Pacific/Fiji";
		expect(
			describeSchedule(
				{ kind: "cron", cron: "0  9 * * *", timezone: other },
				t,
			),
		).toBe(`每天 9:00 · ${other}`);
	});

	it("cron 不是预设时原样显示表达式 + 时区", () => {
		expect(
			describeSchedule(
				{ kind: "cron", cron: "30 18 * * 5", timezone: "UTC" },
				t,
			),
		).toBe("30 18 * * 5 · UTC");
	});

	it("once：本地化时间；没有时间时给兜底", () => {
		const at = Date.UTC(2026, 10, 20, 1, 0);
		expect(describeSchedule({ kind: "once", at, timezone: zone }, t)).toMatch(
			/运行一次$/,
		);
		expect(describeSchedule({ kind: "once", timezone: zone }, t)).toBe(
			"一次（未设时间）",
		);
	});

	it("英文", async () => {
		await i18n.changeLanguage("en");
		try {
			expect(
				describeSchedule({ kind: "every", every: "30m0s", timezone: zone }, t),
			).toMatch(/30/);
			expect(
				describeSchedule(
					{ kind: "cron", cron: "0 * * * *", timezone: zone },
					t,
				),
			).not.toBe("每小时整点");
		} finally {
			await i18n.changeLanguage("zh-CN");
		}
	});
});

describe("时长串与预设", () => {
	it("parseGoDuration 认 orynd 的规范化串，拒绝残缺", () => {
		expect(parseGoDuration("6h0m0s")).toBe(21_600);
		expect(parseGoDuration("90s")).toBe(90);
		expect(parseGoDuration("2h")).toBe(7200);
		expect(parseGoDuration("2d")).toBeNull();
		expect(parseGoDuration("")).toBeNull();
	});

	it("splitEvery / joinEvery 往返；天换算成小时（Go 不认 d）", () => {
		expect(splitEvery(86_400 * 2)).toEqual({ value: 2, unit: "d" });
		expect(splitEvery(7200)).toEqual({ value: 2, unit: "h" });
		expect(splitEvery(1800)).toEqual({ value: 30, unit: "m" });
		expect(joinEvery(2, "d")).toBe("48h");
		expect(joinEvery(0, "m")).toBe("1m");
	});

	it("cronPreset 忽略多余空白", () => {
		expect(cronPreset(" 0  9 * * 1 ")).toBe("monday9");
		expect(cronPreset("0 10 * * *")).toBeNull();
	});
});
