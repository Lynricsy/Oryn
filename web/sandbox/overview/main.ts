// 画布 artifact 的只读概览页（构建成单个 IIFE：dist/sandbox/overview.js）。
//
// 服务端（internal/artifacts/overview.go）生成一张自包含的 index.html：内联样式、
// `<script type="application/json" id="oryn-canvas">` 里的数据与本脚本。用于画布没有自己的 index.html 时的
// 「在浏览器中打开」、缩略图渲染、导出的 zip 与发布页。页面运行在不可信的沙箱里：这里只用 src/lib/artifacts/
// 下的纯函数，不碰应用状态。画框是指向画框文件的相对路径的 iframe，所以和概览页落在同一个版本。

import {
	type Box,
	type CanvasDoc,
	type CanvasNote,
	type CanvasPage,
	joinedEnds,
	noteBox,
	parseCanvasDoc,
} from "../../src/lib/artifacts/canvas-doc";
import { strokePath } from "../../src/lib/artifacts/stroke";

/** 界面文案（服务端按语言给一套；没指定语言时两套都给，按浏览器语言挑）。 */
export interface OverviewText {
	pages: string;
	zoomIn: string;
	zoomOut: string;
	zoomReset: string;
	fit: string;
	empty: string;
	/** 含 `{name}` 占位。 */
	open: string;
	invalid: string;
}

/** `#oryn-canvas` 里的数据。 */
export interface OverviewData {
	title: string;
	doc: unknown;
	text: { zh?: OverviewText; en?: OverviewText };
}

export const ZOOM_MIN = 0.02;
export const ZOOM_MAX = 4;
/** 适合全部、缩略图四周留的边。 */
export const FIT_MARGIN = 64;
/** 拖动超过这个距离（屏幕像素）就不算点击。 */
const CLICK_SLOP = 4;
const SVG_NS = "http://www.w3.org/2000/svg";

/** 镜头：屏幕坐标 = 世界坐标 × zoom + (x, y)。 */
export interface Camera {
	x: number;
	y: number;
	zoom: number;
}

export function clampZoom(z: number): number {
	return Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, z));
}

/** 一组包围盒的外接矩形；没有时返回 null。 */
export function boundsOf(boxes: readonly Box[]): Box | null {
	if (boxes.length === 0) return null;
	let left = Number.POSITIVE_INFINITY;
	let top = Number.POSITIVE_INFINITY;
	let right = Number.NEGATIVE_INFINITY;
	let bottom = Number.NEGATIVE_INFINITY;
	for (const b of boxes) {
		left = Math.min(left, b.x);
		top = Math.min(top, b.y);
		right = Math.max(right, b.x + b.width);
		bottom = Math.max(bottom, b.y + b.height);
	}
	return { x: left, y: top, width: right - left, height: bottom - top };
}

/** 把 box 缩放进 width × height 的视口、四周留 margin 并居中；缩放不超过 maxZoom。 */
export function fitBox(
	box: Box | null,
	width: number,
	height: number,
	margin: number,
	maxZoom: number,
): Camera {
	if (box === null) return { x: width / 2, y: height / 2, zoom: 1 };
	const room = (size: number) => (size > 2 * margin ? size - 2 * margin : size);
	const zoom = clampZoom(
		Math.min(
			maxZoom,
			room(width) / Math.max(1, box.width),
			room(height) / Math.max(1, box.height),
		),
	);
	return {
		x: (width - box.width * zoom) / 2 - box.x * zoom,
		y: (height - box.height * zoom) / 2 - box.y * zoom,
		zoom,
	};
}

/** 以屏幕点 (px, py) 为中心缩放到 zoom：该点下的世界坐标不变。 */
export function zoomAt(
	cam: Camera,
	zoom: number,
	px: number,
	py: number,
): Camera {
	const z = clampZoom(zoom);
	const wx = (px - cam.x) / cam.zoom;
	const wy = (py - cam.y) / cam.zoom;
	return { x: px - wx * z, y: py - wy * z, zoom: z };
}

/** 与视口（四周各外扩半个视口）相交的画框 id。 */
export function visiblePages(
	pages: readonly CanvasPage[],
	cam: Camera,
	width: number,
	height: number,
): Set<string> {
	const left = (-width / 2 - cam.x) / cam.zoom;
	const top = (-height / 2 - cam.y) / cam.zoom;
	const right = (width * 1.5 - cam.x) / cam.zoom;
	const bottom = (height * 1.5 - cam.y) / cam.zoom;
	const out = new Set<string>();
	for (const p of pages) {
		if (
			p.x < right &&
			p.x + p.width > left &&
			p.y < bottom &&
			p.y + p.height > top
		)
			out.add(p.id);
	}
	return out;
}

/** 挑一套文案：只有一套就用它，两套都有时按浏览器语言。 */
export function pickText(
	text: OverviewData["text"],
	language: string,
): OverviewText | undefined {
	if (text.zh && text.en)
		return language.toLowerCase().startsWith("zh") ? text.zh : text.en;
	return text.zh ?? text.en;
}

/** 画框文件的相对地址：逐段编码，文件名里的 #、?、% 与空格不会变成片段、查询或转义。 */
export function pageHref(file: string): string {
	return file.split("/").map(encodeURIComponent).join("/");
}

function svg<K extends keyof SVGElementTagNameMap>(
	doc: Document,
	tag: K,
	attrs: Record<string, string | number>,
): SVGElementTagNameMap[K] {
	const el = doc.createElementNS(SVG_NS, tag);
	for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, String(v));
	return el;
}

/** 箭头的箭头部分：从终点往回、左右各张开 28°。 */
function arrowHead(
	x1: number,
	y1: number,
	x2: number,
	y2: number,
	width: number,
): string {
	const len = Math.max(10, width * 4);
	const angle = Math.atan2(y2 - y1, x2 - x1);
	const spread = (28 * Math.PI) / 180;
	const ax = x2 - len * Math.cos(angle - spread);
	const ay = y2 - len * Math.sin(angle - spread);
	const bx = x2 - len * Math.cos(angle + spread);
	const by = y2 - len * Math.sin(angle + spread);
	return `M${ax} ${ay}L${x2} ${y2}L${bx} ${by}`;
}

/** 一条标注的 SVG。颜色经 CSS 类 `c-<颜色>` 的 --c 变量取，明暗主题各一套。 */
export function noteElement(
	doc: Document,
	note: CanvasNote,
	pages: readonly CanvasPage[],
): SVGElement {
	const g = svg(doc, "g", {
		class: `note c-${note.color}`,
		"data-note": note.id,
	});
	switch (note.kind) {
		case "text": {
			const t = svg(doc, "text", {
				x: note.x,
				y: note.y,
				"font-size": note.size,
				class: "text",
			});
			note.text.split("\n").forEach((line, i) => {
				const span = svg(doc, "tspan", {
					x: note.x,
					dy: i === 0 ? note.size : note.size * 1.3,
				});
				span.textContent = line;
				t.append(span);
			});
			g.append(t);
			break;
		}
		case "sticky": {
			const box = noteBox(note);
			g.setAttribute(
				"class",
				`note c-${note.color}${note.done ? " done" : ""}`,
			);
			g.append(
				svg(doc, "rect", {
					x: box.x,
					y: box.y,
					width: box.width,
					height: box.height,
					rx: 6,
					class: "sticky",
				}),
			);
			const fo = svg(doc, "foreignObject", {
				x: box.x,
				y: box.y,
				width: box.width,
				height: box.height,
			});
			const div = doc.createElement("div");
			div.className = "sticky-text";
			div.textContent = note.text;
			fo.append(div);
			g.append(fo);
			break;
		}
		case "pen":
			g.append(
				svg(doc, "path", {
					d: strokePath(note.points),
					"stroke-width": note.width,
					class: note.marker ? "line marker" : "line",
				}),
			);
			break;
		case "rect":
		case "ellipse": {
			const box = noteBox(note);
			g.append(
				note.kind === "rect"
					? svg(doc, "rect", {
							x: box.x,
							y: box.y,
							width: box.width,
							height: box.height,
							"stroke-width": note.width,
							class: "line",
						})
					: svg(doc, "ellipse", {
							cx: box.x + box.width / 2,
							cy: box.y + box.height / 2,
							rx: box.width / 2,
							ry: box.height / 2,
							"stroke-width": note.width,
							class: "line",
						}),
			);
			break;
		}
		case "line":
		case "arrow": {
			const { x1, y1, x2, y2 } = joinedEnds(note, pages);
			let d = `M${x1} ${y1}L${x2} ${y2}`;
			if (note.kind === "arrow") d += arrowHead(x1, y1, x2, y2, note.width);
			g.append(
				svg(doc, "path", { d, "stroke-width": note.width, class: "line" }),
			);
			break;
		}
	}
	return g;
}

function button(doc: Document, label: string, text: string): HTMLButtonElement {
	const b = doc.createElement("button");
	b.type = "button";
	b.title = label;
	b.setAttribute("aria-label", label);
	b.textContent = text;
	return b;
}

/** 读页面里的数据并渲染概览；`?view=thumbnail` 时不显示任何控件，把第一个画布页的画框铺进视口。 */
export function boot(doc: Document, win: Window): void {
	const dataEl = doc.getElementById("oryn-canvas");
	if (!dataEl) return;
	doc.getElementById("oryn-fallback")?.remove();
	let data: OverviewData;
	let canvas: CanvasDoc;
	let text: OverviewText | undefined;
	try {
		data = JSON.parse(dataEl.textContent ?? "") as OverviewData;
		text = pickText(data.text ?? {}, win.navigator.language ?? "");
		canvas = parseCanvasDoc(JSON.stringify(data.doc));
	} catch {
		const p = doc.createElement("p");
		p.className = "message";
		p.textContent = text?.invalid ?? "This canvas can't be displayed.";
		doc.body.append(p);
		return;
	}
	if (!text) return;
	const t = text;
	const thumbnail =
		new URLSearchParams(win.location.search).get("view") === "thumbnail";
	doc.documentElement.classList.toggle("thumbnail", thumbnail);

	const stage = doc.createElement("div");
	stage.className = "stage";
	const world = doc.createElement("div");
	world.className = "world";
	stage.append(world);
	const empty = doc.createElement("p");
	empty.className = "message";
	empty.textContent = t.empty;

	let current = canvas.canvases[0]?.id ?? "";
	let cam: Camera = { x: 0, y: 0, zoom: 1 };
	const frames = new Map<string, HTMLIFrameElement>();
	const tabs: HTMLButtonElement[] = [];
	let zoomLabel: HTMLButtonElement | null = null;

	const viewport = () => ({
		width: stage.clientWidth || win.innerWidth,
		height: stage.clientHeight || win.innerHeight,
	});
	const sheetPages = () => canvas.pages.filter((p) => p.canvas === current);

	const apply = () => {
		world.style.transform = `translate(${cam.x}px, ${cam.y}px) scale(${cam.zoom})`;
		world.style.setProperty("--z", String(cam.zoom));
		if (zoomLabel) zoomLabel.textContent = `${Math.round(cam.zoom * 100)}%`;
		const { width, height } = viewport();
		for (const id of visiblePages(sheetPages(), cam, width, height)) {
			const frame = frames.get(id);
			const src = frame?.dataset.src;
			if (frame && src && !frame.getAttribute("src"))
				frame.setAttribute("src", src);
		}
	};

	const fitAll = () => {
		const { width, height } = viewport();
		const pages = sheetPages();
		const boxes: Box[] = [...pages];
		if (!thumbnail)
			for (const n of canvas.notes)
				if (n.canvas === current) boxes.push(noteBox(n));
		cam = fitBox(
			boundsOf(boxes),
			width,
			height,
			FIT_MARGIN,
			thumbnail ? ZOOM_MAX : 1,
		);
		apply();
	};

	const show = (id: string) => {
		current = id;
		for (const tab of tabs)
			tab.setAttribute("aria-selected", String(tab.dataset.canvas === id));
		world.replaceChildren();
		frames.clear();
		const pages = sheetPages();
		for (const p of pages) {
			const box = doc.createElement("div");
			box.className = "page";
			box.style.left = `${p.x}px`;
			box.style.top = `${p.y}px`;
			box.style.width = `${p.width}px`;
			box.style.height = `${p.height}px`;
			const frame = doc.createElement("iframe");
			frame.title = p.name;
			frame.tabIndex = -1;
			frame.dataset.src = pageHref(p.file);
			frames.set(p.id, frame);
			box.append(frame);
			if (!thumbnail) {
				const label = doc.createElement("div");
				label.className = "label";
				label.textContent = p.name;
				// 盖在画框上的链接：画框只是预览，点它在当前标签页打开这个画框的文件
				const link = doc.createElement("a");
				link.className = "open";
				link.href = pageHref(p.file);
				link.title = t.open.replace("{name}", p.name);
				link.setAttribute("aria-label", link.title);
				box.append(label, link);
			}
			world.append(box);
		}
		const layer = svg(doc, "svg", { class: "notes", width: 1, height: 1 });
		for (const n of canvas.notes)
			if (n.canvas === id) layer.append(noteElement(doc, n, pages));
		world.append(layer);
		if (pages.length === 0 && !thumbnail) stage.append(empty);
		else empty.remove();
		fitAll();
	};

	if (!thumbnail) {
		const bar = doc.createElement("header");
		bar.className = "bar";
		const title = doc.createElement("h1");
		title.textContent = data.title;
		bar.append(title);
		if (canvas.canvases.length > 1) {
			const list = doc.createElement("div");
			list.className = "tabs";
			list.setAttribute("role", "tablist");
			list.setAttribute("aria-label", t.pages);
			for (const c of canvas.canvases) {
				const tab = doc.createElement("button");
				tab.type = "button";
				tab.setAttribute("role", "tab");
				tab.dataset.canvas = c.id;
				tab.textContent = c.name;
				tab.addEventListener("click", () => show(c.id));
				tabs.push(tab);
				list.append(tab);
			}
			bar.append(list);
		}
		const zoom = doc.createElement("div");
		zoom.className = "zoom";
		const out = button(doc, t.zoomOut, "−");
		const reset = button(doc, t.zoomReset, "100%");
		const zin = button(doc, t.zoomIn, "+");
		const all = button(doc, t.fit, t.fit);
		const centre = (z: number) => {
			const { width, height } = viewport();
			cam = zoomAt(cam, z, width / 2, height / 2);
			apply();
		};
		out.addEventListener("click", () => centre(cam.zoom / 1.25));
		zin.addEventListener("click", () => centre(cam.zoom * 1.25));
		reset.addEventListener("click", () => centre(1));
		all.addEventListener("click", fitAll);
		zoomLabel = reset;
		zoom.append(out, reset, zin, all);
		bar.append(zoom);
		doc.body.append(bar);

		// 平移：拖动（含双指捏合缩放）与滚轮；Ctrl / ⌘ + 滚轮（触控板捏合）缩放
		const pointers = new Map<number, { x: number; y: number }>();
		let moved = 0;
		let pinch = 0;
		stage.addEventListener("pointerdown", (e) => {
			pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
			if (pointers.size === 1) moved = 0;
			pinch = 0;
		});
		stage.addEventListener("pointermove", (e) => {
			const last = pointers.get(e.pointerId);
			if (!last) return;
			const dx = e.clientX - last.x;
			const dy = e.clientY - last.y;
			pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
			moved += Math.abs(dx) + Math.abs(dy);
			if (pointers.size === 2) {
				const [a, b] = [...pointers.values()];
				if (!a || !b) return;
				const dist = Math.hypot(a.x - b.x, a.y - b.y);
				const rect = stage.getBoundingClientRect();
				if (pinch > 0)
					cam = zoomAt(
						cam,
						(cam.zoom * dist) / pinch,
						(a.x + b.x) / 2 - rect.left,
						(a.y + b.y) / 2 - rect.top,
					);
				pinch = dist;
			} else {
				cam = { ...cam, x: cam.x + dx, y: cam.y + dy };
			}
			apply();
		});
		const release = (e: PointerEvent) => {
			pointers.delete(e.pointerId);
			pinch = 0;
		};
		stage.addEventListener("pointerup", release);
		stage.addEventListener("pointercancel", release);
		// 拖动之后松手不算点击，不打开画框
		stage.addEventListener(
			"click",
			(e) => {
				if (moved > CLICK_SLOP) e.preventDefault();
			},
			true,
		);
		stage.addEventListener(
			"wheel",
			(e) => {
				e.preventDefault();
				if (e.ctrlKey || e.metaKey) {
					const rect = stage.getBoundingClientRect();
					cam = zoomAt(
						cam,
						cam.zoom * Math.exp(-e.deltaY * 0.01),
						e.clientX - rect.left,
						e.clientY - rect.top,
					);
				} else {
					cam = { ...cam, x: cam.x - e.deltaX, y: cam.y - e.deltaY };
				}
				apply();
			},
			{ passive: false },
		);
		doc.addEventListener("keydown", (e) => {
			if (e.ctrlKey || e.metaKey || e.altKey) return;
			if (e.key === "+" || e.key === "=") centre(cam.zoom * 1.25);
			else if (e.key === "-") centre(cam.zoom / 1.25);
			else if (e.shiftKey && e.code === "Digit1") fitAll();
			else if (e.shiftKey && e.code === "Digit0") centre(1);
		});
		win.addEventListener("resize", apply);
	}
	doc.body.append(stage);
	// 缩略图只看第一个画布页
	show(current);
}

if (typeof document !== "undefined" && document.getElementById("oryn-canvas"))
	boot(document, window);
