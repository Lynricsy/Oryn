/** 视图表单：初值、必填与范围校验、作答值的转换、已提交值转回草稿。 */
import { describe, expect, it } from "vitest";
import {
	draftFromValues,
	formValues,
	initialFormDraft,
	validateForm,
} from "./form";
import { type FormBlock, normalizeUiSpec } from "./spec";

function form(fields: unknown[]): FormBlock {
	const block = normalizeUiSpec({ blocks: [{ type: "form", fields }] })
		?.blocks[0];
	if (block?.type !== "form") throw new Error("不是表单");
	return block;
}

const OPTS = [
	{ label: "甲", value: "a" },
	{ label: "乙", value: "b" },
];

describe("initialFormDraft", () => {
	it("按种类取 defaultValue，取不到时给空值", () => {
		const f = form([
			{ name: "t", label: "文本", kind: "text", defaultValue: 3 },
			{ name: "n", label: "数", kind: "number", defaultValue: 2.5 },
			{ name: "s", label: "滑块", kind: "slider", min: 10, max: 20 },
			{ name: "c", label: "勾", kind: "checkbox" },
			{ name: "w", label: "开关", kind: "switch", defaultValue: true },
			{
				name: "m",
				label: "多选",
				kind: "multiselect",
				options: OPTS,
				defaultValue: ["b", "x"],
			},
			{
				name: "r",
				label: "单选",
				kind: "radio",
				options: OPTS,
				defaultValue: "x",
			},
		]);
		expect(initialFormDraft(f)).toEqual({
			t: "3",
			n: "2.5",
			s: 10,
			c: false,
			w: true,
			m: ["b"],
			r: "",
		});
	});
});

describe("validateForm", () => {
	const f = form([
		{ name: "t", label: "文本", kind: "text", required: true },
		{
			name: "m",
			label: "多选",
			kind: "multiselect",
			options: OPTS,
			required: true,
		},
		{ name: "c", label: "勾", kind: "checkbox", required: true },
		{ name: "w", label: "开关", kind: "switch", required: true },
		{ name: "n", label: "数", kind: "number", min: 1, max: 9 },
	]);

	it("必填：空白文本、空多选、未勾的复选框；开关不算", () => {
		expect(
			validateForm(f, { t: "  ", m: [], c: false, w: false, n: "" }),
		).toEqual({
			t: { kind: "required" },
			m: { kind: "required" },
			c: { kind: "required" },
		});
	});

	it("数值：非数字与越界", () => {
		const ok = { t: "x", m: ["a"], c: true, w: false };
		expect(validateForm(f, { ...ok, n: "abc" })).toEqual({
			n: { kind: "number" },
		});
		expect(validateForm(f, { ...ok, n: "0" })).toEqual({
			n: { kind: "min", min: 1 },
		});
		expect(validateForm(f, { ...ok, n: "10" })).toEqual({
			n: { kind: "max", max: 9 },
		});
		expect(validateForm(f, { ...ok, n: "5" })).toEqual({});
	});
});

describe("formValues / draftFromValues", () => {
	const f = form([
		{ name: "t", label: "文本", kind: "text" },
		{ name: "n", label: "数", kind: "number" },
		{ name: "r", label: "单选", kind: "radio", options: OPTS },
		{ name: "m", label: "多选", kind: "multiselect", options: OPTS },
		{ name: "s", label: "滑块", kind: "slider" },
	]);

	it("空文本为 null，数字框转成数", () => {
		expect(formValues(f, { t: "", n: " 4 ", r: "", m: [], s: 30 })).toEqual({
			t: null,
			n: 4,
			r: null,
			m: [],
			s: 30,
		});
	});

	it("已提交的值转回控件草稿", () => {
		expect(
			draftFromValues(f, { t: null, n: 4, r: "b", m: ["a"], s: 30 }),
		).toEqual({ t: "", n: "4", r: "b", m: ["a"], s: 30 });
	});
});
