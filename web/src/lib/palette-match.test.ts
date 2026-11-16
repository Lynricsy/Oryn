import { describe, expect, it } from "vitest";
import {
	fold,
	matchFields,
	queryTerms,
	splitHighlights,
	type TextRange,
} from "./palette-match";

/** 取出原文里各高亮区间的文字。 */
function marked(text: string, ranges: readonly TextRange[]): string[] {
	return ranges.map((r) => text.slice(r.start, r.end));
}

describe("palette-match", () => {
	it("折叠大小写与变音符，并记下每个码元的原文来源", () => {
		const f = fold("Café ÅB");
		expect(f.text).toBe("cafe ab");
		// é（1 个码元）→ e；Å → a：长度不变，下标一一对应
		expect(f.starts).toEqual([0, 1, 2, 3, 4, 5, 6]);
		// 分解型输入（e + U+0301）折叠后少一个码元，来源区间覆盖两个原码元
		const d = fold("Cafe\u0301!");
		expect(d.text).toBe("cafe!");
		expect(d.starts[3]).toBe(3);
		expect(d.ends[3]).toBe(5);
		expect(d.starts[4]).toBe(5);
	});

	it("多词是 AND：每个词都要在某个字段里出现", () => {
		const fields = [fold("部署网关"), fold("Oryn"), fold("infra deploy")];
		expect(matchFields(queryTerms("网关 oryn"), fields)).not.toBeNull();
		expect(matchFields(queryTerms("网关 DEPLOY"), fields)).not.toBeNull();
		expect(matchFields(queryTerms("网关 missing"), fields)).toBeNull();
		// 没有词：全部命中、不带高亮
		expect(matchFields([], fields)?.ranges).toEqual([[], [], []]);
	});

	it("高亮映射回原文，变音符与码元长度变化都落在原字符上", () => {
		const title = "Le Cafe\u0301 Crème — café";
		const m = matchFields(queryTerms("CAFE creme"), [fold(title)]);
		expect(m).not.toBeNull();
		expect(marked(title, m?.ranges[0] ?? [])).toEqual([
			"Cafe\u0301",
			"Crème",
			"café",
		]);
		// 代理对（emoji）之后的下标仍正确
		const emoji = "😀 修复登录";
		const e = matchFields(queryTerms("登录"), [fold(emoji)]);
		expect(marked(emoji, e?.ranges[0] ?? [])).toEqual(["登录"]);
	});

	it("相邻与重叠的命中合并成一段", () => {
		const text = "abcabc";
		const m = matchFields(queryTerms("abc bca"), [fold(text)]);
		expect(m?.ranges[0]).toEqual([{ start: 0, end: 6 }]);
	});

	it("标题开头与词首得分高于中间与其它字段", () => {
		const score = (q: string, ...fields: string[]) =>
			matchFields(queryTerms(q), fields.map(fold))?.score ?? 0;
		expect(score("deploy", "deploy app")).toBeGreaterThan(
			score("deploy", "app deploy"),
		);
		expect(score("deploy", "app deploy")).toBeGreaterThan(
			score("deploy", "redeploy"),
		);
		expect(score("deploy", "redeploy")).toBeGreaterThan(
			score("deploy", "app", "deploy"),
		);
	});

	it("按区间切片段", () => {
		expect(
			splitHighlights("hello world", [
				{ start: 0, end: 2 },
				{ start: 6, end: 11 },
			]),
		).toEqual([
			{ text: "he", hit: true },
			{ text: "llo ", hit: false },
			{ text: "world", hit: true },
		]);
	});
});
