/** 视图规格的宽容归一化（docs/plan-m33.md 的 C2 / C10）。 */
import { describe, expect, it } from "vitest";
import {
	findBlock,
	interactiveBlocks,
	normalizeHtmlSpec,
	normalizeUiSpec,
	stripPrototypeKeys,
	type ViewBlock,
} from "./spec";

const paths = (blocks: readonly ViewBlock[]) => blocks.map((b) => b.path);

describe("normalizeUiSpec", () => {
	it("取不到任何块时为 null", () => {
		expect(normalizeUiSpec(undefined)).toBeNull();
		expect(normalizeUiSpec({ title: "x" })).toBeNull();
		expect(normalizeUiSpec({ blocks: [] })).toBeNull();
		expect(normalizeUiSpec({ blocks: [{ type: "nope" }] })).toBeNull();
	});

	it("跳过不合格的块，后面的块路径不错位", () => {
		const spec = normalizeUiSpec({
			title: "T",
			width: "wide",
			blocks: [
				{ type: "heading", text: "h" },
				{ type: "text" },
				{ type: "divider" },
				{ type: "actions", buttons: [{ label: "好", prompt: "好的" }] },
			],
		});
		expect(spec?.title).toBe("T");
		expect(spec?.width).toBe("wide");
		expect(paths(spec?.blocks ?? [])).toEqual(["0", "2", "3"]);
	});

	it("流式中残缺的参数：末尾不完整的块被跳过，完整的块照常显示", () => {
		// 逐 delta 补全括号解析出的对象：最后一个表单的字段还没流到 label / kind
		const spec = normalizeUiSpec({
			blocks: [
				{ type: "stats", items: [{ label: "收入", value: 12 }] },
				{ type: "form", fields: [{ name: "a" }] },
			],
		});
		expect(spec?.blocks.map((b) => b.type)).toEqual(["stats"]);
		// 第一个块的 type 都还没到
		expect(normalizeUiSpec({ blocks: [{}] })).toBeNull();
	});

	it("容器内的块路径：card 用下标，columns 写 c<i>，tabs 写 t<i>", () => {
		const leaf = { type: "text", text: "x" };
		const spec = normalizeUiSpec({
			blocks: [
				leaf,
				{
					type: "tabs",
					tabs: [
						{ label: "一", blocks: [leaf] },
						{ label: "二", blocks: [{ type: "bad" }, leaf] },
					],
				},
				{
					type: "columns",
					columns: [{ blocks: [leaf] }, { blocks: [leaf, leaf] }],
				},
				{ type: "card", title: "c", blocks: [leaf, leaf] },
			],
		});
		const blocks = spec?.blocks ?? [];
		expect(findBlock(blocks, "1.t1.1")?.type).toBe("text");
		expect(findBlock(blocks, "1.t1.0")).toBeUndefined();
		expect(findBlock(blocks, "2.c1.1")?.path).toBe("2.c1.1");
		expect(findBlock(blocks, "3.1")?.path).toBe("3.1");
	});

	it("容器最多嵌套 3 层，更深的容器整个跳过", () => {
		const leaf = { type: "text", text: "x" };
		const card = (inner: unknown) => ({ type: "card", blocks: [leaf, inner] });
		const spec = normalizeUiSpec({ blocks: [card(card(card(card(leaf))))] });
		const l1 = spec?.blocks[0];
		expect(l1?.type).toBe("card");
		const l2 = l1?.type === "card" ? l1.blocks[1] : undefined;
		const l3 = l2?.type === "card" ? l2.blocks[1] : undefined;
		expect(l3?.type).toBe("card");
		// 第 4 层容器被跳过，只剩叶子
		expect(l3?.type === "card" ? paths(l3.blocks) : []).toEqual(["0.1.1.0"]);
	});

	it("交互块按深度优先列出", () => {
		const form = {
			type: "form",
			fields: [{ name: "n", label: "N", kind: "text" }],
		};
		const actions = {
			type: "actions",
			buttons: [{ label: "a", prompt: "p" }],
		};
		const spec = normalizeUiSpec({
			blocks: [actions, { type: "card", blocks: [form] }],
		});
		expect(paths(interactiveBlocks(spec?.blocks ?? []))).toEqual(["0", "1.0"]);
	});
});

describe("stripPrototypeKeys", () => {
	it("深度去掉 __proto__ / constructor / prototype", () => {
		const raw = JSON.parse(
			'{"a":{"__proto__":{"x":1},"b":[{"constructor":1,"c":2}]},"prototype":3}',
		);
		expect(stripPrototypeKeys(raw)).toEqual({ a: { b: [{ c: 2 }] } });
	});
});

describe("normalizeHtmlSpec", () => {
	it("libs 兼容单个字符串与数组，去重、丢弃未知项；高度夹在 120–1600", () => {
		expect(normalizeHtmlSpec({ libs: "chartjs", height: 9000 })).toEqual({
			libs: ["chartjs"],
			height: 1600,
		});
		expect(
			normalizeHtmlSpec({ libs: ["tailwind", "x", "tailwind"], title: "T" }),
		).toEqual({ libs: ["tailwind"], title: "T" });
		expect(normalizeHtmlSpec(null)).toEqual({ libs: [] });
	});
});
