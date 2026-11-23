import { describe, expect, it } from "vitest";
import { loadGlyphCatalog, searchGlyphs } from "./glyphs";

describe("searchGlyphs（完整目录）", async () => {
	const catalog = await loadGlyphCatalog();
	const nerd = catalog.nerd.flatMap((s) => s.glyphs);
	const emoji = catalog.emoji.flatMap((s) => s.glyphs);

	it("名字以查询开头的排最前：rust → dev 的 rust，再是 language rust", () => {
		const labels = searchGlyphs(nerd, "rust").map((g) => g.label);
		expect(labels[0]).toBe("rust");
		expect(labels).toContain("language rust");
	});

	it("多个词都要命中，按序出现的缩写也算：lang ts → language typescript", () => {
		const labels = searchGlyphs(nerd, "lang ts").map((g) => g.label);
		expect(labels).toContain("language typescript");
		expect(labels).not.toContain("rust");
	});

	it("emoji 可按中文名与关键词搜", () => {
		expect(searchGlyphs(emoji, "狐狸")[0]?.char).toBe("🦊");
		expect(searchGlyphs(emoji, "程序员").map((g) => g.char)).toContain("👩‍💻");
		expect(searchGlyphs(emoji, "fox")[0]?.char).toBe("🦊");
	});

	it("没有命中返回空", () => {
		expect(searchGlyphs(nerd, "zzzqqq")).toEqual([]);
	});
});
