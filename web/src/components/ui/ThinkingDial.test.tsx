/** 思考级别圆圈：off → xhigh 按 0%–100% 填充；max 为火苗；未知级别不渲染。 */
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { DIAL_LEVELS, ThinkingDial } from "./ThinkingDial";

afterEach(cleanup);

describe("ThinkingDial", () => {
	it.each([
		["off", 0, "关闭"],
		["minimal", 0.2, "最低"],
		["low", 0.4, "低"],
		["medium", 0.6, "中"],
		["high", 0.8, "高"],
		["xhigh", 1, "超高"],
	])("%s 填充 %s", (level, fill, name) => {
		render(<ThinkingDial level={level} />);
		const dial = screen.getByRole("img", { name: `思考级别：${name}` });
		expect(Number(dial.dataset.fill)).toBeCloseTo(fill);
		expect(dial.getAttribute("title")).toBe(`思考级别：${name}`);
		const svg = dial.querySelector("svg");
		// 0% 只有外圈；100% 是外圈 + 实心内圆；其余是外圈 + 扇形
		expect(svg?.querySelectorAll("circle").length).toBe(fill === 1 ? 2 : 1);
		expect(svg?.querySelectorAll("path").length).toBe(
			fill > 0 && fill < 1 ? 1 : 0,
		);
	});

	it("等级按 omp 枚举等分，相邻两级相差 20%", () => {
		const fills = DIAL_LEVELS.map((l) => {
			const { container, unmount } = render(<ThinkingDial level={l} />);
			const v = Number(
				container.querySelector<HTMLElement>("[data-fill]")?.dataset.fill,
			);
			unmount();
			return v;
		});
		for (let i = 1; i < fills.length; i++)
			expect((fills[i] ?? 0) - (fills[i - 1] ?? 0)).toBeCloseTo(0.2);
	});

	it("扇形从 12 点方向顺时针画：40% 的终点在右下", () => {
		render(<ThinkingDial level="low" size={14} />);
		const d =
			screen
				.getByRole("img", { name: "思考级别：低" })
				.querySelector("path")
				?.getAttribute("d") ?? "";
		const end = d.match(/1 ([\d.]+) ([\d.]+)Z$/);
		expect(end).not.toBeNull();
		expect(Number(end?.[1])).toBeGreaterThan(7);
		expect(Number(end?.[2])).toBeGreaterThan(7);
	});

	it("max 显示火苗", () => {
		render(<ThinkingDial level="max" />);
		const dial = screen.getByRole("img", { name: "思考级别：极限" });
		expect(dial.dataset.level).toBe("max");
		expect(dial.dataset.fill).toBeUndefined();
		expect(dial.className).toContain("text-warn");
		expect(dial.querySelector("svg")).not.toBeNull();
	});

	it.each([[undefined], ["inherit"], ["auto"], ["turbo"]])(
		"未知级别 %s 不渲染",
		(level) => {
			const { container } = render(<ThinkingDial level={level} />);
			expect(container.innerHTML).toBe("");
		},
	);
});
