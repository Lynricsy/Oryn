import { describe, expect, it } from "vitest";
import { detectLocale, resources } from "./index";

function flattenKeys(value: unknown, prefix = ""): string[] {
	if (value === null || typeof value !== "object") return [prefix];
	return Object.entries(value).flatMap(([key, child]) =>
		flattenKeys(child, prefix ? `${prefix}.${key}` : key),
	);
}

describe("i18n 资源", () => {
	it("zh-CN 与 en 的 key 集合完全一致", () => {
		const zh = flattenKeys(resources["zh-CN"].translation).sort();
		const en = flattenKeys(resources.en.translation).sort();
		expect(en).toEqual(zh);
	});

	it("按系统语言选择界面语言并回退到 zh-CN", () => {
		expect(detectLocale(["en-US", "zh-CN"])).toBe("en");
		expect(detectLocale(["zh-TW"])).toBe("zh-CN");
		expect(detectLocale(["fr-FR", "en-GB"])).toBe("en");
		expect(detectLocale(["fr-FR"])).toBe("zh-CN");
		expect(detectLocale([])).toBe("zh-CN");
	});
});
