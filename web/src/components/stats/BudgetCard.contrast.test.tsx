/**
 * 预算卡的级别文字对比度（M10 审查 W4）：「接近上限」用 `text-warn-ink`（`--caution-ink`），「已超出」用 `text-err`（`--bad`），
 * 两者在明暗两套主题、6 个主题色预设下落在卡片底色 `--panel` 上都要 ≥ 4.5:1。jsdom 不算样式，这里直接读 index.css 原文，
 * 按层叠顺序解析令牌（`oklch()`、`color-mix(in oklab, …)`、`var()`），换算到 sRGB（超色域裁剪）后按 WCAG 相对亮度计算。
 */
import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, it } from "vitest";
import type { BudgetStatus } from "../../api/stats-queries";
import { BudgetCard } from "./BudgetCard";

let CSS = "";
beforeAll(async () => {
	// vitest 把 CSS 导入处理成空串，直接读源文件（工作目录是 web/）；前端 tsconfig 不带 Node 类型，只声明用到的函数
	const fs = (await import("node:fs" as string)) as {
		readFileSync(path: string, encoding: "utf8"): string;
	};
	CSS = fs
		.readFileSync("src/index.css", "utf8")
		.replace(/\/\*[\s\S]*?\*\//g, "");
});
const ACCENTS = ["default", "cobalt", "violet", "berry", "fox", "graphite"];

/** 某主题 + 主题色下 `:root` 上生效的自定义属性（按源码顺序叠加匹配的 `:root…` 规则；后出现的选择器特异性不低于前者）。 */
function tokens(dark: boolean, accent: string): Map<string, string> {
	const out = new Map<string, string>();
	for (const [, sel = "", body = ""] of CSS.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
		// 选择器前可能还挂着 `@import …;` 这类语句，只取最后一段
		const s = sel.split(";").at(-1)?.trim() ?? "";
		if (!s.startsWith(":root")) continue;
		const rest = s
			.slice(":root".length)
			.replaceAll(`[data-accent="${accent}"]`, "")
			.replaceAll(`:not([data-theme="dark"])`, dark ? "✗" : "")
			.replaceAll(`[data-theme="dark"]`, dark ? "" : "✗");
		if (rest !== "") continue;
		for (const [, k = "", v = ""] of body.matchAll(
			/(--[\w-]+)\s*:\s*([^;]+);/g,
		))
			out.set(k, v.trim());
	}
	return out;
}

type Lab = [number, number, number];

function resolve(expr: string, env: Map<string, string>): string {
	return expr.replace(/var\((--[\w-]+)\)/g, (_, k: string) =>
		resolve(env.get(k) ?? "", env),
	);
}

function color(expr: string, env: Map<string, string>): Lab {
	const e = resolve(expr, env).trim();
	const ok = /^oklch\(\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)\s*\)$/.exec(e);
	if (ok) {
		const [L, C, h] = ok.slice(1).map(Number) as [number, number, number];
		const r = (h * Math.PI) / 180;
		return [L, C * Math.cos(r), C * Math.sin(r)];
	}
	const mix = /^color-mix\(in oklab,\s*(.+?)\s+([\d.]+)%,\s*(.+)\)$/.exec(e);
	if (mix) {
		const p = Number(mix[2]) / 100;
		const [a0, a1, a2] = color(mix[1] ?? "", env);
		const [b0, b1, b2] = color(mix[3] ?? "", env);
		return [
			p * a0 + (1 - p) * b0,
			p * a1 + (1 - p) * b1,
			p * a2 + (1 - p) * b2,
		];
	}
	throw new Error(`unsupported color: ${e}`);
}

/** OKLab → sRGB（裁剪到色域）→ WCAG 相对亮度。 */
function luminance([L, a, b]: Lab): number {
	const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3;
	const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3;
	const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3;
	const lin = [
		4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
		-1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
		-0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
	].map((c) => {
		// 裁剪后按 sRGB 编码 / 解码一次，与浏览器落到 8 位通道之前的值一致
		const v = Math.min(1, Math.max(0, c));
		const g = v <= 0.0031308 ? 12.92 * v : 1.055 * v ** (1 / 2.4) - 0.055;
		return g <= 0.04045 ? g / 12.92 : ((g + 0.055) / 1.055) ** 2.4;
	}) as [number, number, number];
	return 0.2126 * lin[0] + 0.7152 * lin[1] + 0.0722 * lin[2];
}

function contrast(fg: string, bg: string, env: Map<string, string>): number {
	const [hi, lo] = [luminance(color(fg, env)), luminance(color(bg, env))].sort(
		(x, y) => y - x,
	) as [number, number];
	return (hi + 0.05) / (lo + 0.05);
}

const status: BudgetStatus = {
	budget: {
		dailyUsd: 10,
		monthlyUsd: 200,
		warnPct: 80,
		timezone: "UTC",
		enabled: true,
	},
	day: {
		key: "2026-11-15",
		from: 0,
		to: 1,
		spentUsd: 8.5,
		limitUsd: 10,
		level: "warn",
	},
	month: {
		key: "2026-11",
		from: 0,
		to: 1,
		spentUsd: 214.5,
		limitUsd: 200,
		level: "over",
	},
};

describe("预算级别文字的对比度", () => {
	it("「接近上限」用 text-warn-ink、「已超出」用 text-err；Tailwind 主题把 warn-ink 映射到 --caution-ink", () => {
		render(<BudgetCard status={status} onEdit={() => {}} />);
		const [warn, over] = screen.getAllByTestId("budget-level");
		expect(warn).toHaveClass("text-warn-ink");
		expect(warn).not.toHaveClass("text-warn");
		expect(over).toHaveClass("text-err");
		expect(CSS).toMatch(/--color-warn-ink:\s*var\(--caution-ink\);/);
	});

	it.each(
		ACCENTS.flatMap((accent) =>
			[false, true].map((dark) => ({ accent, dark })),
		),
	)(
		"$accent / dark=$dark：--caution-ink 与 --bad 在 --panel 上 ≥ 4.5:1",
		({ accent, dark }) => {
			const env = tokens(dark, accent);
			expect(
				contrast("var(--caution-ink)", "var(--panel)", env),
			).toBeGreaterThanOrEqual(4.5);
			expect(
				contrast("var(--bad)", "var(--panel)", env),
			).toBeGreaterThanOrEqual(4.5);
		},
	);

	it("解析与换算可信：预设与暗色确实生效；浅色 --caution 在 --panel 上约 3.10:1（审查浏览器实测 3.1032），文字不能直接用它", () => {
		expect(tokens(false, "fox").get("--tint-h")).toBe("55");
		expect(tokens(true, "default").get("--caution-ink")).toBe("var(--caution)");
		expect(tokens(false, "default").get("--caution-ink")).toMatch(/^color-mix/);
		const env = tokens(false, "default");
		expect(contrast("var(--caution)", "var(--panel)", env)).toBeCloseTo(3.1, 1);
	});
});
