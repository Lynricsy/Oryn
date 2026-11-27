/**
 * 统一模型选择器（`components/model-picker/ModelPicker.tsx`）的纯逻辑：厂商显示名、筛选、按厂商分组、自由输入的解析。
 */
import { modelBrand, shortModelName } from "./model-brand";
import type { ModelRef } from "./model-roles";

/** `provider/id` 选择器。 */
export const modelSelector = (m: ModelRef): string => `${m.provider}/${m.id}`;

/** 模型的短名（logo 已说明品牌）；全名放悬停提示。 */
export function modelShortName(m: ModelRef): string {
	return shortModelName(m.name || m.id, modelBrand(m.id, m.provider));
}

/** 选择器里的一个模型：目录里的对话模型，或调用方给的候选（如记忆设置的可选模型）。 */
export interface PickerModel extends ModelRef {
	name: string;
	/** `provider/id` */
	selector: string;
	contextWindow?: number;
	reasoning?: boolean;
	/** 支持的思考级别（不含 auto）；省略 = 不能调节或未知 */
	thinking?: string[];
	input?: string[];
	/** 看得见、选不了的原因（如记忆设置里协议不兼容的模型） */
	unavailable?: string;
	/** 行尾与检视栏的附注（如记忆设置里的协议族） */
	note?: string;
}

/** 常见厂商的显示名；表外的按 `-`/`_` 分词首字母大写。 */
const PROVIDER_LABELS: Record<string, string> = {
	anthropic: "Anthropic",
	openai: "OpenAI",
	"openai-codex": "OpenAI Codex",
	"azure-openai": "Azure OpenAI",
	azure: "Azure",
	google: "Google",
	"google-gemini-cli": "Gemini CLI",
	"google-vertex": "Vertex AI",
	vertex: "Vertex AI",
	bedrock: "AWS Bedrock",
	"amazon-bedrock": "AWS Bedrock",
	deepseek: "DeepSeek",
	xai: "xAI",
	mistral: "Mistral",
	moonshot: "Moonshot",
	zhipu: "Zhipu AI",
	zai: "Z.ai",
	minimax: "MiniMax",
	openrouter: "OpenRouter",
	ollama: "Ollama",
	lmstudio: "LM Studio",
	groq: "Groq",
	cerebras: "Cerebras",
	together: "Together",
	fireworks: "Fireworks",
	"github-copilot": "GitHub Copilot",
	"vercel-ai-gateway": "Vercel AI Gateway",
};

export function providerLabel(provider: string): string {
	return (
		PROVIDER_LABELS[provider] ??
		provider
			.split(/[-_]/)
			.filter(Boolean)
			.map((w) => w.charAt(0).toUpperCase() + w.slice(1))
			.join(" ")
	);
}

/** 筛选词的每个空白分隔片段都要出现在选择器、显示名或厂商名里（不区分大小写）。 */
export function matchesQuery(haystack: string, query: string): boolean {
	const words = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
	if (words.length === 0) return true;
	const h = haystack.toLowerCase();
	return words.every((w) => h.includes(w));
}

export function modelHaystack(m: PickerModel): string {
	return `${m.selector} ${m.name} ${providerLabel(m.provider)}`;
}

export interface ProviderGroup {
	provider: string;
	models: PickerModel[];
}

/** 按厂商分组，组与组内模型都保持输入顺序（目录的顺序）。 */
export function groupByProvider(
	models: readonly PickerModel[],
): ProviderGroup[] {
	const groups = new Map<string, PickerModel[]>();
	for (const m of models) {
		const list = groups.get(m.provider);
		if (list) list.push(m);
		else groups.set(m.provider, [m]);
	}
	return [...groups].map(([provider, list]) => ({ provider, models: list }));
}

/** 输入框里的 `provider/id`（可带 `:级别`，级别不算进模型）；不是这个形状时为 undefined。 */
export function parseModelInput(value: string): ModelRef | undefined {
	const v = value.trim().replace(/:[^/]*$/, "");
	const slash = v.indexOf("/");
	if (slash <= 0 || slash === v.length - 1) return undefined;
	return { provider: v.slice(0, slash), id: v.slice(slash + 1) };
}
