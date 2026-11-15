import { describe, expect, it } from "vitest";
import i18n from "../i18n";
import { coreLabelKey, OMFG_SAVE_TITLE } from "./core-labels";

describe("coreLabelKey：Core 固定文案 → i18n 键", () => {
	it("钉住 C2 字面量：新 Core 的 Oryn 文案与旧 Core / runner 的上游原文都有中文", () => {
		expect(OMFG_SAVE_TITLE).toBe("Save TTSR rule where?");
		const zh = (text: string) => {
			const key = coreLabelKey(text);
			return key ? i18n.t(key, { lng: "zh-CN" }) : undefined;
		};
		expect(zh("Save TTSR rule where?")).toBe("把这条规则保存到哪里？");
		expect(zh("This project (Oryn project config)")).toBe(
			"本项目（Oryn 项目配置）",
		);
		expect(zh("Global — all projects (Oryn central config)")).toBe(
			"全局（Oryn 中央配置）",
		);
		expect(zh("Amend with feedback…")).toBe("修改反馈…");
		expect(zh("This project (.omp/rules)")).toBe("本项目（仓库 .omp/rules）");
		expect(zh("Global — all projects (~/.omp/agent/rules)")).toBe(
			"全局（~/.omp/agent/rules）",
		);
	});

	it("不在表里的原样（返回 undefined），不认原型链上的键", () => {
		expect(coreLabelKey("Approve")).toBeUndefined();
		expect(coreLabelKey("Amend with feedback...")).toBeUndefined();
		expect(coreLabelKey("toString")).toBeUndefined();
		expect(coreLabelKey("")).toBeUndefined();
	});
});
