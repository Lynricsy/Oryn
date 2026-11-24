import { JSDOM } from "jsdom";
import { afterEach, describe, expect, it } from "vitest";
import { frameOf, RESULT_ID, readOptions, report, start } from "./main";
import { check, contrast, type Issue, MAX_PER_RULE, parseColor } from "./rules";

// jsdom 没有布局：盒子、行矩形与滚动尺寸按 data-* 属性 stub。
// data-box="x,y,w,h" → getBoundingClientRect；data-client="w,h" / data-scroll="w,h" → clientWidth/Height、
// scrollWidth/Height；data-lines="n" → 元素内文本的 Range.getClientRects 返回 n 行。

const windows: { close(): void }[] = [];

afterEach(() => {
	for (const w of windows.splice(0)) w.close();
});

function nums(v: string | null): number[] {
	return (v ?? "").split(",").map(Number);
}

function rect(x: number, y: number, w: number, h: number): DOMRect {
	return {
		x,
		y,
		width: w,
		height: h,
		left: x,
		top: y,
		right: x + w,
		bottom: y + h,
		toJSON() {
			return this;
		},
	} as DOMRect;
}

function page(body: string, opts: { head?: string; url?: string } = {}) {
	const dom = new JSDOM(
		`<!doctype html><html><head>${opts.head ?? ""}</head><body>${body}</body></html>`,
		{
			url:
				opts.url ??
				"http://render.oryn.invalid/tok/dist/home.html?__oryn_probe=1",
		},
	);
	const win = dom.window;
	windows.push(win);
	const proto = win.Element.prototype;
	Object.defineProperty(proto, "getBoundingClientRect", {
		configurable: true,
		value(this: Element) {
			const b = this.getAttribute("data-box");
			if (b) {
				const [x = 0, y = 0, w = 0, h = 0] = nums(b);
				return rect(x, y, w, h);
			}
			// 缺省：可见的 100×40 盒子（svg 等按需覆盖）
			return rect(0, 0, 100, 40);
		},
	});
	for (const [prop, attr, idx] of [
		["clientWidth", "data-client", 0],
		["clientHeight", "data-client", 1],
		["scrollWidth", "data-scroll", 0],
		["scrollHeight", "data-scroll", 1],
	] as const) {
		Object.defineProperty(proto, prop, {
			configurable: true,
			get(this: Element) {
				const v = this.getAttribute(attr) ?? this.getAttribute("data-client");
				return v ? (nums(v)[idx] ?? 0) : 0;
			},
		});
	}
	Object.defineProperty(win.Range.prototype, "getClientRects", {
		configurable: true,
		value(this: Range) {
			const host = this.startContainer.parentElement?.closest("[data-lines]");
			const n = Number(host?.getAttribute("data-lines") ?? 1);
			return Array.from({ length: n }, (_, i) => rect(0, i * 20, 80, 18));
		},
	});
	return win;
}

function rules(issues: Issue[], rule: string): Issue[] {
	return issues.filter((i) => i.rule === rule);
}

const ALL = { aesthetic: true, surface: "landing" };

describe("版面规则", () => {
	it("text-clipped：overflow 非 visible 且内容溢出才报，loc 取最近祖先", () => {
		const win = page(
			`<div data-oryn-loc="src/frames/home.tsx:3:5"><button id="b" style="overflow:hidden" data-client="80,30" data-scroll="144,30">立即开始免费试用</button></div>` +
				`<button style="overflow:hidden" data-client="80,30" data-scroll="80,30">够宽</button>` +
				`<div style="overflow:visible" data-client="80,30" data-scroll="300,30">可见溢出</div>`,
		);
		const got = rules(check(win, ALL), "text-clipped");
		expect(got).toEqual([
			{
				rule: "text-clipped",
				loc: "src/frames/home.tsx:3:5",
				text: "立即开始免费试用",
				detail: 64,
			},
		]);
	});

	it("unintended-wrap：按钮文字多于一行才报，写了 <br> 的不算", () => {
		const win = page(
			`<button data-lines="2">提交订单</button><button data-lines="1">好</button><button data-lines="2">第一行<br>第二行</button>`,
		);
		const got = rules(check(win, ALL), "unintended-wrap");
		expect(got.map((i) => [i.text, i.detail])).toEqual([["提交订单", 2]]);
	});

	it("icon-missing：图标类没有 mask、svg 盒为 0 才报", () => {
		const win = page(
			`<span class="icon-[lucide--x]" data-box="0,0,16,16"></span>` +
				`<span class="icon-[lucide--ok]" style="mask-image:url(a.svg)" data-box="0,0,16,16"></span>` +
				`<svg data-box="0,0,0,0"><path d="M0 0"/></svg>` +
				`<svg data-box="0,0,16,16"><path d="M0 0"/></svg>`,
		);
		const got = rules(check(win, ALL), "icon-missing");
		expect(got.map((i) => i.detail)).toEqual(["mask", "box"]);
	});

	it("edge-misaligned：兄弟块左缘差 1–3px 才报，对齐或差得多的不报", () => {
		const off = page(
			`<main><div data-box="24,0,200,20">a</div><div data-box="24,30,200,20">b</div><div data-box="26,60,200,20">c</div></main>`,
		);
		const got = rules(check(off, ALL), "edge-misaligned");
		expect(got.map((i) => [i.text, i.detail])).toEqual([["c", 2]]);
		const ok = page(
			`<main><div data-box="24,0,200,20">a</div><div data-box="24,30,200,20">b</div><div data-box="64,60,200,20">c</div></main>`,
		);
		expect(rules(check(ok, ALL), "edge-misaligned")).toEqual([]);
	});

	it("token-undefined：引用未定义且没有回退值的变量才报", () => {
		const win = page(
			`<p class="bad">坏</p><p class="ok">好</p><p class="fb">回退</p>`,
			{
				head: `<style>:root{--brand:#0f766e} .bad{color:var(--missing)} .ok{color:var(--brand)} .fb{color:var(--nope, red)}</style>`,
			},
		);
		const got = rules(check(win, ALL), "token-undefined");
		expect(got.map((i) => [i.text, i.detail])).toEqual([
			["坏", "color: --missing"],
		]);
	});

	it("contrast-low：按 WCAG 算，浅灰字在白底上报，深字不报，大字阈值 3，取不到背景时跳过", () => {
		const win = page(
			`<div style="background:#fff"><p style="color:#aaa">浅灰</p><p style="color:#333">深灰</p>` +
				`<p style="color:#888;font-size:28px">大字</p></div>` +
				`<div style="background-image:url(x.png)"><p style="color:#eee">图上</p></div>`,
		);
		const got = rules(check(win, ALL), "contrast-low");
		expect(got.map((i) => i.text)).toEqual(["浅灰"]);
		expect(got[0]?.detail).toBeCloseTo(2.32, 1);
	});
});

describe("审美规则", () => {
	it("target-small：可点元素小于 24×24 才报，句中链接例外", () => {
		const win = page(
			`<button data-box="0,0,16,16">x</button><button data-box="0,0,44,44">好</button>` +
				`<p>阅读<a href="#" data-box="0,0,20,14">条款</a>了解</p>`,
		);
		const got = rules(check(win, ALL), "target-small");
		expect(got.map((i) => [i.text, i.detail])).toEqual([["x", "16x16"]]);
	});

	it("dash-in-copy：文本含 U+2014 / U+2013 才报，代码里的不算", () => {
		const win = page(
			`<p>快\u2014而且稳</p><p>普通-连字符</p><code>a\u2013b</code>`,
		);
		const got = rules(check(win, ALL), "dash-in-copy");
		expect(got.map((i) => i.detail)).toEqual(["U+2014"]);
	});

	it("eyebrow-excess：小号大写宽字距标签多于 ceil(sections/3) 才报；app 关闭", () => {
		const tag = `<span style="text-transform:uppercase;letter-spacing:0.1em;font-size:12px">new</span>`;
		const many = page(
			`<section>${tag}</section><section>${tag}</section><section>${tag}</section>`,
		);
		const got = rules(check(many, ALL), "eyebrow-excess");
		expect(got.map((i) => i.detail)).toEqual(["3/1", "3/1"]);
		expect(
			rules(check(many, { aesthetic: true, surface: "app" }), "eyebrow-excess"),
		).toEqual([]);
		const one = page(
			`<section>${tag}</section><section><p>正文</p></section><section></section>`,
		);
		expect(rules(check(one, ALL), "eyebrow-excess")).toEqual([]);
	});

	it("accent-multiple：两个以上饱和色相簇才报", () => {
		const two = page(
			`<button style="background:#e11d48">红</button><button style="background:#2563eb">蓝</button>`,
		);
		expect(
			rules(check(two, ALL), "accent-multiple").map((i) => i.detail),
		).toEqual([2]);
		const one = page(
			`<button style="background:#2563eb">蓝</button><a style="color:#1d4ed8">深蓝</a><p style="color:#555">灰</p>`,
		);
		expect(rules(check(one, ALL), "accent-multiple")).toEqual([]);
	});

	it("radius-single：≥12 个圆角元素都同一个值才报", () => {
		const same = page(
			Array.from(
				{ length: 12 },
				(_, i) => `<div style="border-radius:8px">${i}</div>`,
			).join(""),
		);
		expect(
			rules(check(same, ALL), "radius-single").map((i) => i.detail),
		).toEqual(["8px"]);
		const mixed = page(
			Array.from(
				{ length: 12 },
				(_, i) => `<div style="border-radius:${i % 2 ? 8 : 16}px">${i}</div>`,
			).join(""),
		);
		expect(rules(check(mixed, ALL), "radius-single")).toEqual([]);
	});

	it("pure-black-white：面积 > 5% 的纯黑白背景或文本才报", () => {
		const win = page(
			`<main style="background:#ffffff" data-box="0,0,1280,800"><p style="color:#111" data-box="0,0,100,20">字</p></main>`,
		);
		Object.defineProperty(win, "innerWidth", { value: 1280 });
		Object.defineProperty(win, "innerHeight", { value: 800 });
		expect(
			rules(check(win, ALL), "pure-black-white").map((i) => i.detail),
		).toEqual(["background #fff"]);
		const soft = page(
			`<main style="background:#fafaf9" data-box="0,0,1280,800"><p style="color:#000" data-box="0,0,100,20">小</p></main>`,
		);
		Object.defineProperty(soft, "innerWidth", { value: 1280 });
		Object.defineProperty(soft, "innerHeight", { value: 800 });
		expect(rules(check(soft, ALL), "pure-black-white")).toEqual([]);
	});

	it("split-repeat：连续 3 个左右图文分栏 section 才报；app 关闭", () => {
		const split = (left: boolean) =>
			`<section data-box="0,0,1200,400">` +
			(left
				? `<img data-box="0,0,600,400"><div data-box="600,0,600,400">这里是一段足够长的说明文字，介绍功能。</div>`
				: `<div data-box="0,0,600,400">这里是一段足够长的说明文字，介绍功能。</div><img data-box="600,0,600,400">`) +
			`</section>`;
		const three = page(split(true) + split(false) + split(true));
		expect(rules(check(three, ALL), "split-repeat")).toHaveLength(1);
		expect(
			rules(check(three, { aesthetic: true, surface: "app" }), "split-repeat"),
		).toEqual([]);
		const broken = page(
			split(true) +
				`<section data-box="0,0,1200,400"><h2>居中标题</h2></section>` +
				split(true),
		);
		expect(rules(check(broken, ALL), "split-repeat")).toEqual([]);
	});

	it("raw-color：内联 style 写颜色字面量才报，用 token 的不报", () => {
		const win = page(
			`<p style="color:#ff0000">红</p><p style="color:var(--color-accent)">好</p><p style="margin:4px">无色</p>`,
		);
		const got = rules(check(win, ALL), "raw-color");
		expect(got.map((i) => i.text)).toEqual(["红"]);
	});
});

describe("配置与汇总", () => {
	it("缺省只跑前 6 条，每条至多 3 处", () => {
		const body = `${Array.from(
			{ length: 5 },
			() =>
				`<button style="overflow:hidden" data-client="80,30" data-scroll="144,30">长按钮文字</button>`,
		).join("")}<p style="color:#ff0000">红\u2014色</p>`;
		const win = page(body);
		const got = check(win, { aesthetic: false, surface: "" });
		expect(rules(got, "text-clipped")).toHaveLength(MAX_PER_RULE);
		expect(rules(got, "raw-color")).toEqual([]);
		expect(rules(got, "dash-in-copy")).toEqual([]);
		expect(rules(check(win, ALL), "raw-color")).toHaveLength(1);
	});

	it("readOptions 只认 aesthetic === true 与字符串 surface", () => {
		const win = page("");
		expect(readOptions(win)).toEqual({ aesthetic: false, surface: "" });
		Reflect.set(win, "__orynProbe", { aesthetic: true, surface: "app" });
		expect(readOptions(win)).toEqual({ aesthetic: true, surface: "app" });
		Reflect.set(win, "__orynProbe", { aesthetic: "yes", surface: 3 });
		expect(readOptions(win)).toEqual({ aesthetic: false, surface: "" });
	});

	it("readOptions：没有 window.__orynProbe 时读地址参数，surface 只认 landing / app；window 配置优先", () => {
		const at = (q: string) =>
			page("", { url: `http://127.0.0.1:5173/frames/home?__oryn_probe=1${q}` });
		expect(readOptions(at(""))).toEqual({ aesthetic: false, surface: "" });
		expect(readOptions(at("&__oryn_aesthetic=1&__oryn_surface=app"))).toEqual({
			aesthetic: true,
			surface: "app",
		});
		expect(
			readOptions(at("&__oryn_aesthetic=true&__oryn_surface=landing")),
		).toEqual({ aesthetic: false, surface: "landing" });
		expect(readOptions(at("&__oryn_aesthetic=1&__oryn_surface=evil"))).toEqual({
			aesthetic: true,
			surface: "",
		});
		const both = at("&__oryn_aesthetic=1&__oryn_surface=app");
		Reflect.set(both, "__orynProbe", { aesthetic: false, surface: "" });
		expect(readOptions(both)).toEqual({ aesthetic: false, surface: "" });
	});

	it("地址参数打开审美规则；__oryn_surface=app 关掉 eyebrow-excess 与 split-repeat", () => {
		const tag = `<span style="text-transform:uppercase;letter-spacing:0.1em;font-size:12px">new</span>`;
		const split = (left: boolean) =>
			`<section data-box="0,0,1200,400">` +
			(left
				? `<img data-box="0,0,600,400"><div data-box="600,0,600,400">这里是一段足够长的说明文字，介绍功能。${tag}</div>`
				: `<div data-box="0,0,600,400">这里是一段足够长的说明文字，介绍功能。${tag}</div><img data-box="600,0,600,400">`) +
			`</section>`;
		const body = `${split(true)}${split(false)}${split(true)}<p style="color:#ff0000">红</p>`;
		const at = (q: string) =>
			page(body, {
				url: `http://127.0.0.1:5173/frames/home?__oryn_probe=1${q}`,
			});
		const names = (q: string) =>
			new Set(report(at(q)).issues.map((i) => i.rule));

		const none = names("");
		for (const r of ["raw-color", "eyebrow-excess", "split-repeat"])
			expect(none.has(r)).toBe(false);
		const landing = names("&__oryn_aesthetic=1&__oryn_surface=landing");
		for (const r of ["raw-color", "eyebrow-excess", "split-repeat"])
			expect(landing.has(r)).toBe(true);
		const app = names("&__oryn_aesthetic=1&__oryn_surface=app");
		expect(app.has("raw-color")).toBe(true);
		expect(app.has("eyebrow-excess")).toBe(false);
		expect(app.has("split-repeat")).toBe(false);
	});

	it("frameOf：#oryn-design 优先，其次地址", () => {
		expect(frameOf(page(""))).toBe("home");
		expect(
			frameOf(
				page("", { url: "http://127.0.0.1:5173/frames/login?__oryn_probe=1" }),
			),
		).toBe("login");
		expect(
			frameOf(
				page("", {
					head: `<script type="application/json" id="oryn-design">{"frame":"signup","links":[]}</script>`,
				}),
			),
		).toBe("signup");
		expect(frameOf(page("", { url: "http://x/" }))).toBe("");
	});

	it("report 把结果写进 body 末尾的 #oryn-probe，重复调用只留一个", () => {
		const win = page(
			`<button style="overflow:hidden" data-client="80,30" data-scroll="144,30">a</button>`,
		);
		report(win);
		report(win);
		const nodes = win.document.querySelectorAll(`#${RESULT_ID}`);
		expect(nodes).toHaveLength(1);
		const node = nodes[0];
		expect(node?.getAttribute("type")).toBe("application/json");
		expect(node?.parentElement).toBe(win.document.body);
		const data = JSON.parse(node?.textContent ?? "");
		expect(data).toMatchObject({
			v: 1,
			frame: "home",
			issues: [{ rule: "text-clipped", text: "a" }],
		});
	});

	it("start 只在带 __oryn_probe=1 时运行，等空闲后写结果", async () => {
		const off = page("", { url: "http://x/dist/a.html" });
		expect(await start(off)).toBeNull();
		expect(off.document.getElementById(RESULT_ID)).toBeNull();
		const on = page("<p>x</p>");
		const res = await start(on);
		expect(res?.frame).toBe("home");
		expect(on.document.getElementById(RESULT_ID)).not.toBeNull();
		expect(await start(on)).toBeNull();
	});
});

describe("颜色", () => {
	it("解析常见格式，认不出的返回 null", () => {
		expect(parseColor("rgb(255, 0, 0)")).toEqual([1, 0, 0, 1]);
		expect(parseColor("rgba(0, 0, 0, 0.5)")).toEqual([0, 0, 0, 0.5]);
		expect(parseColor("#fff")).toEqual([1, 1, 1, 1]);
		expect(parseColor("transparent")?.[3]).toBe(0);
		const ok = parseColor("oklch(0.628 0.2577 29.23)");
		expect(ok?.[0]).toBeGreaterThan(0.95);
		expect(ok?.[1]).toBeLessThan(0.05);
		expect(parseColor("color(srgb 0 0.5 1)")).toEqual([0, 0.5, 1, 1]);
		expect(parseColor("var(--x)")).toBeNull();
	});

	it("对比度：黑白 21，同色 1", () => {
		expect(contrast([0, 0, 0, 1], [1, 1, 1, 1])).toBeCloseTo(21, 5);
		expect(contrast([0.5, 0.5, 0.5, 1], [0.5, 0.5, 0.5, 1])).toBe(1);
	});
});
