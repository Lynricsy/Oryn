// 页面声明的调节参数（计划 M14 6.5）：DOM 就绪后读 `<script type="application/json" data-oryn-params>`，
// 把每个参数设成根元素的 CSS 变量 `--<名>` 与属性 `data-<名>`；父页面发来的 `params {values}` 即时套用（不落盘）。
// 规整与转换规则在 `src/lib/artifacts/params.ts`（与服务端相同）。

import {
	coerceParamValue,
	type PageParam,
	type ParamValue,
	paramCss,
	paramsDecl,
	parseParamsDecl,
} from "../../src/lib/artifacts/params";

export interface PageParams {
	/** 规整后的声明，作 `ready.params`。 */
	readonly decl: Record<string, unknown>;
	/** 套用一组预览值；未声明的参数与不合规的值略过。 */
	preview(values: Record<string, ParamValue>): void;
}

/** 页面里第一个 type 为 application/json 的 `data-oryn-params` 脚本的内容；没有时为 `null`。 */
function declText(doc: Document): string | null {
	for (const el of doc.querySelectorAll("script[data-oryn-params]")) {
		const type = el.getAttribute("type") ?? "";
		if (type.trim().toLowerCase() === "application/json")
			return el.textContent ?? "";
	}
	return null;
}

/** 读声明并把声明的值设到根元素上；`parse` 是页面加载前缓存的 `JSON.parse`。 */
export function installParams(
	doc: Document,
	parse: (text: string) => unknown,
): PageParams {
	const text = declText(doc);
	let params: PageParam[] = [];
	if (text !== null) {
		try {
			params = parseParamsDecl(parse(text));
		} catch {
			// 声明不是合法的 JSON：没有参数，页面照常
		}
	}
	const root = doc.documentElement;
	const apply = (p: PageParam, v: ParamValue) => {
		const css = paramCss(p, v);
		root.style.setProperty(`--${p.name}`, css.variable);
		root.setAttribute(`data-${p.name}`, css.attribute);
	};
	for (const p of params) apply(p, p.value);
	return {
		decl: paramsDecl(params),
		preview(values) {
			for (const p of params) {
				if (!Object.hasOwn(values, p.name)) continue;
				const v = coerceParamValue(p, values[p.name]);
				if (v !== null) apply(p, v);
			}
		},
	};
}
