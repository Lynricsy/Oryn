import { describe, expect, it } from "vitest";
import { type DocxBlock, docxPlan, exportDocx } from "./export-docx";
import { rasterInfo } from "./office-images";
import { tinyPng } from "./slides-fixture";

const FIXTURE = `# 周报

## 进展

### 细节

正文有 **加粗**、*斜体*、~~删除~~ 和 \`code\`，以及[官网](https://example.com/a?b=1)。
第二行。

- 一
- 二
  1. 子一
- [x] 完成

| 名称 | 数量 | 备注 |
| :-- | --: | :-: |
| 苹果 | 3 | 红 |
| 梨 | 5 |

\`\`\`go
fmt.Println("hi")
return
\`\`\`

> 引用的话

---

![图](images/a.png)

<div>原生 HTML 不导出</div>
`;

function texts(b: DocxBlock): string {
	if (!("inlines" in b)) return "";
	return b.inlines
		.map((i) =>
			i.kind === "text"
				? i.text
				: i.kind === "link"
					? i.children.map((s) => s.text).join("")
					: "",
		)
		.join("");
}

describe("docxPlan", () => {
	const plan = docxPlan(FIXTURE);

	it("标题层级", () => {
		expect(
			plan.flatMap((b) =>
				b.kind === "heading" ? [[b.level, texts(b)] as const] : [],
			),
		).toEqual([
			[1, "周报"],
			[2, "进展"],
			[3, "细节"],
		]);
	});

	it("行内样式、超链接地址、软换行", () => {
		const p = plan.find((b) => b.kind === "paragraph");
		expect(p?.kind).toBe("paragraph");
		if (p?.kind !== "paragraph") return;
		expect(p.inlines).toContainEqual({
			kind: "text",
			text: "加粗",
			bold: true,
		});
		expect(p.inlines).toContainEqual({
			kind: "text",
			text: "斜体",
			italic: true,
		});
		expect(p.inlines).toContainEqual({
			kind: "text",
			text: "删除",
			strike: true,
		});
		expect(p.inlines).toContainEqual({
			kind: "text",
			text: "code",
			code: true,
		});
		expect(p.inlines).toContainEqual({
			kind: "link",
			url: "https://example.com/a?b=1",
			children: [{ text: "官网" }],
		});
		expect(texts(p)).toContain("。 第二行。");
	});

	it("列表：层级、有序与任务项", () => {
		const items = plan.filter((b) => b.kind === "list-item");
		expect(
			items.map((b) =>
				b.kind === "list-item" ? [texts(b), b.level, b.ordered, b.checked] : [],
			),
		).toEqual([
			["一", 0, false, null],
			["二", 0, false, null],
			["子一", 1, true, null],
			["完成", 0, false, true],
		]);
	});

	it("表格的行列数与对齐，缺的单元格补齐", () => {
		const t = plan.find((b) => b.kind === "table");
		expect(t?.kind).toBe("table");
		if (t?.kind !== "table") return;
		expect(t.rows).toHaveLength(3);
		for (const r of t.rows) expect(r).toHaveLength(3);
		expect(t.align).toEqual(["left", "right", "center"]);
		expect(t.rows[2]?.[2]).toEqual([]);
		expect(t.rows[1]?.[0]).toEqual([{ kind: "text", text: "苹果" }]);
	});

	it("代码块原文、引用、分隔线、图片；原生 HTML 跳过", () => {
		expect(plan).toContainEqual({
			kind: "code",
			lang: "go",
			text: 'fmt.Println("hi")\nreturn',
		});
		expect(plan).toContainEqual({
			kind: "paragraph",
			quote: 1,
			inlines: [{ kind: "text", text: "引用的话" }],
		});
		expect(plan).toContainEqual({ kind: "rule" });
		expect(plan).toContainEqual({
			kind: "paragraph",
			inlines: [{ kind: "image", src: "images/a.png", alt: "图" }],
		});
		expect(JSON.stringify(plan)).not.toContain("原生 HTML");
	});
});

describe("rasterInfo", () => {
	it("认出 PNG、GIF、JPEG 的尺寸", () => {
		expect(rasterInfo(tinyPng(640, 480))).toEqual({
			type: "png",
			width: 640,
			height: 480,
		});
		const gif = new Uint8Array([
			0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x20, 1, 0x10, 0,
		]);
		expect(rasterInfo(gif)).toEqual({ type: "gif", width: 288, height: 16 });
		// SOI、一个 APP0 段（长度 4），然后 SOF0：高 0x0102、宽 0x0304
		const jpeg = new Uint8Array([
			0xff, 0xd8, 0xff, 0xe0, 0, 4, 0, 0, 0xff, 0xc0, 0, 17, 8, 1, 2, 3, 4, 3,
		]);
		expect(rasterInfo(jpeg)).toEqual({ type: "jpg", width: 772, height: 258 });
		expect(rasterInfo(new TextEncoder().encode("<svg/>"))).toBeNull();
	});
});

describe("exportDocx", () => {
	it("生成非空的 .docx；取不到的图片记进 missing", async () => {
		const read: string[] = [];
		const { blob, missing } = await exportDocx({
			title: "周报",
			markdown: `${FIXTURE}\n![坏图](images/gone.png)\n`,
			readFile: async (path) => {
				read.push(path);
				if (path === "images/a.png") return tinyPng(1200, 600);
				throw new Error("not found");
			},
		});
		expect(blob.size).toBeGreaterThan(0);
		expect(read.toSorted()).toEqual(["images/a.png", "images/gone.png"]);
		expect(missing).toEqual(["images/gone.png"]);
	});
});
