/**
 * 画布编辑器的工具条、属性面板、图层面板与画框名称行（计划 M14 6.4）：页面下拉的增删改（最后一页不能删）、
 * 只读时没有编辑类控件、「手机」预设、对齐提交操作、图层面板的选中与改名、名称行的按钮插槽。
 */
import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import i18n from "../../../i18n";
import {
	applyCanvasOps,
	type CanvasDoc,
	type CanvasOp,
	parseCanvasDoc,
} from "../../../lib/artifacts/canvas-doc";
import { pickOption } from "../../../test/select";
import { FrameLabel } from "./FrameLabel";
import { LayersPanel } from "./LayersPanel";
import { PropsPanel } from "./PropsPanel";
import { DEFAULT_STYLES, Toolbar, type ToolbarProps } from "./Toolbar";

const t = i18n.t.bind(i18n);

function docOf(raw: unknown): CanvasDoc {
	return parseCanvasDoc(JSON.stringify(raw));
}

const page = (id: string, x: number, y: number) => ({
	id,
	canvas: "c1",
	name: id.toUpperCase(),
	file: `${id}.html`,
	x,
	y,
	width: 400,
	height: 300,
});

const ONE = docOf({
	format: 1,
	canvases: [{ id: "c1", name: "首页" }],
	pages: [page("a", 0, 0), page("b", 600, 120)],
	notes: [
		{
			id: "n1",
			canvas: "c1",
			kind: "sticky",
			color: "yellow",
			x: 10,
			y: 10,
			width: 240,
			text: "把按钮改成绿色",
			done: true,
			by: "T1",
		},
	],
});

function toolbar(over: Partial<ToolbarProps> = {}) {
	const commit = vi.fn((_ops: CanvasOp[]) => true);
	const onCanvas = vi.fn();
	render(
		<div className="canvas-editor relative">
			<Toolbar
				doc={ONE}
				canvasKey="c1"
				readOnly={false}
				commentable={false}
				tool="move"
				penKind="pen"
				shape="rect"
				styles={DEFAULT_STYLES}
				onTool={vi.fn()}
				onPenKind={vi.fn()}
				onShape={vi.fn()}
				onStyle={vi.fn()}
				onCanvas={onCanvas}
				commit={commit}
				zoom={1}
				hasSelection={false}
				onZoom={vi.fn()}
				{...over}
			/>
		</div>,
	);
	return { commit, onCanvas };
}

describe("工具条", () => {
	it("页面下拉：最后一页不能删；新建页面后切过去", () => {
		const { commit, onCanvas } = toolbar();
		fireEvent.click(screen.getByTestId("canvas-sheet-menu"));
		const del = screen.getByRole("menuitem", {
			name: t("artifacts.canvas.sheets.delete"),
		});
		expect(del).toBeDisabled();
		expect(del).toHaveAttribute("title", t("artifacts.canvas.sheets.lastOne"));
		fireEvent.click(
			screen.getByRole("menuitem", { name: t("artifacts.canvas.sheets.add") }),
		);
		expect(commit).toHaveBeenCalledTimes(1);
		const [ops] = commit.mock.calls[0] ?? [];
		const added = ops?.[0];
		expect(added?.op).toBe("add");
		if (added?.op !== "add") return;
		expect(added.object).toMatchObject({ type: "canvas", name: "页面 2" });
		expect(onCanvas).toHaveBeenCalledWith(added.object.id);
	});

	it("有两页时可以删当前页（非空页先确认），之后切到相邻页", () => {
		const two = applyCanvasOps(ONE, [
			{ op: "add", object: { type: "canvas", id: "c2", name: "详情" } },
		]);
		const { commit, onCanvas } = toolbar({ doc: two });
		fireEvent.click(screen.getByTestId("canvas-sheet-menu"));
		fireEvent.click(
			screen.getByRole("menuitem", {
				name: t("artifacts.canvas.sheets.delete"),
			}),
		);
		expect(commit).not.toHaveBeenCalled();
		fireEvent.click(screen.getByTestId("canvas-sheet-delete-confirm"));
		expect(commit).toHaveBeenCalledWith([{ op: "remove", id: "c1" }]);
		expect(onCanvas).toHaveBeenCalledWith("c2");
	});

	it("只读时没有编辑工具与页面的增删改", () => {
		toolbar({ readOnly: true });
		expect(screen.queryByTestId("canvas-tool-frame")).toBeNull();
		expect(screen.queryByTestId("canvas-tool-pen")).toBeNull();
		fireEvent.click(screen.getByTestId("canvas-sheet-menu"));
		expect(
			screen.queryByRole("menuitem", {
				name: t("artifacts.canvas.sheets.add"),
			}),
		).toBeNull();
		expect(
			screen.getByRole("menuitemradio", { name: /首页/ }),
		).toBeInTheDocument();
	});

	it("画笔工具带粗细与七色浮层", () => {
		const onStyle = vi.fn();
		toolbar({ tool: "pen", onStyle });
		const bar = screen.getByTestId("canvas-style-bar");
		const colors = within(bar).getByRole("radiogroup", {
			name: t("artifacts.canvas.style.color"),
		});
		expect(within(colors).getAllByRole("radio")).toHaveLength(7);
		fireEvent.click(
			within(colors).getByRole("radio", {
				name: t("artifacts.canvas.style.colors.red"),
			}),
		);
		expect(onStyle).toHaveBeenCalledWith("pen", { color: "red" });
		fireEvent.click(
			within(bar).getByRole("radio", {
				name: t("artifacts.canvas.style.widthPx", { n: 6 }),
			}),
		);
		expect(onStyle).toHaveBeenCalledWith("pen", { width: 6 });
	});
});

function props(selected: string[]) {
	const commit = vi.fn((_ops: CanvasOp[]) => true);
	const onSelect = vi.fn();
	render(
		<PropsPanel
			doc={ONE}
			selected={selected}
			threads={[{ threadId: "T1", title: "设计线程" }]}
			commit={commit}
			onSelect={onSelect}
			overlay={false}
		/>,
	);
	return { commit, onSelect };
}

describe("属性面板", () => {
	it("「手机」预设提交 390×844", () => {
		const { commit } = props(["a"]);
		pickOption(screen.getByRole("combobox", { name: /^尺寸/ }), /^手机/);
		expect(commit).toHaveBeenCalledWith([
			{ op: "set", id: "a", key: "width", value: 390 },
			{ op: "set", id: "a", key: "height", value: 844 },
		]);
	});

	it("两个画框顶对齐；不到三个时等距不可用", () => {
		const { commit } = props(["a", "b"]);
		expect(screen.getByTestId("canvas-distribute-x")).toBeDisabled();
		fireEvent.click(screen.getByTestId("canvas-align-top"));
		expect(commit).toHaveBeenCalledWith([
			{ op: "set", id: "b", key: "y", value: 0 },
		]);
	});

	it("锁定选中的对象", () => {
		const { commit } = props(["a"]);
		fireEvent.click(screen.getByTestId("canvas-lock"));
		expect(commit).toHaveBeenCalledWith([
			{ op: "set", id: "a", key: "locked", value: true },
		]);
	});

	it("agent 写的便签处理后显示「{线程} 已处理」；调节标签有空态", () => {
		props(["n1"]);
		expect(
			screen.getByText(
				t("artifacts.canvas.props.doneBy", { agent: "设计线程" }),
			),
		).toBeInTheDocument();
		fireEvent.click(screen.getByTestId("canvas-props-tab-tweaks"));
		expect(
			screen.getByText(t("artifacts.canvas.props.tweaksPick")),
		).toBeInTheDocument();
	});
});

describe("图层面板", () => {
	it("单击选中并定位，Shift 加选，双击画框改名", () => {
		const onSelect = vi.fn();
		const onRename = vi.fn();
		render(
			<LayersPanel
				doc={ONE}
				canvasKey="c1"
				selected={["a"]}
				onSelect={onSelect}
				onRename={onRename}
				onClose={vi.fn()}
				overlay={false}
			/>,
		);
		const tree = screen.getByRole("tree");
		const rowB = within(tree).getByRole("treeitem", { name: /^B/ });
		fireEvent.click(rowB, { shiftKey: true });
		expect(onSelect).toHaveBeenLastCalledWith(["a", "b"], "b");
		fireEvent.click(rowB);
		expect(onSelect).toHaveBeenLastCalledWith(["b"], "b");
		fireEvent.doubleClick(rowB);
		const input = screen.getByRole("textbox", {
			name: t("artifacts.canvas.layers.rename"),
		});
		fireEvent.change(input, { target: { value: "详情页" } });
		fireEvent.keyDown(input, { key: "Enter" });
		expect(onRename).toHaveBeenCalledWith("b", "详情页");
	});
});

describe("画框名称行", () => {
	it("只显示给了回调的按钮，放不下时从右往左隐藏", () => {
		const a = ONE.pages[0];
		if (!a) throw new Error("缺画框");
		const onPng = vi.fn();
		const { rerender } = render(
			<FrameLabel
				page={a}
				left={0}
				top={100}
				width={400}
				selected
				onSelect={vi.fn()}
				onDownloadPng={onPng}
				onFocus={vi.fn()}
			/>,
		);
		expect(screen.queryByTestId("canvas-frame-tweaks")).toBeNull();
		expect(screen.getByTestId("canvas-frame-focus")).toBeInTheDocument();
		fireEvent.click(screen.getByTestId("canvas-frame-png"));
		expect(onPng).toHaveBeenCalled();
		rerender(
			<FrameLabel
				page={a}
				left={0}
				top={100}
				width={80}
				selected
				onSelect={vi.fn()}
				onDownloadPng={onPng}
				onFocus={vi.fn()}
			/>,
		);
		expect(screen.getByTestId("canvas-frame-png")).toBeInTheDocument();
		expect(screen.queryByTestId("canvas-frame-focus")).toBeNull();
	});
});
