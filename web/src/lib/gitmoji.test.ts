import { describe, expect, it } from "vitest";
import { emojify, parseCommitSubject } from "./gitmoji";

describe("parseCommitSubject", () => {
	it.each([
		// Conventional Commits 前缀之后的 emoji：拆出来，前缀保留
		[
			"chore(assets): 📈 更新 star history 图表",
			{ glyph: "📈", text: "chore(assets): 更新 star history 图表" },
		],
		// 短码形式
		[
			"feat(mcp)!: :sparkles: 支持 :bug: 修复",
			{ glyph: "✨", text: "feat(mcp)!: 支持 🐛 修复" },
		],
		// 开头就是 gitmoji；带变体选择符与 ZWJ 序列
		["⚡️ 提速", { glyph: "⚡️", text: "提速" }],
		["🧑‍💻 开发体验", { glyph: "🧑‍💻", text: "开发体验" }],
		// 没有 gitmoji、不认识的短码、emoji 不在开头
		["lib: greet world", { text: "lib: greet world" }],
		[":unknown: x", { text: ":unknown: x" }],
		["fix: 修好 🐛", { text: "fix: 修好 🐛" }],
	])("%s", (subject, want) => {
		expect(parseCommitSubject(subject)).toEqual(want);
	});
});

describe("emojify", () => {
	it("只换已知短码，时间等冒号文本不动", () => {
		expect(emojify("10:30 :rocket: :nope:")).toBe("10:30 🚀 :nope:");
	});
});
