import { describe, expect, it } from "vitest";
import {
	parseFrontmatter,
	parseFrontmatterBlock,
	splitFrontmatter,
	stringifyFrontmatter,
} from "./frontmatter";

/** Core `assembleRuleMarkdown` 的真实形状：字符串都经 JSON.stringify。 */
const RULE = [
	"---",
	"name: no-ack-filler-replies",
	'description: "不用「收到」「等待指示」「ok」这类应答套话，只给要点"',
	'condition: ["收到", "等待指示", "^ok\\\\s*$"]',
	'scope: "text"',
	"---",
	"",
	"只给要点：",
	"- 不写套话",
].join("\n");

describe("splitFrontmatter", () => {
	it("拆出 Core 生成的规则：中文、JSON 引号串、行内数组里的正则反斜杠还原", () => {
		const doc = splitFrontmatter(RULE);
		expect(doc?.fields).toEqual([
			{ key: "name", value: "no-ack-filler-replies" },
			{
				key: "description",
				value: "不用「收到」「等待指示」「ok」这类应答套话，只给要点",
			},
			{ key: "condition", value: ["收到", "等待指示", "^ok\\s*$"] },
			{ key: "scope", value: "text" },
		]);
		expect(doc?.body).toBe("只给要点：\n- 不写套话");
	});

	it("块式数组、单引号串、块标量、纯标量续行、注释", () => {
		const doc = splitFrontmatter(
			[
				"---",
				"# 注释行",
				"name: demo # 行尾注释",
				"tools:",
				"  - read",
				"  - 'it''s'",
				'  - "a, b"',
				"description: >-",
				"  folded",
				"  line",
				"notes: |",
				"  keep",
				"  lines",
				"summary: first",
				"  second",
				"version: 1.10",
				"empty:",
				"---",
				"body",
			].join("\n"),
		);
		expect(doc?.fields).toEqual([
			{ key: "name", value: "demo" },
			{ key: "tools", value: ["read", "it's", "a, b"] },
			{ key: "description", value: "folded line" },
			{ key: "notes", value: "keep\nlines\n" },
			{ key: "summary", value: "first second" },
			{ key: "version", value: "1.10" },
			{ key: "empty", value: "" },
		]);
		expect(doc?.body).toBe("body");
	});

	it("行内数组：纯标量项、空数组、引号里的逗号与右括号", () => {
		expect(parseFrontmatterBlock("a: [x, y z, \"q]r\", 's,t']\nb: []")).toEqual(
			[
				{ key: "a", value: ["x", "y z", "q]r", "s,t"] },
				{ key: "b", value: [] },
			],
		);
	});

	it('双引号转义：\\uXXXX、\\n、\\"', () => {
		expect(parseFrontmatterBlock('a: "\\u4e2d\\n\\"x\\""')).toEqual([
			{ key: "a", value: '中\n"x"' },
		]);
	});

	it("只认文档首行的 frontmatter：正文里的 --- 分隔线不算", () => {
		expect(splitFrontmatter("Intro\n\n---\nname: x\n---\nmore")).toBeNull();
		expect(splitFrontmatter(" ---\nname: x\n---")).toBeNull();
	});

	it("首行是分隔线但块里不是 key: value（普通文章）→ 不算", () => {
		expect(
			splitFrontmatter("---\n\n**标题**\n\n一段话\n\n---\n\n正文"),
		).toBeNull();
	});

	it("没有闭合行 → 不算（流式中途也走这条）", () => {
		expect(splitFrontmatter("---\nname: x\ndescription: y")).toBeNull();
	});

	it("不支持的结构整段回落：嵌套映射、行内映射、数组项里的映射、锚点、坏转义、未闭合引号", () => {
		for (const block of [
			"meta:\n  author: x",
			"meta: {a: 1}",
			"items:\n  - name: x",
			"items:\n  - - x",
			"a: &anchor x",
			'a: "\\q"',
			'a: "open',
			"a: [x, [y]]",
		]) {
			expect(parseFrontmatterBlock(block), block).toBeNull();
		}
	});

	it("非法码点的转义（超出 U+10FFFF、代理区）回落，不抛异常", () => {
		for (const block of [
			'name: "\\U00110000"',
			'name: "\\UFFFFFFFF"',
			'name: "\\uD800"',
		]) {
			expect(() => parseFrontmatterBlock(block), block).not.toThrow();
			expect(parseFrontmatterBlock(block), block).toBeNull();
		}
		// 合法的最大码点照常解码
		expect(parseFrontmatterBlock('name: "\\U0010FFFF"')).toEqual([
			{ key: "name", value: "\u{10FFFF}" },
		]);
	});

	it("CRLF 与 BOM", () => {
		const doc = splitFrontmatter("\uFEFF---\r\nname: x\r\n---\r\n\r\nbody");
		expect(doc?.fields).toEqual([{ key: "name", value: "x" }]);
		expect(doc?.body).toBe("body");
	});
});

const SKILL = [
	"---",
	"name: review",
	"description: 代码评审：先读 diff，再给结论",
	"tools:",
	"  - read",
	"  - grep",
	"blocking: true",
	"---",
	"",
	"# 评审",
	"",
	"正文",
	"",
].join("\n");

describe("parseFrontmatter / stringifyFrontmatter", () => {
	it("按完整 YAML 取值", () => {
		expect(parseFrontmatter(SKILL)).toEqual({
			data: {
				name: "review",
				description: "代码评审：先读 diff，再给结论",
				tools: ["read", "grep"],
				blocking: true,
			},
			body: "# 评审\n\n正文\n",
		});
	});

	it("文本 → 解析 → 生成 往返一致", () => {
		const { data, body } = parseFrontmatter(SKILL);
		expect(stringifyFrontmatter(data, body)).toBe(SKILL);
	});

	it("数据 → 生成 → 解析 往返一致：嵌套、多行、特殊字符、长串不折行", () => {
		const data = {
			name: "x",
			description: "a: b # 不是注释 ".repeat(20).trim(),
			model: ["@smol", "openai/gpt-5-mini:high"],
			meta: { n: 1, flag: false, note: "第一行\n第二行\n" },
			empty: "",
			yes: "true",
		};
		const body = "正文\n\n---\n\n分隔线之后";
		const text = stringifyFrontmatter(data, body);
		expect(text.split("---\n")[1]).not.toMatch(/\n\s+不是注释/);
		expect(parseFrontmatter(text)).toEqual({ data, body });
	});

	it("undefined 的键略去；没有字段时只输出正文", () => {
		expect(stringifyFrontmatter({ a: "x", b: undefined }, "")).toBe(
			"---\na: x\n---\n",
		);
		expect(stringifyFrontmatter({ a: undefined }, "正文")).toBe("正文");
		expect(parseFrontmatter("正文")).toEqual({ data: {}, body: "正文" });
	});

	it("CRLF 与 BOM 统一", () => {
		expect(parseFrontmatter("\uFEFF---\r\nname: x\r\n---\r\nbody")).toEqual({
			data: { name: "x" },
			body: "body",
		});
	});

	it("坏 frontmatter 给出 error，正文保留整份原文", () => {
		for (const text of [
			"---\nname: x\n",
			"---\nname: [x\n---\nbody",
			"---\n- a\n- b\n---\nbody",
		]) {
			const parsed = parseFrontmatter(text);
			expect(parsed.error, text).toBeTruthy();
			expect(parsed.data).toEqual({});
			expect(parsed.body).toBe(text);
		}
	});
});
