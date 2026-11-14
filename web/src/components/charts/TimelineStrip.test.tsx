import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { formatDateTime, formatDuration } from "../../lib/format";
import { TimelineStrip } from "./TimelineStrip";

const H = 3_600_000;
const T0 = Date.UTC(2026, 10, 15, 0, 0);
// 运行 1h → 冻结 1.5h → 失败 0.5h
const segments = [
	{ from: T0, to: T0 + H, state: "ready" },
	{ from: T0 + H, to: T0 + 2.5 * H, state: "frozen" },
	{ from: T0 + 2.5 * H, to: T0 + 3 * H, state: "failed" },
];
const expected = [
	{ state: "运行", from: T0, to: T0 + H },
	{ state: "冻结", from: T0 + H, to: T0 + 2.5 * H },
	{ state: "失败", from: T0 + 2.5 * H, to: T0 + 3 * H },
];

function mount() {
	render(
		<TimelineStrip
			ariaLabel="状态时间线"
			from={T0}
			to={T0 + 3 * H}
			segments={segments}
		/>,
	);
	return screen.getByRole("group", { name: "状态时间线" });
}

function expectTip(i: number) {
	const e = expected[i];
	if (!e) throw new Error("no segment");
	const tip = screen.getByTestId("chart-tooltip");
	expect(tip).toHaveTextContent(e.state);
	expect(within(tip).getByText(formatDateTime(e.from))).toBeInTheDocument();
	expect(within(tip).getByText(formatDateTime(e.to))).toBeInTheDocument();
	expect(
		within(tip).getByText(formatDuration(e.to - e.from)),
	).toBeInTheDocument();
}

describe("TimelineStrip 逐段数据", () => {
	it("可访问列表与各段一一对应：状态、起止时间、时长", () => {
		mount();
		const list = screen.getByRole("list", { name: "状态时间线：各段明细" });
		const items = within(list).getAllByRole("listitem");
		expect(items).toHaveLength(3);
		expected.forEach((e, i) => {
			expect(items[i]).toHaveTextContent(
				`${e.state}，${formatDateTime(e.from)} 至 ${formatDateTime(e.to)}，时长 ${formatDuration(e.to - e.from)}`,
			);
		});
	});

	it("键盘：外层单焦点，方向键 / Home / End 选段，浮层给出状态、起止与时长；Esc 收起", () => {
		const strip = mount();
		expect(strip).toHaveAttribute("tabindex", "0");
		expect(screen.queryByTestId("chart-tooltip")).toBeNull();
		fireEvent.keyDown(strip, { key: "ArrowRight" });
		expectTip(0);
		fireEvent.keyDown(strip, { key: "ArrowRight" });
		expectTip(1);
		expect(
			strip.querySelector("[data-selected]")?.getAttribute("data-state"),
		).toBe("frozen");
		fireEvent.keyDown(strip, { key: "End" });
		expectTip(2);
		fireEvent.keyDown(strip, { key: "Home" });
		expectTip(0);
		fireEvent.keyDown(strip, { key: "Escape" });
		expect(screen.queryByTestId("chart-tooltip")).toBeNull();
	});

	it("触屏点按：点到哪段固定显示哪段，再点同一段或点图外收起", () => {
		const strip = mount();
		// jsdom 不排版，宽度按 400px 兜底：x = 380 ≈ 2.85h，落在「失败」段
		fireEvent.click(strip, { clientX: 380 });
		expectTip(2);
		fireEvent.pointerLeave(strip);
		expectTip(2);
		// x = 200 = 1.5h，落在「冻结」段
		fireEvent.click(strip, { clientX: 200 });
		expectTip(1);
		fireEvent.click(strip, { clientX: 200 });
		expect(screen.queryByTestId("chart-tooltip")).toBeNull();
		fireEvent.click(strip, { clientX: 10 });
		expectTip(0);
		fireEvent.pointerDown(document.body);
		expect(screen.queryByTestId("chart-tooltip")).toBeNull();
	});
});
