import { describe, expect, it } from "vitest";
import i18n from "../i18n";
import { draftFileBase, draftTitle, splitDrafts } from "./draft-blocks";

describe("splitDrafts", () => {
	it("不含 draft- 时整段原样返回（快路径）", () => {
		const text = "普通回复\n\n```ts\nconst a = 1;\n```";
		expect(splitDrafts(text)).toEqual([{ type: "markdown", text }]);
	});

	it("拆出闭合的草稿与前后正文，属性解析", () => {
		const text =
			'好的：\n\n```draft-email subject="周五会议改期" to="sam@example.com" foo="x"\nHi Sam,\n\nThanks\n```\n\n要改再说。';
		expect(splitDrafts(text)).toEqual([
			{ type: "markdown", text: "好的：\n" },
			{
				type: "draft",
				ordinal: 0,
				kind: "email",
				attrs: { subject: "周五会议改期", to: "sam@example.com" },
				body: "Hi Sam,\n\nThanks",
				closed: true,
			},
			{ type: "markdown", text: "\n要改再说。" },
		]);
	});

	it("未闭合的草稿延伸到文末（流式中）", () => {
		const segs = splitDrafts('```draft-doc title="周报"\n# 本周\n- 完成');
		expect(segs).toEqual([
			{
				type: "draft",
				ordinal: 0,
				kind: "doc",
				attrs: { title: "周报" },
				body: "# 本周\n- 完成",
				closed: false,
			},
		]);
	});

	it("四反引号围栏可包含三反引号，短于开围栏的不算闭合", () => {
		const segs = splitDrafts(
			"````draft-message\n用法：\n```\nnpm i\n```\n完毕\n````",
		);
		expect(segs).toHaveLength(1);
		expect(segs[0]).toMatchObject({
			type: "draft",
			kind: "message",
			body: "用法：\n```\nnpm i\n```\n完毕",
			closed: true,
		});
	});

	it("波浪号围栏，闭围栏须同一字符", () => {
		const segs = splitDrafts("~~~draft-message\n```\n还在里面\n~~~~  \n之后");
		expect(segs).toEqual([
			{
				type: "draft",
				ordinal: 0,
				kind: "message",
				attrs: {},
				body: "```\n还在里面",
				closed: true,
			},
			{ type: "markdown", text: "之后" },
		]);
	});

	it("嵌在其它围栏里的 draft 开围栏不识别", () => {
		const text =
			"示例：\n\n````md\n```draft-email\nHi\n```\n````\n\n以上是写法。";
		expect(splitDrafts(text)).toEqual([{ type: "markdown", text }]);
	});

	it("同一段两个草稿按顺序编号，纯空白正文段丢弃", () => {
		const segs = splitDrafts(
			"```draft-message\n甲\n```\n\n```draft-message\n乙\n```\n",
		);
		expect(segs.map((s) => (s.type === "draft" ? s.ordinal : -1))).toEqual([
			0, 1,
		]);
		expect(segs.map((s) => (s.type === "draft" ? s.body : ""))).toEqual([
			"甲",
			"乙",
		]);
	});

	it("CRLF 归一，属性 trim 并截到 200 字符，空值忽略", () => {
		const long = "长".repeat(250);
		const segs = splitDrafts(
			`\`\`\`draft-doc title="  ${long}  " subject="   "\r\nA\r\nB\r\n\`\`\``,
		);
		expect(segs[0]).toMatchObject({
			type: "draft",
			attrs: { title: "长".repeat(200) },
			body: "A\nB",
			closed: true,
		});
	});

	it("开围栏缩进时正文去掉同样多的前导空格", () => {
		const segs = splitDrafts("  ```draft-message\n  甲\n    乙\n  ```");
		expect(segs[0]).toMatchObject({ body: "甲\n  乙", closed: true });
	});

	it("未知类型按普通围栏处理", () => {
		const text = "```draft-tweet\nhi\n```";
		expect(splitDrafts(text)).toEqual([{ type: "markdown", text }]);
	});
});

describe("draftTitle / draftFileBase", () => {
	it("标题依次取 title、subject、类型名", () => {
		const t = i18n.getFixedT("zh-CN");
		expect(draftTitle("doc", { title: "周报", subject: "x" }, t)).toBe("周报");
		expect(draftTitle("email", { subject: "改期" }, t)).toBe("改期");
		expect(draftTitle("email", {}, t)).toBe("邮件草稿");
	});

	it("文件名去掉保留字符与控制字符，截断，空则 draft", () => {
		expect(draftFileBase('a/b\\c:d*e?f"g<h>i|j\u0001k')).toBe(
			"a_b_c_d_e_f_g_h_i_j_k",
		);
		expect(draftFileBase("  ")).toBe("draft");
		expect(draftFileBase("x".repeat(100))).toHaveLength(80);
	});
});
