import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StackedBars } from "./StackedBars";

const buckets = [
	{ key: "2026-11-13", label: "11/13" },
	{ key: "2026-11-14", label: "11/14" },
	{ key: "2026-11-15", label: "11/15" },
];
const series = [
	{ key: "a", label: "Sonnet 5", color: "--viz-1" },
	{ key: "b", label: "GPT 6.1", color: "--viz-2" },
];
// 合计 4 / 8 / 0：纵轴取整到 8（step 2）
const values = [
	[1, 3],
	[6, 2],
	[0, 0],
];

function mount() {
	render(
		<StackedBars
			ariaLabel="按模型的成本"
			buckets={buckets}
			series={series}
			values={values}
			format={(v) => `$${v}`}
			height={220}
		/>,
	);
	return screen.getByTestId("stats-chart");
}

const heightOf = (el: Element | null | undefined) =>
	Number(el?.getAttribute("height"));

describe("StackedBars", () => {
	it("每个桶一个 g[data-bucket]，零值不画柱段", () => {
		const chart = mount();
		const gs = chart.querySelectorAll("g[data-bucket]");
		expect([...gs].map((g) => g.getAttribute("data-bucket"))).toEqual(
			buckets.map((b) => b.key),
		);
		expect(gs[2]?.querySelectorAll("rect[data-series]")).toHaveLength(0);
	});

	it("堆叠高度与数值成比例（系列自下而上）", () => {
		const chart = mount();
		const [g0, g1] = chart.querySelectorAll("g[data-bucket]");
		const a0 = g0?.querySelector("rect[data-series=a]");
		const b0 = g0?.querySelector("rect[data-series=b]");
		const a1 = g1?.querySelector("rect[data-series=a]");
		// 同一根柱子里 b 是 a 的 3 倍；跨柱子 6 是 1 的 6 倍
		expect(heightOf(b0) / heightOf(a0)).toBeCloseTo(3, 5);
		expect(heightOf(a1) / heightOf(a0)).toBeCloseTo(6, 5);
		// 合计 8 = 纵轴上限，第二根柱子顶到绘图区顶边
		const plotH = 220 - 10 - 22;
		expect(
			heightOf(a1) + heightOf(g1?.querySelector("rect[data-series=b]")),
		).toBeCloseTo(plotH, 5);
		// b 叠在 a 上面：b 的底边 = a 的顶边
		expect(Number(b0?.getAttribute("y")) + heightOf(b0)).toBeCloseTo(
			Number(a0?.getAttribute("y")),
			5,
		);
	});

	it("方向键选桶，浮层列出各系列与合计；Esc 收起", () => {
		const chart = mount();
		expect(screen.queryByTestId("chart-tooltip")).toBeNull();
		fireEvent.keyDown(chart, { key: "ArrowRight" });
		let tip = screen.getByTestId("chart-tooltip");
		expect(tip).toHaveTextContent("11/13");
		expect(within(tip).getByText("Sonnet 5")).toBeInTheDocument();
		expect(within(tip).getByText("$1")).toBeInTheDocument();
		expect(within(tip).getByText("$3")).toBeInTheDocument();
		expect(within(tip).getByText("合计")).toBeInTheDocument();
		expect(within(tip).getByText("$4")).toBeInTheDocument();
		fireEvent.keyDown(chart, { key: "ArrowRight" });
		tip = screen.getByTestId("chart-tooltip");
		expect(tip).toHaveTextContent("11/14");
		expect(within(tip).getByText("$8")).toBeInTheDocument();
		expect(
			chart.querySelector("g[data-selected]")?.getAttribute("data-bucket"),
		).toBe("2026-11-14");
		fireEvent.keyDown(chart, { key: "Escape" });
		expect(screen.queryByTestId("chart-tooltip")).toBeNull();
	});

	it("点按一根柱子固定显示浮层，点图外空白处收起", () => {
		const chart = mount();
		const g = chart.querySelector("g[data-bucket='2026-11-15']") as Element;
		fireEvent.click(g);
		const tip = screen.getByTestId("chart-tooltip");
		expect(tip).toHaveTextContent("11/15");
		expect(within(tip).getByText("$0")).toBeInTheDocument();
		// 浮层不随指针离开消失（点按是固定的）
		fireEvent.pointerLeave(chart);
		expect(screen.getByTestId("chart-tooltip")).toBeInTheDocument();
		fireEvent.pointerDown(document.body);
		expect(screen.queryByTestId("chart-tooltip")).toBeNull();
	});
});
