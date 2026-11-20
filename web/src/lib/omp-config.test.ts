import { describe, expect, it } from "vitest";
import type { Document } from "yaml";
import {
	composerPredictionSettings,
	contextFileKey,
	extensionModuleKey,
	hookKey,
	hookKeyFromPath,
	instructionKey,
	promptKey,
	readConfigDoc,
	ruleKey,
	setAgentModelOverride,
	setComposerPredictionSetting,
	setCycleOrder,
	setDisabledAgent,
	setModelRole,
	setModelTagName,
	skillKey,
	slashCommandKey,
	toggleDisabled,
	toolKey,
	writeConfigDoc,
} from "./omp-config";

const CONFIG = [
	"# Oryn 渲染的 omp 配置",
	"",
	"# 角色模型",
	"modelRoles:",
	"  default: anthropic/claude-opus-4 # 主力",
	'  smol: "openai/gpt-5-mini"',
	"",
	"# 已停用的扩展",
	"disabledExtensions:",
	"  - skill:old # 过时了",
	"  - rule:noisy",
	"",
	"task:",
	"  # 不想用的子 agent",
	"  disabledAgents: [explore]",
	"",
	"theme: dark # 主题",
	"",
].join("\n");

function edit(text: string, fn: (doc: Document) => void) {
	const doc = readConfigDoc(text);
	fn(doc);
	return writeConfigDoc(doc);
}

describe("readConfigDoc 编辑保留注释", () => {
	it("改一个角色模型：只有那一行变，其余逐字节不变", () => {
		const out = edit(CONFIG, (doc) =>
			setModelRole(doc, "default", "anthropic/claude-sonnet-4"),
		);
		expect(out).toBe(
			CONFIG.replace(
				"default: anthropic/claude-opus-4 # 主力",
				"default: anthropic/claude-sonnet-4 # 主力",
			),
		);
	});

	it("覆盖标量时保留原来的引号风格", () => {
		const out = edit(CONFIG, (doc) => setModelRole(doc, "smol", "openai/x"));
		expect(out).toBe(CONFIG.replace('"openai/gpt-5-mini"', '"openai/x"'));
	});

	it("新增键追加在映射末尾，其他行不变", () => {
		const out = edit(CONFIG, (doc) =>
			setModelRole(doc, "slow", "anthropic/claude-opus-4:high"),
		);
		expect(out).toBe(
			CONFIG.replace(
				'  smol: "openai/gpt-5-mini"\n',
				'  smol: "openai/gpt-5-mini"\n  slow: anthropic/claude-opus-4:high\n',
			),
		);
	});

	it("删除键：null 删除，映射变空时连同父键一起删，无关注释保留", () => {
		let out = edit(CONFIG, (doc) => setModelRole(doc, "smol", null));
		expect(out).toBe(CONFIG.replace('  smol: "openai/gpt-5-mini"\n', ""));
		out = edit(out, (doc) => setModelRole(doc, "default", null));
		// 文件头注释（与键之间隔着空行）不随 modelRoles 一起删
		expect(out).toBe(
			CONFIG.replace(
				'# 角色模型\nmodelRoles:\n  default: anthropic/claude-opus-4 # 主力\n  smol: "openai/gpt-5-mini"\n\n',
				"",
			),
		);
	});

	it("删除中间的键：上方以空行隔开的分组注释留给下一个键", () => {
		const text = "a: 1\n\n# 分组：模型\n\n# 角色\nmodelRoles:\n  x: y\n";
		expect(
			edit(`${text}\ntheme: dark\n`, (doc) => setModelRole(doc, "x", null)),
		).toBe("a: 1\n\n# 分组：模型\n\ntheme: dark\n");
		expect(edit(text, (doc) => setModelRole(doc, "x", null))).toBe(
			"a: 1\n\n# 分组：模型\n",
		);
		expect(
			edit("# 头\n\n# 角色\nmodelRoles:\n  x: y\n", (doc) =>
				setModelRole(doc, "x", null),
			),
		).toBe("# 头\n\n{}\n");
	});

	it("删除不存在的键不改动文档", () => {
		const out = edit(CONFIG, (doc) => {
			setModelRole(doc, "nope", null);
			setModelTagName(doc, "nope", null);
			setAgentModelOverride(doc, "nope", null);
		});
		expect(out).toBe(CONFIG);
	});

	it("空文档与只有注释的文档也能写入", () => {
		expect(edit("", (doc) => setModelRole(doc, "default", "a/b"))).toBe(
			"modelRoles:\n  default: a/b\n",
		);
		const out = edit("# 只有注释\n", (doc) =>
			toggleDisabled(doc, skillKey("x"), false),
		);
		expect(out).toContain("# 只有注释");
		expect(readConfigDoc(out).toJS()).toEqual({
			disabledExtensions: ["skill:x"],
		});
	});

	it("路径中间不是映射时换成映射（同 Core setByPath）", () => {
		const out = edit("modelRoles:\ntask: 1\n", (doc) => {
			setModelRole(doc, "default", "a/b");
			setAgentModelOverride(doc, "explore", "c/d");
		});
		expect(readConfigDoc(out).toJS()).toEqual({
			modelRoles: { default: "a/b" },
			task: { agentModelOverrides: { explore: "c/d" } },
		});
	});
});

describe("toggleDisabled", () => {
	it("停用追加到末尾、重复调用幂等", () => {
		const doc = readConfigDoc(CONFIG);
		expect(toggleDisabled(doc, "skill:new", false)).toBe(true);
		const once = writeConfigDoc(doc);
		expect(toggleDisabled(doc, "skill:new", false)).toBe(false);
		expect(writeConfigDoc(doc)).toBe(once);
		expect(readConfigDoc(once).toJS().disabledExtensions).toEqual([
			"skill:old",
			"rule:noisy",
			"skill:new",
		]);
		expect(once).toContain("- skill:old # 过时了");
	});

	it("启用移除条目、重复调用幂等；列表空了删除键", () => {
		const doc = readConfigDoc(CONFIG);
		expect(toggleDisabled(doc, "rule:noisy", true)).toBe(true);
		expect(toggleDisabled(doc, "rule:noisy", true)).toBe(false);
		expect(
			readConfigDoc(writeConfigDoc(doc)).toJS().disabledExtensions,
		).toEqual(["skill:old"]);
		expect(toggleDisabled(doc, "skill:old", true)).toBe(true);
		const out = writeConfigDoc(doc);
		expect(out).not.toContain("disabledExtensions");
		expect(out).toContain("theme: dark # 主题");
	});

	it("启用不在列表里的条目不改动", () => {
		const out = edit(CONFIG, (doc) => {
			expect(toggleDisabled(doc, "skill:absent", true)).toBe(false);
		});
		expect(out).toBe(CONFIG);
	});
});

describe("setDisabledAgent", () => {
	it("行内列表保持行内风格，追加不重复；移除到空时连 task 一起删", () => {
		const doc = readConfigDoc(CONFIG);
		expect(setDisabledAgent(doc, "plan", true)).toBe(true);
		expect(setDisabledAgent(doc, "plan", true)).toBe(false);
		expect(writeConfigDoc(doc)).toContain("disabledAgents: [explore, plan]");
		setDisabledAgent(doc, "explore", false);
		setDisabledAgent(doc, "plan", false);
		const out = writeConfigDoc(doc);
		expect(out).not.toContain("task:");
		expect(out).not.toContain("disabledAgents");
	});
});

describe("其他设置项", () => {
	it("setCycleOrder 整体替换并沿用块式 / 行内风格；null 删除", () => {
		const block = "cycleOrder:\n  - smol\n  - default\n";
		expect(edit(block, (doc) => setCycleOrder(doc, ["default", "slow"]))).toBe(
			"cycleOrder:\n  - default\n  - slow\n",
		);
		expect(
			edit("cycleOrder: [smol]\n", (doc) => setCycleOrder(doc, ["a", "b"])),
		).toBe("cycleOrder: [a, b]\n");
		expect(edit(block, (doc) => setCycleOrder(doc, null))).toBe("{}\n");
	});

	it("setModelTagName 写 modelTags.<role>.name；null 删除，空标签一并删除", () => {
		const text = "modelTags:\n  smol:\n    name: 快\n    color: green\n";
		let out = edit(text, (doc) => setModelTagName(doc, "smol", "轻量"));
		expect(out).toBe("modelTags:\n  smol:\n    name: 轻量\n    color: green\n");
		out = edit(out, (doc) => setModelTagName(doc, "slow", "深思"));
		expect(readConfigDoc(out).toJS().modelTags.slow).toEqual({ name: "深思" });
		out = edit(out, (doc) => setModelTagName(doc, "slow", null));
		expect(out).toBe("modelTags:\n  smol:\n    name: 轻量\n    color: green\n");
		out = edit("modelTags:\n  x:\n    name: y\n", (doc) =>
			setModelTagName(doc, "x", null),
		);
		expect(out).toBe("{}\n");
	});

	it("setAgentModelOverride 支持单个与多个选择器", () => {
		let out = edit("", (doc) =>
			setAgentModelOverride(doc, "explore", "anthropic/claude-haiku-4"),
		);
		expect(out).toBe(
			"task:\n  agentModelOverrides:\n    explore: anthropic/claude-haiku-4\n",
		);
		out = edit(out, (doc) =>
			setAgentModelOverride(doc, "explore", ["@smol", "openai/gpt-5-mini"]),
		);
		expect(readConfigDoc(out).toJS()).toEqual({
			task: {
				agentModelOverrides: { explore: ["@smol", "openai/gpt-5-mini"] },
			},
		});
		out = edit(out, (doc) => setAgentModelOverride(doc, "explore", null));
		expect(out).toBe("{}\n");
	});

	it("输入框预测按 Core 默认值读写：predictions 缺省为开，关闭写 false；alternatives 缺省为关", () => {
		expect(composerPredictionSettings(readConfigDoc(""))).toEqual({
			predictions: true,
			alternatives: false,
		});
		let out = edit("", (doc) =>
			setComposerPredictionSetting(doc, "predictions", false),
		);
		expect(out).toBe("composer:\n  predictions: false\n");
		expect(composerPredictionSettings(readConfigDoc(out)).predictions).toBe(
			false,
		);
		out = edit(out, (doc) =>
			setComposerPredictionSetting(doc, "predictions", true),
		);
		expect(out).toBe("{}\n");
		out = edit(out, (doc) =>
			setComposerPredictionSetting(doc, "alternatives", true),
		);
		expect(readConfigDoc(out).toJS()).toEqual({
			composer: { predictionAlternatives: true },
		});
	});

	it('输入框预测：字符串 "false" 不是布尔值，Core 按默认开启，这里也显示开启；关闭时写入真正的 false', () => {
		const raw = 'composer:\n  predictions: "false"\n';
		expect(composerPredictionSettings(readConfigDoc(raw)).predictions).toBe(
			true,
		);
		const out = edit(raw, (doc) =>
			setComposerPredictionSetting(doc, "predictions", false),
		);
		expect(readConfigDoc(out).toJS()).toEqual({
			composer: { predictions: false },
		});
	});
});

describe("启停键", () => {
	it("与 Core toExtensionId 格式一致", () => {
		expect(skillKey("x")).toBe("skill:x");
		expect(hookKey("pre", "bash", "bash.ts")).toBe("hook:pre:bash:bash.ts");
		expect(extensionModuleKey("m")).toBe("extension-module:m");
		expect(toolKey("t")).toBe("tool:t");
		expect(ruleKey("r")).toBe("rule:r");
		expect(promptKey("p")).toBe("prompt:p");
		expect(instructionKey("i")).toBe("instruction:i");
		expect(slashCommandKey("c")).toBe("slash-command:c");
		expect(contextFileKey("user", "AGENTS.md")).toBe(
			"context-file:user:AGENTS.md",
		);
	});

	it("hookKeyFromPath：name 是完整文件名，tool 去掉最后一个扩展名", () => {
		expect(hookKeyFromPath("hooks/pre/bash.ts")).toBe("hook:pre:bash:bash.ts");
		expect(hookKeyFromPath("hooks/post/edit.sh")).toBe(
			"hook:post:edit:edit.sh",
		);
		expect(hookKeyFromPath("hooks/pre/*.ts")).toBe("hook:pre:*:*.ts");
		expect(hookKeyFromPath("hooks/pre/a.b.ts")).toBe("hook:pre:a.b:a.b.ts");
		expect(hookKeyFromPath("hooks/pre/guard")).toBe("hook:pre:guard:guard");
	});

	it("hookKeyFromPath：不是 hook 文件时返回 undefined", () => {
		for (const path of [
			"hooks/pre/.hidden.ts",
			"hooks/pre/sub/x.ts",
			"hooks/mid/x.ts",
			"hooks/pre",
			"hooks/pre/",
			"skills/x/SKILL.md",
			"other/hooks/pre/x.ts",
		]) {
			expect(hookKeyFromPath(path), path).toBeUndefined();
		}
	});
});
