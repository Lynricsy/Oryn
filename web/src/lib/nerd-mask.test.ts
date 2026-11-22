import { describe, expect, it } from "vitest";
import { glyphTile } from "./nerd-mask";

const COLORS = { bg: "#f6dcc6", fg: "#7a3b00" };

describe("glyphTile", () => {
	it("Nerd 字形只交模板图，没画好时不交字形（原生会画成方框）", () => {
		expect(glyphTile("\uf0ad", "iVBORw0K", COLORS)).toEqual({
			mask: "iVBORw0K",
			...COLORS,
		});
		expect(
			glyphTile(String.fromCodePoint(0xf0093), null, COLORS),
		).toBeUndefined();
	});
	it("emoji 与首字母照旧交字形", () => {
		expect(glyphTile("🧪", null, COLORS)).toEqual({ glyph: "🧪", ...COLORS });
		expect(glyphTile("O", "ignored", COLORS)).toEqual({
			glyph: "O",
			...COLORS,
		});
	});
});
