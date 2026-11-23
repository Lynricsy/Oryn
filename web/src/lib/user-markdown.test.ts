import { describe, expect, it } from "vitest";
import { type UserMdSegment, userMarkdown } from "./user-markdown";

const joined = (segs: UserMdSegment[]) => segs.map((s) => s.text).join("");
/** 带某种样式的片段文字（相邻片段合并），便于断言。 */
const styled = (segs: UserMdSegment[], pick: (s: UserMdSegment) => boolean) =>
	segs.filter(pick).map((s) => s.text);

describe("userMarkdown", () => {
	it("片段按顺序拼起来就是原文，一个字符都不少", () => {
		const samples = [
			"",
			"\n\n  前后空行与缩进  \n\n",
			"## 标题 #\n正文 **粗** *斜* ~~删~~ `code` __下__ _斜_\n\n> 引用 **粗**\n>> 嵌套",
			"- a\n  * b\n1. c\n10) d\n---\n***",
			"```ts\nconst a = `x`;\n\n  缩进\n```\n后\n~~~\n未闭合围栏\n```",
			"[文字](https://x.test/a_(b)) ![图](blob:x) [坏](javascript:alert(1)) https://x.test/p?q=1。",
			"\\*不是斜体\\* snake_case_name a*b c ** 空 ** `` a`b `` ` 孤立",
			"命令：/jobs kill <id> & <script>alert(1)</script>\t制表\r\nCRLF",
			"中文**粗体**紧挨 _x_y_ 3*4*5",
		];
		for (const s of samples) expect(joined(userMarkdown(s))).toBe(s);
	});

	it("语法记号变淡但保留，正文按记号加样式", () => {
		const segs = userMarkdown("## 标题\n这是 **粗体**、`a<b>` 和 *斜体*");
		expect(styled(segs, (s) => !!s.style.marker)).toEqual([
			"## ",
			"**",
			"**",
			"`",
			"`",
			"*",
			"*",
		]);
		expect(styled(segs, (s) => !!s.style.heading)).toEqual(["标题"]);
		expect(styled(segs, (s) => !!s.style.strong && !s.style.marker)).toEqual([
			"粗体",
		]);
		expect(styled(segs, (s) => !!s.style.code && !s.style.marker)).toEqual([
			"a<b>",
		]);
		expect(styled(segs, (s) => !!s.style.em && !s.style.marker)).toEqual([
			"斜体",
		]);
	});

	it("围栏代码块里的内容不再按行内语法解析，未闭合时延续到末尾", () => {
		const segs = userMarkdown(
			"```\n**不是粗体**\n```\n**粗体**\n~~~\n# 不是标题",
		);
		expect(styled(segs, (s) => !!s.style.strong && !s.style.marker)).toEqual([
			"粗体",
		]);
		expect(styled(segs, (s) => !!s.style.heading)).toEqual([]);
		expect(segs.at(-1)).toEqual({ text: "# 不是标题", style: { block: true } });
	});

	it("链接只给 http(s) / mailto 地址；snake_case 与转义的星号不当强调", () => {
		const segs = userMarkdown(
			"[文档](https://x.test/d) [坏](javascript:alert(1)) 见 https://x.test/p。 snake_case_name \\*x\\*",
		);
		const links = segs.filter((s) => s.href).map((s) => [s.text, s.href]);
		expect(links).toEqual([
			["文档", "https://x.test/d"],
			["https://x.test/p", "https://x.test/p"],
		]);
		expect(segs.some((s) => s.style.em)).toBe(false);
	});
});
