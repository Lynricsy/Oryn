/**
 * 角形降级：只在不支持 `corner-shape` 的引擎里由 lib/squircle.ts 动态加载（Safari / WKWebView 27.3 以前、Firefox、
 * WebKitGTK）。Safari 27.3 起原生支持，macOS 桌面壳随系统 WebKit 升级后自然走原生路径，不再下载本分块。
 *
 * 只做一件事：squircle-patch.ts 把样式表里每个 border-*-radius 改写成「原值 × --sq-s」，--sq-s 注册成不继承、
 * 初始值为 ROUND_SCALE（≈0.698）的自定义属性，正圆与胶囊由 index.css「角形」段落设为 1。原生圆角因此缩成恰好包住
 * 超椭圆的「包络圆」，与 superellipse(1.6) 的最大法向偏差 1.42% 半径（r = 36px 时 0.51px，r = 18px 时 0.26px）。
 * 背景、边框、投影、焦点环、玻璃模糊都由浏览器照常绘制、照常过渡，跟着同一个圆走，带投影的盒子不会出现双层圆角。
 *
 * 为什么不逐元素剪出精确的超椭圆（以前的 clip-path 薄片 + SVG border-image 环带）：只换来上面那点亚像素精度，代价却大——
 * clip-path 只剪得到盒内，box-shadow 仍按包络圆画在盒外，薄片处漏出投影，实机看是「超椭圆下面还垫着一层普通圆角」；
 * 逐元素跟踪（ResizeObserver / MutationObserver / 规则属性切换）让侧栏与面板开合动画每帧的样式 + 布局耗时约翻 5 倍。
 *
 * 接受的差别（都只是略圆一点）：
 * - 相邻半径之和超过边长时（CSS 等比缩小规则），浏览器按缩放后的包络圆半径等比缩小，近胶囊的形状比原生超椭圆略圆；
 * - 行内样式（style 属性、React style、Web Animations 关键帧）里的半径不改写，停在原值的普通圆角。
 *
 * 不读布局、不碰元素：只在样式表可能变化时重扫，按顶层规则数跳过没变过的表——
 * <head> 子树变动（<style> 增删与改文本：Vite 热更新、CodeMirror 主题）、<link> 加载完成（懒加载的 CSS 分块）、
 * <body> 直接子节点里新增的 <style> / <link>。别处的 <style>（Mermaid 每次重绘在 SVG 里换一张）不触发重扫，
 * 只在下次重扫时顺带过一遍；它们没有半径声明，改写不改任何东西。
 */
import { ROUND_SCALE } from "./squircle-geometry";
import { patchRules, SCALE_VAR } from "./squircle-patch";

/** 根元素上的开关属性（index.css 里正圆与胶囊的 --sq-s = 1 挂在它下面）。 */
const ROOT_ATTR = "data-squircle";

/**
 * 把 --sq-s 注册成不继承、初始值为 ROUND_SCALE 的自定义属性：每个元素与伪元素不写任何规则就是包络圆系数，
 * 正圆与胶囊由 index.css 设为 1，不会传给后代。
 * 比通配规则（`*, ::before, ::after { --sq-s: … }`）便宜：通配规则让每个元素在每次样式重算里都要匹配、带上自己的变量表。
 * 不支持 CSS.registerProperty 的引擎（Firefox 128 以前）退回通配规则。
 */
function registerScale(doc: Document): void {
	const initial = ROUND_SCALE.toFixed(5);
	if (
		typeof CSS !== "undefined" &&
		typeof CSS.registerProperty === "function"
	) {
		try {
			// syntax "*"：值只当作记号传给 var()，不参与插值（作者的 transition: all 不会把它过渡）
			CSS.registerProperty({
				name: SCALE_VAR,
				syntax: "*",
				inherits: false,
				initialValue: initial,
			});
			return;
		} catch {
			// 同名属性已被注册（不会发生：每个文档只装一次）——仍按通配规则兜底
		}
	}
	const sheet = new CSSStyleSheet();
	sheet.insertRule(
		`:root[${ROOT_ATTR}],:root[${ROOT_ATTR}] *,:root[${ROOT_ATTR}] ::before,:root[${ROOT_ATTR}] ::after{${SCALE_VAR}:${initial}}`,
	);
	doc.adoptedStyleSheets = [...doc.adoptedStyleSheets, sheet];
}

/**
 * 启用降级：注册 --sq-s，改写现有与以后加入的样式表。每个文档只装一次（以根元素上的开关属性为准）。
 * 须在首屏渲染前调用（main.tsx 在挂载 React 之前 await），否则已经画出来的圆角会先闪一下原值的普通圆角。
 */
export function installSquircleFallback(doc: Document): void {
	const root = doc.documentElement;
	if (root.hasAttribute(ROOT_ATTR)) return;
	root.setAttribute(ROOT_ATTR, "fallback");
	registerScale(doc);

	/** 每张表改写时的顶层规则数：没变就跳过（改写是幂等的，规则数只用来省掉重复遍历）。 */
	const ruleCounts = new WeakMap<CSSStyleSheet, number>();
	const patchSheets = () => {
		for (const sheet of [
			...Array.from(doc.styleSheets),
			...doc.adoptedStyleSheets,
		]) {
			let rules: CSSRuleList;
			try {
				rules = sheet.cssRules;
			} catch {
				continue; // 跨源样式表读不到规则
			}
			if (ruleCounts.get(sheet) === rules.length) continue;
			patchRules(rules);
			ruleCounts.set(sheet, rules.length);
		}
	};

	// MutationObserver 的回调在本轮渲染之前执行：新样式表画出来之前就已改写，不会先闪一帧普通圆角
	const observer = new MutationObserver((records) => {
		if (
			records.some(
				(r) =>
					r.target !== doc.body ||
					Array.from(r.addedNodes).some(
						(n) => n.nodeName === "STYLE" || n.nodeName === "LINK",
					),
			)
		) {
			patchSheets();
		}
	});
	observer.observe(doc.head, {
		childList: true,
		subtree: true,
		characterData: true,
	});
	if (doc.body) observer.observe(doc.body, { childList: true });
	// <link> 样式表加载完才有规则（load 不冒泡，在捕获阶段接）
	doc.addEventListener(
		"load",
		(e) => {
			if (e.target instanceof HTMLLinkElement) patchSheets();
		},
		true,
	);

	patchSheets();
}
