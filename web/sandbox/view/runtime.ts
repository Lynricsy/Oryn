// HTML 视图的运行时（M33，docs/plan-m33.md 的 C9）：orynd 把它内联在视图文档 `<head>` 的最前面，先于模型写的任何脚本。
// 职责：同步应用 URL 片段里的主题（不闪烁）、注入基础样式、与父页面握手、报告内容高度、提供 `window.oryn`。
import {
	cleanTheme,
	decodeThemeFragment,
	fitsViewMessage,
	MAX_VIEW_PROMPT_CHARS,
	parseViewParentMessage,
	VIEW_PROTOCOL_VERSION,
	VIEW_THEME_VARS,
	type ViewPageBody,
	type ViewTheme,
} from "./messages";

/** `window.oryn`：页面能用的全部 API。 */
export interface ViewApi {
	/** 把一段文本回传给 agent；每个视图只交付第一次调用。 */
	sendPrompt(text: string): void;
	/** 图表系列色（--c1…--c6 的当前值）。 */
	readonly palette: string[];
}

const PALETTE_VARS = ["--c1", "--c2", "--c3", "--c4", "--c5", "--c6"];

interface ChartLike {
	defaults: {
		color: unknown;
		borderColor: unknown;
		font: { family?: unknown };
		plugins?: { colors?: { enabled?: boolean } };
	};
	instances: Record<string, { update(): void }>;
	register(...items: unknown[]): void;
}

interface DatasetLike {
	backgroundColor?: unknown;
	borderColor?: unknown;
	data?: unknown[];
}

/**
 * 在窗口 `win` 上安装运行时；`parent` 是页面加载前缓存的 `window.parent`，`css` 是基础样式。
 * 只能调用一次（`window.oryn` 定义后不可再定义）。
 */
export function install(win: Window, parent: Window, css: string): void {
	const doc = win.document;
	const root = doc.documentElement;
	const getStyle = win.getComputedStyle.bind(win);
	let channel: string | null = null;
	let lastHeight = -1;
	let prompted = false;
	let pendingPrompt: string | null = null;

	const send = (body: ViewPageBody) => {
		if (channel === null) return;
		const msg = { v: VIEW_PROTOCOL_VERSION, channel, ...body };
		if (!fitsViewMessage(msg)) return;
		parent.postMessage(msg, "*");
	};

	const cssVar = (name: string) => getStyle(root).getPropertyValue(name).trim();

	// ---- 主题 ----
	const applyTheme = (theme: ViewTheme) => {
		for (const name of VIEW_THEME_VARS) {
			const v = theme.vars[name];
			if (v) root.style.setProperty(name, v);
		}
		root.style.colorScheme = theme.scheme;
		root.setAttribute("data-scheme", theme.scheme);
	};
	const initial = decodeThemeFragment(win.location.hash);
	if (initial) applyTheme(initial);

	const style = doc.createElement("style");
	style.textContent = css;
	(doc.head ?? root).appendChild(style);

	// ---- 图表（libs 含 chartjs 时，orynd 在 Chart.js 之后调用 __orynViewChart） ----
	// 没有给颜色的数据集按 --c1…--c6 上色；主题变化后重新上色并重绘。
	const colored = new WeakSet<object>();
	let chartPluginAdded = false;
	const chartOf = (): ChartLike | null => {
		const Chart: unknown = Reflect.get(win, "Chart");
		// Chart.js 的 UMD 产物挂在全局上；它的形状由库保证，这里只确认存在
		return typeof Chart === "function" && "defaults" in Chart
			? (Chart as unknown as ChartLike)
			: null;
	};
	const paletteNow = () => PALETTE_VARS.map((n) => cssVar(n) || "#888");
	const chartTheme = () => {
		const Chart = chartOf();
		if (!Chart) return;
		Chart.defaults.color = cssVar("--muted") || "#888";
		Chart.defaults.borderColor = cssVar("--border") || "rgba(127,127,127,.2)";
		Chart.defaults.font.family = cssVar("--font") || "system-ui";
		if (Chart.defaults.plugins?.colors)
			Chart.defaults.plugins.colors.enabled = false;
		if (chartPluginAdded) return;
		chartPluginAdded = true;
		Chart.register({
			id: "orynPalette",
			beforeUpdate(chart: {
				config: { type?: string };
				data: { datasets?: DatasetLike[] };
			}) {
				const palette = paletteNow();
				const perPoint = ["pie", "doughnut", "polarArea"].includes(
					chart.config.type ?? "",
				);
				const surface = cssVar("--surface") || "#fff";
				(chart.data.datasets ?? []).forEach((ds, i) => {
					const own =
						ds.backgroundColor === undefined && ds.borderColor === undefined;
					if (!own && !colored.has(ds)) return;
					colored.add(ds);
					if (perPoint) {
						const n = Array.isArray(ds.data) ? ds.data.length : 0;
						ds.backgroundColor = Array.from(
							{ length: n },
							(_, j) => palette[j % palette.length],
						);
						ds.borderColor = surface;
					} else {
						const c = palette[i % palette.length];
						ds.backgroundColor = c;
						ds.borderColor = c;
					}
				});
			},
		});
	};
	Object.defineProperty(win, "__orynViewChart", { value: chartTheme });

	// ---- window.oryn ----
	const flushPrompt = () => {
		if (channel === null || pendingPrompt === null) return;
		send({ kind: "prompt", text: pendingPrompt });
		pendingPrompt = null;
	};
	const api: ViewApi = Object.freeze({
		sendPrompt(text: string) {
			if (prompted || typeof text !== "string") return;
			const t = text.slice(0, MAX_VIEW_PROMPT_CHARS);
			if (!t.trim()) return;
			prompted = true;
			pendingPrompt = t;
			flushPrompt();
		},
		get palette() {
			return paletteNow();
		},
	});
	Object.defineProperty(win, "oryn", { value: api, enumerable: true });

	// ---- 高度 ----
	// scrollHeight 取整时可能舍掉小数部分（差零点几像素也会出现内部滚动条），与布局框的实际高度取大再向上取整
	const measure = () => {
		const body = doc.body;
		if (!body) return Math.ceil(root.scrollHeight);
		const cs = getStyle(body);
		return Math.ceil(
			Math.max(body.scrollHeight, body.getBoundingClientRect().height) +
				(Number.parseFloat(cs.marginTop) || 0) +
				(Number.parseFloat(cs.marginBottom) || 0),
		);
	};
	let scheduled = false;
	const report = () => {
		scheduled = false;
		const h = measure();
		if (h === lastHeight || channel === null) return;
		lastHeight = h;
		send({ kind: "resize", height: h });
	};
	const scheduleReport = () => {
		if (scheduled) return;
		scheduled = true;
		win.requestAnimationFrame(report);
	};
	const observer =
		typeof ResizeObserver === "function"
			? new ResizeObserver(scheduleReport)
			: null;
	observer?.observe(root);

	// ---- 父页面消息 ----
	win.addEventListener(
		"message",
		(event: MessageEvent) => {
			if (event.source !== parent) return;
			const msg = parseViewParentMessage(event.data);
			if (!msg) return;
			if (channel === null) {
				if (msg.kind !== "hello") return;
				channel = msg.channel;
			} else if (msg.channel !== channel) {
				return;
			}
			// 协议消息不再交给页面自己的监听器
			event.stopImmediatePropagation();
			if (msg.kind === "hello") {
				send({ kind: "ready" });
				lastHeight = -1;
				report();
				flushPrompt();
				return;
			}
			const theme = cleanTheme(msg);
			if (!theme) return;
			applyTheme(theme);
			const Chart = chartOf();
			if (Chart && chartPluginAdded) {
				chartTheme();
				for (const c of Object.values(Chart.instances)) c.update();
			}
		},
		true,
	);

	// ---- DOM 就绪后 ----
	const onReady = () => {
		if (doc.body) observer?.observe(doc.body);
		for (const el of doc.querySelectorAll<HTMLInputElement>(
			'input[type="range"]',
		))
			rangeFill(el);
		scheduleReport();
	};
	if (doc.readyState === "loading")
		doc.addEventListener("DOMContentLoaded", onReady, { once: true });
	else onReady();
	win.addEventListener("load", scheduleReport);

	// 滑块轨道的填充比例
	win.addEventListener(
		"input",
		(event) => {
			const t = event.target as Partial<HTMLInputElement> | null;
			if (t?.type === "range") rangeFill(t as HTMLInputElement);
		},
		true,
	);

	// 帧不能弹窗、也不该被导航走：跳出本文档的链接一律拦下（页内锚点照常）
	win.addEventListener(
		"click",
		(event: MouseEvent) => {
			const target = event.target as Partial<Element> | null;
			if (typeof target?.closest !== "function") return;
			const link = target.closest("a[href]");
			if (!link) return;
			const href = link.getAttribute("href") ?? "";
			if (href.startsWith("#")) return;
			event.preventDefault();
		},
		true,
	);
}

function rangeFill(el: HTMLInputElement): void {
	const min = Number.parseFloat(el.min || "0");
	const max = Number.parseFloat(el.max || "100");
	const v = Number.parseFloat(el.value);
	const p = max > min ? ((v - min) / (max - min)) * 100 : 0;
	el.style.setProperty("--p", `${Math.max(0, Math.min(100, p))}%`);
}
