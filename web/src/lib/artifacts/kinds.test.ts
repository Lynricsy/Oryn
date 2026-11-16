import { describe, expect, it } from "vitest";
import zh from "../../i18n/locales/zh-CN.json";
import {
	ARTIFACT_KINDS,
	isArtifactKind,
	KIND_ICON,
	kindIcon,
	kindLabelKey,
} from "./kinds";

describe("artifact 类型", () => {
	it("四种类型各有图标与中文名", () => {
		const kinds = zh.artifacts.kinds as Record<string, string>;
		expect([...ARTIFACT_KINDS].sort()).toEqual(Object.keys(KIND_ICON).sort());
		for (const k of ARTIFACT_KINDS) {
			expect(kindLabelKey(k)).toBe(`artifacts.kinds.${k}`);
			expect(kinds[k]).toBeTruthy();
		}
		expect(kinds).toMatchObject({
			web: "页面",
			canvas: "画布",
			document: "文档",
			slides: "幻灯片",
		});
	});

	it("不认识的类型按页面处理", () => {
		expect(isArtifactKind("pdf")).toBe(false);
		expect(isArtifactKind(1)).toBe(false);
		expect(kindLabelKey("pdf")).toBe("artifacts.kinds.web");
		expect(kindIcon("pdf")).toBe("browsers");
		expect(kindIcon("slides")).toBe("presentation");
	});
});
