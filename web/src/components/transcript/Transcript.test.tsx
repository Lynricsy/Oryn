import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { JsonRecord } from "../../state/message-builder";
import type { TranscriptItem } from "../../state/transcript";
import { TranscriptContext, type TranscriptContextValue } from "./context";
import { MessageView, summarizeTurn } from "./MessageView";
import {
	clearPendingPrompt,
	pendingPromptStore,
	setPendingPrompt,
} from "./pending-prompt";
import { layoutTurns } from "./Transcript";

function assistant(
	text: string,
	usage: { input: number; output: number; cacheRead: number; cost: number },
	extra: JsonRecord = {},
): JsonRecord {
	return {
		role: "assistant",
		model: "claude-sonnet-5",
		content: [{ type: "text", text }],
		usage: {
			input: usage.input,
			output: usage.output,
			cacheRead: usage.cacheRead,
			cost: { total: usage.cost },
		},
		...extra,
	};
}

function msg(seq: number, message: JsonRecord, live = false): TranscriptItem {
	return {
		kind: "message",
		key: `e${seq}`,
		seq,
		entryId: `E${seq}`,
		message,
		live,
	};
}

const user = (seq: number) =>
	msg(seq, { role: "user", content: [{ type: "text", text: "hi" }] });

describe("summarizeTurn", () => {
	it("累加同一回合各条助手消息的用量，模型取最后一条", () => {
		const sum = summarizeTurn([
			assistant("a", { input: 1000, output: 10, cacheRead: 0, cost: 0.01 }),
			assistant(
				"b",
				{ input: 2000, output: 20, cacheRead: 500, cost: 0.02 },
				{ model: "other", duration: 1500 },
			),
		]);
		expect(sum).toMatchObject({
			model: "other",
			hasUsage: true,
			input: 3000,
			output: 30,
			cacheRead: 500,
			duration: 1500,
			text: "a\n\nb",
		});
		expect(sum.cost).toBeCloseTo(0.03);
	});
});

describe("layoutTurns", () => {
	const u = { input: 100, output: 1, cacheRead: 0, cost: 0 };
	it("每个回合只在最后一条助手消息挂脚注、只在第一条 agent 行放头像", () => {
		const items: TranscriptItem[] = [
			user(1),
			msg(2, assistant("a", u)),
			{ kind: "meta", key: "e3", seq: 3, entry: { type: "model_change" } },
			msg(4, assistant("b", u)),
			msg(5, assistant("c", u)),
			user(6),
			msg(7, assistant("d", u)),
		];
		const { starts, footers } = layoutTurns(items);
		expect([...starts]).toEqual([1, 6]);
		expect([...footers.keys()]).toEqual([4, 6]);
		expect(footers.get(4)?.input).toBe(300);
		expect(footers.get(6)?.input).toBe(100);
	});

	it("回合还在进行（最后一行是流式消息或运行中工具）时不挂脚注", () => {
		const streaming = layoutTurns([
			user(1),
			msg(2, assistant("a", u)),
			msg(3, assistant("b", u), true),
		]);
		expect(streaming.footers.size).toBe(0);
		const tool = layoutTurns([
			user(1),
			msg(2, assistant("a", u)),
			{ kind: "tool", key: "lt", seq: 3, toolCallId: "x", toolName: "bash" },
		]);
		expect(tool.footers.size).toBe(0);
	});
});

const ctx: TranscriptContextValue = {
	threadId: "T-transcript",
	toolResults: new Map(),
	liveTools: {},
	subagents: {},
	openSubagent: () => {},
	forkFrom: () => {},
	labelEntry: () => {},
};

describe("MessageView 回合脚注与消息菜单", () => {
	it("用量脚注只挂在传入 turn 的消息上；中间消息仍有自己的菜单", () => {
		const m = assistant("x", { input: 1, output: 1, cacheRead: 0, cost: 0 });
		render(
			<TranscriptContext.Provider value={ctx}>
				<MessageView message={m} live={false} entryId="E1" />
				<MessageView
					message={m}
					live={false}
					entryId="E2"
					turn={summarizeTurn([
						assistant("x", {
							input: 18000,
							output: 920,
							cacheRead: 0,
							cost: 0,
						}),
						assistant("y", { input: 2000, output: 80, cacheRead: 0, cost: 0 }),
					])}
				/>
			</TranscriptContext.Provider>,
		);
		expect(screen.getAllByText("输入")).toHaveLength(1);
		expect(screen.getByText("20k")).toBeTruthy();
		expect(screen.getByText("1.0k")).toBeTruthy();
		expect(screen.getAllByRole("button", { name: "消息操作" })).toHaveLength(2);
	});

	it("中间的助手消息的菜单作用于它自己的 entry", () => {
		const forkFrom = vi.fn();
		const labelEntry = vi.fn();
		// jsdom 没有 scrollIntoView；菜单打开后会调用它把弹层滚进可见区
		Element.prototype.scrollIntoView = vi.fn();
		const m = assistant("x", { input: 1, output: 1, cacheRead: 0, cost: 0 });
		render(
			<TranscriptContext.Provider value={{ ...ctx, forkFrom, labelEntry }}>
				<MessageView message={m} live={false} entryId="E-mid" turnStart />
			</TranscriptContext.Provider>,
		);
		const open = () =>
			fireEvent.click(screen.getByRole("button", { name: "消息操作" }));
		open();
		fireEvent.click(screen.getByRole("menuitem", { name: "从这里分叉" }));
		expect(forkFrom).toHaveBeenCalledWith("E-mid");
		open();
		fireEvent.click(screen.getByRole("menuitem", { name: "加标签" }));
		expect(labelEntry).toHaveBeenCalledWith("E-mid");
	});
});

describe("pending prompt", () => {
	it("空文字不写入；清除后不再存在", () => {
		setPendingPrompt("T-p", "");
		expect(pendingPromptStore.getState().prompts["T-p"]).toBeUndefined();
		setPendingPrompt("T-p", "任务");
		expect(pendingPromptStore.getState().prompts["T-p"]).toBe("任务");
		clearPendingPrompt("T-p");
		expect(pendingPromptStore.getState().prompts["T-p"]).toBeUndefined();
	});
});
