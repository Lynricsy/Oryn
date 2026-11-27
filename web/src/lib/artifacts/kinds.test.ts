import { describe, expect, it } from "vitest";
import zh from "../../i18n/locales/zh-CN.json";
import {
	ARTIFACT_KINDS,
	isArtifactKind,
	KIND_ICON,
	kindIcon,
	kindLabelKey,
	typeBadge,
} from "./kinds";

describe("artifact 类型", () => {
	it("五种类型各有图标与中文名", () => {
		const kinds = zh.artifacts.kinds as Record<string, string>;
		expect(ARTIFACT_KINDS).toEqual([
			"document",
			"slides",
			"sheet",
			"web",
			"canvas",
		]);
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
			sheet: "表格",
		});
		expect(kindIcon("sheet")).toBe("table");
	});

	it("不认识的类型按页面处理", () => {
		expect(isArtifactKind("pdf")).toBe(false);
		expect(isArtifactKind(1)).toBe(false);
		expect(kindLabelKey("pdf")).toBe("artifacts.kinds.web");
		expect(kindIcon("pdf")).toBe("browsers");
		expect(kindIcon("slides")).toBe("presentation");
	});

	it("Office 类型有徽标，其余没有", () => {
		expect(typeBadge("docx")).toBe("Word");
		expect(typeBadge("docm")).toBe("Word · 宏");
		expect(typeBadge("xlsx")).toBe("Excel");
		expect(typeBadge("xlsm")).toBe("Excel · 宏");
		expect(typeBadge("pptx")).toBe("PowerPoint");
		expect(typeBadge("pptm")).toBe("PowerPoint · 宏");
		expect(typeBadge("pdf")).toBe("PDF");
		expect(typeBadge("document")).toBeNull();
		expect(typeBadge("slides")).toBeNull();
		expect(typeBadge("constructor")).toBeNull();
		expect(typeBadge(undefined)).toBeNull();
	});
});
