import { describe, expect, it } from "vitest";
import {
	coarseGrain,
	DEFAULT_PARAMS,
	grainFor,
	parseStatsParams,
	previousWindow,
	statsSearch,
	statsWindow,
} from "./statsParams";

/** 本地时间 2026-11-15 14:30（窗口按本地零点切，与测试机时区无关）。 */
const NOW = new Date(2026, 10, 15, 14, 30).getTime();
const day = (y: number, m: number, d: number) =>
	new Date(y, m - 1, d).getTime();

describe("statsWindow", () => {
	it.each([
		["today", day(2026, 11, 15), 1, "hour"],
		["7d", day(2026, 11, 9), 7, "day"],
		["30d", day(2026, 10, 17), 30, "day"],
		["90d", day(2026, 8, 18), 90, "day"],
		["month", day(2026, 11, 1), 15, "day"],
	] as const)(
		"%s：从本地零点到明天零点，粒度按天数",
		(range, from, days, grain) => {
			const w = statsWindow({ range }, NOW, undefined, "Asia/Shanghai");
			expect(w).toEqual({
				from,
				to: day(2026, 11, 16),
				tz: "Asia/Shanghai",
				days,
				grain,
			});
		},
	);

	it("all：起点取 earliestTs 当天零点；没有时退回 30 天", () => {
		const w = statsWindow(
			{ range: "all" },
			NOW,
			day(2025, 9, 3) + 5 * 3600_000,
		);
		expect(w.from).toBe(day(2025, 9, 3));
		expect(w.grain).toBe("month");
		expect(statsWindow({ range: "all" }, NOW).from).toBe(day(2026, 10, 17));
	});

	it("custom：首尾两天都含；跨 120 天按周", () => {
		const w = statsWindow(
			{ range: "custom", from: "2026-07-01", to: "2026-10-28" },
			NOW,
		);
		expect(w.from).toBe(day(2026, 7, 1));
		expect(w.to).toBe(day(2026, 10, 29));
		expect(w.days).toBe(120);
		expect(w.grain).toBe("week");
	});

	it("粒度阈值：≤2 hour、≤92 day、≤400 week、其余 month；orb / 产出没有 hour", () => {
		expect([1, 2, 3, 92, 93, 400, 401].map(grainFor)).toEqual([
			"hour",
			"hour",
			"day",
			"day",
			"week",
			"week",
			"month",
		]);
		expect(coarseGrain("hour")).toBe("day");
		expect(coarseGrain("week")).toBe("week");
	});

	it("上一周期是等长的前一段", () => {
		const w = statsWindow({ range: "7d" }, NOW);
		expect(previousWindow(w)).toEqual({
			from: day(2026, 11, 2),
			to: day(2026, 11, 9),
		});
	});
});

describe("URL 往返", () => {
	it("缺省值不进 URL", () => {
		expect(statsSearch(DEFAULT_PARAMS)).toBe("");
		expect(parseStatsParams(new URLSearchParams())).toEqual(DEFAULT_PARAMS);
	});

	it("全部字段往返不变", () => {
		const p = {
			range: "custom" as const,
			from: "2026-11-01",
			to: "2026-11-10",
			by: "project" as const,
			metric: "tokens" as const,
			project: "prj_1",
			space: "none",
			model: "anthropic/claude-sonnet-5",
			thread: "T-1",
		};
		const s = statsSearch(p);
		expect(s).toBe(
			"range=custom&from=2026-11-01&to=2026-11-10&by=project&metric=tokens&project=prj_1&space=none&model=anthropic%2Fclaude-sonnet-5&thread=T-1",
		);
		expect(parseStatsParams(new URLSearchParams(s))).toEqual(p);
	});

	it("非法取值退回缺省；custom 日期不合法或颠倒时退回 30d", () => {
		expect(
			parseStatsParams(new URLSearchParams("range=year&by=x&metric=y")),
		).toEqual(DEFAULT_PARAMS);
		expect(
			parseStatsParams(
				new URLSearchParams("range=custom&from=2026-11-10&to=2026-11-01"),
			).range,
		).toBe("30d");
		expect(
			parseStatsParams(
				new URLSearchParams("range=custom&from=2026-02-30&to=2026-03-01"),
			).range,
		).toBe("30d");
	});
});
