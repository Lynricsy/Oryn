/**
 * task 工具卡片的 agent 头像（M13 R12 / R9）：参数卡用 `unknown`，结果卡按结果状态；分享页只用公开身份里的
 * 自定义头像，站内宿主缺省时只用默认字形。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it } from "vitest";
import type { Schemas } from "../../api/client";
import {
	TranscriptContext,
	type TranscriptContextValue,
} from "../../components/transcript/context";
import { ToolCard } from "../../components/transcript/MessageView";
import { agentDefaultGlyph } from "../../lib/avatar-defaults";
import type { JsonRecord } from "../../state/message-builder";
import { ToolView } from "../ToolView";

const IMAGE = "data:image/png;base64,iVBORw0KGgo=";

function shareCtx(
	agentFaces: readonly Schemas["PublicAgentIdentity"][],
	toolResults = new Map<string, JsonRecord>(),
): TranscriptContextValue {
	return {
		threadId: "share:x",
		toolResults,
		liveTools: {},
		subagents: {},
		openSubagent: () => undefined,
		forkFrom: () => undefined,
		labelEntry: () => undefined,
		readOnly: true,
		agentFaces,
	};
}

function wrap(ctx: TranscriptContextValue, node: ReactNode) {
	// 与分享页一样：转录子树的查询一律不发请求
	const client = new QueryClient({
		defaultOptions: { queries: { enabled: false, retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<TranscriptContext.Provider value={ctx}>
				{node}
			</TranscriptContext.Provider>
		</QueryClientProvider>,
	);
}

describe("task 卡片的 agent 头像", () => {
	it("分享页：设了自定义头像的 agent 渲染为图片，参数卡状态为 unknown", () => {
		const { container } = wrap(
			shareCtx([{ name: "scout", avatarImage: IMAGE }]),
			<ToolCard
				toolCallId="c1"
				name="task"
				args={{ tasks: [{ name: "Alpha", agent: "scout", task: "看一眼" }] }}
			/>,
		);
		const img = container.querySelector('img[data-avatar="image"]');
		expect(img).not.toBeNull();
		expect(img).toHaveAttribute("src", IMAGE);
		expect(img?.closest("[data-state]")).toHaveAttribute(
			"data-state",
			"unknown",
		);
		expect(img?.closest("[data-state]")).toHaveAttribute("title", "scout");
	});

	it("分享页：公开身份里没有的 agent 用默认字形，不显示别的 agent 的头像", () => {
		const { container } = wrap(
			shareCtx([{ name: "scout", avatarImage: IMAGE }]),
			<ToolCard
				toolCallId="c1"
				name="task"
				args={{ tasks: [{ name: "Beta", agent: "reviewer", task: "审一下" }] }}
			/>,
		);
		expect(container.querySelector('img[data-avatar="image"]')).toBeNull();
		const face = container.querySelector('[data-avatar="default"]');
		expect(face).toHaveTextContent(agentDefaultGlyph("reviewer"));
	});

	it("结果卡按结果状态取头像状态，tooltip 是 agent 名与状态", () => {
		const results = new Map<string, JsonRecord>([
			[
				"c1",
				{
					role: "toolResult",
					toolCallId: "c1",
					content: [{ type: "text", text: "ok" }],
					details: {
						results: [
							{ id: "Alpha", agent: "scout", exitCode: 0, output: "done" },
							{ id: "Beta", agent: "scout", exitCode: 1, output: "" },
							{ id: "Gamma", agent: "scout", exitCode: 1, aborted: true },
						],
					},
				},
			],
		]);
		const { container } = wrap(
			shareCtx([{ name: "scout", avatarIcon: "S" }], results),
			<ToolCard
				toolCallId="c1"
				name="task"
				args={{
					tasks: [
						{ name: "Alpha", agent: "scout", task: "a" },
						{ name: "Beta", agent: "scout", task: "b" },
						{ name: "Gamma", agent: "scout", task: "c" },
					],
				}}
			/>,
		);
		fireEvent.click(container.querySelector(".tv-head") as HTMLElement);
		// 结果行的行首是「头像 + 子 agent id」；参数行的头像在值里，行首只有 id
		const stateOf = (id: string) =>
			[...container.querySelectorAll(".tv-row-key")]
				.find(
					(k) => k.textContent?.includes(id) && k.querySelector("[data-state]"),
				)
				?.querySelector("[data-state]");
		expect(stateOf("Alpha")).toHaveAttribute("data-state", "done");
		expect(stateOf("Alpha")).toHaveAttribute("title", "scout · 已完成");
		expect(stateOf("Beta")).toHaveAttribute("data-state", "failed");
		expect(stateOf("Gamma")).toHaveAttribute("data-state", "aborted");
		// 自定义字形头像（公开身份里的 avatarIcon）
		expect(container.querySelector('[data-avatar="icon"]')).toHaveTextContent(
			"S",
		);
	});

	it("宿主不提供头像时（导出、测试）只用默认字形，不需要查询客户端", () => {
		const { container } = render(
			<ToolView
				name="task"
				args={{ agent: "scout", task: "看一眼" }}
				defaultOpen
			/>,
		);
		const face = container.querySelector('[data-avatar="default"]');
		expect(face).toHaveTextContent(agentDefaultGlyph("scout"));
		expect(face?.closest("[data-state]")).toHaveAttribute(
			"data-state",
			"unknown",
		);
	});
});
