// 保留 Vite 的静态资源和 SPA 路由；只在 iOS E2E 的 mock 文档里插入观察器。
import { createWriteStream } from "node:fs";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { preview } from "vite";

function watchSink() {
	const base = "/__oryn_e2e_sink";
	const card = () => document.querySelector('[data-testid="sink-card"]');
	const label = (node) => node?.getAttribute?.("data-testid") ?? node?.localName;
	// Beacon 到达顺序不保证与调用顺序一致；同一页按 seq 回放，与 Swift 事件按 ts 对齐。
	let seq = 0;
	const line = (event, info) => {
		const payload = {
			ts: Date.now(),
			nav: performance.timeOrigin,
			seq: ++seq,
			event,
			open: card()?.hasAttribute("data-open"),
			info,
		};
		navigator.sendBeacon(base, JSON.stringify(payload));
	};
	const watch = (obj, method, detail) => {
		const original = obj[method];
		obj[method] = function (...args) {
			line(method, detail(this, args));
			return Reflect.apply(original, this, args);
		};
	};
	const rootScroll = (_, [arg]) => ({
		x: typeof arg === "number" ? arg : arg?.left,
		stack: new Error().stack,
	});
	for (const method of ["scroll", "scrollTo", "scrollBy"]) {
		watch(window, method, rootScroll);
	}
	watch(Element.prototype, "scrollIntoView", (node) => ({
		target: label(node),
		stack: new Error().stack,
	}));
	watch(HTMLElement.prototype, "focus", (node, [opts]) => ({
		target: label(node),
		preventScroll: opts?.preventScroll,
	}));
	const opts = { capture: true, passive: true };
	// 触摸事件带上当时的根横滚位置：按住拖动的步骤以 touchend 的 x 算卡片实际平移（sink 宽 − x），不靠手指位移推算
	for (const type of [
		"click", "touchstart", "touchend", "touchcancel", "focusin", "popstate", "resize",
	]) {
		window.addEventListener(
			type, (event) => line(type, { target: label(event.target), x: window.scrollX }), opts,
		);
	}
	window.visualViewport?.addEventListener(
		"resize", () => line("visualViewport.resize"), { passive: true },
	);
	// 视口采样（软键盘步骤 h-keyboard 的断言）：视觉视口的偏移 / 高 / 缩放、根滚动位置、--kb-inset、聚焦元素底边。
	// 探针装在页面脚本之前，同一事件里先于 lib/viewport.ts 的拉回执行：键盘相关的采样推到下一帧，记的是拉回之后的状态；
	// focusin 当场记一条，作为键盘弹出前的基线。与上一条相同的采样不重复发。
	let lastSample;
	const sample = (why) => {
		const vv = window.visualViewport;
		const active = document.activeElement;
		const info = {
			top: vv?.offsetTop,
			h: vv?.height,
			scale: vv?.scale,
			ih: window.innerHeight,
			sx: window.scrollX,
			sy: window.scrollY,
			kb: getComputedStyle(document.documentElement).getPropertyValue("--kb-inset").trim(),
			active: label(active),
			bottom: active && active !== document.body ? active.getBoundingClientRect().bottom : null,
		};
		const key = JSON.stringify(info);
		if (key === lastSample) return;
		lastSample = key;
		line("viewport", { why, ...info });
	};
	const later = (why) => () => requestAnimationFrame(() => sample(why));
	window.visualViewport?.addEventListener("resize", later("vv.resize"), { passive: true });
	window.visualViewport?.addEventListener("scroll", later("vv.scroll"), { passive: true });
	window.addEventListener("focusin", () => sample("focusin"), { capture: true, passive: true });
	// 只关心纵向：横向是下沉侧栏的开合，拖动时每帧都变。按纵向值有没有变化调度（含回到 0：纯根滚动复位时
	// offsetTop 一直是 0，不会有 visualViewport.scroll，不记这一条最后一条采样就停在推走时的旧值）
	let lastY = "0|0";
	window.addEventListener("scroll", () => {
		const y = `${window.scrollY}|${window.visualViewport?.offsetTop ?? 0}`;
		if (y === lastY) return;
		lastY = y;
		later("scroll")();
	}, { passive: true });
	let lastBand;
	window.addEventListener("scroll", () => {
		const x = window.scrollX;
		const band = Math.round(x / 16);
		if (band === lastBand) return;
		lastBand = band;
		line("window.scroll", { x, active: label(document.activeElement) });
	}, { passive: true });
	new MutationObserver((records) => {
		for (const event of records) {
			if (event.target.matches('[data-testid="sink-card"]')) line("data-open");
		}
	}).observe(document.documentElement, { subtree: true, attributes: true, attributeFilter: ["data-open"] });
	// 折叠屏往返（ios-duo-check.sh）：应用壳的 <main> 每出现一个新元素记一次 main.mount（不重挂载时全程只有一条）；
	// 下沉卡片出现 / 消失（跨 600px 换布局）时记 layout，带当时的视口宽
	const mains = new WeakSet();
	let mounts = 0;
	let sink;
	new MutationObserver(() => {
		for (const main of document.querySelectorAll("main")) {
			if (mains.has(main)) continue;
			mains.add(main);
			line("main.mount", { n: ++mounts });
		}
		const now = card() !== null;
		if (now !== sink) {
			sink = now;
			line("layout", { sink: now, w: window.innerWidth });
		}
		railSoon();
	}).observe(document.documentElement, { subtree: true, childList: true });
	// iPhone Duo 竖栏（ios-duo-check.sh）：竖栏一侧、竖栏里的侧栏开关（中心点，CSS px = 屏幕 pt，供 duoctl tap）、
	// 操作槽里的按钮数、操作槽里一个有开合状态的按钮（优先 aria-pressed 的信息面板，没有时取 aria-expanded 的「更多」菜单）
	// 的中心点与状态、竖排底栏在不在、
	// 页头里还看得见几个侧栏开关（竖栏在时应为 0）。与上一条相同不重复发。
	// 只在布局稳定后采：根滚动（下沉卡片开合）与视口变化的同一拍里 WebKit 可能给出过渡中的矩形（本机 x86Sim 实测：
	// 根滚动到 384 那一拍竖栏开关量成 x = 26，下一帧才回到 410），所以一律推到两帧之后再量；滚动也触发重采。
	// 同时记根滚动与视觉视口的横向位置（sx / vx）：本机还见过合上后稳定量成 26、画面上却在 410 的情况，
	// 量出的点落不进原生竖栏矩形时（ios-duo-check.sh 的 rail_settled），靠这两个值分辨是不是视口错位
	let lastRail;
	const center = (el) => {
		const r = el?.getBoundingClientRect();
		return r ? { x: Math.round(r.x + r.width / 2), y: Math.round(r.y + r.height / 2) } : null;
	};
	const railSample = () => {
		const q = (s) => document.querySelector(s);
		const act =
			q('[data-testid="rail-actions"] button[aria-pressed]') ??
			q('[data-testid="rail-actions"] button[aria-expanded]');
		const info = {
			side: document.documentElement.dataset.rail ?? null,
			tap: center(q('[data-testid="rail-sidebar"]')),
			actions: document.querySelectorAll('[data-testid="rail-actions"] button').length,
			act: center(act),
			pressed: act ? (act.getAttribute("aria-pressed") ?? act.getAttribute("aria-expanded")) === "true" : null,
			tabs: q('[data-testid="rail"] [data-tone="rail"]') !== null,
			headerToggles: [...document.querySelectorAll("[data-sidebar-toggle]")].filter((e) => e.getClientRects().length > 0).length,
			w: window.innerWidth,
			sx: Math.round(window.scrollX),
			vx: Math.round(window.visualViewport?.pageLeft ?? window.scrollX),
		};
		const key = JSON.stringify(info);
		if (key === lastRail) return;
		lastRail = key;
		line("rail", info);
	};
	let queued = false;
	const railSoon = () => {
		if (queued) return;
		queued = true;
		requestAnimationFrame(() =>
			requestAnimationFrame(() => {
				queued = false;
				railSample();
			}),
		);
	};
	new MutationObserver(railSoon).observe(document.documentElement, {
		subtree: true, attributes: true, attributeFilter: ["data-rail", "aria-pressed", "aria-expanded"],
	});
	window.addEventListener("resize", railSoon, { passive: true });
	window.addEventListener("scroll", railSoon, { passive: true });
	window.visualViewport?.addEventListener("resize", railSoon, { passive: true });
	line("ready");
}

const [dist, port, artifact] = process.argv.slice(2);
const path = "/__oryn_e2e_sink";
const script = `<script>(${watchSink.toString()})();</script>`;
const html = (await readFile(join(dist, "index.html"), "utf8")).replace(
	"<head>",
	`<head>${script}`,
);
const output = createWriteStream(artifact, { flags: "a" });
await preview({
	build: { outDir: dist },
	preview: { host: "::", port: Number(port), strictPort: true },
	plugins: [{
		name: "oryn-ios-e2e-sink-trace",
		configurePreviewServer({ middlewares }) {
			middlewares.use(path, async (req, res) => {
				const isLoopback = ["::1", "127.0.0.1", "::ffff:127.0.0.1"].includes(
					req.socket.remoteAddress,
				);
				if (!isLoopback || req.method !== "POST" || req.headers.host !== `localhost:${port}`) {
					res.statusCode = 403;
					res.end();
					return;
				}
				try {
					let text = "";
					for await (const chunk of req) {
						text += chunk;
						if (text.length > 4096) {
							res.statusCode = 413;
							res.end();
							return;
						}
					}
					output.write(`${JSON.stringify(JSON.parse(text))}\n`);
					res.statusCode = 204;
				} catch {
					res.statusCode = 400;
				}
				res.end();
			});
			// Vite 已执行 SPA fallback，静态资源仍由它提供。
			return () => middlewares.use((req, res, next) => {
				if (req.url !== "/index.html") return next();
				res.setHeader("Content-Type", "text/html; charset=utf-8");
				res.end(html);
			});
		},
	}],
});
