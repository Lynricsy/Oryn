import { describe, expect, it } from "vitest";
import { modelLabel } from "./model-label";

describe("modelLabel", () => {
	const known = {
		id: "claude-sonnet-5",
		name: "Claude Sonnet 5",
		provider: "anthropic",
	};

	it("与当前会话模型同 id 时借用显示名，写成与输入坞一致的短名", () => {
		// 助手消息：model 是原始模型 id，provider 单独给出
		expect(
			modelLabel({ id: "claude-sonnet-5", provider: "anthropic" }, known),
		).toEqual({
			short: "Sonnet 5",
			full: "Claude Sonnet 5 (anthropic/claude-sonnet-5)",
		});
	});

	it("不认识的模型按 id 去掉系列名，悬停给完整路径", () => {
		expect(
			modelLabel({ id: "claude-opus-5-5", provider: "anthropic" }, known),
		).toEqual({
			short: "opus-5-5",
			full: "anthropic/claude-opus-5-5",
		});
		expect(modelLabel({ id: "my-local-model" })).toEqual({
			short: "my-local-model",
			full: "my-local-model",
		});
	});

	it("模型 id 自带命名空间时不当作 provider 丢弃", () => {
		// 助手消息：provider 单独给出，整个 model 字段就是模型 id
		expect(
			modelLabel({ id: "qwen/qwen3-max", provider: "openrouter" })?.full,
		).toBe("openrouter/qwen/qwen3-max");
		// 用完整 id 匹配当前会话模型，借到显示名
		const qwen = {
			id: "qwen/qwen3-max",
			name: "Qwen3 Max",
			provider: "openrouter",
		};
		const expected = {
			short: "3 Max",
			full: "Qwen3 Max (openrouter/qwen/qwen3-max)",
		};
		expect(
			modelLabel({ id: "qwen/qwen3-max", provider: "openrouter" }, qwen),
		).toEqual(expected);
	});

	it("空引用返回 null", () => {
		expect(modelLabel({ id: "" })).toBeNull();
	});
});
