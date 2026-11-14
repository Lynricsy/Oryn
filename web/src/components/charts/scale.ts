/** 图表的纯计算：纵轴取整与横轴标签抽稀（M10 统计页）。 */

/**
 * 纵轴上限按 1-2-5 取整：`step` 是 `ticks` 等分后取到的 1 / 2 / 5 × 10^n，`max = step × ticks`（≥ 输入）。
 * 输入为 0、负数或非有限数时给一根单位刻度（`{max: ticks, step: 1}`），空图也有轴。
 */
export function niceScale(
	max: number,
	ticks = 4,
): { max: number; step: number } {
	if (!(Number.isFinite(max) && max > 0)) return { max: ticks, step: 1 };
	const raw = max / ticks;
	const mag = 10 ** Math.floor(Math.log10(raw));
	const frac = raw / mag;
	const nice = frac <= 1 ? 1 : frac <= 2 ? 2 : frac <= 5 ? 5 : 10;
	// 浮点误差（0.1 × 3 = 0.30000000000000004）：按量级取整到 12 位有效数字
	const step = Number((nice * mag).toPrecision(12));
	return { max: Number((step * ticks).toPrecision(12)), step };
}

/**
 * 横轴标签抽稀：返回与 `items` 等长的布尔数组，为真的位置显示标签。
 * 等间隔取，首个必显示；不超过 `maxLabels` 个（至少 1 个）。
 */
export function thin<T>(items: readonly T[], maxLabels: number): boolean[] {
	const n = items.length;
	const cap = Math.max(1, Math.floor(maxLabels));
	const every = Math.max(1, Math.ceil(n / cap));
	return items.map((_, i) => i % every === 0);
}
