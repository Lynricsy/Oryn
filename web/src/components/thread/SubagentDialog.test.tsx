/**
 * 子 agent 离线可看：agent 不可查询时对话框用已入库的子 agent 会话条目，不向 agent 发命令，控制按钮禁用；
 * 信息面板的子 agent 列表也列出这些已入库的子 agent。存档按完整路径区分，标签读持久化标题。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Entry } from "../../api/client";
import i18n from "../../i18n";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import type { SubagentLive, ThreadViewState } from "../../state/thread-reducer";
import { uiStore } from "../../state/ui";
import { SubagentDialog } from "./Dialogs";
import { InfoPanel } from "./InfoPanel";

const { cmd } = vi.hoisted(() => ({ cmd: vi.fn() }));
vi.mock("../../realtime/socket", () => ({
	realtime: {
		cmd: (...args: unknown[]) => cmd(...args),
		subscribe: () => () => undefined,
		focus: () => undefined,
	},
	OmpCommandError: class extends Error {},
}));

const T = "T-subagent-dialog";
const ART = "2026-11-15T00-00-00_abc";
const SUB = `${ART}/0-Scout.jsonl`;
/** 同一线程里较晚的另一个主会话，子 agent 同样编号 0-Scout。 */
const ART2 = "2026-11-15T09-00-00_def";
const SUB2 = `${ART2}/0-Scout.jsonl`;
/** session_init.task 是完整任务提示词，不是单行标签。 */
const LONG_TASK = `你是负责调查的子 agent。${"请逐条核对 foo 相关文件。".repeat(12)}`;
const TITLE = "核对 foo";

function entry(
	seq: number,
	file: string,
	type: string,
	payload: Record<string, unknown>,
): Entry {
	return { seq, file, entryId: `e${seq}`, type, payload };
}

function text(seq: number, file: string, content: string): Entry {
	return entry(seq, file, "message", { message: { role: "user", content } });
}

const ENTRIES: Entry[] = [
	entry(1, `${ART}.jsonl`, "message", {
		message: { role: "user", content: "主线消息" },
	}),
	entry(2, SUB, "session_init", { agent: "scout", task: LONG_TASK, tools: [] }),
	entry(3, SUB, "message", {
		message: { role: "user", content: "子 agent 的任务" },
	}),
	entry(4, SUB, "message", {
		message: {
			role: "assistant",
			content: [{ type: "text", text: "子 agent 的回答" }],
		},
	}),
	// 标题晚于初始化与首批消息到达
	entry(5, SUB, "title_change", { title: TITLE, source: "auto" }),
];

function liveSub(patch: Partial<SubagentLive>): Record<string, SubagentLive> {
	return {
		k: {
			key: "k",
			index: 0,
			id: "0-Scout",
			agent: "scout",
			updatedAt: 0,
			...patch,
		},
	};
}

function setup(patch: Partial<ThreadViewState>, file?: string) {
	getThreadStore(T).getState().patchLocal(patch);
	return render(
		<MemoryRouter>
			<QueryClientProvider client={new QueryClient()}>
				<SubagentDialog
					threadId={T}
					id="0-Scout"
					file={file}
					onClose={() => undefined}
				/>
			</QueryClientProvider>
		</MemoryRouter>,
	);
}

function renderPanel(patch: Partial<ThreadViewState>) {
	getThreadStore(T).getState().patchLocal(patch);
	return render(
		<QueryClientProvider client={new QueryClient()}>
			<InfoPanel threadId={T} />
		</QueryClientProvider>,
	);
}

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
	resetThreadStores();
	uiStore.getState().close();
	cmd.mockReset();
	cmd.mockResolvedValue({ messages: [] });
});

describe("SubagentDialog", () => {
	it("agent 不可查询：显示已入库的转录与离线提示，不发命令，控制按钮禁用", async () => {
		setup({ agentStatus: "none", entries: ENTRIES });
		expect(await screen.findByText("子 agent 的任务")).toBeInTheDocument();
		expect(screen.getByText("子 agent 的回答")).toBeInTheDocument();
		expect(screen.queryByText("主线消息")).toBeNull();
		expect(screen.getByRole("status")).toHaveTextContent(
			i18n.t("subagent.offline"),
		);
		expect(cmd).not.toHaveBeenCalled();
		for (const name of ["取消", "终止", "复活", "发送"])
			expect(screen.getByRole("button", { name })).toBeDisabled();
		expect(screen.getByPlaceholderText("引导这个子 agent…")).toBeDisabled();
	});

	it("agent 在线但已有入库转录：仍用本地，不拉 get_subagent_messages", async () => {
		setup({ agentStatus: "ready", entries: ENTRIES });
		expect(await screen.findByText("子 agent 的回答")).toBeInTheDocument();
		expect(cmd).not.toHaveBeenCalled();
		expect(screen.queryByRole("status")).toBeNull();
		expect(screen.getByRole("button", { name: "取消" })).toBeEnabled();
	});

	it("没有入库转录且 agent 在线：向 agent 拉取", async () => {
		setup({ agentStatus: "ready", entries: [] });
		await vi.waitFor(() =>
			expect(cmd).toHaveBeenCalledWith(T, {
				type: "get_subagent_messages",
				subagentId: "0-Scout",
			}),
		);
	});

	it("冷加载标题读持久化的 title_change，不用完整 task；与热态的 description 一致", () => {
		const cold = setup({ agentStatus: "none", entries: ENTRIES });
		const coldName = screen.getByRole("dialog").getAttribute("aria-label");
		expect(coldName).toBe(`scout · ${TITLE}`);
		cold.unmount();
		resetThreadStores();
		setup({
			agentStatus: "none",
			entries: ENTRIES,
			subagents: liveSub({ description: TITLE, task: LONG_TASK }),
		});
		expect(screen.getByRole("dialog").getAttribute("aria-label")).toBe(
			coldName,
		);
	});

	it("运行中的状态徽标用中性色，状态文字保留", () => {
		setup({
			agentStatus: "none",
			entries: ENTRIES,
			subagents: liveSub({ status: "running" }),
		});
		const badge = screen.getByText("running").parentElement;
		expect(badge).toHaveClass("text-muted");
		expect(badge).not.toHaveClass("text-accent");
	});

	it("指定较早主会话的同名存档：只显示该文件的消息，不当作在线的同 id 子 agent 控制", async () => {
		setup(
			{
				agentStatus: "ready",
				entries: [text(1, SUB, "旧会话的消息"), text(2, SUB2, "新会话的消息")],
				subagents: liveSub({ status: "running" }),
			},
			SUB,
		);
		expect(await screen.findByText("旧会话的消息")).toBeInTheDocument();
		expect(screen.queryByText("新会话的消息")).toBeNull();
		expect(screen.queryByText("running")).toBeNull();
		expect(screen.getByRole("status")).toHaveTextContent(
			i18n.t("subagent.archived"),
		);
		for (const name of ["取消", "终止", "复活", "发送"])
			expect(screen.getByRole("button", { name })).toBeDisabled();
		expect(cmd).not.toHaveBeenCalled();
	});

	it("只给 id 时取当前（最晚）主会话的存档", async () => {
		setup({
			agentStatus: "none",
			entries: [text(1, SUB, "旧会话的消息"), text(2, SUB2, "新会话的消息")],
		});
		expect(await screen.findByText("新会话的消息")).toBeInTheDocument();
		expect(screen.queryByText("旧会话的消息")).toBeNull();
	});
});

describe("信息面板子 agent 列表", () => {
	it("agent 离线、无实时与快照：列出已入库的子 agent（标签是持久化标题），点开即该存档的对话框", () => {
		renderPanel({ agentStatus: "none", entries: ENTRIES });
		const row = screen.getByRole("button", { name: /scout/ });
		expect(row).toHaveTextContent(TITLE);
		expect(row).not.toHaveTextContent("请逐条核对");
		fireEvent.click(row);
		expect(uiStore.getState().dialog).toEqual({
			kind: "subagent",
			id: "0-Scout",
			file: SUB,
		});
		expect(cmd).not.toHaveBeenCalled();
	});

	it("热态（实时 description）与冷态（持久化标题）的行标签一致", () => {
		const cold = renderPanel({ agentStatus: "none", entries: ENTRIES });
		const coldText = screen.getByRole("button", { name: /scout/ }).textContent;
		cold.unmount();
		resetThreadStores();
		renderPanel({
			agentStatus: "none",
			entries: ENTRIES,
			subagents: liveSub({ description: TITLE, task: LONG_TASK }),
		});
		expect(screen.getByRole("button", { name: /scout/ }).textContent).toBe(
			coldText,
		);
	});

	it("实时子 agent 没有 description 时用其存档的持久化标题", () => {
		renderPanel({
			agentStatus: "none",
			entries: ENTRIES,
			subagents: liveSub({ task: LONG_TASK }),
		});
		const row = screen.getByRole("button", { name: /scout/ });
		expect(row).toHaveTextContent(TITLE);
		expect(row).not.toHaveTextContent("请逐条核对");
	});

	it("不同主会话的同名存档各占一行，分别打开各自的文件", () => {
		renderPanel({
			agentStatus: "none",
			entries: [text(1, SUB, "旧"), text(2, SUB2, "新")],
		});
		const rows = screen.getAllByRole("button", { name: /0-Scout/ });
		expect(rows).toHaveLength(2);
		fireEvent.click(rows[0] as HTMLElement);
		expect(uiStore.getState().dialog).toMatchObject({ file: SUB });
		fireEvent.click(rows[1] as HTMLElement);
		expect(uiStore.getState().dialog).toMatchObject({ file: SUB2 });
	});

	it("顾问转录不列为子 agent", () => {
		renderPanel({
			agentStatus: "none",
			entries: [
				text(1, `${ART}/__advisor.jsonl`, "主顾问"),
				text(2, `${ART}/0-Scout/__advisor.jsonl`, "子顾问"),
			],
		});
		expect(screen.queryByRole("button", { name: /advisor/ })).toBeNull();
	});

	it("运行中的圆点是中性色带动画，不用强调色", () => {
		renderPanel({
			agentStatus: "none",
			entries: ENTRIES,
			subagents: liveSub({ status: "running", description: TITLE }),
		});
		const row = screen.getByRole("button", { name: /scout/ });
		expect(within(row).queryByText(TITLE)).not.toBeNull();
		expect(row.querySelector(".bg-accent")).toBeNull();
		expect(row.querySelector(".animate-ping")).not.toBeNull();
	});
});
