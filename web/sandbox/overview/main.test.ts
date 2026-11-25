import { JSDOM } from "jsdom";
import { describe, expect, it } from "vitest";
import {
	boot,
	boundsOf,
	type Camera,
	fitBox,
	mockupBgStyle,
	type OverviewData,
	pageHref,
	parseMockupQuery,
	pickText,
	shellChrome,
	visiblePages,
	ZOOM_MAX,
	ZOOM_MIN,
	zoomAt,
} from "./main";

const ZH = {
	pages: "页面",
	zoomIn: "放大",
	zoomOut: "缩小",
	zoomReset: "缩放到 100%",
	fit: "适合全部",
	empty: "这个页面还没有画框",
	open: "打开「{name}」",
	invalid: "无法显示这个画布",
};
const EN = { ...ZH, empty: "This page has no frames yet", open: "Open {name}" };

const DOC = {
	format: 1,
	canvases: [
		{ id: "c1", name: "首页" },
		{ id: "c2", name: "空页" },
	],
	pages: [
		{
			id: "a",
			canvas: "c1",
			name: "A",
			file: "a.html",
			x: 0,
			y: 0,
			width: 1280,
			height: 800,
			locked: false,
		},
		{
			id: "b",
			canvas: "c1",
			name: "B 页",
			file: "dir/b #1.html",
			x: 1328,
			y: 0,
			width: 1280,
			height: 800,
			locked: false,
		},
		{
			id: "far",
			canvas: "c1",
			name: "远处",
			file: "far.html",
			x: 20000,
			y: 4000,
			width: 1280,
			height: 800,
			locked: false,
		},
	],
	notes: [
		{
			id: "n1",
			canvas: "c1",
			kind: "sticky",
			color: "yellow",
			locked: false,
			by: null,
			x: 10,
			y: 10,
			width: 240,
			text: "改成绿色",
			done: false,
		},
		{
			id: "n2",
			canvas: "c1",
			kind: "pen",
			color: "red",
			locked: false,
			by: null,
			points: [0, 0, 1, 10, 10, 1, 20, 0, 1],
			width: 3,
			marker: true,
		},
		{
			id: "n3",
			canvas: "c1",
			kind: "arrow",
			color: "blue",
			locked: false,
			by: null,
			x1: 0,
			y1: 0,
			x2: 50,
			y2: 50,
			width: 2,
			from: "a",
			to: "b",
		},
		{
			id: "n4",
			canvas: "c1",
			kind: "text",
			color: "ink",
			locked: false,
			by: null,
			x: 5,
			y: 900,
			text: "第一行\n第二行",
			size: 24,
		},
	],
};

function page(data: OverviewData | string, search = "") {
	const json = typeof data === "string" ? data : JSON.stringify(data);
	const w = new JSDOM(
		`<!doctype html><html><head></head><body><main id="oryn-fallback">x</main><script type="application/json" id="oryn-canvas">${json}</script></body></html>`,
		{ url: `https://a-token.example/_oryn/v/3/${search}` },
	).window;
	boot(w.document, w);
	return w;
}

describe("镜头", () => {
	it("zoomAt 后指针下的世界坐标不变，缩放夹在范围内", () => {
		const cam: Camera = { x: 30, y: -40, zoom: 0.5 };
		const next = zoomAt(cam, 2, 200, 100);
		expect((200 - next.x) / next.zoom).toBeCloseTo((200 - cam.x) / cam.zoom);
		expect((100 - next.y) / next.zoom).toBeCloseTo((100 - cam.y) / cam.zoom);
		expect(zoomAt(cam, 100, 0, 0).zoom).toBe(ZOOM_MAX);
		expect(zoomAt(cam, 0.0001, 0, 0).zoom).toBe(ZOOM_MIN);
	});

	it("fitBox 四周留边并居中，不超过 maxZoom", () => {
		const box = { x: 100, y: 100, width: 1000, height: 500 };
		const cam = fitBox(box, 1280, 800, 64, 4);
		expect(cam.zoom).toBeCloseTo((1280 - 128) / 1000);
		expect(box.x * cam.zoom + cam.x).toBeCloseTo(64);
		const mid = (box.y + box.height / 2) * cam.zoom + cam.y;
		expect(mid).toBeCloseTo(400);
		expect(fitBox(box, 1280, 800, 64, 1).zoom).toBe(1);
	});

	it("boundsOf 与可见画框", () => {
		expect(boundsOf([])).toBeNull();
		expect(
			boundsOf([
				{ x: 0, y: 0, width: 10, height: 10 },
				{ x: -5, y: 20, width: 10, height: 10 },
			]),
		).toEqual({ x: -5, y: 0, width: 15, height: 30 });
		const vis = visiblePages(DOC.pages, { x: 0, y: 0, zoom: 1 }, 1280, 800);
		expect([...vis]).toEqual(["a", "b"]);
	});
});

describe("文案与地址", () => {
	it("两套文案按浏览器语言挑，只有一套就用它", () => {
		expect(pickText({ zh: ZH, en: EN }, "zh-CN")).toBe(ZH);
		expect(pickText({ zh: ZH, en: EN }, "en-US")).toBe(EN);
		expect(pickText({ en: EN }, "zh-CN")).toBe(EN);
	});

	it("画框地址逐段编码", () => {
		expect(pageHref("dir/b #1.html")).toBe("dir/b%20%231.html");
	});
});

describe("概览页", () => {
	const data: OverviewData = { title: "设计稿", doc: DOC, text: { zh: ZH } };

	it("渲染标题、画布页标签、画框与标注，画框懒加载、点开是相对路径", () => {
		const w = page(data);
		const d = w.document;
		expect(d.getElementById("oryn-fallback")).toBeNull();
		expect(d.querySelector("h1")?.textContent).toBe("设计稿");
		const tabs = [...d.querySelectorAll('[role="tab"]')];
		expect(tabs.map((t) => t.textContent)).toEqual(["首页", "空页"]);
		expect(tabs[0]?.getAttribute("aria-selected")).toBe("true");

		const frames = [...d.querySelectorAll("iframe")];
		expect(frames).toHaveLength(3);
		expect(frames[0]?.getAttribute("src")).toBe("a.html");
		expect(frames[1]?.getAttribute("src")).toBe("dir/b%20%231.html");
		// 适合全部之后三个画框都在视口里
		expect(frames[2]?.getAttribute("src")).toBe("far.html");
		const links = [...d.querySelectorAll("a.open")];
		expect(links.map((a) => a.getAttribute("href"))).toEqual([
			"a.html",
			"dir/b%20%231.html",
			"far.html",
		]);
		expect(links[1]?.getAttribute("aria-label")).toBe("打开「B 页」");

		expect(d.querySelectorAll("svg.notes g.note")).toHaveLength(4);
		expect(d.querySelector('[data-note="n1"] .sticky-text')?.textContent).toBe(
			"改成绿色",
		);
		expect(
			d.querySelector('[data-note="n2"] path')?.getAttribute("class"),
		).toBe("line marker");
		expect(d.querySelectorAll('[data-note="n4"] tspan')).toHaveLength(2);

		// 切到空的画布页
		(tabs[1] as HTMLElement).click();
		expect(d.querySelectorAll("iframe")).toHaveLength(0);
		expect(d.querySelector(".message")?.textContent).toBe("这个页面还没有画框");
	});

	it("缩放按钮改百分比", () => {
		const d = page(data).document;
		const [out, reset, zin] = [
			...d.querySelectorAll(".zoom button"),
		] as HTMLButtonElement[];
		(reset as HTMLButtonElement).click();
		expect(reset?.textContent).toBe("100%");
		(zin as HTMLButtonElement).click();
		expect(reset?.textContent).toBe("125%");
		(out as HTMLButtonElement).click();
		(out as HTMLButtonElement).click();
		expect(reset?.textContent).toBe("80%");
	});

	it("?view=thumbnail 不显示控件，第一个画布页的画框铺进视口并全部加载", () => {
		const w = page(data, "?view=thumbnail");
		const d = w.document;
		expect(d.documentElement.classList.contains("thumbnail")).toBe(true);
		expect(d.querySelector("header")).toBeNull();
		expect(d.querySelector("a.open")).toBeNull();
		const frames = [...d.querySelectorAll("iframe")];
		expect(frames.every((f) => f.getAttribute("src"))).toBe(true);
		// 三个画框的外接框缩放进视口、四周留 64
		const world = d.querySelector(".world") as HTMLElement;
		const m =
			/translate\(([-\d.e]+)px, ([-\d.e]+)px\) scale\(([-\d.e]+)\)/.exec(
				world.style.transform,
			);
		expect(m).not.toBeNull();
		const zoom = Number(m?.[3]);
		const all = fitBox(
			boundsOf(DOC.pages),
			w.innerWidth,
			w.innerHeight,
			64,
			ZOOM_MAX,
		);
		expect(zoom).toBeCloseTo(all.zoom);
	});

	it("数据坏了时显示提示", () => {
		const d = page({
			title: "x",
			doc: { pages: "?" },
			text: { zh: ZH },
		}).document;
		expect(d.querySelector(".message")?.textContent).toBe("无法显示这个画布");
		expect(d.querySelector("iframe")).toBeNull();
	});
});

describe("样机模式", () => {
	const DESIGN_DOC = {
		format: 2,
		design: { engine: 1, start: "home" },
		canvases: [{ id: "c1", name: "首页" }],
		pages: [
			{
				id: "home",
				canvas: "c1",
				name: "首页",
				file: "dist/home.html",
				source: "src/frames/home.tsx",
				device: "browser",
				x: 0,
				y: 0,
				width: 1280,
				height: 800,
				locked: false,
			},
			{
				id: "mobile",
				canvas: "c1",
				name: "移动端",
				file: "dist/mobile.html",
				source: "src/frames/mobile.tsx",
				device: "phone",
				x: 1328,
				y: 0,
				width: 390,
				height: 844,
				locked: false,
			},
			{
				id: "pad",
				canvas: "c1",
				name: "平板",
				file: "dist/pad.html",
				source: "src/frames/pad.tsx",
				device: "tablet",
				x: 1766,
				y: 0,
				width: 820,
				height: 1180,
				locked: false,
			},
		],
		notes: [],
	};

	it("按配置渲染选中帧、设备外壳、背景、圆角、边框与阴影，无交互工具栏", () => {
		const w = page({
			title: "设计稿",
			doc: DESIGN_DOC,
			text: {},
			view: "mockup",
			mockup: {
				frames: ["mobile", "home"],
				heights: { home: 2400 },
				radius: 16,
				border: true,
				bg: "dark",
				shadow: true,
				shell: true,
				width: 1830,
				height: 2532,
			},
		});
		const d = w.document;
		expect(d.documentElement.classList.contains("mockup")).toBe(true);
		expect(d.documentElement.classList.contains("mockup-dark")).toBe(true);
		expect(d.querySelector("header")).toBeNull();
		expect(d.querySelector("a.open")).toBeNull();
		expect(d.querySelector("style[data-mockup-page]")?.textContent).toContain(
			"1830px 2532px",
		);

		const items = [...d.querySelectorAll<HTMLElement>(".mockup-item")];
		expect(items.map((el) => el.dataset.frame)).toEqual(["mobile", "home"]);
		expect(items[0]?.dataset.device).toBe("phone");
		expect(items[0]?.style.width).toBe("414px");
		expect(items[0]?.style.height).toBe("904px");
		expect(items[0]?.classList.contains("has-border")).toBe(true);
		expect(items[0]?.classList.contains("has-shadow")).toBe(true);
		expect(items[0]?.querySelector(".mockup-notch")).not.toBeNull();

		expect(items[1]?.dataset.device).toBe("browser");
		expect(items[1]?.style.width).toBe("1280px");
		expect(items[1]?.style.height).toBe("2436px");
		expect(
			items[1]?.querySelector(".mockup-chrome .mockup-title")?.textContent,
		).toBe("首页");
		const iframes = [...d.querySelectorAll("iframe")];
		expect(iframes.map((f) => f.getAttribute("src"))).toEqual([
			"dist/mobile.html",
			"dist/home.html",
		]);
		expect(iframes[1]?.style.height).toBe("2400px");
	});

	it("?view=mockup 查询参数模式：关闭外壳/边框/阴影与自定义色背景", () => {
		const w = page(
			{ title: "设计稿", doc: DESIGN_DOC, text: { zh: ZH } },
			"?view=mockup&frames=pad&radius=0&border=0&shadow=0&shell=0&bg=%23102030",
		);
		const d = w.document;
		expect(d.documentElement.classList.contains("mockup-dark")).toBe(true);
		const items = [...d.querySelectorAll<HTMLElement>(".mockup-item")];
		expect(items).toHaveLength(1);
		expect(items[0]?.dataset.frame).toBe("pad");
		expect(items[0]?.dataset.device).toBe("none");
		expect(items[0]?.style.width).toBe("820px");
		expect(items[0]?.style.height).toBe("1180px");
		expect(items[0]?.style.borderRadius).toBe("0px");
		expect(items[0]?.classList.contains("has-border")).toBe(false);
		expect(items[0]?.classList.contains("has-shadow")).toBe(false);
	});

	it("辅助函数：parseMockupQuery / shellChrome / mockupBgStyle", () => {
		expect(shellChrome("phone", true)).toEqual({
			top: 36,
			right: 12,
			bottom: 24,
			left: 12,
		});
		expect(shellChrome("tablet", true)).toEqual({
			top: 24,
			right: 16,
			bottom: 24,
			left: 16,
		});
		expect(shellChrome("browser", true)).toEqual({
			top: 36,
			right: 0,
			bottom: 0,
			left: 0,
		});
		expect(shellChrome("phone", false)).toEqual({
			top: 0,
			right: 0,
			bottom: 0,
			left: 0,
		});
		expect(mockupBgStyle("transparent")).toEqual({
			cssBg: "transparent",
			dark: false,
		});
		expect(mockupBgStyle("#ffffff")).toEqual({
			cssBg: "#ffffff",
			dark: false,
		});
		expect(mockupBgStyle("#000000")).toEqual({
			cssBg: "#000000",
			dark: true,
		});
		expect(
			parseMockupQuery(
				new URLSearchParams("frames=a,b&radius=999&heights=a:1500,b:900"),
			),
		).toEqual({
			frames: ["a", "b"],
			heights: { a: 1500, b: 900 },
			radius: 64,
			border: true,
			bg: "light",
			shadow: true,
			shell: true,
		});
	});
});
