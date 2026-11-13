import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../../api/client";
import { qk } from "../../api/queries";
import type { SessionState } from "../../omp/types";
import type { JsonRecord } from "../../state/message-builder";
import { getThreadStore, indexStore } from "../../state/stores";
import type { TranscriptItem } from "../../state/transcript";
import { TranscriptContext, type TranscriptContextValue } from "./context";
import { MessageView, summarizeTurn } from "./MessageView";
import {
	clearPendingPrompt,
	pendingPromptStore,
	setPendingPrompt,
} from "./pending-prompt";
import { layoutTurns, Transcript } from "./Transcript";

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
	it("累加同一回合各条助手消息的用量", () => {
		const sum = summarizeTurn([
			assistant("a", { input: 1000, output: 10, cacheRead: 0, cost: 0.01 }),
			assistant(
				"b",
				{ input: 2000, output: 20, cacheRead: 500, cost: 0.02 },
				{ model: "other", duration: 1500 },
			),
		]);
		expect(sum).toMatchObject({
			hasUsage: true,
			input: 3000,
			output: 30,
			cacheRead: 500,
			duration: 1500,
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

	it("连续的用户消息只有第一条放用户头像", () => {
		const { userStarts } = layoutTurns([
			user(1),
			user(2),
			msg(3, assistant("a", u)),
			user(4),
			{ kind: "meta", key: "e5", seq: 5, entry: { type: "model_change" } },
			user(6),
			user(7),
		]);
		expect([...userStarts]).toEqual([0, 3, 5]);
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

	it("回合模型取第一条带模型的助手消息，挂在回合起点", () => {
		const { models } = layoutTurns([
			user(1),
			{ kind: "tool", key: "lt", seq: 2, toolCallId: "x", toolName: "bash" },
			msg(
				3,
				assistant("a", u, { model: "claude-opus-5-5", provider: "anthropic" }),
			),
			msg(4, assistant("b", u, { model: "other" })),
			user(5),
			msg(6, { role: "assistant", content: [] }, true),
		]);
		expect([...models.entries()]).toEqual([
			[1, { model: "claude-opus-5-5", provider: "anthropic" }],
		]);
	});

	it("流式增量只重算进行中的回合：历史回合的汇总对象引用不变，尾回合结束后汇总正确", () => {
		// buildTranscript 每次都新建 item 包装，但历史条目的 message 对象引用稳定
		const history = [
			{ role: "user", content: [{ type: "text", text: "hi" }] },
			assistant("a", u),
			assistant("b", { input: 200, output: 2, cacheRead: 0, cost: 0 }),
			{ role: "user", content: [{ type: "text", text: "again" }] },
			assistant("c", { input: 5, output: 5, cacheRead: 0, cost: 0 }),
		];
		const build = (tail: JsonRecord, live: boolean): TranscriptItem[] => [
			...history.map((m, i) => msg(i + 1, m)),
			msg(history.length + 1, tail, live),
		];
		const first = layoutTurns(build(assistant("d", u), true));
		// 下一个流式增量：尾消息换成新对象（message-builder 每次克隆）
		const second = layoutTurns(build(assistant("de", u), true));
		expect(first.footers.get(2)).toBeDefined();
		expect(second.footers.get(2)).toBe(first.footers.get(2));
		expect(second.footers.get(2)?.input).toBe(300);
		expect(second.footers.has(5)).toBe(false);

		const ended = layoutTurns(
			build(
				assistant("def", { input: 7, output: 1, cacheRead: 0, cost: 0 }),
				false,
			),
		);
		expect(ended.footers.get(2)).toBe(first.footers.get(2));
		expect(ended.footers.get(5)).toMatchObject({ input: 12, output: 6 });
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

	it("回合脚注的成本只有一个 $，也不再重复写模型名", () => {
		const u = { input: 1, output: 1, cacheRead: 0, cost: 0.18 };
		const turn = summarizeTurn([assistant("x", u)]);
		const { container } = render(
			<TranscriptContext.Provider value={ctx}>
				<MessageView message={assistant("x", u)} live={false} turn={turn} />
			</TranscriptContext.Provider>,
		);
		const cost = screen.getByTitle("成本");
		expect(cost.textContent).toBe("成本$0.18");
		expect(cost.querySelector("svg")).toBeNull();
		expect(container.textContent).not.toContain("claude-sonnet-5");
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

	it("回合脚注的「复制」在点击时给出整回合的助手正文", () => {
		const writeText = vi.fn(() => Promise.resolve());
		Object.defineProperty(navigator, "clipboard", {
			configurable: true,
			value: { writeText },
		});
		Element.prototype.scrollIntoView = vi.fn();
		const u = { input: 1, output: 1, cacheRead: 0, cost: 0 };
		const turn = summarizeTurn([assistant("a", u), assistant("b", u)]);
		render(
			<TranscriptContext.Provider value={ctx}>
				<MessageView message={assistant("b", u)} live={false} turn={turn} />
			</TranscriptContext.Provider>,
		);
		fireEvent.click(screen.getByRole("button", { name: "消息操作" }));
		fireEvent.click(screen.getByRole("menuitem", { name: "复制" }));
		expect(writeText).toHaveBeenCalledWith("a\n\nb");
	});

	it("用户回合起点的气泡右侧有头像，但头像不进入无障碍树", () => {
		const client = new QueryClient({
			defaultOptions: {
				queries: { retry: false, staleTime: Number.POSITIVE_INFINITY },
			},
		});
		client.setQueryData(qk.userIdentity, { avatarIcon: "🦊" });
		client.setQueryData(qk.gitIdentity, {});
		const m: JsonRecord = {
			role: "user",
			content: [{ type: "text", text: "你好" }],
		};
		const { container } = render(
			<QueryClientProvider client={client}>
				<TranscriptContext.Provider value={ctx}>
					<div data-testid="start">
						<MessageView message={m} live={false} entryId="U1" turnStart />
					</div>
					<div data-testid="next">
						<MessageView message={m} live={false} entryId="U2" />
					</div>
				</TranscriptContext.Provider>
			</QueryClientProvider>,
		);
		const avatars = container.querySelectorAll('[data-avatar-of="user"]');
		expect(avatars).toHaveLength(1);
		expect(screen.getByTestId("start").contains(avatars[0] ?? null)).toBe(true);
		expect(avatars[0]?.closest('[aria-hidden="true"]')).not.toBeNull();
		// 「你」仍是 sr-only
		expect(screen.getAllByText("你")[0]?.className).toContain("sr-only");
		expect(screen.getAllByRole("button", { name: "消息操作" })).toHaveLength(2);
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

describe("助手头像闪烁", () => {
	const items: TranscriptItem[] = [
		user(1),
		msg(2, assistant("a", { input: 1, output: 1, cacheRead: 0, cost: 0 })),
		user(3),
		msg(4, assistant("b", { input: 1, output: 1, cacheRead: 0, cost: 0 })),
		msg(5, { role: "assistant", content: [] }, true),
	];

	afterEach(() => {
		vi.restoreAllMocks();
		indexStore.setState({ threads: {} });
	});

	function renderTranscript(state: Thread["state"]) {
		// jsdom 没有布局：给虚拟列表一个足够高的视口，所有行都挂载
		vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(
			4000,
		);
		vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(800);
		indexStore.setState({
			threads: { [ctx.threadId]: { id: ctx.threadId, state } as Thread },
		});
		const client = new QueryClient({
			defaultOptions: {
				queries: { retry: false, staleTime: Number.POSITIVE_INFINITY },
			},
		});
		client.setQueryData(qk.agentIdentity, {});
		client.setQueryData(qk.userIdentity, {});
		client.setQueryData(qk.gitIdentity, {});
		return render(
			<QueryClientProvider client={client}>
				<TranscriptContext.Provider value={ctx}>
					<Transcript items={items} />
				</TranscriptContext.Provider>
			</QueryClientProvider>,
		);
	}

	const carets = (root: HTMLElement) =>
		[...root.querySelectorAll("[data-brand-caret]")].map((el) => ({
			row: el.closest("[data-index]")?.getAttribute("data-index"),
			blink: el.classList.contains("caret-blink"),
		}));

	it("工作中只有最后一个助手回合的默认头像闪", () => {
		const { container } = renderTranscript("working");
		expect(carets(container)).toEqual([
			{ row: "1", blink: false },
			{ row: "3", blink: true },
		]);
	});

	it("空闲时所有头像都静止", () => {
		const { container } = renderTranscript("idle");
		expect(carets(container).map((c) => c.blink)).toEqual([false, false]);
	});

	it("助手头像在回合首行内部单独一行，位于内容之前，不留左侧头像列", () => {
		const { container } = renderTranscript("idle");
		for (const index of ["1", "3"]) {
			const row = container.querySelector(`[data-index="${index}"]`);
			const body = row?.firstElementChild as HTMLElement;
			const avatarRow = body.firstElementChild as HTMLElement;
			expect(
				avatarRow.querySelector('[data-avatar-of="agent"]'),
			).not.toBeNull();
			// 头像行之后才是消息内容
			expect(avatarRow.nextElementSibling?.className).toContain("group/msg");
			expect(body.className).not.toMatch(/\bpl-8\b/);
		}
		// 回合中间的行（第二个回合的流式消息）没有头像
		const mid = container.querySelector('[data-index="4"]');
		expect(mid?.querySelector('[data-avatar-of="agent"]')).toBeNull();
	});

	it("头像行旁写该回合模型的短名，完整 id 在悬停提示里", () => {
		getThreadStore(ctx.threadId)
			.getState()
			.patchLocal({
				state: {
					model: {
						id: "claude-sonnet-5",
						name: "Claude Sonnet 5",
						provider: "anthropic",
					},
				} as SessionState,
			});
		const { container } = renderTranscript("idle");
		const names = [
			...container.querySelectorAll(
				'[data-index="1"] [title], [data-index="3"] [title]',
			),
		].filter((el) => el.textContent === "Sonnet 5");
		expect(names).toHaveLength(2);
		expect(names[0]?.getAttribute("title")).toBe(
			"Claude Sonnet 5 (anthropic/claude-sonnet-5)",
		);
		getThreadStore(ctx.threadId).getState().patchLocal({ state: undefined });
	});

	it("最后一行是用户消息时没有进行中的回合", () => {
		expect(layoutTurns(items).openStart).toBe(3);
		expect(layoutTurns([...items, user(6)]).openStart).toBeUndefined();
	});
});

describe("贴底跟随", () => {
	const u = { input: 1, output: 1, cacheRead: 0, cost: 0 };
	const base: TranscriptItem[] = [
		user(1),
		msg(2, assistant("a", u)),
		user(3),
		msg(4, assistant("b", u)),
	];

	afterEach(() => {
		vi.restoreAllMocks();
		indexStore.setState({ threads: {} });
	});

	/** 给滚动容器一个可控的滚动模型：jsdom 没有布局，scrollHeight / scrollTop 由测试决定。 */
	function scrollModel(el: HTMLElement, client = 500) {
		const m = { height: 1000, top: 0 };
		Object.defineProperty(el, "scrollHeight", {
			configurable: true,
			get: () => m.height,
		});
		Object.defineProperty(el, "clientHeight", {
			configurable: true,
			get: () => client,
		});
		Object.defineProperty(el, "scrollTop", {
			configurable: true,
			get: () => m.top,
			set: (v: number) => {
				m.top = Math.max(0, Math.min(v, m.height - client));
			},
		});
		return m;
	}

	function mount(items: TranscriptItem[], startAtTop = false) {
		vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(
			4000,
		);
		vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(800);
		const client = new QueryClient({
			defaultOptions: {
				queries: { retry: false, staleTime: Number.POSITIVE_INFINITY },
			},
		});
		client.setQueryData(qk.agentIdentity, {});
		client.setQueryData(qk.userIdentity, {});
		client.setQueryData(qk.gitIdentity, {});
		const tree = (list: TranscriptItem[]) => (
			<QueryClientProvider client={client}>
				<TranscriptContext.Provider value={ctx}>
					<Transcript items={list} startAtTop={startAtTop} />
				</TranscriptContext.Provider>
			</QueryClientProvider>
		);
		const r = render(tree(items));
		const el = screen.getByTestId("transcript");
		const m = scrollModel(el);
		return {
			el,
			m,
			rerender: (list: TranscriptItem[]) => r.rerender(tree(list)),
		};
	}

	const jump = () => screen.queryByRole("button", { name: "回到底部" });

	it("首轮补发期间的非用户滚动（测高补偿 / 内容先长高）不会让转录退出跟随", () => {
		const { el, m } = mount(base);
		m.top = 500;
		fireEvent.scroll(el);
		expect(jump()).toBeNull();
		// 后续批次与测高让内容长高，浏览器在贴底纠偏之前先派发了一次滚动事件：距底 2000px
		m.height = 3000;
		fireEvent.scroll(el);
		expect(jump()).toBeNull();
		expect(m.top).toBe(2500);
	});

	it("用户真的往上翻（滚轮）才退出跟随；之后内容增长不抢回底部，「回到底部」恢复跟随", () => {
		const { el, m, rerender } = mount(base);
		m.top = 500;
		fireEvent.scroll(el);
		m.height = 3000;
		fireEvent.wheel(el, { deltaY: -120 });
		m.top = 400;
		fireEvent.scroll(el);
		expect(jump()).not.toBeNull();
		m.height = 3400;
		rerender([...base, user(5)]);
		expect(m.top).toBe(400);
		fireEvent.click(jump() as HTMLElement);
		expect(m.top).toBe(2900);
		expect(jump()).toBeNull();
	});

	it("触摸往下拖（内容上翻）同样算用户意图", () => {
		const { el, m } = mount(base);
		m.top = 500;
		fireEvent.scroll(el);
		fireEvent.touchStart(el, { touches: [{ clientY: 100 }] });
		fireEvent.touchMove(el, { touches: [{ clientY: 180 }] });
		m.top = 300;
		fireEvent.scroll(el);
		expect(jump()).not.toBeNull();
	});

	it("startAtTop（快照）：非用户滚动不会开始跟随", () => {
		const { el, m } = mount(base, true);
		m.height = 3000;
		fireEvent.scroll(el);
		expect(m.top).toBe(0);
		expect(jump()).not.toBeNull();
	});
});

describe("定位条目", () => {
	afterEach(() => {
		vi.restoreAllMocks();
		getThreadStore(ctx.threadId).setState({ entries: [] });
	});

	it("revealEntry：滚到该行、退出跟随并描边；工具输出描边对应工具卡", async () => {
		vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(
			4000,
		);
		vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(800);
		const call = {
			role: "assistant",
			content: [
				{ type: "toolCall", id: "call_9", name: "bash", arguments: {} },
			],
		};
		getThreadStore(ctx.threadId).setState({
			entries: [
				{
					seq: 1,
					file: "s.jsonl",
					entryId: "E1",
					type: "message",
					payload: {
						type: "message",
						message: { role: "user", content: "hi" },
					},
				},
				{
					seq: 3,
					file: "s.jsonl",
					entryId: "E3",
					type: "message",
					payload: {
						type: "message",
						message: { role: "toolResult", toolCallId: "call_9", content: [] },
					},
				},
			],
		});
		const client = new QueryClient({
			defaultOptions: {
				queries: { retry: false, staleTime: Number.POSITIVE_INFINITY },
			},
		});
		client.setQueryData(qk.agentIdentity, {});
		client.setQueryData(qk.userIdentity, {});
		client.setQueryData(qk.gitIdentity, {});
		const { container } = render(
			<QueryClientProvider client={client}>
				<TranscriptContext.Provider value={ctx}>
					<Transcript items={[user(1), msg(2, call)]} />
				</TranscriptContext.Provider>
			</QueryClientProvider>,
		);
		const { revealEntry } = await import("./reveal");
		expect(revealEntry(ctx.threadId, { entryId: "E9", seq: 9 })).toBe(false);
		let ok = false;
		act(() => {
			ok = revealEntry(ctx.threadId, { entryId: "E1", seq: 1 });
		});
		expect(ok).toBe(true);
		const row = container.querySelector('[data-index="0"] > div');
		expect(row?.className).toContain("outline-warn");
		expect(screen.getByRole("button", { name: "回到底部" })).not.toBeNull();

		act(() => {
			revealEntry(ctx.threadId, { entryId: "E3", seq: 3 });
		});
		await waitFor(() =>
			expect(
				container.querySelector('[data-tool-call-id="call_9"]')?.className,
			).toContain("outline-warn"),
		);
		expect(
			container.querySelector('[data-index="1"] > div')?.className,
		).not.toContain("outline-warn");
	});
});
