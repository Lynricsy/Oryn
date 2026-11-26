import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import { SILENT_ABORT_MARKER } from "../../lib/custom-messages";
import type { JsonRecord } from "../../state/message-builder";
import { TranscriptContext, type TranscriptContextValue } from "./context";
import { CompactionCard, MessageView } from "./MessageView";
import { layoutTurns } from "./Transcript";

function makeCtx(
	overrides: Partial<TranscriptContextValue> = {},
): TranscriptContextValue {
	return {
		threadId: "T-test",
		toolResults: new Map(),
		liveTools: {},
		subagents: {},
		openSubagent: vi.fn(),
		forkFrom: vi.fn(),
		rewindTo: vi.fn(),
		labelEntry: vi.fn(),
		readOnly: true,
		...overrides,
	};
}

function renderInTranscript(
	ui: ReactNode,
	ctx: TranscriptContextValue = makeCtx(),
) {
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter>
				<TranscriptContext.Provider value={ctx}>
					{ui}
				</TranscriptContext.Provider>
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

describe("AdvisorCard 与自定义消息渲染", () => {
	it("单条 advisor 消息显示严重度卡片与正文，即使没有 details 也不会被 <advisory> XML 吞空", () => {
		const msg: JsonRecord = {
			role: "custom",
			customType: "advisor",
			display: true,
			content:
				'<advisory severity="concern" guidance="weigh, don\'t blindly obey">\n构建脚本第 42 行漏掉了错误检查 `set -e`。\n</advisory>',
		};
		renderInTranscript(<MessageView message={msg} live={false} />);
		const card = screen.getByTestId("advisor-card");
		expect(card.getAttribute("data-severity")).toBe("concern");
		expect(screen.getByText(/构建脚本第 42 行漏掉了错误检查/)).toBeTruthy();
		expect(screen.getByText("set -e")).toBeTruthy();
		// 不应把原始 <advisory> 标签漏到页面里
		expect(card.textContent).not.toContain("<advisory");
	});

	it("多条 advisor 消息按最重严重度着色、展示分级计数与多顾问来源，超过 3 条默认折叠并可展开", () => {
		const msg: JsonRecord = {
			role: "custom",
			customType: "advisor",
			display: true,
			content: "<advisory>fallback</advisory>",
			details: {
				notes: [
					{
						note: "直接拼接 SQL 存在注入风险",
						severity: "blocker",
						advisor: "security",
						turnsAgo: 1,
					},
					{
						note: "此处缺少事务回滚处理",
						severity: "concern",
						advisor: "default",
					},
					{
						note: "变量命名可以更清晰",
						severity: "nit",
						advisor: "security",
					},
					{
						note: "第四条折叠的建议正文",
						severity: "nit",
						advisor: "perf",
					},
				],
			},
		};
		renderInTranscript(<MessageView message={msg} live={false} />);
		const card = screen.getByTestId("advisor-card");
		expect(card.getAttribute("data-severity")).toBe("blocker");
		// 默认显示前 3 条，第 4 条收起
		expect(screen.getAllByTestId("advisor-note")).toHaveLength(3);
		expect(screen.queryByText("第四条折叠的建议正文")).toBeNull();
		// 多来源时展示顾问名
		expect(screen.getAllByText("security").length).toBeGreaterThanOrEqual(1);
		// 点击展开后显示第 4 条
		const toggle = screen.getByRole("button", {
			name: /展开其余 1 条建议|Show 1 more note/,
		});
		fireEvent.click(toggle);
		expect(screen.getAllByTestId("advisor-note")).toHaveLength(4);
		expect(screen.getByText("第四条折叠的建议正文")).toBeTruthy();
		expect(screen.getByText("perf")).toBeTruthy();
	});

	it("通用 CustomMessage 包裹在 <system-notice> 等 XML 标签内时不再被 Markdown 吞空", () => {
		const msg: JsonRecord = {
			role: "custom",
			customType: "some-extension-notice",
			display: true,
			content:
				'<system-notice id="ext">\n重要扩展提示：不要跳过校验。\n</system-notice>',
		};
		renderInTranscript(<MessageView message={msg} live={false} />);
		expect(screen.getByText(/重要扩展提示：不要跳过校验/)).toBeTruthy();
	});

	it("长通用 CustomMessage 折叠摘要跳过带属性的 XML 开标签", () => {
		const lines = Array.from({ length: 10 }, (_, i) => `第 ${i + 1} 行内容`);
		const msg: JsonRecord = {
			role: "custom",
			customType: "verbose-ext",
			display: true,
			content: `<system-notice id="prelude-guide">\n${lines.join("\n")}\n</system-notice>`,
		};
		renderInTranscript(<MessageView message={msg} live={false} />);
		expect(screen.getByText("第 1 行内容")).toBeTruthy();
		expect(screen.queryByText(/<system-notice/)).toBeNull();
	});

	it("lsp-late-diagnostic 渲染成迟到诊断卡并显示预览行与展开详情", () => {
		const msg: JsonRecord = {
			role: "custom",
			customType: "lsp-late-diagnostic",
			display: true,
			content: "<system-notice>\nLate LSP diagnostics\n</system-notice>",
			details: {
				files: [
					{
						path: "src/app.ts",
						summary: "1 error",
						errored: true,
						messages: ["src/app.ts:12:5 [error] Cannot find name 'foo'"],
					},
				],
			},
		};
		renderInTranscript(<MessageView message={msg} live={false} />);
		const card = screen.getByTestId("late-diagnostics-card");
		expect(card.getAttribute("data-errored")).toBe("true");
		expect(screen.getByText(/Cannot find name 'foo'/)).toBeTruthy();
	});

	it("skill-prompt 直接突出用户指令并将完整技能提示词折叠收起", () => {
		const msg: JsonRecord = {
			role: "custom",
			customType: "skill-prompt",
			display: true,
			content: "[IMPORTANT: User invoked skill]\n\n很长的技能说明正文",
			details: {
				name: "code-review",
				prompt: "/skill:code-review 请重点检查并发安全",
				lineCount: 88,
			},
		};
		renderInTranscript(<MessageView message={msg} live={false} />);
		expect(screen.getByTestId("skill-prompt-card")).toBeTruthy();
		expect(screen.getByText("/skill:code-review")).toBeTruthy();
		expect(screen.getByText("请重点检查并发安全")).toBeTruthy();
		expect(screen.queryByText(/很长的技能说明正文/)).toBeNull();
		fireEvent.click(
			screen.getByRole("button", { name: /展开技能提示词|Show skill prompt/ }),
		);
		expect(screen.getByText(/很长的技能说明正文/)).toBeTruthy();
	});

	it("handoff 剥离 <handoff-context> 标签后可正常展开阅读", () => {
		const msg: JsonRecord = {
			role: "custom",
			customType: "handoff",
			display: true,
			content:
				"<handoff-context>\n已完成数据库迁移，接下来补回归测试。\n</handoff-context>",
		};
		renderInTranscript(<MessageView message={msg} live={false} />);
		const btn = screen.getByRole("button", { name: /会话交接|Handed off/ });
		fireEvent.click(btn);
		expect(screen.getByText(/已完成数据库迁移，接下来补回归测试/)).toBeTruthy();
	});

	it("irc:incoming 渲染发送方与正文，不被 <irc> 标签吞空", () => {
		const msg: JsonRecord = {
			role: "custom",
			customType: "irc:incoming",
			display: true,
			content: "<irc>\nraw\n</irc>",
			details: {
				from: "scout-1",
				fromLabel: "Scout",
				message: "已经定位到配置文件位置",
			},
		};
		renderInTranscript(<MessageView message={msg} live={false} />);
		expect(screen.getByTestId("irc-message-card")).toBeTruthy();
		expect(screen.getByText("Scout")).toBeTruthy();
		expect(screen.getByText("已经定位到配置文件位置")).toBeTruthy();
	});

	it("AssistantMessage 遇到 Core 静默中止哨兵时不显示中断提示框", () => {
		const silent: JsonRecord = {
			role: "assistant",
			stopReason: "aborted",
			errorMessage: SILENT_ABORT_MARKER,
			content: [{ type: "text", text: "切入执行阶段" }],
		};
		renderInTranscript(<MessageView message={silent} live={false} />);
		expect(screen.getByText("切入执行阶段")).toBeTruthy();
		expect(screen.queryByText(/已中断|Aborted/)).toBeNull();
	});

	it("CompactionCard 存在 warning 时在展开体展示警告", () => {
		renderInTranscript(
			<CompactionCard
				entry={{
					tokensBefore: 120000,
					tokensAfter: 18000,
					warning: "部分旧工具输出被截断",
					summary: "摘要正文 <placeholder>",
				}}
			/>,
		);
		fireEvent.click(
			screen.getByRole("button", { name: /上下文已压缩|Context compacted/ }),
		);
		expect(screen.getByText("部分旧工具输出被截断")).toBeTruthy();
		expect(screen.getByText(/<placeholder>/)).toBeTruthy();
	});

	it("layoutTurns 将 skill-prompt 视作回合边界", () => {
		const u = { input: 100, output: 10, cacheRead: 0, cost: 0 };
		const items = [
			{
				kind: "message" as const,
				key: "e1",
				seq: 1,
				message: {
					role: "assistant",
					content: [{ type: "text", text: "a" }],
					usage: { ...u, cost: { total: 0 } },
				},
				live: false,
			},
			{
				kind: "custom_message" as const,
				key: "e2",
				seq: 2,
				entry: {
					customType: "skill-prompt",
					display: true,
					content: "body",
					details: { name: "review" },
				},
			},
			{
				kind: "message" as const,
				key: "e3",
				seq: 3,
				message: {
					role: "assistant",
					content: [{ type: "text", text: "b" }],
					usage: { ...u, cost: { total: 0 } },
				},
				live: false,
			},
		];
		const { starts, footers } = layoutTurns(items);
		expect([...starts]).toEqual([0, 2]);
		expect([...footers.keys()]).toEqual([0, 2]);
	});
});
