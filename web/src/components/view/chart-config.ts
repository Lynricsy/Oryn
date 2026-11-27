/**
 * 视图 `chart` 块交给 Chart.js 的配置：没有给颜色的数据集按 `--viz-1…8` 上色，基础选项被模型的 `options` 覆盖。
 * 纯函数，不碰 DOM（主题色由调用方读好传入）。
 */
import type { ChartBlock, ChartType } from "../../lib/view/spec";

/** 当前主题下的图表配色（都是 `#rrggbb`）。 */
export interface ChartTheme {
	/** `--viz-1…8`。 */
	palette: string[];
	/** 刻度与图例文字。 */
	text: string;
	/** 网格线。 */
	grid: string;
	/** 扇区之间的描边（与底色相同，读作缝隙）。 */
	surface: string;
	font: string;
}

export interface ChartConfigShape {
	type: ChartType;
	data: { labels?: (string | number)[]; datasets: Record<string, unknown>[] };
	options: Record<string, unknown>;
}

/** 按数据点着色、每个数据点一种颜色的图型。 */
const PER_POINT: Partial<Record<string, true>> = {
	pie: true,
	doughnut: true,
	polarArea: true,
};
/** 描边为主、填充只做淡底的图型。 */
const OUTLINED: Partial<Record<string, true>> = {
	line: true,
	radar: true,
	scatter: true,
	bubble: true,
};

function isPlainObject(v: unknown): v is Record<string, unknown> {
	return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** 把 `over` 深合并进 `base`（over 优先；数组整体替换）。两者都不被修改。 */
function merge(
	base: Record<string, unknown>,
	over: Record<string, unknown>,
): Record<string, unknown> {
	const out: Record<string, unknown> = { ...base };
	for (const [k, v] of Object.entries(over)) {
		const b = out[k];
		out[k] = isPlainObject(b) && isPlainObject(v) ? merge(b, v) : v;
	}
	return out;
}

function colorDataset(
	ds: Record<string, unknown>,
	index: number,
	chartType: ChartType,
	theme: ChartTheme,
): Record<string, unknown> {
	// 模型给了任一种颜色就完全照它的
	if (ds.backgroundColor !== undefined || ds.borderColor !== undefined)
		return ds;
	const palette = theme.palette;
	const type = typeof ds.type === "string" ? ds.type : chartType;
	if (PER_POINT[type]) {
		const n = Array.isArray(ds.data) ? ds.data.length : 0;
		const colors = Array.from(
			{ length: n },
			(_, i) => palette[i % palette.length],
		);
		return {
			...ds,
			backgroundColor:
				type === "polarArea" ? colors.map((c) => `${c}b3`) : colors,
			borderColor: theme.surface,
			borderWidth: 2,
		};
	}
	const color = palette[index % palette.length];
	if (OUTLINED[type])
		return {
			...ds,
			borderColor: color,
			backgroundColor: `${color}33`,
			pointBackgroundColor: color,
		};
	return { ...ds, backgroundColor: color, borderColor: color };
}

export function buildChartConfig(
	block: Pick<ChartBlock, "chartType" | "data" | "options">,
	theme: ChartTheme,
	animate: boolean,
): ChartConfigShape {
	const datasets = block.data.datasets.map((ds, i) =>
		colorDataset(ds, i, block.chartType, theme),
	);
	const base: Record<string, unknown> = {
		responsive: true,
		maintainAspectRatio: false,
		plugins: {
			legend: {
				// 只有一组数据的直角坐标图不需要图例
				display: datasets.length > 1 || PER_POINT[block.chartType] === true,
				labels: { boxWidth: 10, boxHeight: 10, color: theme.text },
			},
		},
	};
	if (!animate) base.animation = false;
	return {
		type: block.chartType,
		data: { ...block.data, datasets },
		options: merge(base, block.options ?? {}),
	};
}
