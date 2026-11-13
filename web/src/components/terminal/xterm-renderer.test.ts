/**
 * 终端渲染器守卫（M6 计划 6.4）：xterm 6 只剩 DOM 与 WebGL 两种渲染器，不加载插件即默认 DOM。
 * 桌面壳在 Linux（WebKitGTK）上必须用 DOM 渲染器，发布给壳的 Web 产物里也不能带 WebGL / canvas 插件。
 */
import { describe, expect, it } from "vitest";
import pkg from "../../../package.json";
import factorySource from "./xterm.ts?raw";

const FORBIDDEN = ["@xterm/addon-webgl", "@xterm/addon-canvas"];

describe("xterm 渲染器", () => {
	it("package.json 不依赖 WebGL / canvas 渲染器插件", () => {
		const deps = {
			...pkg.dependencies,
			...pkg.devDependencies,
		} as Record<string, string>;
		for (const name of FORBIDDEN) expect(deps).not.toHaveProperty(name);
	});

	it("终端工厂不导入 WebGL / canvas 渲染器插件", () => {
		expect(factorySource).not.toMatch(/addon-webgl|addon-canvas/);
	});
});
