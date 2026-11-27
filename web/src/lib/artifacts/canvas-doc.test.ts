import vectors from "../../../../schema/testdata/canvas-ops.json";
import {
	ALTERNATES_ID,
	applyCanvasOps,
	archivedPages,
	type CanvasDoc,
	CanvasError,
	type CanvasOp,
	canvasId,
	expandPicks,
	inverseOps,
	joinedEnds,
	missingPageFiles,
	noteBox,
	type PenNote,
	parseCanvasDoc,
	parseCanvasOps,
	type ShapeNote,
	type StickyNote,
	stringifyCanvasDoc,
} from "./canvas-doc";

interface Vector {
	name: string;
	doc: unknown;
	ops: unknown;
	want?: unknown;
	error?: string;
	/** 逆操作恢复不了原文档的原因（例如删的是封存维护的设计画框）；有它时不检查撤销。 */
	noUndo?: string;
	/** 先用 expandPicks 展开 ops。 */
	expand?: boolean;
}

function run(v: Vector): { doc: CanvasDoc; ops: CanvasOp[]; out: CanvasDoc } {
	const doc = parseCanvasDoc(JSON.stringify(v.doc));
	const parsed = parseCanvasOps(v.ops);
	const ops = v.expand ? expandPicks(doc, parsed) : parsed;
	return { doc, ops, out: applyCanvasOps(doc, ops) };
}

describe("canvas-ops.json 测试向量", () => {
	const all = vectors as Vector[];

	it("向量文件非空，且每条恰好给 want 或 error 之一", () => {
		expect(all.length).toBeGreaterThan(0);
		for (const v of all)
			expect(v.want === undefined, v.name).toBe(v.error !== undefined);
	});

	it.each(all.map((v) => [v.name, v] as const))("%s", (_name, v) => {
		if (v.error !== undefined) {
			expect(() => run(v)).toThrow(
				expect.objectContaining({ name: "CanvasError", where: v.error }),
			);
			return;
		}
		const { doc, ops, out } = run(v);
		expect(out).toEqual(v.want);
		// 规范化输出再解析得到同一份文档
		expect(parseCanvasDoc(stringifyCanvasDoc(out))).toEqual(out);
		// 重复展开结果不变
		if (v.expand)
			expect(applyCanvasOps(doc, expandPicks(doc, ops))).toEqual(out);
		// 逆操作把文档恢复原样
		if (v.noUndo === undefined)
			expect(applyCanvasOps(out, inverseOps(doc, ops))).toEqual(doc);
	});
});

describe("方案组", () => {
	const doc = parseCanvasDoc(
		JSON.stringify({
			pages: [
				{ id: "a1", name: "a", file: "a.html", x: 0, y: 0 },
				{ id: "b1", name: "b", file: "b.html", x: 1328, y: 0 },
			],
			groups: [
				{
					id: "home",
					name: "首页",
					variants: [
						{ id: "a", name: "A", pages: ["a1"] },
						{ id: "b", name: "B", pages: ["b1"] },
					],
				},
			],
		}),
	);

	it("archivedPages 只由 groups 推导，选定后落选画框搬到备选方案页", () => {
		expect(archivedPages(doc).size).toBe(0);
		const ops = expandPicks(doc, [
			{ op: "set", id: "home", key: "picked", value: "b" },
		]);
		const picked = applyCanvasOps(doc, ops);
		expect([...archivedPages(picked)]).toEqual(["a1"]);
		expect(picked.pages.find((p) => p.id === "a1")?.canvas).toBe(ALTERNATES_ID);
	});

	it("撤销删画框时恢复方案组与选定者", () => {
		const picked = applyCanvasOps(doc, [
			{ op: "set", id: "home", key: "picked", value: "a" },
			{ op: "set", id: "home", key: "pickedBy", value: "thr_1" },
		]);
		const ops: CanvasOp[] = [{ op: "remove", id: "a1" }];
		const after = applyCanvasOps(picked, ops);
		expect(after.groups?.[0]?.picked).toBeNull();
		expect(applyCanvasOps(after, inverseOps(picked, ops))).toEqual(picked);
	});
});

const base = parseCanvasDoc(
	JSON.stringify({
		pages: [
			{
				id: "p1",
				name: "a",
				file: "a.html",
				x: 0,
				y: 0,
				width: 100,
				height: 100,
			},
			{
				id: "p2",
				name: "b",
				file: "b.html",
				x: 300,
				y: 0,
				width: 100,
				height: 100,
			},
		],
		notes: [
			{
				id: "n1",
				kind: "arrow",
				x1: 0,
				y1: 0,
				x2: 10,
				y2: 10,
				from: "p1",
				to: "p2",
			},
			{ id: "n2", kind: "sticky", x: 5, y: 5, text: "改这里" },
		],
	}),
);

describe("applyCanvasOps", () => {
	it("失败时不改原文档", () => {
		const before = stringifyCanvasDoc(base);
		expect(() =>
			applyCanvasOps(base, [
				{ op: "set", id: "p1", key: "name", value: "新" },
				{ op: "remove", id: "canvas-1" },
			]),
		).toThrow(/canvases/);
		expect(stringifyCanvasDoc(base)).toBe(before);
	});

	it("报错写出位置", () => {
		try {
			applyCanvasOps(base, [{ op: "set", id: "p2", key: "width", value: 1 }]);
			expect.unreachable();
		} catch (e) {
			expect(e).toBeInstanceOf(CanvasError);
			expect((e as CanvasError).where).toBe("ops[0].width");
		}
	});
});

describe("inverseOps", () => {
	it("撤销删画框时恢复它的位置与两端吸附", () => {
		const ops: CanvasOp[] = [
			{ op: "remove", id: "p1" },
			{ op: "set", id: "n2", key: "done", value: true },
		];
		const after = applyCanvasOps(base, ops);
		expect((after.notes[0] as ShapeNote).from).toBeNull();
		expect(applyCanvasOps(after, inverseOps(base, ops))).toEqual(base);
	});

	it("撤销 add 与 order", () => {
		const ops: CanvasOp[] = [
			{
				op: "add",
				object: {
					type: "note",
					id: "n3",
					kind: "text",
					x: 1,
					y: 1,
					text: "hi",
				},
			},
			{ op: "order", id: "n3", index: 0 },
			{ op: "order", id: "p2", index: 0 },
		];
		const after = applyCanvasOps(base, ops);
		expect(after.notes[0]?.id).toBe("n3");
		expect(applyCanvasOps(after, inverseOps(base, ops))).toEqual(base);
	});
});

describe("几何", () => {
	it("joinedEnds 把吸附端放到画框边上", () => {
		const n = base.notes[0] as ShapeNote;
		expect(joinedEnds(n, base.pages)).toEqual({
			x1: 100,
			y1: 50,
			x2: 300,
			y2: 50,
		});
		expect(joinedEnds({ ...n, from: null, to: null }, base.pages)).toEqual({
			x1: 0,
			y1: 0,
			x2: 10,
			y2: 10,
		});
		// 只吸附一端：朝另一端的自由点
		expect(joinedEnds({ ...n, to: null, x2: 50, y2: 300 }, base.pages)).toEqual(
			{ x1: 50, y1: 100, x2: 50, y2: 300 },
		);
	});

	it("noteBox", () => {
		expect(
			noteBox({
				...(base.notes[0] as ShapeNote),
				x1: 10,
				y1: 20,
				x2: 0,
				y2: 5,
			}),
		).toEqual({
			x: 0,
			y: 5,
			width: 10,
			height: 15,
		});
		const pen = parseCanvasDoc(
			JSON.stringify({
				notes: [
					{ id: "n", kind: "pen", points: [0, 0, 1, 10, 20, 1], width: 4 },
				],
			}),
		).notes[0] as PenNote;
		expect(noteBox(pen)).toEqual({ x: -2, y: -2, width: 14, height: 24 });
		expect(noteBox(base.notes[1] as StickyNote)).toMatchObject({
			x: 5,
			y: 5,
			width: 240,
		});
	});
});

it("missingPageFiles 去重并保持顺序", () => {
	expect(missingPageFiles(base, (p) => p === "a.html")).toEqual(["b.html"]);
});

it("canvasId 生成合法且不重复的 id", () => {
	const a = canvasId("p");
	expect(a).toMatch(/^p[a-z0-9]{12}$/);
	expect(canvasId("p")).not.toBe(a);
});
