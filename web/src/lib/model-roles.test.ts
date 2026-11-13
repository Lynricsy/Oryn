import { describe, expect, it } from "vitest";
import { ACCENTS } from "../state/theme";
import {
	ACCENT_HUE,
	buildRoleSlots,
	type CurrentModel,
	currentRoleIndex,
	DEFAULT_CYCLE_ORDER,
	hashHue,
	lastModelChangeRole,
	type ModelRef,
	parseCycleOrder,
	parseThinkingLevel,
	ROLE_PALETTE,
	type RoleEntry,
	resolveRoleValue,
	roleHue,
	rolePalette,
	slotSelector,
	splitThinkingSuffix,
	stepRole,
} from "./model-roles";

/**
 * 夹具取自主人真实配置（~/.omp/agent/config.yml）经 oryn-core 18.19.0 `get_model_roles` / `get_settings` 回读的结果，
 * 只把 candidates 裁到相关的几个（真实数据里同一个 id 还挂在 deepseek 等转发 provider 下，保留一条用来验证 provider 精确匹配）。
 */
const CANDIDATES: ModelRef[] = [
	{ provider: "anthropic", id: "claude-fable-5-2", name: "Claude Fable 5.2" },
	{ provider: "anthropic", id: "claude-opus-5-5", name: "Claude Opus 5.5" },
	{ provider: "anthropic", id: "claude-sonnet-5-5", name: "Claude Sonnet 5.5" },
	{ provider: "deepseek", id: "claude-opus-5-5", name: "claude-opus-5-5" },
	{
		provider: "deepseek",
		id: "deepseek-flash",
		name: "DeepSeek V4.1 Flash 1017",
	},
	{ provider: "google", id: "gemini-4-carbon", name: "Gemini 4 Carbon" },
	{ provider: "openai", id: "gpt-6.1-sol", name: "GPT-6.1 Sol" },
];

const role = (
	id: string,
	name: string,
	model?: string,
	selected?: RoleEntry["selected"],
): RoleEntry => ({
	id,
	name,
	section: "chat",
	model,
	selected,
	candidates: CANDIDATES,
});

const REAL_ROLES: RoleEntry[] = [
	// `@Cyrene` 别名：omp 不给 selected
	role("default", "Default", "@Cyrene"),
	role("smol", "Fast", "anthropic/claude-sonnet-5-5:medium", {
		provider: "anthropic",
		id: "claude-sonnet-5-5",
		thinkingLevel: "medium",
	}),
	role("slow", "Thinking", "openai/gpt-6.1-sol:high", {
		provider: "openai",
		id: "gpt-6.1-sol",
		thinkingLevel: "high",
	}),
	role("commit", "Commit", "openai/gpt-6.1-sol:medium", {
		provider: "openai",
		id: "gpt-6.1-sol",
		thinkingLevel: "medium",
	}),
	role("Hyacinthia", "Hyacinthia", "anthropic/claude-fable-5-2:high", {
		provider: "anthropic",
		id: "claude-fable-5-2",
		thinkingLevel: "high",
	}),
	role("Nahida", "Nahida", "google/gemini-4-carbon:high", {
		provider: "google",
		id: "gemini-4-carbon",
		thinkingLevel: "high",
	}),
	role("Mare", "Mare", "openai/gpt-6.1-sol:high", {
		provider: "openai",
		id: "gpt-6.1-sol",
		thinkingLevel: "high",
	}),
	role("Cyrene", "Cyrene", "anthropic/claude-opus-5-5:high", {
		provider: "anthropic",
		id: "claude-opus-5-5",
		thinkingLevel: "high",
	}),
	role("Arianna", "Arianna", "deepseek/deepseek-flash:max", {
		provider: "deepseek",
		id: "deepseek-flash",
		thinkingLevel: "max",
	}),
];
const REAL_ORDER = ["Hyacinthia", "Nahida", "Mare", "Cyrene", "Arianna"];

const OPUS_HIGH: CurrentModel = {
	provider: "anthropic",
	id: "claude-opus-5-5",
	thinkingLevel: "high",
};
const SOL_HIGH: CurrentModel = {
	provider: "openai",
	id: "gpt-6.1-sol",
	thinkingLevel: "high",
};

const rolesMap = (list: RoleEntry[]) => new Map(list.map((r) => [r.id, r]));

describe("选择器解析", () => {
	it("拆 :level 后缀：完整名、无歧义缩写、max/auto；非法后缀不拆", () => {
		expect(splitThinkingSuffix("openai/gpt-6.1-sol:xhigh")).toEqual({
			base: "openai/gpt-6.1-sol",
			level: "xhigh",
		});
		expect(splitThinkingSuffix("x:max").level).toBe("max");
		expect(splitThinkingSuffix("x:auto").level).toBe("auto");
		expect(parseThinkingLevel("med")).toBe("medium");
		expect(parseThinkingLevel("m")).toBeUndefined();
		expect(splitThinkingSuffix("openrouter/x:exacto")).toEqual({
			base: "openrouter/x:exacto",
		});
	});

	it("带 :level 的选择器（omp 没给 selected 时）解析出模型与显式级别", () => {
		const roles = rolesMap(REAL_ROLES);
		expect(
			resolveRoleValue("openai/gpt-6.1-sol:xhigh", roles, CANDIDATES),
		).toEqual({
			model: CANDIDATES[6],
			thinkingLevel: "xhigh",
		});
		// 裸 id、名字都能匹配，大小写不敏感
		expect(
			resolveRoleValue("DEEPSEEK-FLASH:max", roles, CANDIDATES).thinkingLevel,
		).toBe("max");
		expect(
			resolveRoleValue("gemini 4 carbon", roles, CANDIDATES).model?.id,
		).toBe("gemini-4-carbon");
		// provider 精确：deepseek/claude-opus-5-5 不会被认成 anthropic 的
		expect(
			resolveRoleValue("deepseek/claude-opus-5-5", roles, CANDIDATES).model
				?.provider,
		).toBe("deepseek");
		// 没写级别：沿用当前级别
		expect(
			resolveRoleValue("google/gemini-4-carbon", roles, CANDIDATES)
				.thinkingLevel,
		).toBeUndefined();
	});

	it("fallback chain 取第一个能解析的", () => {
		const r = resolveRoleValue(
			"nope/missing:high, google/gemini-4-carbon:low, openai/gpt-6.1-sol",
			rolesMap(REAL_ROLES),
			CANDIDATES,
		);
		expect(r).toEqual({ model: CANDIDATES[5], thinkingLevel: "low" });
	});

	it("@ 引用：default: @Cyrene 解析到 Cyrene 的模型与级别；链式引用；别名上的级别覆盖", () => {
		const roles = rolesMap([
			...REAL_ROLES,
			role("a", "a", "@b"),
			role("b", "b", "pi/Cyrene"),
		]);
		expect(resolveRoleValue("@Cyrene", roles, CANDIDATES)).toEqual({
			model: CANDIDATES[1],
			thinkingLevel: "high",
		});
		expect(resolveRoleValue("@a", roles, CANDIDATES).model?.id).toBe(
			"claude-opus-5-5",
		);
		expect(
			resolveRoleValue("@Cyrene:low", roles, CANDIDATES).thinkingLevel,
		).toBe("low");
		// `*` 是 default 的别名
		expect(resolveRoleValue("*", roles, CANDIDATES).model?.id).toBe(
			"claude-opus-5-5",
		);
	});

	it("@ 引用成环时不死循环、解析失败", () => {
		const roles = rolesMap([role("x", "x", "@y"), role("y", "y", "@x")]);
		expect(resolveRoleValue("@x", roles, CANDIDATES, "x")).toEqual({});
	});

	it("没配置的内置角色按 omp 的回退：tiny → smol、smol/slow → default", () => {
		const roles = rolesMap([
			role("default", "Default", "google/gemini-4-carbon"),
			role("smol", "Fast", "openai/gpt-6.1-sol:low"),
			role("tiny", "Tiny"),
			role("slow", "Thinking"),
		]);
		expect(resolveRoleValue("@tiny", roles, CANDIDATES)).toEqual({
			model: CANDIDATES[6],
			thinkingLevel: "low",
		});
		expect(resolveRoleValue("@slow", roles, CANDIDATES).model?.id).toBe(
			"gemini-4-carbon",
		);
	});
});

describe("buildRoleSlots", () => {
	it("按 cycleOrder 排列，自定义角色原样保留名字，selected 的级别一起带上", () => {
		const slots = buildRoleSlots(REAL_ORDER, REAL_ROLES, [], OPUS_HIGH);
		expect(slots.map((s) => [s.id, s.name, s.status, slotSelector(s)])).toEqual(
			[
				["Hyacinthia", "Hyacinthia", "ok", "anthropic/claude-fable-5-2:high"],
				["Nahida", "Nahida", "ok", "google/gemini-4-carbon:high"],
				["Mare", "Mare", "ok", "openai/gpt-6.1-sol:high"],
				["Cyrene", "Cyrene", "ok", "anthropic/claude-opus-5-5:high"],
				["Arianna", "Arianna", "ok", "deepseek/deepseek-flash:max"],
			],
		);
		expect(slots[3]?.model?.name).toBe("Claude Opus 5.5");
	});

	it("没有 selected 的 default（@Cyrene）照 omp 规则解析，不被丢掉", () => {
		const [def] = buildRoleSlots(["default"], REAL_ROLES, [], SOL_HIGH);
		expect(def).toMatchObject({
			id: "default",
			status: "ok",
			raw: "@Cyrene",
			model: { provider: "anthropic", id: "claude-opus-5-5" },
			thinkingLevel: "high",
		});
	});

	it("default 没配置时用当前模型（同 omp）", () => {
		const [def] = buildRoleSlots(
			["default"],
			[role("default", "Default")],
			[],
			SOL_HIGH,
		);
		expect(def).toMatchObject({
			status: "ok",
			model: { provider: "openai", id: "gpt-6.1-sol" },
		});
		expect(def?.thinkingLevel).toBeUndefined();
	});

	it("解析不出来的保留为禁用项，位置不变；可用模型列表也参与匹配", () => {
		const slots = buildRoleSlots(
			["smol", "ghost", "lost", "extra"],
			[
				role("smol", "Fast", "anthropic/claude-sonnet-5-5:medium"),
				role("lost", "lost", "nope/unknown:high, @nobody"),
				{ id: "extra", name: "extra", model: "kimi-k3:high" },
			],
			[{ provider: "moonshot", id: "kimi-k3", name: "Kimi K3" }],
			SOL_HIGH,
		);
		expect(slots.map((s) => [s.id, s.status])).toEqual([
			["smol", "ok"],
			["ghost", "unconfigured"],
			["lost", "unresolved"],
			["extra", "ok"],
		]);
		expect(slots[2]?.raw).toBe("nope/unknown:high, @nobody");
		expect(slots[3]?.model?.provider).toBe("moonshot");
	});
});

describe("当前角色", () => {
	const slots = buildRoleSlots(
		["slow", ...REAL_ORDER, "commit"],
		REAL_ROLES,
		[],
		SOL_HIGH,
	);
	const idOf = (i: number) => slots[i]?.id;

	it("(a) 本地记忆的模型与级别仍是当前状态时优先（网页 set_model 让 omp 记成 default 也不影响）", () => {
		const i = currentRoleIndex(
			slots,
			SOL_HIGH,
			{
				role: "Mare",
				provider: "openai",
				id: "gpt-6.1-sol",
				thinkingLevel: "high",
			},
			"default",
		);
		expect(idOf(i)).toBe("Mare");
	});

	it("记忆过期（级别变了）就不再信它", () => {
		const cur = { ...SOL_HIGH, thinkingLevel: "medium" };
		const i = currentRoleIndex(slots, cur, {
			role: "Mare",
			provider: "openai",
			id: "gpt-6.1-sol",
			thinkingLevel: "high",
		});
		// (c) 模型 + 级别：commit 是 medium
		expect(idOf(i)).toBe("commit");
	});

	it("(b) 最后一条 model_change 的 role，且它的模型仍是当前模型", () => {
		expect(idOf(currentRoleIndex(slots, SOL_HIGH, undefined, "Mare"))).toBe(
			"Mare",
		);
		// role 的模型已不是当前模型：不采用
		expect(idOf(currentRoleIndex(slots, SOL_HIGH, undefined, "Cyrene"))).toBe(
			"slow",
		);
	});

	it("(c) 模型 + 级别都一致的取 cycleOrder 里最先出现的（slow 与 Mare 完全相同时取 slow）", () => {
		expect(idOf(currentRoleIndex(slots, SOL_HIGH))).toBe("slow");
	});

	it("(d) 都不命中返回 -1，轮换从头开始", () => {
		const cur = { provider: "x", id: "y" };
		expect(currentRoleIndex(slots, cur)).toBe(-1);
		expect(idOf(stepRole(slots, -1, 1) ?? -1)).toBe(idOf(1));
	});

	it("真机回读：网页选了 Mare 后 omp 记下 role=default，仍认出 Mare，Ctrl+P 下一个是 Cyrene", () => {
		const cycle = buildRoleSlots(REAL_ORDER, REAL_ROLES, [], SOL_HIGH);
		const entries = [
			{
				type: "model_change",
				payload: { model: "openai/gpt-6.1-sol", role: "default" },
			},
		];
		const i = currentRoleIndex(
			cycle,
			SOL_HIGH,
			{
				role: "Mare",
				provider: "openai",
				id: "gpt-6.1-sol",
				thinkingLevel: "high",
			},
			lastModelChangeRole(entries),
		);
		expect(cycle[i]?.id).toBe("Mare");
		expect(cycle[stepRole(cycle, i, 1) ?? -1]?.id).toBe("Cyrene");
		// 选了 Cyrene 之后回读：不是 default
		const j = currentRoleIndex(
			cycle,
			OPUS_HIGH,
			{
				role: "Cyrene",
				provider: "anthropic",
				id: "claude-opus-5-5",
				thinkingLevel: "high",
			},
			"default",
		);
		expect(cycle[j]?.id).toBe("Cyrene");
	});

	it("没有本地记忆时（换了浏览器），model_change 的 default 不可信：对话框里打勾在 Cyrene 而不是 default", () => {
		const list = buildRoleSlots(
			[...REAL_ORDER, "default"],
			REAL_ROLES,
			[],
			OPUS_HIGH,
		);
		const i = currentRoleIndex(list, OPUS_HIGH, undefined, "default");
		expect(list[i]?.id).toBe("Cyrene");
	});
});

describe("stepRole", () => {
	const slots = buildRoleSlots(
		["a", "b", "c", "d"],
		[
			role("a", "a", "openai/gpt-6.1-sol"),
			role("b", "b", "nope/none"),
			role("c", "c", "google/gemini-4-carbon"),
			role("d", "d", "anthropic/claude-opus-5-5"),
		],
		[],
		undefined,
	);

	it("正反向取模，跳过禁用格", () => {
		expect(stepRole(slots, 0, 1)).toBe(2);
		expect(stepRole(slots, 3, 1)).toBe(0);
		expect(stepRole(slots, 0, -1)).toBe(3);
		expect(stepRole(slots, 2, -1)).toBe(0);
	});

	it("可用格少于 2 个时返回 undefined", () => {
		expect(stepRole(slots.slice(0, 2), 0, 1)).toBeUndefined();
	});
});

describe("其余", () => {
	it("cycleOrder 不是字符串数组时用默认值", () => {
		expect(parseCycleOrder(["Mare", 3, ""])).toEqual(["Mare"]);
		expect(parseCycleOrder(undefined)).toEqual([...DEFAULT_CYCLE_ORDER]);
		expect(parseCycleOrder([])).toEqual([...DEFAULT_CYCLE_ORDER]);
	});

	it("最后一条 model_change 的 role；空 role 视为没有", () => {
		expect(
			lastModelChangeRole([
				{ type: "model_change", payload: { role: "smol" } },
				{ type: "message", payload: {} },
			]),
		).toBe("smol");
		expect(
			lastModelChangeRole([
				{ type: "model_change", payload: { role: "smol" } },
				{ type: "model_change", payload: {} },
			]),
		).toBeUndefined();
	});

	it("色相：轨道按位置取色板（8 色循环），其余内置固定、未知按名字哈希且稳定", () => {
		expect(roleHue("Mare", 2)).toBe("var(--role-h-2)");
		expect(roleHue("Mare", 9)).toBe("var(--role-h-1)");
		expect(roleHue("smol")).toBe("var(--role-h-smol)");
		expect(roleHue("Hyacinthia")).toBe(String(hashHue("Hyacinthia")));
		expect(roleHue("Hyacinthia")).toBe(roleHue("Hyacinthia"));
		expect(roleHue("constructor")).toBe(String(hashHue("constructor")));
	});

	it("色相：轨道色板跳过和当前强调色相差 ≤25° 的色相，石墨不跳", () => {
		// 青碧 205：跳过 180（差 25）与 225（差 20），可用序号 0 1 3 5 6 7
		expect(rolePalette("teal")).toEqual([0, 1, 3, 5, 6, 7]);
		expect(roleHue("Mare", 2, "teal")).toBe("var(--role-h-3)");
		expect(roleHue("Mare", 6, "teal")).toBe("var(--role-h-0)");
		expect(rolePalette("cobalt")).not.toContain(0);
		expect(rolePalette("violet")).toEqual([1, 2, 4, 5, 6, 7]);
		expect(rolePalette("berry")).not.toContain(6);
		expect(rolePalette("fox")).not.toContain(1);
		expect(rolePalette("graphite")).toEqual([0, 1, 2, 3, 4, 5, 6, 7]);
		for (const accent of ACCENTS) {
			const ah = ACCENT_HUE[accent];
			if (ah === undefined) continue;
			for (const i of rolePalette(accent)) {
				const d = Math.abs((ROLE_PALETTE[i] ?? 0) - ah) % 360;
				expect(Math.min(d, 360 - d)).toBeGreaterThan(25);
			}
		}
	});
});
