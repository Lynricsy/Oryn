import { describe, expect, it } from "vitest";
import { ArrivalLog, ENTER_WINDOW } from "./entrance";
import { nextShown } from "./smooth-text";

const rows = (...keys: string[]) => keys.map((key) => ({ key }));
const all = () => true;

describe("ArrivalLog：只有追加在末尾的新行入场", () => {
	it("首批行与未启用时到达的行不入场；之后追加的行在有效期内入场", () => {
		const log = new ArrivalLog<{ key: string }>();
		log.note(rows("a", "b"), false, all, 0);
		expect(log.entering("a", 0)).toBe(false);
		log.note(rows("a", "b", "c"), false, all, 10);
		expect(log.entering("c", 10)).toBe(false);
		log.note(rows("a", "b", "c", "d"), true, all, 20);
		expect(log.entering("d", 20)).toBe(true);
		expect(log.entering("d", 20 + ENTER_WINDOW)).toBe(false);
	});

	it("加载更早（插在已知行之前）、一次并入过多与交接行都不入场", () => {
		const log = new ArrivalLog<{ key: string }>();
		log.note(rows("c", "d"), true, all, 0);
		log.note(rows("a", "b", "c", "d"), true, all, 1);
		expect(log.entering("a", 1)).toBe(false);
		log.note(rows("a", "b", "c", "d", "e", "f", "g", "h", "i"), true, all, 2);
		expect(log.entering("e", 2)).toBe(false);
		log.note(
			rows("a", "b", "c", "d", "e", "f", "g", "h", "i", "j"),
			true,
			(r) => r.key !== "j",
			3,
		);
		expect(log.entering("j", 3)).toBe(false);
	});
});

describe("nextShown：流式正文的显现长度", () => {
	it("不切开代理对，且有限步内追平", () => {
		const text = `${"字".repeat(10)}😀尾`;
		let shown = 0;
		let steps = 0;
		while (shown < text.length) {
			shown = nextShown(text, shown, 16);
			const code = text.charCodeAt(shown - 1);
			expect(code >= 0xd800 && code <= 0xdbff).toBe(false);
			steps++;
		}
		expect(shown).toBe(text.length);
		expect(steps).toBeLessThan(30);
	});
});
