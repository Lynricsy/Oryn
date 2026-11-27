import type { CSSProperties } from "react";

/**
 * 把 omp SVG 约定的主题变量（`--fg --muted --border --surface --accent --success --warning --error --c1…--c6`）
 * 映射到 Oryn 当前明暗 / 主题色令牌。放在内联 SVG 的容器上（ask 表单的插图、视图的 `svg` 块）。
 */
export const SVG_THEME_VARS = {
	"--fg": "var(--text)",
	"--muted": "var(--text-muted)",
	"--border": "var(--line)",
	"--surface": "var(--panel)",
	"--accent": "var(--brand)",
	"--success": "var(--good)",
	"--warning": "var(--caution)",
	"--error": "var(--bad)",
	"--c1": "var(--viz-1)",
	"--c2": "var(--viz-2)",
	"--c3": "var(--viz-3)",
	"--c4": "var(--viz-4)",
	"--c5": "var(--viz-5)",
	"--c6": "var(--viz-6)",
} as CSSProperties;
