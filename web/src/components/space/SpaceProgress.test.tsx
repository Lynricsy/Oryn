/**
 * Space 近 30 天进度图的键盘与读屏入口（M9 审查 W8）：视觉隐藏的日期滑块只用键盘就能选到任一天，
 * 读数行（`aria-live`）与滑块的 `aria-valuetext` 跟着变；聚焦时图表外框给焦点环。
 */
import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { SpaceProgressDay } from "../../api/space-queries";
import { SpaceProgress } from "./SpaceProgress";

/** 30 天：第 i 天交付 i % 3、归档 i % 2、未结束 i。 */
const DAYS: SpaceProgressDay[] = Array.from({ length: 30 }, (_, i) => ({
	date: `2026-10-${String(i + 1).padStart(2, "0")}`,
	shipped: i % 3,
	archived: i % 2,
	open: i,
}));

const readout = () => screen.getByTestId("space-progress-readout");

describe("SpaceProgress：键盘选日期", () => {
	it("只用键盘（左右键、Home、End）切换日期，读数与 aria-valuetext 跟着变", () => {
		render(<SpaceProgress days={DAYS} />);
		const slider = screen.getByRole("slider", { name: "选择日期" });
		expect(readout()).toHaveAttribute("aria-live", "polite");
		// 缺省是最近一天
		expect(slider).toHaveValue("29");
		expect(within(readout()).getByText("2026-10-30")).toBeTruthy();
		slider.focus();
		expect(slider).toHaveFocus();
		fireEvent.keyDown(slider, { key: "ArrowLeft" });
		expect(slider).toHaveValue("28");
		expect(within(readout()).getByText("2026-10-29")).toBeTruthy();
		expect(readout()).toHaveTextContent("交付 1");
		expect(readout()).toHaveTextContent("归档 0");
		expect(readout()).toHaveTextContent("未结束 28");
		expect(slider).toHaveAttribute(
			"aria-valuetext",
			"2026-10-29：交付 1，归档 0，未结束 28",
		);
		fireEvent.keyDown(slider, { key: "Home" });
		expect(slider).toHaveAttribute(
			"aria-valuetext",
			"2026-10-01：交付 0，归档 0，未结束 0",
		);
		expect(within(readout()).getByText("2026-10-01")).toBeTruthy();
		// 到头了不越界
		fireEvent.keyDown(slider, { key: "ArrowLeft" });
		expect(slider).toHaveValue("0");
		fireEvent.keyDown(slider, { key: "ArrowRight" });
		expect(within(readout()).getByText("2026-10-02")).toBeTruthy();
		fireEvent.keyDown(slider, { key: "End" });
		expect(within(readout()).getByText("2026-10-30")).toBeTruthy();
		fireEvent.keyDown(slider, { key: "PageUp" });
		expect(within(readout()).getByText("2026-10-23")).toBeTruthy();
	});

	it("悬停只是临时查看：移开后回到键盘 / 点按选定的那天", () => {
		const { container } = render(<SpaceProgress days={DAYS} />);
		const slider = screen.getByRole("slider", { name: "选择日期" });
		fireEvent.keyDown(slider, { key: "Home" });
		const svg = container.querySelector("svg") as SVGElement;
		fireEvent.mouseEnter(
			container.querySelector('[data-day="2026-10-15"]') as Element,
		);
		expect(within(readout()).getByText("2026-10-15")).toBeTruthy();
		fireEvent.mouseLeave(svg);
		expect(within(readout()).getByText("2026-10-01")).toBeTruthy();
	});

	it("没有数据时不渲染滑块", () => {
		render(<SpaceProgress days={[]} />);
		expect(screen.queryByRole("slider")).toBeNull();
	});
});
