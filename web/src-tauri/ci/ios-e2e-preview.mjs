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
	for (const type of [
		"click", "touchstart", "touchend", "touchcancel", "focusin", "popstate", "resize",
	]) {
		window.addEventListener(type, (event) => line(type, { target: label(event.target) }), opts);
	}
	window.visualViewport?.addEventListener(
		"resize", () => line("visualViewport.resize"), { passive: true },
	);
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
