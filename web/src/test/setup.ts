import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, beforeAll } from "vitest";
import i18n from "../i18n";

// 测试统一用默认语言 zh-CN，不随 jsdom 的 navigator.language 变化
beforeAll(async () => {
	await i18n.changeLanguage("zh-CN");
});

afterEach(() => {
	cleanup();
});
