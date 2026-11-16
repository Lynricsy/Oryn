import { describe, expect, it } from "vitest";
import {
	applyCanvasOps,
	type CanvasDoc,
	inverseOps,
	parseCanvasDoc,
} from "../../../lib/artifacts/canvas-doc";
import {
	alignOps,
	copySheetOps,
	deleteSheetOps,
	distributeOps,
	lockOps,
	orderOps,
	presetOf,
	presetOps,
	renameSheetOps,
	sizeOps,
} from "./arrange";

function docOf(raw: unknown): CanvasDoc {
	return parseCanvasDoc(JSON.stringify(raw));
}

const page = (
	id: string,
	x: number,
	y: number,
	width = 400,
	height = 300,
	extra: Record<string, unknown> = {},
) => ({
	id,
	canvas: "c1",
	name: id.toUpperCase(),
	file: `${id}.html`,
	x,
	y,
	width,
	height,
	...extra,
});

const THREE = docOf({
	format: 1,
	canvases: [{ id: "c1", name: "首页" }],
	pages: [page("a", 100, 0), page("b", 700, 250, 200), page("c", 1800, 80)],
	notes: [],
});

function apply(doc: CanvasDoc, ops: Parameters<typeof applyCanvasOps>[1]) {
	return applyCanvasOps(doc, ops);
}

describe("对齐", () => {
	it("三个画框左对齐后 x 相同", () => {
		const next = apply(THREE, alignOps(THREE, ["a", "b", "c"], "left"));
		expect(next.pages.map((p) => p.x)).toEqual([100, 100, 100]);
		// y 不动
		expect(next.pages.map((p) => p.y)).toEqual([0, 250, 80]);
	});

	it("右对齐、顶对齐、垂直居中", () => {
		const right = apply(THREE, alignOps(THREE, ["a", "b", "c"], "right"));
		expect(right.pages.map((p) => p.x + p.width)).toEqual([2200, 2200, 2200]);
		const top = apply(THREE, alignOps(THREE, ["a", "b"], "top"));
		expect(top.pages.map((p) => p.y)).toEqual([0, 0, 80]);
		const mid = apply(THREE, alignOps(THREE, ["a", "b", "c"], "centerV"));
		expect(mid.pages.map((p) => p.y + p.height / 2)).toEqual([275, 275, 275]);
	});

	it("锁定的画框参与参照但不动；少于两个画框没有操作", () => {
		const doc = docOf({
			...JSON.parse(JSON.stringify(THREE)),
			pages: [page("a", 100, 0, 400, 300, { locked: true }), page("b", 0, 50)],
		});
		const next = apply(doc, alignOps(doc, ["a", "b"], "right"));
		expect(next.pages.map((p) => p.x)).toEqual([100, 100]);
		expect(alignOps(THREE, ["a"], "left")).toEqual([]);
		expect(alignOps(THREE, ["a", "n1"], "left")).toEqual([]);
	});
});

describe("等距", () => {
	it("水平等距后间距相等，首尾不动", () => {
		const next = apply(THREE, distributeOps(THREE, ["c", "a", "b"], "x"));
		const [a, b, c] = next.pages;
		if (!a || !b || !c) throw new Error("缺画框");
		expect(a.x).toBe(100);
		expect(c.x).toBe(1800);
		expect(b.x - (a.x + a.width)).toBeCloseTo(c.x - (b.x + b.width), 2);
	});

	it("垂直等距；少于三个画框没有操作", () => {
		const next = apply(THREE, distributeOps(THREE, ["a", "b", "c"], "y"));
		const sorted = next.pages.toSorted((p, q) => p.y - q.y);
		const gaps = sorted
			.slice(1)
			.map((p, i) => p.y - ((sorted[i]?.y ?? 0) + (sorted[i]?.height ?? 0)));
		expect(gaps[0]).toBeCloseTo(gaps[1] ?? Number.NaN, 2);
		expect(distributeOps(THREE, ["a", "b"], "x")).toEqual([]);
	});
});

describe("层级与锁定", () => {
	it("移到最前 / 最后保持多个对象的先后，撤销回到原顺序", () => {
		const front = apply(THREE, orderOps(THREE, ["a", "b"], "front"));
		expect(front.pages.map((p) => p.id)).toEqual(["c", "a", "b"]);
		const back = apply(THREE, orderOps(THREE, ["b", "c"], "back"));
		expect(back.pages.map((p) => p.id)).toEqual(["b", "c", "a"]);
		const ops = orderOps(THREE, ["a"], "front");
		const undone = apply(apply(THREE, ops), inverseOps(THREE, ops));
		expect(undone.pages.map((p) => p.id)).toEqual(["a", "b", "c"]);
	});

	it("锁定只改还不是该状态的对象", () => {
		const locked = apply(THREE, lockOps(THREE, ["a", "b"], true));
		expect(locked.pages.map((p) => p.locked)).toEqual([true, true, false]);
		expect(lockOps(locked, ["a", "b"], true)).toEqual([]);
	});
});

describe("尺寸", () => {
	it("「手机」预设得到 390×844", () => {
		const a = THREE.pages[0];
		if (!a) throw new Error("缺画框");
		const ops = presetOps(a, "phone");
		expect(ops).toEqual([
			{ op: "set", id: "a", key: "width", value: 390 },
			{ op: "set", id: "a", key: "height", value: 844 },
		]);
		const next = apply(THREE, ops).pages[0];
		expect(next && [next.width, next.height]).toEqual([390, 844]);
		expect(next && presetOf(next)).toBe("phone");
		expect(presetOf(a)).toBe("custom");
		expect(presetOps(a, "custom")).toEqual([]);
	});

	it("宽高夹在 64–8000 里", () => {
		const a = THREE.pages[0];
		if (!a) throw new Error("缺画框");
		expect(sizeOps(a, 10, 99999)).toEqual([
			{ op: "set", id: "a", key: "width", value: 64 },
			{ op: "set", id: "a", key: "height", value: 8000 },
		]);
	});
});

describe("画布页", () => {
	const TWO = docOf({
		format: 1,
		canvases: [
			{ id: "c1", name: "首页" },
			{ id: "c2", name: "详情" },
		],
		pages: [page("a", 0, 0), page("b", 600, 0)],
		notes: [
			{
				id: "n1",
				canvas: "c1",
				kind: "arrow",
				color: "red",
				x1: 0,
				y1: 0,
				x2: 10,
				y2: 10,
				width: 3,
				from: "a",
				to: "b",
			},
		],
	});

	it("最后一页不能删", () => {
		const one = apply(TWO, deleteSheetOps(TWO, "c2") ?? []);
		expect(one.canvases.map((c) => c.id)).toEqual(["c1"]);
		expect(deleteSheetOps(one, "c1")).toBeNull();
		expect(deleteSheetOps(TWO, "nope")).toBeNull();
	});

	it("删页面连带画框与标注，撤销全部回来", () => {
		const ops = deleteSheetOps(TWO, "c1") ?? [];
		const next = apply(TWO, ops);
		expect(next.pages).toEqual([]);
		expect(next.notes).toEqual([]);
		expect(apply(next, inverseOps(TWO, ops))).toEqual(TWO);
	});

	it("复制页面：画框带 copyOf，吸附指向复制出的画框，紧跟在原页之后", () => {
		const copy = copySheetOps(TWO, "c1", "首页 副本");
		if (!copy) throw new Error("没复制");
		const next = apply(TWO, copy.ops);
		expect(next.canvases.map((c) => c.id)).toEqual(["c1", copy.id, "c2"]);
		const pages = next.pages.filter((p) => p.canvas === copy.id);
		expect(pages.map((p) => [p.x, p.y, p.name])).toEqual([
			[0, 0, "A"],
			[600, 0, "B"],
		]);
		expect(
			copy.ops.filter((o) => o.op === "add" && o.copyOf).map((o) => o.op),
		).toHaveLength(2);
		const arrow = next.notes.find((n) => n.canvas === copy.id);
		expect(arrow?.kind).toBe("arrow");
		if (arrow?.kind !== "arrow") return;
		expect(arrow.from).toBe(pages[0]?.id);
		expect(arrow.to).toBe(pages[1]?.id);
	});

	it("改名去掉首尾空白；空名或没变时没有操作", () => {
		expect(renameSheetOps(TWO, "c1", "  概览 ")).toEqual([
			{ op: "set", id: "c1", key: "name", value: "概览" },
		]);
		expect(renameSheetOps(TWO, "c1", "首页")).toEqual([]);
		expect(renameSheetOps(TWO, "c1", "  ")).toEqual([]);
	});
});
