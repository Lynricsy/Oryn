import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { formatPercent } from "../../lib/format";
import { LineChart } from "./LineChart";

const series = [{ key: "cpu", label: "CPU", color: "--viz-1" }];

function mount(values: number[], yFloor?: number) {
	render(
		<LineChart
			ariaLabel="CPU"
			points={values.map((v, i) => ({ x: i * 15_000, ys: [v] }))}
			series={series}
			yFloor={yFloor}
			formatX={(x) => `t${x / 15_000}`}
			formatY={formatPercent}
			height={160}
		/>,
	);
	return screen.getByTestId("line-chart");
}

/** 折线各点的 y 像素坐标。 */
function ys(chart: HTMLElement): number[] {
	const pts = chart.querySelector("polyline")?.getAttribute("points") ?? "";
	return pts.split(" ").map((p) => Number(p.split(",")[1]));
}

/** 纵轴刻度文字（自下而上）。 */
function ticks(chart: HTMLElement): string[] {
	return [...chart.querySelectorAll("text[text-anchor=end]")].map(
		(el) => el.textContent ?? "",
	);
}

describe("LineChart 纵轴", () => {
	it("CPU 超过单核 100% 不裁平：50/150/250 三点 y 各不相同，纵轴上限覆盖 250，浮层仍按原值（单核口径）", () => {
		const chart = mount([50, 150, 250], 100);
		const [a, b, c] = ys(chart) as [number, number, number];
		// 越大越靠上（y 越小），且三点互不相同
		expect(a).toBeGreaterThan(b);
		expect(b).toBeGreaterThan(c);
		// 与数值成比例：y = top + plotH × (1 − v / max)，三点等距
		expect(a - b).toBeCloseTo(b - c, 5);
		const top = ticks(chart).at(-1) ?? "";
		expect(Number.parseInt(top, 10)).toBeGreaterThanOrEqual(250);
		// 250 不顶到绘图区上沿（10px）
		expect(c).toBeGreaterThan(10);
		fireEvent.keyDown(chart, { key: "End" });
		const tip = screen.getByTestId("chart-tooltip");
		expect(within(tip).getByText("250%")).toBeInTheDocument();
	});

	it("数据不到下限时纵轴取下限（CPU 至少 100%）", () => {
		const chart = mount([20, 60], 100);
		expect(ticks(chart)).toEqual(["0%", "25%", "50%", "75%", "100%"]);
		// 60% 落在 60% 高度处
		const plotH = 160 - 10 - 22;
		expect(ys(chart)[1]).toBeCloseTo(10 + plotH * 0.4, 5);
	});
});
