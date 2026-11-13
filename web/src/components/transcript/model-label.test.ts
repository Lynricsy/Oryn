import { describe, expect, it } from "vitest";
import { modelLabel } from "./model-label";

describe("modelLabel", () => {
	const known = {
		id: "claude-sonnet-5",
		name: "Claude Sonnet 5",
		provider: "anthropic",
	};

	it("与当前会话模型同 id 时借用显示名，写成与输入坞一致的短名", () => {
		expect(modelLabel("claude-sonnet-5", "anthropic", known)).toEqual({
			short: "Sonnet 5",
			full: "Claude Sonnet 5 (anthropic/claude-sonnet-5)",
		});
		// model_change 条目是 provider/id 路径
		expect(modelLabel("anthropic/claude-sonnet-5", undefined, known)).toEqual({
			short: "Sonnet 5",
			full: "Claude Sonnet 5 (anthropic/claude-sonnet-5)",
		});
	});

	it("不认识的模型按 id 去掉系列名，悬停给完整路径", () => {
		expect(modelLabel("anthropic/claude-opus-5-5", undefined, known)).toEqual({
			short: "opus-5-5",
			full: "anthropic/claude-opus-5-5",
		});
		expect(modelLabel("my-local-model")).toEqual({
			short: "my-local-model",
			full: "my-local-model",
		});
	});

	it("空引用返回 null", () => {
		expect(modelLabel("")).toBeNull();
		expect(modelLabel("/")).toBeNull();
	});
});
