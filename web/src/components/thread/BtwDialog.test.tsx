/**
 * /btw 旁路提问：对话框展示首轮与全部追问、追问发送带 recordId、最新一轮运行中显示取消、引导块注入与拒绝原因、
 * 有运行中时禁用提问与追问、打开期间的 btw-history 提示；输入区 `/btw <问题>` 发送后打开对话框。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import type { RpcBtwHistoryRecord } from "../../omp/types";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { Composer } from "../composer/Composer";
import { BtwDialog } from "./BtwDialog";

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

const T = "T-btw-dialog";

const BASE: RpcBtwHistoryRecord = {
	id: "b1",
	leafId: null,
	question: "首轮问题",
	answer: "首轮回答",
	status: "complete",
	createdAt: 1000,
	updatedAt: 1000,
};

function framesOf(type: string): Record<string, unknown>[] {
	return cmd.mock.calls
		.map((c) => c[1] as Record<string, unknown>)
		.filter((f) => f.type === type);
}

function seed(...records: RpcBtwHistoryRecord[]) {
	getThreadStore(T)
		.getState()
		.apply(
			records.map((record) => ({
				type: "frame" as const,
				frame: { type: "btw_record", record },
			})),
		);
}

function wrap(children: ReactNode) {
	return render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<MemoryRouter>{children}</MemoryRouter>
		</QueryClientProvider>,
	);
}

function topic(question: string): HTMLElement {
	return screen.getByRole("region", { name: question });
}

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
	resetThreadStores();
	uiStore.getState().close();
	cmd.mockReset();
	cmd.mockImplementation((_id: string, frame: { type: string }) =>
		Promise.resolve(frame.type === "get_btw_history" ? { records: [] } : {}),
	);
});

describe("BtwDialog", () => {
	it("显示首轮与追问；追问发送 btw 带 recordId", async () => {
		seed({
			...BASE,
			followUps: [
				{ ...BASE, question: "追问一", answer: "追问一的回答" },
				{
					...BASE,
					question: "追问二",
					answer: "追问二被取消",
					status: "cancelled",
				},
			],
		});
		wrap(<BtwDialog threadId={T} onClose={() => undefined} />);
		const card = topic("首轮问题");
		expect(within(card).getByText("首轮回答")).toBeInTheDocument();
		expect(within(card).getByText("追问一")).toBeInTheDocument();
		expect(within(card).getByText("追问一的回答")).toBeInTheDocument();
		expect(within(card).getByText("追问二被取消")).toBeInTheDocument();
		// 状态落在所属的那一轮（最新一轮被取消）
		expect(
			within(card).getByText(i18n.t("btw.status.cancelled")),
		).toBeInTheDocument();

		const input = within(card).getByRole("textbox", {
			name: i18n.t("btw.followUpPlaceholder"),
		});
		fireEvent.change(input, { target: { value: "  再问一句 " } });
		await act(async () => {
			fireEvent.submit(input);
		});
		expect(framesOf("btw")).toEqual([
			{ type: "btw", question: "再问一句", recordId: "b1" },
		]);
	});

	it("追问运行中：状态与取消落在最新一轮，取消发 btw_cancel；提问与追问禁用并提示", async () => {
		seed({
			...BASE,
			followUps: [{ ...BASE, question: "追问", answer: "", status: "running" }],
		});
		wrap(<BtwDialog threadId={T} onClose={() => undefined} />);
		const card = topic("首轮问题");
		expect(
			within(card).getByText(i18n.t("btw.status.running")),
		).toBeInTheDocument();
		expect(within(card).queryByText(i18n.t("btw.status.complete"))).toBeNull();

		await act(async () => {
			fireEvent.click(
				within(card).getByRole("button", { name: i18n.t("common.cancel") }),
			);
		});
		expect(framesOf("btw_cancel")).toEqual([
			{ type: "btw_cancel", recordId: "b1" },
		]);

		expect(
			screen.getByRole("textbox", { name: i18n.t("btw.placeholder") }),
		).toBeDisabled();
		expect(
			screen.getByRole("button", { name: i18n.t("btw.ask") }),
		).toBeDisabled();
		expect(
			within(card).getByRole("button", { name: i18n.t("btw.followUp") }),
		).toBeDisabled();
		expect(
			within(card).getByRole("textbox", {
				name: i18n.t("btw.followUpPlaceholder"),
			}),
		).toBeDisabled();
		expect(screen.getByText(i18n.t("btw.busy"))).toBeInTheDocument();
	});

	it("首轮完成、无运行中：没有取消按钮，提问可用并发送不带 recordId 的 btw", async () => {
		seed(BASE);
		wrap(<BtwDialog threadId={T} onClose={() => undefined} />);
		expect(
			screen.queryByRole("button", { name: i18n.t("common.cancel") }),
		).toBeNull();
		const input = screen.getByRole("textbox", {
			name: i18n.t("btw.placeholder"),
		});
		fireEvent.change(input, { target: { value: "新问题" } });
		await act(async () => {
			fireEvent.submit(input);
		});
		expect(framesOf("btw")).toEqual([{ type: "btw", question: "新问题" }]);
	});

	it("inject ready：引导块单独成卡，点击发 btw_inject，被拒时显示原因", async () => {
		cmd.mockImplementation((_id: string, frame: { type: string }) =>
			Promise.resolve(
				frame.type === "get_btw_history"
					? { records: [] }
					: frame.type === "btw_inject"
						? { sent: false, reason: "主会话已结束" }
						: {},
			),
		);
		seed({
			...BASE,
			answer: "前文\n<inject>请改用 pnpm</inject>",
			inject: "ready",
		});
		wrap(<BtwDialog threadId={T} onClose={() => undefined} />);
		const card = topic("首轮问题");
		expect(within(card).getByText("请改用 pnpm")).toBeInTheDocument();
		await act(async () => {
			fireEvent.click(
				within(card).getByRole("button", { name: i18n.t("btw.inject.send") }),
			);
		});
		expect(framesOf("btw_inject")).toEqual([
			{ type: "btw_inject", recordId: "b1" },
		]);
		expect(
			within(card).getByText(
				i18n.t("btw.inject.refused", { reason: "主会话已结束" }),
			),
		).toBeInTheDocument();
	});

	it("inject sending / sent 显示状态，不再给发送按钮", () => {
		seed(
			{ ...BASE, answer: "<inject>甲</inject>", inject: "sending" },
			{
				...BASE,
				id: "b2",
				question: "第二个",
				createdAt: 2000,
				answer: "<inject>乙</inject>",
				inject: "sent",
			},
		);
		wrap(<BtwDialog threadId={T} onClose={() => undefined} />);
		// 较旧的话题默认折叠，展开后才有回答
		expect(
			within(topic("首轮问题")).queryByText(i18n.t("btw.inject.sending")),
		).toBeNull();
		fireEvent.click(
			within(topic("首轮问题")).getByRole("button", { name: "首轮问题" }),
		);
		expect(
			within(topic("首轮问题")).getByText(i18n.t("btw.inject.sending")),
		).toBeInTheDocument();
		expect(
			within(topic("第二个")).getByText(i18n.t("btw.inject.sent")),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: i18n.t("btw.inject.send") }),
		).toBeNull();
	});

	it("topic 按创建时间新的在前；最新的展开，旧的折叠成一行并标出非完成状态", () => {
		seed(
			{
				...BASE,
				id: "a",
				question: "旧的",
				answer: "旧回答",
				status: "interrupted",
				createdAt: 1,
			},
			{ ...BASE, id: "c", question: "新的", answer: "新回答", createdAt: 3 },
		);
		wrap(<BtwDialog threadId={T} onClose={() => undefined} />);
		expect(
			screen.getAllByRole("region").map((r) => r.getAttribute("aria-label")),
		).toEqual(["新的", "旧的"]);
		expect(within(topic("新的")).getByText("新回答")).toBeInTheDocument();
		const old = topic("旧的");
		expect(within(old).queryByText("旧回答")).toBeNull();
		expect(
			within(old).getByText(i18n.t("btw.status.interrupted")),
		).toBeInTheDocument();
		fireEvent.click(within(old).getByRole("button", { name: "旧的" }));
		expect(within(old).getByText("旧回答")).toBeInTheDocument();
		expect(
			within(old).getByText(i18n.t("btw.note.interrupted")),
		).toBeInTheDocument();
	});

	it("出错的一轮显示错误提示与原因", () => {
		seed({ ...BASE, answer: "", status: "error", error: "429 rate limited" });
		wrap(<BtwDialog threadId={T} onClose={() => undefined} />);
		const card = topic("首轮问题");
		expect(
			within(card).getByText(i18n.t("btw.note.error")),
		).toBeInTheDocument();
		expect(within(card).getByText("429 rate limited")).toBeInTheDocument();
	});

	it("没有历史时显示空状态", async () => {
		wrap(<BtwDialog threadId={T} onClose={() => undefined} />);
		expect(await screen.findByText(i18n.t("btw.empty"))).toBeInTheDocument();
	});

	it("打开期间到达的 btw-history 提示在对话框里显示", () => {
		wrap(<BtwDialog threadId={T} onClose={() => undefined} />);
		act(() => {
			getThreadStore(T)
				.getState()
				.apply([
					{
						type: "frame",
						frame: {
							type: "notice",
							level: "error",
							message: "Could not save /btw history: disk full",
							source: "btw-history",
						},
					},
				]);
		});
		expect(
			screen.getByText("Could not save /btw history: disk full"),
		).toBeInTheDocument();
	});
});

describe("Composer /btw", () => {
	it("/btw <问题> 发送 btw 并打开 btw 对话框", async () => {
		wrap(<Composer threadId={T} />);
		const box = screen.getByRole("textbox", { name: i18n.t("composer.label") });
		fireEvent.change(box, { target: { value: "/btw 这是什么" } });
		await act(async () => {
			fireEvent.keyDown(box, { key: "Enter" });
		});
		expect(framesOf("btw")).toEqual([{ type: "btw", question: "这是什么" }]);
		expect(uiStore.getState().dialog).toEqual({ kind: "btw" });
	});

	it("btw 被拒（已有运行中）时不打开对话框", async () => {
		cmd.mockImplementation(() =>
			Promise.reject(
				new Error("A /btw question is still running; cancel it first"),
			),
		);
		wrap(<Composer threadId={T} />);
		const box = screen.getByRole("textbox", { name: i18n.t("composer.label") });
		fireEvent.change(box, { target: { value: "/btw 这是什么" } });
		await act(async () => {
			fireEvent.keyDown(box, { key: "Enter" });
		});
		expect(uiStore.getState().dialog).toBeNull();
	});
});
