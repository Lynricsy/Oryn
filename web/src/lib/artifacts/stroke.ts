// 画笔笔迹的平滑：画布编辑器与只读概览页共用。纯函数，不依赖应用状态（沙箱里的脚本也 import 它）。

/** 保留两位小数，路径文本短且稳定。 */
function num(v: number): string {
	return String(Math.round(v * 100) / 100);
}

/**
 * 把笔迹的点（x、y、压力三个一组）用中点二次贝塞尔平滑成 SVG 路径：相邻两点的中点作为曲线的端点、
 * 原始点作为控制点，首尾直线连到第一个与最后一个点。线宽由调用方的 stroke-width 决定（等宽，不看压力）。
 * 只有一个点时返回一段零长度的线，配合圆形线帽画成一个点；没有点时返回空串。
 */
export function strokePath(points: readonly number[]): string {
	const xs: number[] = [];
	const ys: number[] = [];
	for (let i = 0; i + 1 < points.length; i += 3) {
		xs.push(points[i] ?? 0);
		ys.push(points[i + 1] ?? 0);
	}
	const n = xs.length;
	if (n === 0) return "";
	const x0 = num(xs[0] ?? 0);
	const y0 = num(ys[0] ?? 0);
	if (n === 1) return `M${x0} ${y0}L${x0} ${y0}`;
	if (n === 2) return `M${x0} ${y0}L${num(xs[1] ?? 0)} ${num(ys[1] ?? 0)}`;
	let d = `M${x0} ${y0}`;
	// 先直线到第一段的中点，再逐点以原始点为控制点画到下一个中点
	d += `L${num(((xs[0] ?? 0) + (xs[1] ?? 0)) / 2)} ${num(((ys[0] ?? 0) + (ys[1] ?? 0)) / 2)}`;
	for (let i = 1; i < n - 1; i++) {
		const cx = xs[i] ?? 0;
		const cy = ys[i] ?? 0;
		const mx = (cx + (xs[i + 1] ?? 0)) / 2;
		const my = (cy + (ys[i + 1] ?? 0)) / 2;
		d += `Q${num(cx)} ${num(cy)} ${num(mx)} ${num(my)}`;
	}
	d += `L${num(xs[n - 1] ?? 0)} ${num(ys[n - 1] ?? 0)}`;
	return d;
}
