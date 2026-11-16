/**
 * 画框的调节参数（计划 M14 6.5）：页面在 `<script type="application/json" data-oryn-params>` 里声明「参数名 → 定义」，
 * 用户在画布的「调节」面板里改值，服务端写回这段 JSON 落成一个用户版本。
 *
 * 声明的规整与值的校验和服务端 `internal/artifacts/params.go` 逐条相同，两边读同一份测试向量
 * `schema/testdata/artifact-params.json`。解析是宽松的：不合规的项逐个略过，其余照常。
 * 沙箱运行时与 SPA 共用这个文件，所以只放纯函数与类型。
 */
import { runeCount } from "./editable";

export const MAX_PARAMS = 24;
export const PARAM_TEXT_RUNES = 200;
export const PARAM_LABEL_RUNES = 60;
export const PARAM_MAX_OPTIONS = 20;
const MIN_STEP = 0.001;

export const PARAM_NAME = /^[a-z][a-z0-9-]{0,31}$/;
const COLOR = /^#([0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$/;
const UNIT = /^[a-z%]{0,8}$/;

export type ParamValue = string | number | boolean;

interface ParamBase {
	name: string;
	/** 去掉首尾空白、最多 60 个字；没有时为空串。 */
	label: string;
}

export type PageParam =
	| (ParamBase & { type: "color"; value: string })
	| (ParamBase & { type: "text"; value: string })
	| (ParamBase & {
			type: "number";
			value: number;
			min: number;
			max: number;
			step: number;
			unit: string;
	  })
	| (ParamBase & { type: "boolean"; value: boolean })
	| (ParamBase & { type: "choice"; value: string; options: string[] });

export type ParamType = PageParam["type"];

type Fields = Record<string, unknown>;

function isFields(x: unknown): x is Fields {
	return typeof x === "object" && x !== null && !Array.isArray(x);
}

function isNumber(x: unknown): x is number {
	return typeof x === "number" && Number.isFinite(x);
}

/** 可选的数字字段：缺省回 `fallback`；给了却不是有限数字回 `null`。 */
function optNumber(def: Fields, key: string, fallback: number): number | null {
	if (!Object.hasOwn(def, key)) return fallback;
	const v = def[key];
	return isNumber(v) ? v : null;
}

/** 校验参数 `p` 的一个新值：数字夹回 [min, max]；不合规回 `null`。 */
export function coerceParamValue(p: PageParam, v: unknown): ParamValue | null {
	switch (p.type) {
		case "color":
			return typeof v === "string" && COLOR.test(v) ? v : null;
		case "text":
			return typeof v === "string" && runeCount(v) <= PARAM_TEXT_RUNES
				? v
				: null;
		case "number":
			return isNumber(v) ? Math.min(p.max, Math.max(p.min, v)) : null;
		case "boolean":
			return typeof v === "boolean" ? v : null;
		case "choice":
			return typeof v === "string" && p.options.includes(v) ? v : null;
	}
}

function parseParam(name: string, raw: unknown): PageParam | null {
	if (!PARAM_NAME.test(name) || !isFields(raw)) return null;
	const base = {
		name,
		label:
			typeof raw.label === "string"
				? Array.from(raw.label.trim()).slice(0, PARAM_LABEL_RUNES).join("")
				: "",
	};
	let p: PageParam;
	switch (raw.type) {
		case "number": {
			const min = optNumber(raw, "min", 0);
			const max = optNumber(raw, "max", 100);
			const step = optNumber(raw, "step", 1);
			const unit = Object.hasOwn(raw, "unit") ? raw.unit : "";
			if (
				min === null ||
				max === null ||
				step === null ||
				typeof unit !== "string" ||
				!UNIT.test(unit) ||
				min > max
			)
				return null;
			p = {
				...base,
				type: "number",
				value: 0,
				min,
				max,
				step: Math.max(step, MIN_STEP),
				unit,
			};
			break;
		}
		case "choice": {
			const options = raw.options;
			if (
				!Array.isArray(options) ||
				options.length === 0 ||
				options.length > PARAM_MAX_OPTIONS ||
				!options.every((o) => typeof o === "string")
			)
				return null;
			p = { ...base, type: "choice", value: "", options: [...options] };
			break;
		}
		case "color":
		case "text":
			p = { ...base, type: raw.type, value: "" };
			break;
		case "boolean":
			p = { ...base, type: "boolean", value: false };
			break;
		default:
			return null;
	}
	const value = coerceParamValue(p, raw.value);
	if (value === null) return null;
	return { ...p, value } as PageParam;
}

/** 规整声明（`JSON.parse` 的结果）：按声明顺序，不合规的项略过，最多取 `MAX_PARAMS` 个合规的。 */
export function parseParamsDecl(raw: unknown): PageParam[] {
	if (!isFields(raw)) return [];
	const out: PageParam[] = [];
	for (const name of Object.keys(raw)) {
		if (out.length === MAX_PARAMS) break;
		const p = parseParam(name, raw[name]);
		if (p) out.push(p);
	}
	return out;
}

/** 规整后的参数再写回「参数名 → 定义」的形状（运行时用它作 `ready.params`，父页面再 `parseParamsDecl` 一遍）。 */
export function paramsDecl(
	params: readonly PageParam[],
): Record<string, unknown> {
	const out: Record<string, unknown> = {};
	for (const { name, ...def } of params) out[name] = def;
	return out;
}

/** 参数在根元素上的两种形式：CSS 变量 `--<名>` 的值与属性 `data-<名>` 的值。 */
export interface ParamCss {
	variable: string;
	attribute: string;
}

/** 数字带单位（属性不带）、文本的变量是 `JSON.stringify` 的结果、其余原样。与服务端的 `CSSOf` 相同。 */
export function paramCss(p: PageParam, v: ParamValue): ParamCss {
	if (typeof v === "number") {
		const n = String(v);
		return { variable: p.type === "number" ? n + p.unit : n, attribute: n };
	}
	if (typeof v === "boolean")
		return { variable: String(v), attribute: String(v) };
	return { variable: p.type === "text" ? JSON.stringify(v) : v, attribute: v };
}

/**
 * 校验父页面发来的 `params.values`：普通对象，最多 `MAX_PARAMS` 项，键是合法的参数名，值是字符串、有限数字或布尔。
 * 值是否符合各参数的定义由页面运行时按自己的声明再判。不合规回 `null`。
 */
export function parseParamValues(
	x: unknown,
): Record<string, ParamValue> | null {
	if (!isFields(x)) return null;
	const keys = Object.keys(x);
	if (keys.length > MAX_PARAMS) return null;
	const out: Record<string, ParamValue> = {};
	for (const k of keys) {
		const v = x[k];
		if (
			!PARAM_NAME.test(k) ||
			!(typeof v === "string" || typeof v === "boolean" || isNumber(v))
		)
			return null;
		out[k] = v;
	}
	return out;
}
