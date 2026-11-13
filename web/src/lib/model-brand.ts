/**
 * 模型名 → 厂商品牌识别（给 ModelLogo 选 logo 用）。
 *
 * 先认模型本身（id 或显示名），认不出再看路径里的厂商段与 provider 名。
 * openrouter、bedrock 这类聚合商只托管别人的模型，不作为品牌返回。
 */

export const BRANDS = [
	"anthropic",
	"openai",
	"gemini",
	"xai",
	"deepseek",
	"qwen",
	"moonshot",
	"zhipu",
	"mistral",
	"meta",
	"minimax",
	"cohere",
	"aws",
	"doubao",
	"wenxin",
	"hunyuan",
	"stepfun",
	"zeroone",
	"microsoft",
	"nvidia",
	"ibm",
	"perplexity",
	"baichuan",
	"internlm",
	"ai21",
	"xiaomi",
	"longcat",
	"inclusionai",
	"liquid",
	"exa",
] as const;

export type Brand = (typeof BRANDS)[number];

// 词首边界：前面不是字母（数字、分隔符、开头都算），避免 "lingo" 里认出 ling 这类误判
const W = "(?:^|[^a-z])";

/** 模型 id / 显示名里的系列名。按在字符串中最早出现的位置取胜（`deepseek-r1-distill-qwen` → deepseek）。 */
const MODEL_RULES: [RegExp, Brand][] = [
	[new RegExp(`${W}claude`), "anthropic"],
	[
		new RegExp(
			`${W}(?:chatgpt|gpt|codex|o[1345](?![a-z0-9])|davinci|dall-?e|whisper|sora)`,
		),
		"openai",
	],
	[new RegExp(`${W}(?:gemini|gemma|learnlm|palm)`), "gemini"],
	[new RegExp(`${W}grok`), "xai"],
	[new RegExp(`${W}deepseek`), "deepseek"],
	[new RegExp(`${W}(?:qwen|qwq|qvq)`), "qwen"],
	[new RegExp(`${W}(?:kimi|moonshot)`), "moonshot"],
	[new RegExp(`${W}(?:chatglm|glm|codegeex)`), "zhipu"],
	[
		new RegExp(
			`${W}(?:mistral|mixtral|codestral|devstral|magistral|ministral|pixtral|voxtral)`,
		),
		"mistral",
	],
	[new RegExp(`${W}llama`), "meta"],
	[new RegExp(`${W}(?:minimax|abab)`), "minimax"],
	[new RegExp(`${W}(?:command(?![a-z])|aya(?![a-z])|c4ai)`), "cohere"],
	[new RegExp(`${W}(?:nova(?![a-z])|titan(?![a-z]))`), "aws"],
	[new RegExp(`${W}(?:doubao|skylark)`), "doubao"],
	[new RegExp(`${W}ernie`), "wenxin"],
	[new RegExp(`${W}hunyuan`), "hunyuan"],
	// step 太常见，要求后面紧跟版本号：step-2、step3
	[new RegExp(`${W}step-?\\d`), "stepfun"],
	[new RegExp(`${W}yi(?![a-z])`), "zeroone"],
	[new RegExp(`${W}phi(?![a-z])`), "microsoft"],
	[new RegExp(`${W}nemotron`), "nvidia"],
	[new RegExp(`${W}granite`), "ibm"],
	[new RegExp(`${W}(?:sonar|pplx)`), "perplexity"],
	[new RegExp(`${W}baichuan`), "baichuan"],
	[new RegExp(`${W}(?:internlm|intern-s)`), "internlm"],
	[new RegExp(`${W}(?:jamba|jurassic)`), "ai21"],
	[new RegExp(`${W}mimo(?![a-z])`), "xiaomi"],
	[new RegExp(`${W}longcat`), "longcat"],
	[new RegExp(`${W}(?:ling|ring)-`), "inclusionai"],
	[new RegExp(`${W}lfm`), "liquid"],
	[new RegExp(`${W}exa(?![a-z])`), "exa"],
];

/** 厂商名（provider 名、`厂商/模型` 的前缀段）。聚合商不在表里，认不出返回 null。 */
const VENDOR_RULES: [RegExp, Brand][] = [
	[/anthropic|claude/, "anthropic"],
	[/openai|chatgpt/, "openai"],
	[/^google(?!-vertex)|gemini/, "gemini"],
	[/^x-?ai(?![a-z])|grok/, "xai"],
	[/deepseek/, "deepseek"],
	[/qwen|alibaba|dashscope|bailian/, "qwen"],
	[/moonshot|kimi/, "moonshot"],
	[/zhipu|^z-?ai(?![a-z])|bigmodel|thudm/, "zhipu"],
	[/mistral/, "mistral"],
	[/^meta(?![a-z])|meta-llama/, "meta"],
	[/minimax/, "minimax"],
	[/cohere/, "cohere"],
	// 只认 `amazon/nova-x` 这种厂商段；amazon-bedrock 是聚合商
	[/^amazon$|^nova(?![a-z])/, "aws"],
	[/doubao|bytedance|volcengine|^ark(?![a-z])/, "doubao"],
	[/baidu|wenxin|qianfan/, "wenxin"],
	[/hunyuan|tencent/, "hunyuan"],
	[/stepfun/, "stepfun"],
	[/^01-?ai|lingyiwanwu/, "zeroone"],
	[/^microsoft/, "microsoft"],
	[/^nvidia/, "nvidia"],
	[/^ibm/, "ibm"],
	[/perplexity/, "perplexity"],
	[/baichuan/, "baichuan"],
	[/internlm|shanghai-ai-lab/, "internlm"],
	[/ai21/, "ai21"],
	[/xiaomi/, "xiaomi"],
	[/meituan|longcat/, "longcat"],
	[/inclusionai|antgroup|^ant-ling/, "inclusionai"],
	[/liquid/, "liquid"],
	[/^exa(?![a-z])/, "exa"],
];

function byModel(name: string): Brand | null {
	let best: Brand | null = null;
	let at = Number.POSITIVE_INFINITY;
	for (const [re, brand] of MODEL_RULES) {
		const m = re.exec(name);
		if (m && m.index < at) {
			at = m.index;
			best = brand;
		}
	}
	return best;
}

function byVendor(name: string): Brand | null {
	for (const [re, brand] of VENDOR_RULES) if (re.test(name)) return brand;
	return null;
}

/**
 * 识别模型所属品牌。`model` 可以是 id（`claude-sonnet-5`）、显示名（`Claude Sonnet 5`）
 * 或带路径的形式（`openrouter/anthropic/claude-x:thinking`）；大小写与 `:后缀` 不影响结果。
 */
export function modelBrand(model?: string, provider?: string): Brand | null {
	if (model) {
		// 倒序：先是模型 id，后面依次是更靠前的厂商段
		const [id, ...vendors] = model
			.trim()
			.toLowerCase()
			.split("/")
			.map((s) => s.replace(/:.*$/, ""))
			.filter(Boolean)
			.reverse();
		const hit = id ? byModel(id) : null;
		if (hit) return hit;
		// 模型本身认不出：从右往左看厂商段（`openrouter/qwen/foo` → qwen）
		for (const seg of vendors) {
			const v = byVendor(seg);
			if (v) return v;
		}
	}
	return provider ? byVendor(provider.trim().toLowerCase()) : null;
}

/**
 * 各品牌模型显示名开头的系列名：输入框里已有品牌 logo，这个词就不必再写一遍。
 * 只收「系列名 = 品牌招牌」的词；gemma、qwq、codestral、o3 这类子系列名本身是信息，不在表里。
 */
const FAMILY_PREFIX: Partial<Record<Brand, RegExp>> = {
	anthropic: /^claude/i,
	openai: /^(?:chatgpt|gpt)/i,
	gemini: /^gemini/i,
	xai: /^grok/i,
	deepseek: /^deepseek/i,
	qwen: /^qwen/i,
	moonshot: /^kimi/i,
	zhipu: /^(?:chatglm|glm)/i,
	mistral: /^mistral/i,
	meta: /^llama/i,
	minimax: /^minimax/i,
	doubao: /^doubao/i,
	wenxin: /^ernie/i,
	hunyuan: /^hunyuan/i,
	baichuan: /^baichuan/i,
	longcat: /^longcat/i,
};

/**
 * 去掉显示名开头的品牌系列名：`Claude Opus 5.5` → `Opus 5.5`，`gpt-6.1-sol` → `6.1-sol`。
 * `brand` 是 `modelBrand` 的结果；为 null（不显示品牌 logo）时原样返回。
 * 系列名后面必须是空格、`-`、`_` 或数字（`Qwen3` → `3`），连同分隔符一起去掉；去完为空或只剩标点时保留原名。
 */
export function shortModelName(name: string, brand: Brand | null): string {
	const re = brand ? FAMILY_PREFIX[brand] : undefined;
	const m = re?.exec(name.trim());
	if (!m) return name;
	const rest = name.trim().slice(m[0].length);
	if (!/^(?:[\s_-]|\d)/.test(rest)) return name;
	const short = rest.replace(/^[\s_-]+/, "");
	return /^[\p{P}\p{S}\s]*$/u.test(short) ? name : short;
}
