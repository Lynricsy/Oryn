/** 图表配置：调色板上色只补没给颜色的数据集，模型的 options 深合并覆盖基础选项。 */
import { describe, expect, it } from "vitest";
import { buildChartConfig, type ChartTheme } from "./chart-config";

const THEME: ChartTheme = {
	palette: ["#111111", "#222222", "#333333"],
	text: "#555555",
	grid: "#dddddd",
	surface: "#ffffff",
	font: "sans-serif",
};

describe("buildChartConfig", () => {
	it("直角坐标图按数据集取色，已给颜色的不动", () => {
		const c = buildChartConfig(
			{
				chartType: "bar",
				data: {
					labels: ["a"],
					datasets: [
						{ data: [1] },
						{ data: [2], backgroundColor: "red" },
						{ data: [3], type: "line" },
					],
				},
			},
			THEME,
			true,
		);
		expect(c.data.datasets[0]).toMatchObject({ backgroundColor: "#111111" });
		expect(c.data.datasets[1]).toEqual({ data: [2], backgroundColor: "red" });
		expect(c.data.datasets[2]).toMatchObject({
			borderColor: "#333333",
			backgroundColor: "#33333333",
		});
	});

	it("饼图按数据点取色，扇区间用底色描边", () => {
		const c = buildChartConfig(
			{ chartType: "pie", data: { datasets: [{ data: [1, 2, 3, 4] }] } },
			THEME,
			true,
		);
		expect(c.data.datasets[0]).toMatchObject({
			backgroundColor: ["#111111", "#222222", "#333333", "#111111"],
			borderColor: "#ffffff",
		});
		expect(c.options).toMatchObject({ plugins: { legend: { display: true } } });
	});

	it("options 深合并覆盖基础选项；不动画时关掉动画", () => {
		const c = buildChartConfig(
			{
				chartType: "line",
				data: { datasets: [{ data: [1] }] },
				options: { plugins: { legend: { display: true } }, responsive: false },
			},
			THEME,
			false,
		);
		expect(c.options).toMatchObject({
			responsive: false,
			maintainAspectRatio: false,
			animation: false,
			plugins: {
				legend: { display: true, labels: { color: "#555555" } },
			},
		});
	});
});
