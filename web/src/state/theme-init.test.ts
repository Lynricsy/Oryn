import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { afterEach, describe, expect, it } from "vitest";

/** `public/theme-init.js` 是首帧前的经典脚本，这里直接执行它的源码，检查只靠它写出的 `<html>` 属性。 */
const source = readFileSync(
	resolve(__dirname, "../../public/theme-init.js"),
	"utf8",
);
const runInit = () => new Function(source)();

describe("theme-init.js 首帧属性", () => {
	afterEach(() => {
		localStorage.clear();
		const root = document.documentElement;
		delete root.dataset.theme;
		delete root.dataset.accent;
		delete root.dataset.wallpaper;
	});

	it.each(["aurora", "waves", "grain"])(
		"显式深色 + %s 壁纸时首帧就是深色",
		(wallpaper) => {
			localStorage.setItem(
				"oryn.theme",
				JSON.stringify({ mode: "dark", accent: "fox", wallpaper }),
			);
			runInit();
			expect(document.documentElement.dataset).toMatchObject({
				theme: "dark",
				accent: "fox",
				wallpaper,
			});
		},
	);

	it("选了自定义壁纸但本机没有图片时留在极光，明暗不受影响", () => {
		localStorage.setItem(
			"oryn.theme",
			JSON.stringify({ mode: "dark", wallpaper: "custom" }),
		);
		runInit();
		expect(document.documentElement.dataset).toMatchObject({
			theme: "dark",
			wallpaper: "aurora",
		});
	});
});
