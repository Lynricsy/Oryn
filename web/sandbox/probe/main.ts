// 设计探针（构建成单个 IIFE：dist/sandbox/probe.js；plan-m29 C8）。
//
// 只在页面地址带 `?__oryn_probe=1` 时运行。两种加载方式：设计画框的引擎入口在 React 首次渲染提交后加载同源的
// `./probe.js`；控制面渲染 HTML 画框时把本脚本内联进页面。审美配置 `{aesthetic, surface}` 由启动方给：控制面先设
// `window.__orynProbe`；orb 里的 design.check 没法往页面里注入脚本，改用地址的 `__oryn_aesthetic=1`、
// `__oryn_surface=<landing|app>`（缺省只跑前 6 条版面规则）。等页面 load、字体就绪、再等一次空闲后测量，把结果写成
// `<script id="oryn-probe" type="application/json">{v:1, frame, height, issues}</script>` 追加到 body，
// 由 headless Chromium 的 --dump-dom 带出。页面运行在不可信的沙箱里，这里不 import 任何应用代码。

import { check, type Issue, type Options } from "./rules";

/** 结果节点的 id。 */
export const RESULT_ID = "oryn-probe";

export interface ProbeResult {
	v: 1;
	frame: string;
	height: number;
	issues: Issue[];
}

/** 启动方的配置：`window.__orynProbe` 存在时只看它（控制面），否则看地址的 `__oryn_aesthetic` / `__oryn_surface`
 * （orb 的 design.check；surface 只认 landing、app，别的值当作空）。缺省或不合法时只跑版面规则。 */
export function readOptions(win: Window): Options {
	const raw: unknown = Reflect.get(win, "__orynProbe");
	if (typeof raw !== "object" || raw === null) {
		const q = new URLSearchParams(win.location.search);
		const surface = q.get("__oryn_surface");
		return {
			aesthetic: q.get("__oryn_aesthetic") === "1",
			surface: surface === "landing" || surface === "app" ? surface : "",
		};
	}
	const surface = Reflect.get(raw, "surface");
	return {
		aesthetic: Reflect.get(raw, "aesthetic") === true,
		surface: typeof surface === "string" ? surface : "",
	};
}

/** 帧 id：先看 dev 入口的 `#oryn-design`，再从地址推（`…/<id>.html` 或 dev 的 `/frames/<id>`），都取不到为空串。 */
export function frameOf(win: Window): string {
	const el = win.document.getElementById("oryn-design");
	if (el?.textContent) {
		try {
			const v: unknown = JSON.parse(el.textContent);
			const f =
				typeof v === "object" && v !== null
					? Reflect.get(v, "frame")
					: undefined;
			if (typeof f === "string" && f) return f;
		} catch {
			// 不合法时按地址推
		}
	}
	const path = win.location.pathname;
	const m =
		/\/([a-z0-9][a-z0-9-]{0,63})\.html$/.exec(path) ??
		/\/frames\/([a-z0-9][a-z0-9-]{0,63})\/?$/.exec(path);
	return m?.[1] ?? "";
}

/** 测量一次并把结果写进 body（替换页面里已有的同 id 节点）。 */
export function report(win: Window): ProbeResult {
	const doc = win.document;
	for (const old of doc.querySelectorAll(`#${RESULT_ID}`)) old.remove();
	const result: ProbeResult = {
		v: 1,
		frame: frameOf(win),
		height: Math.ceil(
			Math.max(doc.documentElement.scrollHeight, doc.body?.scrollHeight ?? 0),
		),
		issues: check(win, readOptions(win)),
	};
	const out = doc.createElement("script");
	out.id = RESULT_ID;
	out.setAttribute("type", "application/json");
	// `<` 转义后结果里不会出现 `</script`，抽取方按纯文本截取
	out.textContent = JSON.stringify(result).replaceAll("<", "\\u003c");
	(doc.body ?? doc.documentElement).append(out);
	return result;
}

/** 等页面 load、字体就绪、一次空闲（没有 requestIdleCallback 时用 setTimeout）。
 * tsconfig 的 lib 是 ES2023，没有 Promise.withResolvers，用构造函数。 */
export async function settle(win: Window): Promise<void> {
	if (win.document.readyState !== "complete")
		await new Promise<void>((resolve) =>
			win.addEventListener("load", () => resolve(), { once: true }),
		);
	const fonts: { ready?: Promise<unknown> } | undefined = Reflect.get(
		win.document,
		"fonts",
	);
	if (fonts?.ready) await fonts.ready.catch(() => undefined);
	await new Promise<void>((resolve) => {
		if (typeof win.requestIdleCallback === "function")
			win.requestIdleCallback(() => resolve(), { timeout: 1000 });
		else win.setTimeout(() => resolve(), 0);
	});
}

/** 入口：地址带 `__oryn_probe=1` 时（每个页面只一次）等页面稳定后报告。 */
export function start(win: Window): Promise<ProbeResult | null> {
	if (new URLSearchParams(win.location.search).get("__oryn_probe") !== "1")
		return Promise.resolve(null);
	if (Reflect.get(win, "__orynProbeStarted")) return Promise.resolve(null);
	Reflect.set(win, "__orynProbeStarted", true);
	return settle(win).then(() => report(win));
}

if (typeof window !== "undefined" && typeof document !== "undefined")
	void start(window);
