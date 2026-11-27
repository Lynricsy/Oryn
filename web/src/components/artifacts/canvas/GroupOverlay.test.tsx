/**
 * 方案组覆盖层（M34 S9）：未选定 / 已选定两种状态下的按钮与徽标、选定提交的操作（含搬移画框），备选方案页上的改选，
 * 只读时没有按钮，以及设计画布的选定提示条不带发送按钮。
 */
import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../../i18n";
import {
	ALTERNATES_ID,
	applyCanvasOps,
	CANVAS_FORMAT_GROUPS,
	type CanvasDoc,
	type CanvasOp,
	type CanvasPage,
	expandPicks,
} from "../../../lib/artifacts/canvas-doc";
import { IDENTITY } from "./camera";
import { GroupOverlay, groupSpots, PickNotice, pickOps } from "./GroupOverlay";

function frame(id: string, x: number, y: number): CanvasPage {
	return {
		id,
		canvas: "canvas-1",
		name: id,
		file: `page-${id}.html`,
		x,
		y,
		width: 400,
		height: 300,
		locked: false,
	};
}

const DOC: CanvasDoc = {
	format: CANVAS_FORMAT_GROUPS,
	canvases: [{ id: "canvas-1", name: "首页" }],
	pages: [
		frame("home-a", 0, 0),
		frame("home-b", 500, 0),
		frame("pricing-b", 500, 400),
		frame("home-c", 1000, 0),
		frame("about", 0, 800),
	],
	notes: [],
	groups: [
		{
			id: "home",
			canvas: "canvas-1",
			name: "首页首屏",
			variants: [
				{
					id: "a",
					name: "冷静克制",
					note: "大留白",
					theme: "",
					pages: ["home-a"],
				},
				{
					id: "b",
					name: "暖调杂志",
					note: "",
					theme: "",
					pages: ["home-b", "pricing-b"],
				},
				{
					id: "c",
					name: "高对比",
					note: "深色底",
					theme: "",
					pages: ["home-c"],
				},
			],
			picked: null,
			pickedBy: null,
		},
	],
};

const PICK_B: CanvasOp = { op: "set", id: "home", key: "picked", value: "b" };

function row(group: string, variant: string): HTMLElement {
	const el = document.querySelector<HTMLElement>(
		`[data-variant="${group}/${variant}"]`,
	);
	if (!el) throw new Error(`没有方案标签 ${group}/${variant}`);
	return el;
}

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
});

describe("GroupOverlay", () => {
	it("未选定：组标题、每个方案的标签都有「选定此方案」，note 作悬停提示", () => {
		render(
			<GroupOverlay
				doc={DOC}
				canvas="canvas-1"
				cam={IDENTITY}
				commit={() => true}
			/>,
		);
		expect(screen.getByTestId("canvas-group-title").textContent).toBe(
			"方案组 · 首页首屏",
		);
		for (const v of ["a", "b", "c"])
			expect(within(row("home", v)).getByRole("button").textContent).toBe(
				"选定此方案",
			);
		expect(screen.queryByTestId("canvas-group-picked")).toBeNull();
		expect(within(row("home", "a")).getByText("冷静克制").title).toBe("大留白");
		// 方案标签在它最上面的画框上方（屏幕坐标 = 世界坐标，缩放 1）
		expect(row("home", "b").style.left).toBe("500px");
	});

	it("点「选定此方案」提交 expandPicks 展开后的操作：落选画框搬到备选方案页，选中方案不动", () => {
		const commit = vi.fn((_ops: CanvasOp[]) => true);
		const onPicked = vi.fn();
		render(
			<GroupOverlay
				doc={DOC}
				canvas="canvas-1"
				cam={IDENTITY}
				commit={commit}
				onPicked={onPicked}
			/>,
		);
		fireEvent.click(within(row("home", "b")).getByRole("button"));
		expect(commit).toHaveBeenCalledTimes(1);
		const ops = commit.mock.calls[0]?.[0] ?? [];
		expect(ops).toEqual(expandPicks(DOC, [PICK_B]));
		expect(ops[0]).toEqual(PICK_B);
		expect(ops).toContainEqual({
			op: "add",
			object: { type: "canvas", id: ALTERNATES_ID, name: "备选方案" },
		});
		const moved = ops.flatMap((op) =>
			op.op === "set" && op.key === "canvas" ? [op.id] : [],
		);
		expect(moved).toEqual(["home-a", "home-c"]);
		expect(onPicked).toHaveBeenCalledWith("home");
	});

	it("提交被拒（commit 回 false）时不报选定", () => {
		const onPicked = vi.fn();
		render(
			<GroupOverlay
				doc={DOC}
				canvas="canvas-1"
				cam={IDENTITY}
				commit={() => false}
				onPicked={onPicked}
			/>,
		);
		fireEvent.click(within(row("home", "a")).getByRole("button"));
		expect(onPicked).not.toHaveBeenCalled();
	});

	it("已选定：本页只剩选中方案并显示「已选定」；备选方案页上的方案可以改选，改选把画框搬回组的画布页", () => {
		const picked = applyCanvasOps(DOC, pickOps(DOC, "home", "b"));
		const commit = vi.fn((_ops: CanvasOp[]) => true);
		const { unmount } = render(
			<GroupOverlay
				doc={picked}
				canvas="canvas-1"
				cam={IDENTITY}
				commit={commit}
			/>,
		);
		expect(within(row("home", "b")).getByText("已选定")).toBeTruthy();
		expect(within(row("home", "b")).queryByRole("button")).toBeNull();
		expect(document.querySelector('[data-variant="home/a"]')).toBeNull();
		unmount();

		render(
			<GroupOverlay
				doc={picked}
				canvas={ALTERNATES_ID}
				cam={IDENTITY}
				commit={commit}
			/>,
		);
		expect(screen.getByTestId("canvas-group-title")).toBeTruthy();
		expect(document.querySelector('[data-variant="home/b"]')).toBeNull();
		const repick = within(row("home", "a")).getByRole("button");
		expect(repick.textContent).toBe("改选此方案");
		expect(within(row("home", "c")).getByRole("button").textContent).toBe(
			"改选此方案",
		);
		fireEvent.click(repick);
		const ops = commit.mock.calls[0]?.[0] ?? [];
		expect(ops).toEqual(pickOps(picked, "home", "a"));
		const after = applyCanvasOps(picked, ops);
		const sheet = (id: string) => after.pages.find((p) => p.id === id)?.canvas;
		expect(sheet("home-a")).toBe("canvas-1");
		expect(sheet("home-b")).toBe(ALTERNATES_ID);
		expect(sheet("pricing-b")).toBe(ALTERNATES_ID);
		expect(sheet("home-c")).toBe(ALTERNATES_ID);
		expect(sheet("about")).toBe("canvas-1");
	});

	it("只读（不给 commit）：没有按钮，已选定的徽标照常显示", () => {
		const picked = applyCanvasOps(DOC, pickOps(DOC, "home", "b"));
		render(<GroupOverlay doc={DOC} canvas="canvas-1" cam={IDENTITY} />);
		expect(screen.queryAllByRole("button")).toHaveLength(0);
		render(<GroupOverlay doc={picked} canvas="canvas-1" cam={IDENTITY} />);
		expect(screen.getByTestId("canvas-group-picked")).toBeTruthy();
	});

	it("组里没有画框在当前页时不画；外接框包住组里本页的画框", () => {
		expect(groupSpots(DOC, "other")).toEqual([]);
		const [spot] = groupSpots(DOC, "canvas-1");
		expect(spot?.box).toEqual({ x: 0, y: 0, width: 1400, height: 700 });
		expect(spot?.variants.map((v) => v.top.id)).toEqual([
			"home-a",
			"home-b",
			"home-c",
		]);
	});
});

describe("PickNotice", () => {
	it("设计画布：提示 Agent 下一回合统一主题，没有发送按钮；可以关掉", () => {
		const doc: CanvasDoc = {
			...applyCanvasOps(DOC, pickOps(DOC, "home", "c")),
			design: { engine: 1, start: null },
		};
		const onClose = vi.fn();
		render(
			<PickNotice
				artifactId="art_1"
				doc={doc}
				group="home"
				onClose={onClose}
			/>,
		);
		expect(screen.getByTestId("canvas-pick-notice").textContent).toContain(
			"已选定「高对比」，Agent 会在下一回合统一主题。",
		);
		expect(screen.queryByTestId("canvas-pick-unify")).toBeNull();
		fireEvent.click(screen.getByTestId("canvas-pick-dismiss"));
		expect(onClose).toHaveBeenCalled();
	});

	it("组没有选定时不显示", () => {
		render(
			<PickNotice
				artifactId="art_1"
				doc={DOC}
				group="home"
				onClose={vi.fn()}
			/>,
		);
		expect(screen.queryByTestId("canvas-pick-notice")).toBeNull();
	});
});
