/** 视图的 `chart` 块：Chart.js 懒加载（独立分块），主题切换后按新令牌重画，卸载时销毁。 */
import type { Chart, ChartConfiguration } from "chart.js";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { cssColor } from "../../lib/css-color";
import { motionAllowed } from "../../lib/motion";
import type { ChartBlock } from "../../lib/view/spec";
import { THEME_EVENT } from "../../state/theme";
import { buildChartConfig, type ChartTheme } from "./chart-config";

let chartModule: Promise<typeof Chart> | null = null;

function loadChartJs(): Promise<typeof Chart> {
	chartModule ??= import("chart.js/auto").then(
		(m) => m.default,
		(err: unknown) => {
			// 失败（断网、分块过期）不缓存，下次挂载再试
			chartModule = null;
			throw err;
		},
	);
	return chartModule;
}

/** 当前主题的图表配色；读不到画布（jsdom）时取浅色主题的近似值。 */
function readTheme(): ChartTheme {
	const fallback = ["#3f7fbf", "#c2417f", "#3fb3c2", "#5b3fbf", "#d9822b"];
	return {
		palette: Array.from({ length: 8 }, (_, i) =>
			cssColor(`--viz-${i + 1}`, fallback[i % fallback.length] ?? "#888888"),
		),
		text: cssColor("--text-muted", "#5f6368"),
		grid: cssColor("--line", "#e3e5e8"),
		surface: cssColor("--panel", "#fcfcfd"),
		font:
			getComputedStyle(document.documentElement)
				.getPropertyValue("--sans")
				.trim() || "sans-serif",
	};
}

/** 主题切换计数：每次 THEME_EVENT 加一。 */
function useThemeVersion(): number {
	const [version, setVersion] = useState(0);
	useEffect(() => {
		const onTheme = () => setVersion((v) => v + 1);
		window.addEventListener(THEME_EVENT, onTheme);
		return () => window.removeEventListener(THEME_EVENT, onTheme);
	}, []);
	return version;
}

function Skeleton({ height }: { height: number }): ReactNode {
	return (
		<div
			aria-hidden
			className="w-full rounded-md bg-inset"
			style={{ height }}
		/>
	);
}

export function ChartView({
	block,
	streaming,
}: {
	block: ChartBlock;
	streaming: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const canvasRef = useRef<HTMLCanvasElement>(null);
	const [state, setState] = useState<"loading" | "ready" | "error">("loading");
	const themeVersion = useThemeVersion();
	// 规格每次渲染都会重新归一化成新对象：按内容判断要不要重画
	const configKey = useMemo(
		() => JSON.stringify([block.chartType, block.data, block.options]),
		[block.chartType, block.data, block.options],
	);
	const blockRef = useRef(block);
	blockRef.current = block;

	// biome-ignore lint/correctness/useExhaustiveDependencies: configKey 与 themeVersion 是重画的触发条件，块本身经 ref 读取
	useEffect(() => {
		if (streaming) return;
		let alive = true;
		let chart: Chart | null = null;
		setState("loading");
		loadChartJs()
			.then((ChartJs) => {
				const canvas = canvasRef.current;
				if (!alive || !canvas) return;
				const theme = readTheme();
				ChartJs.defaults.color = theme.text;
				ChartJs.defaults.borderColor = theme.grid;
				ChartJs.defaults.font.family = theme.font;
				ChartJs.defaults.font.size = 12;
				const config = buildChartConfig(
					blockRef.current,
					theme,
					motionAllowed(),
				);
				// 配置来自模型：结构由 Chart.js 自己解释，这里只保证是普通对象
				chart = new ChartJs(canvas, config as unknown as ChartConfiguration);
				setState("ready");
			})
			.catch(() => {
				if (alive) setState("error");
			});
		return () => {
			alive = false;
			chart?.destroy();
		};
	}, [configKey, themeVersion, streaming]);

	const label = block.title ?? t("view.block.chart");
	return (
		<figure className="m-0 flex min-w-0 flex-col gap-2">
			{block.title && (
				<figcaption className="font-medium text-fg text-sm">
					{block.title}
				</figcaption>
			)}
			{streaming ? (
				<Skeleton height={block.height} />
			) : state === "error" ? (
				<p
					className="m-0 flex items-center justify-center rounded-md bg-inset text-faint text-xs"
					style={{ height: block.height }}
				>
					{t("view.block.chartFailed")}
				</p>
			) : (
				<div className="relative w-full" style={{ height: block.height }}>
					{state === "loading" && (
						<div aria-hidden className="absolute inset-0 rounded-md bg-inset" />
					)}
					<canvas ref={canvasRef} role="img" aria-label={label} />
				</div>
			)}
		</figure>
	);
}
