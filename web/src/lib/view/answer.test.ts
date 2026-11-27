/** 视图作答的线上格式（docs/plan-m33.md 的 C4）：作答正文、回执的生成与解析往返、阻塞应答。 */
import { describe, expect, it } from "vitest";
import {
	buildViewReceipt,
	encodeAwaitValue,
	formatFormAnswer,
	isAnswerableCallId,
	parseViewReceipt,
	readViewAnswer,
	type ViewAnswer,
	viewAwaitCallId,
} from "./answer";
import type { FormBlock } from "./spec";

const form: FormBlock = {
	type: "form",
	path: "1.c0.0",
	title: "出行",
	columns: 1,
	fields: [
		{
			name: "city",
			label: "城市",
			kind: "select",
			required: true,
			options: [{ label: "北京", value: "bj" }],
		},
		{ name: "days", label: "天数", kind: "number", required: false },
		{
			name: "tags",
			label: "偏好",
			kind: "multiselect",
			required: false,
			options: [
				{ label: "美食", value: "food" },
				{ label: "博物馆", value: "museum" },
			],
		},
		{ name: "hotel", label: "订酒店", kind: "switch", required: false },
		{ name: "note", label: "备注", kind: "textarea", required: false },
	],
};

describe("作答正文", () => {
	it("表单：标题、每字段一行显示值、空行、JSON 围栏", () => {
		const text = formatFormAnswer(form, {
			city: "bj",
			days: 3,
			tags: ["food", "museum"],
			hotel: true,
			note: null,
		});
		expect(text).toBe(
			[
				"出行",
				"- 城市: 北京",
				"- 天数: 3",
				"- 偏好: 美食, 博物馆",
				"- 订酒店: ✓",
				"- 备注: —",
				"",
				"```json",
				'{"city":"bj","days":3,"tags":["food","museum"],"hotel":true,"note":null}',
				"```",
			].join("\n"),
		);
	});
});

describe("回执往返", () => {
	it("表单：values 取自最后一个 JSON 围栏，display 去掉围栏", () => {
		const values = {
			city: "bj",
			days: 3,
			tags: ["food"],
			hotel: false,
			note: null,
		};
		const text = formatFormAnswer(form, values);
		const answer: ViewAnswer = { block: form.path, kind: "form", values };
		const msg = buildViewReceipt("call_1", answer, text);
		expect(msg.startsWith("[Oryn ")).toBe(false);
		const parsed = parseViewReceipt(msg);
		expect(parsed?.toolCallId).toBe("call_1");
		expect(parsed?.answer).toEqual(answer);
		expect(parsed?.text).toBe(text);
		expect(parsed?.display).not.toContain("```");
		expect(parsed?.display.startsWith("出行\n- 城市: 北京")).toBe(true);
	});

	it("多行字段里有未闭合的 ```json 时仍取末尾生成的围栏，display 保留主人写的内容", () => {
		const values = {
			city: "bj",
			days: null,
			tags: [],
			hotel: false,
			note: "示例：\n```json\n{ 没写完",
		};
		const text = formatFormAnswer(form, values);
		const answer: ViewAnswer = { block: form.path, kind: "form", values };
		const parsed = parseViewReceipt(buildViewReceipt("c1", answer, text));
		expect(parsed?.answer).toEqual(answer);
		expect(parsed?.display).toContain("```json\n{ 没写完");
		expect(parsed?.display).not.toContain('"city":"bj"');
	});

	it("按钮：带 choice，正文是按钮的 prompt", () => {
		const answer: ViewAnswer = { block: "3", kind: "action", choice: 2 };
		const parsed = parseViewReceipt(
			buildViewReceipt("c|2", answer, "就选第三个"),
		);
		expect(parsed).toMatchObject({
			toolCallId: "c|2",
			answer,
			text: "就选第三个",
			display: "就选第三个",
		});
	});

	it("HTML：块固定为 html", () => {
		const answer: ViewAnswer = { block: "html", kind: "prompt" };
		const parsed = parseViewReceipt(buildViewReceipt("c", answer, "a\nb"));
		expect(parsed?.answer).toEqual(answer);
		expect(parsed?.text).toBe("a\nb");
	});

	it("不是回执、或格式不对时为 null", () => {
		expect(parseViewReceipt("你好")).toBeNull();
		expect(
			parseViewReceipt(
				'<oryn-view-answer call="a b" block="1">\nx\n</oryn-view-answer>',
			),
		).toBeNull();
		expect(
			parseViewReceipt(
				'前言\n<oryn-view-answer call="a" block="1">\nx\n</oryn-view-answer>',
			),
		).toBeNull();
		expect(
			parseViewReceipt(
				'<oryn-view-answer call="a" block="1">\nx\n</oryn-view-answer>\n追加',
			),
		).toBeNull();
	});

	it("JSON 里的 __proto__ 键不进 values", () => {
		const msg =
			'<oryn-view-answer call="a" block="0">\n```json\n{"__proto__":["x"],"k":"v"}\n```\n</oryn-view-answer>';
		const parsed = parseViewReceipt(msg);
		expect(parsed?.answer).toEqual({
			block: "0",
			kind: "form",
			values: { k: "v" },
		});
		const values = parsed?.answer.kind === "form" ? parsed.answer.values : {};
		expect(Object.getPrototypeOf(values)).toBe(Object.prototype);
	});
});

describe("可作答的 callId", () => {
	it("含引号、空白、尖括号或超过 256 字符时不可作答", () => {
		expect(isAnswerableCallId("toolu_01AbC|fc_9")).toBe(true);
		expect(isAnswerableCallId("")).toBe(false);
		expect(isAnswerableCallId('a"b')).toBe(false);
		expect(isAnswerableCallId("a b")).toBe(false);
		expect(isAnswerableCallId("a<b")).toBe(false);
		expect(isAnswerableCallId("x".repeat(257))).toBe(false);
	});
});

describe("阻塞通道", () => {
	it("只认 input + oryn-view: 标记", () => {
		expect(viewAwaitCallId("input", "oryn-view:call_1")).toBe("call_1");
		expect(viewAwaitCallId("input", "oryn-view:")).toBeNull();
		expect(viewAwaitCallId("input", "随便")).toBeNull();
		expect(viewAwaitCallId("editor", "oryn-view:call_1")).toBeNull();
	});

	it("应答值与工具结果里的作答", () => {
		const answer: ViewAnswer = { block: "0", kind: "action", choice: 1 };
		expect(JSON.parse(encodeAwaitValue(answer, "t"))).toEqual({
			v: 1,
			answer,
			text: "t",
		});
		expect(readViewAnswer(answer)).toEqual(answer);
		expect(readViewAnswer({ block: "html", kind: "prompt" })).toEqual({
			block: "html",
			kind: "prompt",
		});
		expect(readViewAnswer({ block: "1", kind: "prompt" })).toBeNull();
		expect(
			readViewAnswer({
				block: "1",
				kind: "form",
				values: { a: 1, b: { x: 1 } },
			}),
		).toEqual({
			block: "1",
			kind: "form",
			values: { a: 1, b: null },
		});
		expect(readViewAnswer("x")).toBeNull();
	});
});
