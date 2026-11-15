/**
 * /btw 旁路提问对话框（左栏话题列表 + 右栏详情；窄屏列表页 ↔ 详情页）：
 * 详情展示首轮与全部追问；页脚「追问」展开输入并发送带 recordId 的 btw；运行中主按钮变为取消（btw_cancel）；
 * 新话题发送不带 recordId 的 btw 并选中新话题；引导块注入与拒绝原因；有运行中时禁用提问与追问；
 * 列表新的在前、默认选中最新、点击与上下键切换；复制回答；打开期间的 btw-history 提示；
 * 输入区 `/btw <问题>` 发送后打开对话框。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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

/** 模拟 Core：`btw` 先发 btw_record（运行中），再回应答里的 record。新话题 id 为 `n1`。 */
function coreReply(frame: {
	type: string;
	question?: string;
	recordId?: string;
}): unknown {
	if (frame.type === "get_btw_history") return { records: [] };
	if (frame.type !== "btw") return {};
	const prev =
		frame.recordId === undefined
			? undefined
			: getThreadStore(T).getState().btw[frame.recordId];
	const turn = {
		question: frame.question ?? "",
		answer: "",
		status: "running" as const,
		createdAt: 5000,
		updatedAt: 5000,
	};
	const record: RpcBtwHistoryRecord = prev
		? { ...prev, followUps: [...(prev.followUps ?? []), turn] }
		: { ...turn, id: "n1", leafId: null };
	seed(record);
	return { record };
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

function openDialog() {
	return wrap(<BtwDialog threadId={T} onClose={() => undefined} />);
}

/** 右栏详情（以首轮问题为名的 region）。 */
function topic(question: string): HTMLElement {
	return screen.getByRole("region", { name: question });
}

function option(question: string): HTMLElement {
	return screen.getByRole("option", { name: new RegExp(question) });
}

function button(name: string): HTMLElement {
	return screen.getByRole("button", { name });
}

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
	resetThreadStores();
	uiStore.getState().close();
	window.innerWidth = 1024;
	cmd.mockReset();
	cmd.mockImplementation((_id: string, frame: { type: string }) =>
		Promise.resolve(coreReply(frame)),
	);
});

afterEach(() => {
	window.innerWidth = 1024;
	Reflect.deleteProperty(navigator, "clipboard");
});

describe("BtwDialog", () => {
	it("详情显示首轮与追问；「追问」展开输入并聚焦，发送 btw 带 recordId 后收起", async () => {
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
		openDialog();
		const card = topic("首轮问题");
		expect(
			within(card).getByRole("heading", { name: "首轮问题" }),
		).toBeInTheDocument();
		expect(within(card).getByText("首轮回答")).toBeInTheDocument();
		expect(within(card).getByText("追问一")).toBeInTheDocument();
		expect(within(card).getByText("追问一的回答")).toBeInTheDocument();
		expect(within(card).getByText("追问二被取消")).toBeInTheDocument();
		// 状态落在所属的那一轮（最新一轮被取消）
		expect(
			within(card).getByText(i18n.t("btw.status.cancelled")),
		).toBeInTheDocument();
		// 列表项写追问轮数
		expect(
			within(option("首轮问题")).getByText(
				i18n.t("btw.followUps", { count: 2 }),
			),
		).toBeInTheDocument();

		// 追问输入默认收起，点页脚主按钮才展开并聚焦
		const followName = i18n.t("btw.followUpPlaceholder");
		expect(screen.queryByRole("textbox", { name: followName })).toBeNull();
		fireEvent.click(button(i18n.t("btw.followUp")));
		const input = screen.getByRole("textbox", { name: followName });
		expect(input).toHaveFocus();
		// 空内容不能发
		expect(button(i18n.t("btw.followUp"))).toBeDisabled();
		fireEvent.change(input, { target: { value: "  再问一句 " } });
		await act(async () => {
			fireEvent.click(button(i18n.t("btw.followUp")));
		});
		expect(framesOf("btw")).toEqual([
			{ type: "btw", question: "再问一句", recordId: "b1" },
		]);
		// 发出后收起输入；新一轮运行中，主按钮变为取消
		expect(screen.queryByRole("textbox", { name: followName })).toBeNull();
		expect(within(topic("首轮问题")).getByText("再问一句")).toBeInTheDocument();
		expect(button(i18n.t("common.cancel"))).toBeInTheDocument();
	});

	it("最新一轮运行中：主按钮是取消并发 btw_cancel；其他话题的追问与新话题的提问都禁用并提示", async () => {
		seed(
			{ ...BASE, id: "old", question: "旧话题", createdAt: 1 },
			{
				...BASE,
				followUps: [
					{ ...BASE, question: "追问", answer: "", status: "running" },
				],
			},
		);
		openDialog();
		const card = topic("首轮问题");
		expect(
			within(card).getByText(i18n.t("btw.status.running")),
		).toBeInTheDocument();
		// 运行中的话题在列表里带中性的「回答中」
		expect(
			within(option("首轮问题")).getByText(i18n.t("btw.status.running")),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: i18n.t("btw.followUp") }),
		).toBeNull();

		await act(async () => {
			fireEvent.click(button(i18n.t("common.cancel")));
		});
		expect(framesOf("btw_cancel")).toEqual([
			{ type: "btw_cancel", recordId: "b1" },
		]);

		// 切到另一个话题：追问禁用（Core 一次只跑一个）
		fireEvent.click(option("旧话题"));
		expect(button(i18n.t("btw.followUp"))).toBeDisabled();
		expect(screen.getByText(i18n.t("btw.busy"))).toBeInTheDocument();

		// 新话题：输入与提问都禁用
		fireEvent.click(button(i18n.t("btw.newTopic")));
		expect(
			screen.getByRole("textbox", { name: i18n.t("btw.placeholder") }),
		).toBeDisabled();
		expect(button(i18n.t("btw.ask"))).toBeDisabled();
		expect(screen.getByText(i18n.t("btw.busy"))).toBeInTheDocument();
	});

	it("新话题：右栏变成提问输入，发送不带 recordId 的 btw，发完选中新话题", async () => {
		seed(BASE, { ...BASE, id: "b2", question: "第二个", createdAt: 2000 });
		openDialog();
		expect(
			screen.queryByRole("button", { name: i18n.t("common.cancel") }),
		).toBeNull();
		// 先点选较旧的话题（不再跟随最新），发完新话题仍要切到新话题
		fireEvent.click(option("首轮问题"));
		fireEvent.click(button(i18n.t("btw.newTopic")));
		expect(screen.queryByRole("region", { name: "首轮问题" })).toBeNull();
		expect(
			screen.getByRole("button", { name: i18n.t("btw.newTopic") }),
		).toHaveAttribute("aria-pressed", "true");
		// 新话题输入态没有选中的话题，也不给复制回答
		expect(option("首轮问题")).toHaveAttribute("aria-selected", "false");
		expect(
			screen.queryByRole("button", { name: i18n.t("btw.copyAnswer") }),
		).toBeNull();
		const input = screen.getByRole("textbox", {
			name: i18n.t("btw.placeholder"),
		});
		expect(input).toHaveFocus();
		expect(button(i18n.t("btw.ask"))).toBeDisabled();
		fireEvent.change(input, { target: { value: "新问题" } });
		await act(async () => {
			fireEvent.click(button(i18n.t("btw.ask")));
		});
		expect(framesOf("btw")).toEqual([{ type: "btw", question: "新问题" }]);
		expect(topic("新问题")).toBeInTheDocument();
		expect(option("新问题")).toHaveAttribute("aria-selected", "true");
		expect(button(i18n.t("common.cancel"))).toBeInTheDocument();
	});

	it("inject ready：引导块单独成卡，点击发 btw_inject，被拒时显示原因", async () => {
		cmd.mockImplementation((_id: string, frame: { type: string }) =>
			Promise.resolve(
				frame.type === "btw_inject"
					? { sent: false, reason: "主会话已结束" }
					: coreReply(frame),
			),
		);
		seed({
			...BASE,
			answer: "前文\n<inject>请改用 pnpm</inject>",
			inject: "ready",
		});
		openDialog();
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
		openDialog();
		expect(
			within(topic("第二个")).getByText(i18n.t("btw.inject.sent")),
		).toBeInTheDocument();
		fireEvent.click(option("首轮问题"));
		expect(
			within(topic("首轮问题")).getByText(i18n.t("btw.inject.sending")),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: i18n.t("btw.inject.send") }),
		).toBeNull();
	});

	it("列表按创建时间新的在前，默认选中最新；点击与上下键切换话题，旧话题标出非完成状态", () => {
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
		openDialog();
		const options = screen.getAllByRole("option");
		expect(options.map((o) => o.textContent?.slice(0, 2))).toEqual([
			"新的",
			"旧的",
		]);
		expect(option("新的")).toHaveAttribute("aria-selected", "true");
		expect(option("新的")).toHaveFocus();
		expect(topic("新的")).toBeInTheDocument();
		expect(screen.queryByText("旧回答")).toBeNull();
		expect(
			within(option("旧的")).getByText(i18n.t("btw.status.interrupted")),
		).toBeInTheDocument();

		fireEvent.keyDown(option("新的"), { key: "ArrowDown" });
		expect(option("旧的")).toHaveAttribute("aria-selected", "true");
		expect(option("旧的")).toHaveFocus();
		expect(within(topic("旧的")).getByText("旧回答")).toBeInTheDocument();
		expect(
			within(topic("旧的")).getByText(i18n.t("btw.note.interrupted")),
		).toBeInTheDocument();
		// 到底再按不越界
		fireEvent.keyDown(option("旧的"), { key: "ArrowDown" });
		expect(option("旧的")).toHaveAttribute("aria-selected", "true");

		fireEvent.keyDown(option("旧的"), { key: "ArrowUp" });
		expect(topic("新的")).toBeInTheDocument();
		fireEvent.click(option("旧的"));
		expect(topic("旧的")).toBeInTheDocument();
		expect(screen.queryByRole("region", { name: "新的" })).toBeNull();
	});

	it("历史在打开后才读回：选中最新话题并把焦点移到它上面", async () => {
		cmd.mockImplementation((_id: string, frame: { type: string }) =>
			Promise.resolve(
				frame.type === "get_btw_history"
					? {
							records: [
								{ ...BASE, id: "c", question: "新的", createdAt: 3 },
								{ ...BASE, id: "a", question: "旧的", createdAt: 1 },
							],
						}
					: coreReply(frame),
			),
		);
		openDialog();
		const newest = await screen.findByRole("option", { name: /新的/ });
		expect(newest).toHaveAttribute("aria-selected", "true");
		expect(newest).toHaveFocus();
		expect(topic("新的")).toBeInTheDocument();
	});

	it("出错的一轮显示错误提示与原因", () => {
		seed({ ...BASE, answer: "", status: "error", error: "429 rate limited" });
		openDialog();
		const card = topic("首轮问题");
		expect(
			within(card).getByText(i18n.t("btw.note.error")),
		).toBeInTheDocument();
		expect(within(card).getByText("429 rate limited")).toBeInTheDocument();
	});

	it("复制回答：复制选中话题最近一个非空回答，按钮短暂显示已复制", async () => {
		const writeText = vi.fn(() => Promise.resolve());
		Object.defineProperty(navigator, "clipboard", {
			value: { writeText },
			configurable: true,
		});
		seed({
			...BASE,
			followUps: [
				{ ...BASE, question: "追问", answer: "  追问的回答\n" },
				{ ...BASE, question: "又追问", answer: "", status: "cancelled" },
			],
		});
		openDialog();
		await act(async () => {
			fireEvent.click(button(i18n.t("btw.copyAnswer")));
		});
		expect(writeText).toHaveBeenCalledWith("  追问的回答\n");
		expect(button(i18n.t("common.copied"))).toBeInTheDocument();
	});

	it("没有历史时左栏写空态，右栏直接是新话题输入", async () => {
		openDialog();
		expect(await screen.findByText(i18n.t("btw.empty"))).toBeInTheDocument();
		expect(
			screen.getByRole("textbox", { name: i18n.t("btw.placeholder") }),
		).toBeInTheDocument();
		expect(screen.queryByRole("listbox")).toBeNull();
	});

	it("窄屏：列表页 → 详情页 → 返回列表；详情页页脚有复制与追问", () => {
		window.innerWidth = 390;
		seed(
			{ ...BASE, id: "a", question: "旧的", answer: "旧回答", createdAt: 1 },
			{ ...BASE, id: "c", question: "新的", answer: "新回答", createdAt: 3 },
		);
		openDialog();
		// 列表页：没有详情，页脚只有关闭
		expect(screen.queryByRole("region")).toBeNull();
		expect(option("新的")).toHaveAttribute("aria-selected", "true");
		expect(
			screen.queryByRole("button", { name: i18n.t("btw.followUp") }),
		).toBeNull();
		fireEvent.click(option("旧的"));
		expect(screen.queryByRole("listbox")).toBeNull();
		expect(within(topic("旧的")).getByText("旧回答")).toBeInTheDocument();
		expect(button(i18n.t("btw.copyAnswer"))).toBeInTheDocument();
		expect(button(i18n.t("btw.followUp"))).toBeInTheDocument();
		fireEvent.click(button(i18n.t("btw.topics")));
		expect(screen.queryByRole("region")).toBeNull();
		expect(option("旧的")).toHaveAttribute("aria-selected", "true");
		expect(option("旧的")).toHaveFocus();
		// 新话题是单独一页，也能返回
		fireEvent.click(button(i18n.t("btw.newTopic")));
		expect(
			screen.getByRole("textbox", { name: i18n.t("btw.placeholder") }),
		).toBeInTheDocument();
		fireEvent.click(button(i18n.t("btw.topics")));
		expect(screen.getByRole("listbox")).toBeInTheDocument();
	});

	it("窄屏：最新话题正在回答（刚用 /btw 发问）时打开直接进详情", () => {
		window.innerWidth = 390;
		seed(
			{ ...BASE, id: "a", question: "旧的", createdAt: 1 },
			{ ...BASE, id: "c", question: "刚问的", answer: "", status: "running" },
		);
		openDialog();
		expect(topic("刚问的")).toBeInTheDocument();
		expect(button(i18n.t("common.cancel"))).toBeInTheDocument();
	});

	it("窄屏：话题在历史读回后才出现、最新的正在回答时进详情，并且回答完也不跳回列表", async () => {
		window.innerWidth = 390;
		const runningTopic: RpcBtwHistoryRecord = {
			...BASE,
			id: "c",
			question: "刚问的",
			answer: "",
			status: "running",
			createdAt: 3,
		};
		cmd.mockImplementation((_id: string, frame: { type: string }) =>
			Promise.resolve(
				frame.type === "get_btw_history"
					? {
							records: [
								runningTopic,
								{ ...BASE, id: "a", question: "旧的", createdAt: 1 },
							],
						}
					: coreReply(frame),
			),
		);
		openDialog();
		expect(await screen.findByRole("region", { name: "刚问的" })).toBeVisible();
		act(() => {
			seed({ ...runningTopic, answer: "答完了", status: "complete" });
		});
		expect(within(topic("刚问的")).getByText("答完了")).toBeInTheDocument();
		expect(screen.queryByRole("listbox")).toBeNull();
	});

	it("打开期间到达的 btw-history 提示在对话框里显示", () => {
		openDialog();
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
