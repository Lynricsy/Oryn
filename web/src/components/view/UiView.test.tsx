/** 声明式视图：各类块的渲染、表单校验与锁定、按钮组、流式骨架、只读禁用、已作答回显。 */
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { formatFormAnswer, type ViewAnswer } from "../../lib/view/answer";
import { type FormBlock, normalizeUiSpec } from "../../lib/view/spec";
import { UiView, type UiViewProps } from "./UiView";

const chartMock = vi.hoisted(() => ({
	configs: [] as unknown[],
	destroyed: 0,
}));

vi.mock("chart.js/auto", () => ({
	default: class {
		static defaults = { font: {} };
		constructor(_canvas: unknown, config: unknown) {
			chartMock.configs.push(config);
		}
		destroy() {
			chartMock.destroyed++;
		}
	},
}));

const SVG =
	'<svg viewBox="0 0 10 10" xmlns="http://www.w3.org/2000/svg"><rect width="10" height="10" style="fill:var(--c1)"/><script>alert(1)</script></svg>';

const ALL_BLOCKS = {
	title: "周报",
	description: "本周概况",
	blocks: [
		{ type: "heading", text: "概览", level: 1 },
		{
			type: "text",
			text: "**加粗**正文 ![图](https://example.com/a.png) <b>内嵌</b>",
		},
		{ type: "badges", items: [{ label: "已上线", tone: "success" }] },
		{ type: "alert", tone: "danger", title: "注意", text: "磁盘快满了" },
		{ type: "divider" },
		{
			type: "stats",
			items: [{ label: "请求数", value: 12840, delta: "+3%", trend: "up" }],
		},
		{ type: "keyValue", items: [{ label: "区域", value: "上海" }] },
		{
			type: "table",
			caption: "按端",
			columns: [
				{ key: "c", label: "客户端" },
				{ key: "ok", label: "达标", align: "center" },
			],
			rows: [{ c: "Web", ok: true }],
		},
		{
			type: "list",
			ordered: true,
			items: [{ title: "第一步", description: "说明", meta: "a1" }],
		},
		{ type: "progress", label: "完成度", value: 40 },
		{ type: "image", src: "https://example.com/p.png", alt: "截图" },
		{
			type: "chart",
			chartType: "bar",
			title: "趋势",
			data: { labels: ["a", "b"], datasets: [{ label: "x", data: [1, 2] }] },
		},
		{ type: "svg", svg: SVG, caption: "示意" },
		{
			type: "card",
			title: "卡片",
			blocks: [{ type: "text", text: "卡片正文" }],
		},
		{
			type: "columns",
			columns: [
				{ blocks: [{ type: "text", text: "左栏" }] },
				{ blocks: [{ type: "text", text: "右栏" }] },
			],
		},
		{
			type: "tabs",
			tabs: [
				{ label: "甲", blocks: [{ type: "text", text: "甲页" }] },
				{ label: "乙", blocks: [{ type: "text", text: "乙页" }] },
			],
		},
	],
};

const FORM = {
	blocks: [
		{
			type: "form",
			title: "选项",
			submitLabel: "确定",
			fields: [
				{ name: "who", label: "姓名", kind: "text", required: true },
				{ name: "n", label: "数量", kind: "number", min: 5, max: 10 },
				{
					name: "pick",
					label: "颜色",
					kind: "radio",
					options: [
						{ label: "红", value: "r" },
						{ label: "蓝", value: "b" },
					],
					defaultValue: "b",
				},
				{ name: "ok", label: "同意", kind: "switch", defaultValue: true },
			],
		},
	],
};

const ACTIONS = {
	blocks: [
		{
			type: "actions",
			buttons: [
				{ label: "好", prompt: "就这么办" },
				{ label: "不", prompt: "先别动", variant: "primary" },
			],
		},
	],
};

function spec(raw: unknown) {
	const s = normalizeUiSpec(raw);
	if (!s) throw new Error("规格无效");
	return s;
}

function renderView(raw: unknown, props: Partial<UiViewProps> = {}) {
	return render(
		<UiView
			spec={spec(raw)}
			toolCallId="toolu_1"
			answers={new Map()}
			interactive
			{...props}
		/>,
	);
}

describe("UiView 块渲染", () => {
	it("18 种块都能渲染出来", async () => {
		const { container } = renderView(ALL_BLOCKS);
		expect(screen.getByRole("region", { name: "周报" })).toBeInTheDocument();
		expect(screen.getByText("本周概况")).toBeInTheDocument();
		expect(screen.getByRole("heading", { name: "概览" })).toBeInTheDocument();
		expect(screen.getByText("加粗").tagName).toBe("STRONG");
		expect(screen.getByText("已上线")).toBeInTheDocument();
		expect(screen.getByText("磁盘快满了")).toBeInTheDocument();
		expect(container.querySelector("hr")).not.toBeNull();
		expect(screen.getByText("12,840")).toBeInTheDocument();
		expect(screen.getByRole("img", { name: "上升" })).toBeInTheDocument();
		expect(screen.getByText("上海")).toBeInTheDocument();
		expect(screen.getByRole("columnheader", { name: "客户端" })).toBeVisible();
		expect(screen.getByRole("img", { name: "是" })).toBeInTheDocument();
		expect(screen.getByText("第一步")).toBeInTheDocument();
		expect(screen.getByRole("meter", { name: "完成度" })).toBeInTheDocument();
		expect(screen.getByRole("img", { name: "截图" })).toHaveAttribute(
			"referrerpolicy",
			"no-referrer",
		);
		expect(screen.getByText("卡片正文")).toBeInTheDocument();
		expect(screen.getByText("左栏")).toBeInTheDocument();
		expect(screen.getByText("右栏")).toBeInTheDocument();
		// svg：清洗后挂载，脚本被剔除，容器带主题变量
		const svgHost = screen.getByRole("img", { name: "示意" });
		expect(svgHost.querySelector("svg rect")).not.toBeNull();
		expect(svgHost.querySelector("script")).toBeNull();
		expect(svgHost.parentElement?.style.getPropertyValue("--c1")).toBe(
			"var(--viz-1)",
		);
		// chart：懒加载后建图，没给颜色的数据集按调色板上色
		await waitFor(() => expect(chartMock.configs).toHaveLength(1));
		const config = chartMock.configs[0] as {
			type: string;
			data: { datasets: { backgroundColor?: string }[] };
		};
		expect(config.type).toBe("bar");
		expect(config.data.datasets[0]?.backgroundColor).toMatch(/^#/);
		expect(screen.getByRole("img", { name: "趋势" }).tagName).toBe("CANVAS");
	});

	it("Markdown 正文不加载图片、不渲染内嵌 HTML", () => {
		const { container } = renderView({
			blocks: [
				{
					type: "text",
					text: "看图 ![图](https://example.com/a.png) <b>粗</b>",
				},
			],
		});
		expect(container.querySelector("img")).toBeNull();
		expect(container.querySelector("b")).toBeNull();
		expect(screen.getByRole("link", { name: "图" })).toHaveAttribute(
			"href",
			"https://example.com/a.png",
		);
	});

	it("标签页：点另一页显示该页内容，其它页隐藏但保持挂载", () => {
		renderView(ALL_BLOCKS);
		expect(screen.getByText("甲页")).toBeVisible();
		expect(screen.getByText("乙页")).not.toBeVisible();
		fireEvent.click(screen.getByRole("tab", { name: "乙" }));
		expect(screen.getByText("乙页")).toBeVisible();
		expect(screen.getByText("甲页")).not.toBeVisible();
		expect(screen.getByRole("tab", { name: "乙" })).toHaveAttribute(
			"aria-selected",
			"true",
		);
	});

	it("流式中 chart 与 svg 只显示骨架", () => {
		const before = chartMock.configs.length;
		const { container } = renderView(ALL_BLOCKS, { streaming: true });
		expect(container.querySelector("canvas")).toBeNull();
		expect(container.querySelector("svg rect")).toBeNull();
		expect(chartMock.configs).toHaveLength(before);
	});

	it("卸载时销毁图表", async () => {
		const before = chartMock.configs.length;
		const { unmount } = renderView({ blocks: [ALL_BLOCKS.blocks[11]] });
		await waitFor(() => expect(chartMock.configs).toHaveLength(before + 1));
		const destroyed = chartMock.destroyed;
		unmount();
		expect(chartMock.destroyed).toBe(destroyed + 1);
	});
});

describe("UiView 表单", () => {
	it("必填与数值范围不通过时不提交", () => {
		const onAnswer = vi.fn(async () => {});
		renderView(FORM, { onAnswer });
		fireEvent.click(screen.getByRole("button", { name: "确定" }));
		expect(screen.getByText("请填写这一项")).toBeInTheDocument();
		expect(screen.getByLabelText(/姓名/)).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		fireEvent.change(screen.getByLabelText(/姓名/), {
			target: { value: "阿青" },
		});
		fireEvent.change(screen.getByLabelText("数量"), {
			target: { value: "3" },
		});
		fireEvent.click(screen.getByRole("button", { name: "确定" }));
		expect(screen.getByText("不能小于 5")).toBeInTheDocument();
		expect(onAnswer).not.toHaveBeenCalled();
	});

	it("提交后锁定并显示提交的值", async () => {
		const onAnswer = vi.fn(async () => {});
		renderView(FORM, { onAnswer });
		fireEvent.change(screen.getByLabelText(/姓名/), {
			target: { value: "阿青" },
		});
		fireEvent.change(screen.getByLabelText("数量"), {
			target: { value: "7" },
		});
		fireEvent.click(screen.getByLabelText("红"));
		fireEvent.click(screen.getByRole("button", { name: "确定" }));
		const form = spec(FORM).blocks[0] as FormBlock;
		const values = { who: "阿青", n: 7, pick: "r", ok: true };
		expect(onAnswer).toHaveBeenCalledWith(
			{ block: "0", kind: "form", values },
			formatFormAnswer(form, values),
		);
		await screen.findByText("已提交");
		expect(screen.queryByRole("button", { name: "确定" })).toBeNull();
		expect(screen.getByLabelText(/姓名/)).toBeDisabled();
		expect(screen.getByLabelText(/姓名/)).toHaveValue("阿青");
		expect(screen.getByLabelText("红")).toBeChecked();
	});

	it("提交失败时解锁并显示错误", async () => {
		const onAnswer = vi.fn(async () => {
			throw new Error("断线了");
		});
		renderView(FORM, { onAnswer });
		fireEvent.change(screen.getByLabelText(/姓名/), {
			target: { value: "阿青" },
		});
		await act(async () => {
			fireEvent.click(screen.getByRole("button", { name: "确定" }));
		});
		expect(await screen.findByRole("alert")).toHaveTextContent("断线了");
		expect(screen.getByRole("button", { name: "确定" })).toBeEnabled();
		expect(screen.getByLabelText(/姓名/)).toBeEnabled();
	});

	it("answers 里已有作答时显示作答并锁定", () => {
		const answers = new Map<string, ViewAnswer>([
			[
				"0",
				{
					block: "0",
					kind: "form",
					values: { who: "小周", n: 9, pick: "r", ok: false },
				},
			],
		]);
		renderView(FORM, { answers, onAnswer: vi.fn() });
		expect(screen.getByText("已提交")).toBeInTheDocument();
		expect(screen.getByLabelText(/姓名/)).toHaveValue("小周");
		expect(screen.getByLabelText("数量")).toHaveValue(9);
		expect(screen.getByLabelText("红")).toBeChecked();
		expect(screen.getByRole("switch")).not.toBeChecked();
		expect(screen.getByRole("switch")).toBeDisabled();
	});
});

describe("UiView 按钮组", () => {
	it("点一次后整组锁定，选中的那个标为按下", async () => {
		const onAnswer = vi.fn(async () => {});
		renderView(ACTIONS, { onAnswer });
		fireEvent.click(screen.getByRole("button", { name: "不" }));
		expect(onAnswer).toHaveBeenCalledWith(
			{ block: "0", kind: "action", choice: 1 },
			"先别动",
		);
		await screen.findByRole("status");
		expect(screen.getByRole("button", { name: "不" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.getByRole("button", { name: "好" })).toBeDisabled();
		fireEvent.click(screen.getByRole("button", { name: "不" }));
		fireEvent.click(screen.getByRole("button", { name: "好" }));
		expect(onAnswer).toHaveBeenCalledTimes(1);
	});

	it("answers 里已有作答时显示选中的按钮", () => {
		const answers = new Map<string, ViewAnswer>([
			["0", { block: "0", kind: "action", choice: 0 }],
		]);
		renderView(ACTIONS, { answers, onAnswer: vi.fn() });
		expect(screen.getByRole("button", { name: "好" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.getByRole("button", { name: "不" })).toBeDisabled();
	});
});

describe("UiView 不可交互", () => {
	it("interactive=false 时所有控件禁用", () => {
		const onAnswer = vi.fn(async () => {});
		renderView(
			{ blocks: [...FORM.blocks, ...ACTIONS.blocks] },
			{ interactive: false, onAnswer },
		);
		for (const el of [
			screen.getByLabelText(/姓名/),
			screen.getByLabelText("数量"),
			screen.getByLabelText("红"),
			screen.getByRole("switch"),
			screen.getByRole("button", { name: "确定" }),
			screen.getByRole("button", { name: "好" }),
			screen.getByRole("button", { name: "不" }),
		])
			expect(el).toBeDisabled();
		fireEvent.click(screen.getByRole("button", { name: "好" }));
		expect(onAnswer).not.toHaveBeenCalled();
	});

	it("没有 onAnswer（只读页）时同样禁用", () => {
		renderView(ACTIONS);
		expect(screen.getByRole("button", { name: "好" })).toBeDisabled();
	});
});
