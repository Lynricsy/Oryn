import { describe, expect, it } from "vitest";
import { type PredictionInput, predictionView } from "./composer-prediction";

const CHOICES = ["Run the tests", "Run the tests and fix failures", "Ship it"];

function view(over: Partial<PredictionInput>) {
	const draft = over.draft ?? "";
	return predictionView({
		choices: CHOICES,
		draft,
		caret: draft.length,
		enabled: true,
		inserted: null,
		...over,
	});
}

describe("predictionView：幽灵字", () => {
	it("空草稿显示首个候选", () => {
		expect(view({}).ghost).toEqual({
			choice: "Run the tests",
			suffix: "Run the tests",
		});
	});

	it("草稿是前缀时显示剩余部分；取第一个匹配的候选", () => {
		expect(view({ draft: "Run" }).ghost).toEqual({
			choice: "Run the tests",
			suffix: " the tests",
		});
		expect(view({ draft: "Sh" }).ghost?.choice).toBe("Ship it");
	});

	it("区分大小写；不匹配时不显示", () => {
		expect(view({ draft: "run" }).ghost).toBeNull();
		expect(view({ draft: "Deploy" }).ghost).toBeNull();
	});

	it("严格前缀：草稿等于候选时跳到更长的候选，没有更长的就不显示", () => {
		expect(view({ draft: "Run the tests" }).ghost).toEqual({
			choice: "Run the tests and fix failures",
			suffix: " and fix failures",
		});
		expect(view({ draft: "Ship it" }).ghost).toBeNull();
	});

	it("光标不在末尾、草稿多行、被禁用、没有候选时不显示", () => {
		expect(view({ draft: "Run", caret: 1 }).ghost).toBeNull();
		expect(view({ draft: "Run\n" }).ghost).toBeNull();
		expect(view({ draft: "Run", enabled: false }).ghost).toBeNull();
		expect(view({ draft: "", choices: [] }).ghost).toBeNull();
	});

	it("光标位置大于草稿长度（草稿被程序清空 / 缩短）仍视为在末尾", () => {
		expect(view({ draft: "", caret: 12 }).ghost?.choice).toBe("Run the tests");
	});
});

describe("predictionView：插入后的轮换", () => {
	it("刚插入的候选不再被更长的候选延伸，Tab 换到下一个", () => {
		const v = view({ draft: "Run the tests", inserted: "Run the tests" });
		expect(v.ghost).toBeNull();
		expect(v.cycle).toEqual({
			index: 0,
			total: 3,
			next: "Run the tests and fix failures",
		});
	});

	it("最后一个候选循环回第一个", () => {
		expect(view({ draft: "Ship it", inserted: "Ship it" }).cycle).toEqual({
			index: 2,
			total: 3,
			next: "Run the tests",
		});
	});

	it("插入后又编辑过（inserted 已清空或与草稿不同）按普通前缀处理", () => {
		const v = view({ draft: "Run the tests", inserted: null });
		expect(v.cycle).toBeNull();
		expect(v.ghost?.choice).toBe("Run the tests and fix failures");
		expect(
			view({ draft: "Run the test", inserted: "Run the tests" }).cycle,
		).toBeNull();
	});

	it("只有一个候选时不轮换，也不显示幽灵字", () => {
		const v = view({
			draft: "Ship it",
			inserted: "Ship it",
			choices: ["Ship it"],
		});
		expect(v).toEqual({ ghost: null, cycle: null });
	});

	it("被禁用（菜单、运行中等）时不轮换", () => {
		expect(
			view({ draft: "Ship it", inserted: "Ship it", enabled: false }).cycle,
		).toBeNull();
	});

	it("光标离开末尾后不轮换（同幽灵字）", () => {
		expect(
			view({ draft: "Ship it", inserted: "Ship it", caret: 3 }).cycle,
		).toBeNull();
	});
});
