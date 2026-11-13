/**
 * 信息面板上下文卡：分项来自 `get_context_report`。报告要跟当前上下文对上——
 * 关掉面板期间上下文变了，重新打开时即便缓存的报告还在保鲜期内，也要按节流语义补取一次。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import type { ResponseData } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { getThreadStore } from "../../state/stores";
import type { ThreadViewState } from "../../state/thread-reducer";
import { InfoPanel } from "./InfoPanel";

const WINDOW = 200_000;

/** 报告分项：只有系统提示词与消息两段，合计 = `used`。 */
function report(used: number): ResponseData<"get_context_report"> {
	return {
		contextWindow: WINDOW,
		model: "m",
		breakdown: {
			contextWindow: WINDOW,
			usedTokens: used,
			systemPromptTokens: 8_000,
			systemContextTokens: 0,
			instructionFilesTokens: 0,
			skillsTokens: 0,
			systemToolsTokens: 0,
			messagesTokens: used - 8_000,
		},
	} as unknown as ResponseData<"get_context_report">;
}

function setContext(id: string, tokens: number) {
	getThreadStore(id)
		.getState()
		.patchLocal({
			agentStatus: "ready",
			state: {
				contextUsage: {
					tokens,
					contextWindow: WINDOW,
					percent: (tokens / WINDOW) * 100,
				},
			},
		} as unknown as Partial<ThreadViewState>);
}

/** 上下文卡里所有量表（展开的分段条与卡头摘要的迷你条）的读数。 */
function meterValues(): string[] {
	return screen
		.getAllByRole("meter", { name: "上下文" })
		.map((m) => m.getAttribute("aria-valuenow") ?? "");
}

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
	vi.useFakeTimers();
});

afterEach(() => {
	vi.useRealTimers();
	vi.restoreAllMocks();
});

describe("InfoPanel 上下文分项", () => {
	it("关面板期间上下文变了，重开后在节流到期时补取，量表跟上当前上下文", async () => {
		const id = "T-ctx-reopen";
		let tokens = 48_000;
		const cmd = vi
			.spyOn(realtime, "cmd")
			.mockImplementation(
				async (_id, frame) =>
					(frame.type === "get_context_report"
						? report(tokens)
						: { subagents: [] }) as never,
			);
		const reports = () =>
			cmd.mock.calls.filter(([, f]) => f.type === "get_context_report").length;
		const client = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		const mount = () =>
			render(
				<QueryClientProvider client={client}>
					<InfoPanel threadId={id} />
				</QueryClientProvider>,
			);

		setContext(id, tokens);
		const first = mount();
		await act(() => vi.advanceTimersByTimeAsync(0));
		expect(reports()).toBe(1);
		expect(meterValues().every((v) => v === "24")).toBe(true);

		// 关右栏；上下文涨到 100k（50%），随即重开——缓存报告仍在 15 秒保鲜期内
		first.unmount();
		tokens = 100_000;
		setContext(id, tokens);
		mount();
		await act(() => vi.advanceTimersByTimeAsync(0));
		// 节流：距上次取到不足 15 秒，不立刻补取
		expect(reports()).toBe(1);

		await act(() => vi.advanceTimersByTimeAsync(16_000));
		expect(reports()).toBe(2);
		expect(meterValues().every((v) => v === "50")).toBe(true);

		// 报告已对上当前上下文：之后不再重复请求
		await act(() => vi.advanceTimersByTimeAsync(60_000));
		expect(reports()).toBe(2);
	});
});
