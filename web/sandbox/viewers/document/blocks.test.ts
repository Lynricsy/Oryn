import { describe, expect, it } from "vitest";
import { joinBlocks, replaceBlock, splitBlocks } from "./blocks";

const DOC = `
# 重连机制

断线后按指数退避重连。

第二段：**15 秒**封顶，[详见](https://example.com)。

- 第一次 0.5s
- 第二次 1s
  - 子项

| 次数 | 等待 |
| --- | --- |
| 1 | 0.5s |

`;

describe("splitBlocks", () => {
	it("标题 + 两段 + 列表 + 表格拆成 5 块，拼回与原文逐字节相同", () => {
		const blocks = splitBlocks(DOC);
		expect(blocks).toHaveLength(5);
		expect(blocks.map((b) => b.body.split("\n")[0])).toEqual([
			"# 重连机制",
			"断线后按指数退避重连。",
			"第二段：**15 秒**封顶，[详见](https://example.com)。",
			"- 第一次 0.5s",
			"| 次数 | 等待 |",
		]);
		expect(blocks[0]?.lead).toBe("\n");
		expect(joinBlocks(blocks)).toBe(DOC);
	});

	it("链接引用定义并入前一块", () => {
		const md = "见 [文档][d]。\n\n[d]: https://example.com\n\n## 下一节\n";
		const blocks = splitBlocks(md);
		expect(blocks).toHaveLength(2);
		expect(blocks[0]?.body).toBe("见 [文档][d]。\n\n[d]: https://example.com");
		expect(joinBlocks(blocks)).toBe(md);
	});

	it("空文档是一个空块", () => {
		expect(splitBlocks("")).toEqual([{ lead: "", body: "", tail: "" }]);
		expect(joinBlocks(splitBlocks("\n\n"))).toBe("\n\n");
	});

	it("replaceBlock 只换那一块；清空则连同分隔删掉", () => {
		const blocks = splitBlocks("# A\n\nB\n\nC\n");
		expect(replaceBlock(blocks, 1, "B2")).toBe("# A\n\nB2\n\nC\n");
		expect(replaceBlock(blocks, 1, "  ")).toBe("# A\n\nC\n");
		expect(replaceBlock(splitBlocks("only\n"), 0, "")).toBe("\n");
	});
});
