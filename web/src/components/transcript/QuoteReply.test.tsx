/**
 * 引用回复（交互）：在 agent 消息里选中文字 → 「引用回复」→ 输入区出现引用条 → 发出的正文是引用块 + 回复；
 * Esc 之后同一段选区不再弹出；有选区期间不随新内容贴底，选区取消后补一次贴底；用户消息开头的引用块单独排版。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../../api/client";
import { qk } from "../../api/queries";
import i18n from "../../i18n";
import {
	getComposerDraft,
	resetComposerDrafts,
} from "../../state/composer-drafts";
import type { JsonRecord } from "../../state/message-builder";
import { indexStore, resetThreadStores } from "../../state/stores";
import type { TranscriptItem } from "../../state/transcript";
import { Composer } from "../composer/Composer";
import { TranscriptContext, type TranscriptContextValue } from "./context";
import { Transcript } from "./Transcript";

const { cmd } = vi.hoisted(() => ({ cmd: vi.fn() }));
vi.mock("../../realtime/socket", () => ({
	realtime: {
		cmd: (...args: unknown[]) => cmd(...args),
		uiResponse: () => true,
		subscribe: () => () => undefined,
		focus: () => undefined,
	},
	OmpCommandError: class extends Error {},
}));

const T = "T-quote-reply";
const ctx: TranscriptContextValue = {
	threadId: T,
	toolResults: new Map(),
	liveTools: {},
	subagents: {},
	openSubagent: () => {},
	forkFrom: () => {},
	labelEntry: () => {},
};

function message(seq: number, m: JsonRecord): TranscriptItem {
	return {
		kind: "message",
		key: `e${seq}`,
		seq,
		entryId: `E${seq}`,
		message: m,
		live: false,
	};
}

const userText = (seq: number, text: string) =>
	message(seq, { role: "user", content: [{ type: "text", text }] });
const agentText = (seq: number, text: string) =>
	message(seq, { role: "assistant", content: [{ type: "text", text }] });

const items: TranscriptItem[] = [
	userText(1, "explain"),
	agentText(2, "alpha beta gamma"),
];

function mount(list: TranscriptItem[] = items, withComposer = true) {
	// jsdom 没有布局：视口给足、每行一个非零高度，所有行都挂载
	vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(
		20_000,
	);
	vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(40);
	vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(800);
	indexStore.setState({
		threads: { [T]: { id: T, state: "idle" } as Thread },
	});
	const client = new QueryClient({
		defaultOptions: {
			queries: { retry: false, staleTime: Number.POSITIVE_INFINITY },
		},
	});
	client.setQueryData(qk.agentIdentity, {});
	client.setQueryData(qk.userIdentity, {});
	client.setQueryData(qk.gitIdentity, {});
	const tree = (l: TranscriptItem[]) => (
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<TranscriptContext.Provider value={ctx}>
					<Transcript items={l} />
					{withComposer && <Composer threadId={T} />}
				</TranscriptContext.Provider>
			</MemoryRouter>
		</QueryClientProvider>
	);
	const r = render(tree(list));
	return { ...r, rerender: (l: TranscriptItem[]) => r.rerender(tree(l)) };
}

/** 选中某段文字里的一截（jsdom 不派发 selectionchange，手动派发）。 */
function select(text: string, from: number, to: number) {
	const node = screen.getByText(text).firstChild as Text;
	const range = document.createRange();
	range.setStart(node, from);
	range.setEnd(node, to);
	const sel = document.getSelection();
	sel?.removeAllRanges();
	sel?.addRange(range);
	act(() => {
		document.dispatchEvent(new Event("selectionchange"));
	});
}

const quoteButton = () =>
	screen.queryByRole("button", { name: i18n.t("transcript.quoteReply") });

describe("引用回复", () => {
	beforeEach(() => {
		cmd.mockReset();
		cmd.mockResolvedValue({});
		resetThreadStores();
		resetComposerDrafts();
	});
	afterEach(() => {
		document.getSelection()?.removeAllRanges();
		vi.restoreAllMocks();
		indexStore.setState({ threads: {} });
	});

	it("选中 agent 消息里的一段 → 引用回复 → 发出引用块 + 回复，发送后引用条清掉", async () => {
		mount();
		select("alpha beta gamma", 6, 16);
		await waitFor(() => expect(quoteButton()).not.toBeNull());
		fireEvent.click(quoteButton() as HTMLElement);
		expect(quoteButton()).toBeNull();
		expect(document.getSelection()?.isCollapsed).toBe(true);
		expect(screen.getByTestId("composer-quote").textContent).toContain(
			"beta gamma",
		);
		const box = screen.getByRole("textbox", {
			name: i18n.t("composer.label"),
		});
		expect(document.activeElement).toBe(box);
		fireEvent.change(box, { target: { value: "why?" } });
		await act(async () => {
			fireEvent.click(screen.getByRole("button", { name: "发送" }));
		});
		expect(cmd).toHaveBeenCalledWith(
			T,
			expect.objectContaining({
				type: "prompt",
				message: "> beta gamma\n\nwhy?",
			}),
		);
		expect(screen.queryByTestId("composer-quote")).toBeNull();
		expect(getComposerDraft(T).quote).toBeNull();
	});

	it("引用条可以移除；空输入框里退格同样移除；bash 不带走引用", async () => {
		mount();
		select("alpha beta gamma", 0, 5);
		await waitFor(() => expect(quoteButton()).not.toBeNull());
		fireEvent.click(quoteButton() as HTMLElement);
		const box = screen.getByRole("textbox", {
			name: i18n.t("composer.label"),
		});
		fireEvent.change(box, { target: { value: "!ls" } });
		await act(async () => {
			fireEvent.keyDown(box, { key: "Enter" });
		});
		expect(cmd).toHaveBeenCalledWith(
			T,
			expect.objectContaining({ type: "bash", command: "ls" }),
			expect.anything(),
		);
		expect(screen.getByTestId("composer-quote")).toBeTruthy();
		fireEvent.keyDown(box, { key: "Backspace" });
		expect(screen.queryByTestId("composer-quote")).toBeNull();
	});

	it("选区不在 agent 消息里（用户消息）不出现入口", async () => {
		mount();
		select("explain", 0, 7);
		await new Promise((r) => setTimeout(r, 250));
		expect(quoteButton()).toBeNull();
	});

	it("Esc 关掉后同一段选区不再弹出；换一段选区再出现", async () => {
		mount();
		select("alpha beta gamma", 0, 5);
		await waitFor(() => expect(quoteButton()).not.toBeNull());
		fireEvent.keyDown(document, { key: "Escape" });
		expect(quoteButton()).toBeNull();
		// 重渲染等引起的选区变化：选区没变，不再弹出
		act(() => {
			document.dispatchEvent(new Event("selectionchange"));
		});
		await new Promise((r) => setTimeout(r, 250));
		expect(quoteButton()).toBeNull();
		select("alpha beta gamma", 0, 10);
		await waitFor(() => expect(quoteButton()).not.toBeNull());
	});

	it("有选区时新内容不把转录推到底部；用户取消选区后补一次贴底", async () => {
		const { rerender } = mount(items, false);
		const el = screen.getByTestId("transcript");
		const m = { height: 1000, top: 500 };
		Object.defineProperty(el, "scrollHeight", {
			configurable: true,
			get: () => m.height,
		});
		Object.defineProperty(el, "clientHeight", {
			configurable: true,
			get: () => 500,
		});
		Object.defineProperty(el, "scrollTop", {
			configurable: true,
			get: () => m.top,
			set: (v: number) => {
				m.top = Math.max(0, Math.min(v, m.height - 500));
			},
		});
		select("alpha beta gamma", 0, 5);
		await waitFor(() => expect(quoteButton()).not.toBeNull());
		m.height = 1600;
		rerender([...items, agentText(3, "streaming more")]);
		expect(m.top).toBe(500);
		document.getSelection()?.removeAllRanges();
		act(() => {
			fireEvent.pointerDown(document.body);
		});
		expect(m.top).toBe(1100);
	});

	it("用户消息开头的引用块单独排版，其后是回复正文", () => {
		mount([userText(1, "> line one\n>\n> line two\n\nmy reply")], false);
		const quote = screen.getByTestId("user-quote");
		expect(quote.tagName).toBe("BLOCKQUOTE");
		expect(quote.textContent).toBe("line one\n\nline two");
		expect(screen.getByText("my reply")).toBeTruthy();
	});
});
