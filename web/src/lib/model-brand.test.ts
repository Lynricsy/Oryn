import { describe, expect, it } from "vitest";
import { modelBrand } from "./model-brand";

describe("modelBrand", () => {
	it.each([
		["claude-sonnet-5", "anthropic"],
		["Claude Opus 5", "anthropic"],
		["gpt-5.5", "openai"],
		["GPT-4o", "openai"],
		["o3-mini", "openai"],
		["o1", "openai"],
		["o4-mini-high", "openai"],
		["gpt-5-codex", "openai"],
		["codex-mini-latest", "openai"],
		["chatgpt-4o-latest", "openai"],
		["gemini-2.5-pro", "gemini"],
		["gemma-3-27b-it", "gemini"],
		["grok-4", "xai"],
		["deepseek-v4", "deepseek"],
		["deepseek-r1-distill-qwen-32b", "deepseek"],
		["qwen3-coder-plus", "qwen"],
		["qwq-32b", "qwen"],
		["kimi-k2-thinking", "moonshot"],
		["moonshot-v1-128k", "moonshot"],
		["glm-4.6", "zhipu"],
		["mistral-large-latest", "mistral"],
		["codestral-2508", "mistral"],
		["devstral-medium", "mistral"],
		["magistral-small", "mistral"],
		["llama-4-maverick", "meta"],
		["MiniMax-M2", "minimax"],
		["abab6.5s-chat", "minimax"],
		["command-r-plus", "cohere"],
		["nova-pro-v1", "aws"],
		["doubao-seed-1.6", "doubao"],
		["ernie-4.5-turbo", "wenxin"],
		["hunyuan-t1", "hunyuan"],
		["step-2-16k", "stepfun"],
		["step3", "stepfun"],
		["yi-large", "zeroone"],
		["phi-4", "microsoft"],
		["nemotron-nano-9b", "nvidia"],
		["granite-3.3-8b", "ibm"],
		["sonar-pro", "perplexity"],
		["baichuan4", "baichuan"],
		["jamba-large", "ai21"],
		["mimo-v2-flash", "xiaomi"],
		["longcat-flash-chat", "longcat"],
		["ling-1t", "inclusionai"],
		["lfm-40b", "liquid"],
	])("%s → %s", (model, brand) => {
		expect(modelBrand(model)).toBe(brand);
	});

	it.each([
		["anthropic/claude-opus-5-5", undefined, "anthropic"],
		["anthropic/claude-sonnet-5-5", undefined, "anthropic"],
		["anthropic/claude-fable-5-2", undefined, "anthropic"],
		["claude-opus-5-5:high", "anthropic", "anthropic"],
		["openai/gpt-6.1-sol", undefined, "openai"],
		["gpt-6-astra", "openai-codex", "openai"],
		["openai-codex/gpt-6-astra", undefined, "openai"],
		["openai/gpt-image-2.6-sunburst", undefined, "openai"],
		["google/gemini-4-carbon", undefined, "gemini"],
		["google/gemini-3.8-flash:low", undefined, "gemini"],
		["deepseek/deepseek-flash", undefined, "deepseek"],
		["exa", "web", "exa"],
		["web/exa", undefined, "exa"],
	])("主人在用的模型 %s（provider %s）→ %s", (model, provider, brand) => {
		expect(modelBrand(model, provider)).toBe(brand);
	});

	it("处理 provider/id、聚合商前缀、:后缀与大小写", () => {
		expect(modelBrand("openrouter/anthropic/claude-x")).toBe("anthropic");
		expect(modelBrand("anthropic/claude-sonnet-5:thinking")).toBe("anthropic");
		expect(modelBrand("OpenRouter/X-AI/Grok-4:Free")).toBe("xai");
		expect(modelBrand("claude-x", "openrouter")).toBe("anthropic");
		expect(modelBrand("meta-llama/llama-3.3-70b-instruct", "groq")).toBe(
			"meta",
		);
	});

	it("模型名认不出时看厂商段与 provider", () => {
		expect(modelBrand("openrouter/qwen/some-new-model")).toBe("qwen");
		expect(modelBrand("mystery-1", "anthropic")).toBe("anthropic");
		expect(modelBrand("mystery-1", "google")).toBe("gemini");
		expect(modelBrand(undefined, "xai")).toBe("xai");
		expect(modelBrand("", "openai-codex")).toBe("openai");
	});

	it("聚合商与未知模型回退 null", () => {
		expect(modelBrand("openrouter/auto")).toBeNull();
		expect(modelBrand("mystery-1", "openrouter")).toBeNull();
		expect(modelBrand("mystery-1", "amazon-bedrock")).toBeNull();
		expect(modelBrand("typesafe/jev-latest")).toBeNull();
		expect(modelBrand("jev-latest", "typesafe")).toBeNull();
		expect(modelBrand("web/unknown-tool", "web")).toBeNull();
		expect(modelBrand("my-local-model")).toBeNull();
		expect(modelBrand()).toBeNull();
		// 系列名必须在词首：这些不该被误认
		expect(modelBrand("bigstep-model")).toBeNull();
		expect(modelBrand("sapphire")).toBeNull();
	});
});
