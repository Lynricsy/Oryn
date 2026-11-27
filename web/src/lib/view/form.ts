/**
 * 视图表单（`form` 块）的草稿、校验与取值。
 *
 * 草稿与作答值的区别只在数字框：编辑中保留原样字符串（允许暂时打出 `-`、`1.`），提交时才转成数。
 * 校验只管必填与数值范围（C2）：语义由 agent 复核，邮箱、网址等格式不在这里判。
 */
import type { FieldValue, FormBlock, FormField } from "./spec";

/** 编辑中的表单：字段名 → 草稿值（数字框是字符串）。 */
export type FormDraft = Record<string, FieldValue>;

export type FieldError =
	| { kind: "required" }
	| { kind: "number" }
	| { kind: "min"; min: number }
	| { kind: "max"; max: number };

function optionValues(field: FormField): Set<string> {
	return new Set(field.options?.map((o) => o.value));
}

/** 滑块没有初值时的落点：min，没有 min 时 0，再夹进 max。 */
function sliderStart(field: FormField): number {
	const lo = field.min ?? 0;
	return field.max !== undefined ? Math.min(lo, field.max) : lo;
}

/** 一个字段的草稿值：按种类把任意来源（defaultValue 或已提交的值）收成控件能显示的形状。 */
function draftValue(field: FormField, raw: unknown): FieldValue {
	switch (field.kind) {
		case "checkbox":
		case "switch":
			return raw === true || raw === "true";
		case "slider": {
			const n = typeof raw === "number" ? raw : Number(raw);
			return typeof raw !== "boolean" &&
				raw !== null &&
				raw !== "" &&
				Number.isFinite(n)
				? n
				: sliderStart(field);
		}
		case "multiselect": {
			const allowed = optionValues(field);
			const list = Array.isArray(raw)
				? raw
				: typeof raw === "string"
					? [raw]
					: [];
			return list.filter((v): v is string => allowed.has(v));
		}
		case "select":
		case "radio": {
			const v = typeof raw === "number" ? String(raw) : raw;
			return typeof v === "string" && optionValues(field).has(v) ? v : "";
		}
		default:
			return typeof raw === "string" || typeof raw === "number"
				? String(raw)
				: "";
	}
}

/** 表单初值：取各字段的 `defaultValue`。 */
export function initialFormDraft(form: FormBlock): FormDraft {
	const out: FormDraft = {};
	for (const f of form.fields) out[f.name] = draftValue(f, f.defaultValue);
	return out;
}

/** 已提交的值转回草稿（锁定后按提交的内容显示控件）。 */
export function draftFromValues(
	form: FormBlock,
	values: Readonly<Record<string, FieldValue>>,
): FormDraft {
	const out: FormDraft = {};
	for (const f of form.fields) out[f.name] = draftValue(f, values[f.name]);
	return out;
}

function isBlank(v: FieldValue | undefined): boolean {
	return (
		v === null ||
		v === undefined ||
		(typeof v === "string" && v.trim() === "") ||
		(Array.isArray(v) && v.length === 0)
	);
}

/** 校验草稿；返回出错的字段名 → 错误。空对象即通过。 */
export function validateForm(
	form: FormBlock,
	draft: Readonly<FormDraft>,
): Record<string, FieldError> {
	const errors: Record<string, FieldError> = {};
	for (const f of form.fields) {
		const v = draft[f.name];
		if (f.kind === "checkbox") {
			if (f.required && v !== true) errors[f.name] = { kind: "required" };
			continue;
		}
		if (f.kind === "switch") continue;
		if (isBlank(v)) {
			if (f.required) errors[f.name] = { kind: "required" };
			continue;
		}
		if (f.kind !== "number" && f.kind !== "slider") continue;
		const n = typeof v === "number" ? v : Number(String(v).trim());
		if (!Number.isFinite(n)) errors[f.name] = { kind: "number" };
		else if (f.min !== undefined && n < f.min)
			errors[f.name] = { kind: "min", min: f.min };
		else if (f.max !== undefined && n > f.max)
			errors[f.name] = { kind: "max", max: f.max };
	}
	return errors;
}

/** 通过校验的草稿转成作答值（C4 的 `values`）：空文本为 null，数字框转成数。 */
export function formValues(
	form: FormBlock,
	draft: Readonly<FormDraft>,
): Record<string, FieldValue> {
	const out: Record<string, FieldValue> = {};
	for (const f of form.fields) {
		const v = draft[f.name] ?? null;
		if (f.kind === "number") {
			out[f.name] = isBlank(v) ? null : Number(String(v).trim());
		} else if (typeof v === "string") {
			out[f.name] = isBlank(v) ? null : v;
		} else {
			out[f.name] = v;
		}
	}
	return out;
}
