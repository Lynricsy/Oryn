/**
 * 通用渲染（未知工具兜底，也是 MCP / 设备文档 / 宿主工具的结果底座）：参数是可折叠的 JSON 树，
 * 结果按 JSON 树 / Markdown / 文本展示，出错首行醒目，成功却没有输出时给空状态；残缺参数不崩。
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { looksLikeMarkdown } from "./generic";
import { resolveToolRenderer } from "./registry";
import { ToolView } from "./ToolView";
import type { ToolResultLike } from "./types";

const ok = (text: string): ToolResultLike => ({
	content: [{ type: "text", text }],
});

describe("通用渲染", () => {
	it("未知工具走通用渲染：摘要是 key=value，展开体逐行列出参数", () => {
		const { container } = render(
			<ToolView
				name="acme_deploy"
				args={{ service: "web", dryRun: false, env: { name: "pr-1" } }}
				result={ok("done")}
				defaultOpen
			/>,
		);
		expect(container.querySelector(".tv-sum")).toHaveTextContent(
			'service=web dryRun=false env={"name":"pr-1"}',
		);
		const input = container.querySelector(".tv-sec--in");
		expect(input).toHaveTextContent('"service": "web"');
		expect(input).toHaveTextContent('"dryRun": false');
	});

	it("JSON 结果是可折叠树：深层先收起成项数，点开后可见；数组结果的项数在读数里", () => {
		const value = [
			{ id: 1, tags: { a: "x".repeat(40), b: "y".repeat(40) } },
			{ id: 2 },
		];
		const { container } = render(
			<ToolView
				name="acme_list"
				args={{}}
				result={ok(JSON.stringify(value))}
				defaultOpen
			/>,
		);
		expect(container.querySelector(".tv-meta")).toHaveTextContent("2 项");
		const out = container.querySelector(".tv-sec--out");
		// 第三层的 tags 收起
		expect(out).not.toHaveTextContent("xxxx");
		expect(out).toHaveTextContent("2 个键");
		fireEvent.click(screen.getByRole("button", { name: "2 个键" }));
		expect(out).toHaveTextContent(`"a": "${"x".repeat(40)}"`);
	});

	it("出错：首行醒目，其余弱化；成功但没有输出：空状态", () => {
		const { container, unmount } = render(
			<ToolView
				name="acme_x"
				args={{}}
				result={{
					content: [{ type: "text", text: "boom: denied\nat step 3" }],
					isError: true,
				}}
				defaultOpen
			/>,
		);
		expect(container.querySelector(".tv-errblock-head")).toHaveTextContent(
			"boom: denied",
		);
		expect(container.querySelector(".tv-errblock")).toHaveTextContent(
			"at step 3",
		);
		unmount();
		const empty = render(
			<ToolView name="acme_x" args={{}} result={{ content: [] }} defaultOpen />,
		);
		expect(empty.container.querySelector(".tv-empty")).toHaveTextContent(
			"没有输出",
		);
	});

	it("Markdown 判别：结构性记号一个就够，弱记号要两种以上；日志不算", () => {
		expect(looksLikeMarkdown("# Title\nbody")).toBe(true);
		expect(looksLikeMarkdown("- a **b**")).toBe(true);
		expect(looksLikeMarkdown("Found **2** issues")).toBe(false);
		expect(looksLikeMarkdown("ok: 3 passed\nwarn: 1 skipped")).toBe(false);
	});

	it.each<[string, unknown]>([
		["参数不是对象", "oops"],
		["参数里有 null 与深层数组", { a: null, b: [[[1, [2]]]], c: undefined }],
		["长字符串", { s: "长".repeat(900) }],
	])("残缺输入不崩（%s）", (_label, args) => {
		expect(() =>
			render(
				<ToolView name="acme_y" args={args} result={ok("[]")} defaultOpen />,
			),
		).not.toThrow();
		expect(resolveToolRenderer("acme_y")).toBeDefined();
	});
});
