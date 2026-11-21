/** `.env` 解析（M24 阶段 ④ 批量添加）：注释、export、引号与转义、行内注释、各类不可导入的行与重名。 */
import { describe, expect, it } from "vitest";
import { type DotenvIssue, parseDotenv } from "./dotenv";

describe("parseDotenv", () => {
	it.each([
		["FOO=bar", "FOO", "bar"],
		["export TOKEN='a b'", "TOKEN", "a b"],
		['X="l1\\nl2" # c', "X", "l1\nl2"],
		['Q="say \\"hi\\" \\\\ \\t"', "Q", 'say "hi" \\ \\t'],
		["URL=a#b", "URL", "a#b"],
		["URL=a #b", "URL", "a"],
		["  SPACED =  v  ", "SPACED", "v"],
		["S='#not a comment'", "S", "#not a comment"],
	])("%j → %s=%j", (text, name, value) => {
		expect(parseDotenv(text)).toEqual({
			entries: [{ line: 1, name, value }],
			issues: [],
		});
	});

	it("跳过空行与注释，行号从 1 起算", () => {
		expect(parseDotenv("# c\n\nFOO=1").entries).toEqual([
			{ line: 3, name: "FOO", value: "1" },
		]);
		expect(parseDotenv("A=1\r\nB=2").entries.map((e) => e.name)).toEqual([
			"A",
			"B",
		]);
	});

	it.each<[string, DotenvIssue]>([
		["foo=1", { line: 1, reason: "badName" }],
		["1A=1", { line: 1, reason: "badName" }],
		["NOEQ", { line: 1, reason: "noEquals" }],
		['A="x', { line: 1, reason: "unterminatedQuote" }],
		["A='x", { line: 1, reason: "unterminatedQuote" }],
		['A="x" y', { line: 1, reason: "trailingText" }],
		["A=", { line: 1, reason: "emptyValue" }],
		["A= #c", { line: 1, reason: "emptyValue" }],
		['A=""', { line: 1, reason: "emptyValue" }],
	])("%j 不导入", (text, issue) => {
		expect(parseDotenv(text)).toEqual({ entries: [], issues: [issue] });
	});

	it("重名以后者为准，前一行记 duplicate", () => {
		expect(parseDotenv("A=1\nA=2")).toEqual({
			entries: [{ line: 2, name: "A", value: "2" }],
			issues: [{ line: 1, reason: "duplicate", other: 2 }],
		});
	});

	it("条目按行号排序，问题与条目分开", () => {
		const r = parseDotenv("B=1\nbad\nA=2");
		expect(r.entries.map((e) => e.name)).toEqual(["B", "A"]);
		expect(r.issues).toEqual([{ line: 2, reason: "noEquals" }]);
	});
});
