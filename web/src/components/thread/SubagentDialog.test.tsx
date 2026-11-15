/**
 * 子 agent 离线可看：agent 不可查询时对话框用已入库的子 agent 会话条目，不向 agent 发命令，控制按钮禁用；
 * 信息面板的子 agent 列表也列出这些已入库的子 agent。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Entry } from "../../api/client";
import i18n from "../../i18n";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import type { ThreadViewState } from "../../state/thread-reducer";
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

function entry(
	seq: number,
	file: string,
	type: string,
	payload: Record<string, unknown>,
): Entry {
	return { seq, file, entryId: `e${seq}`, type, payload };
}

const ENTRIES: Entry[] = [
	entry(1, `${ART}.jsonl`, "message", {
		message: { role: "user", content: "主线消息" },
	}),
	entry(2, SUB, "session_init", { agent: "scout", task: "找 foo", tools: [] }),
	entry(3, SUB, "message", {
		message: { role: "user", content: "子 agent 的任务" },
	}),
	entry(4, SUB, "message", {
		message: {
			role: "assistant",
			content: [{ type: "text", text: "子 agent 的回答" }],
		},
	}),
];

function setup(patch: Partial<ThreadViewState>) {
	getThreadStore(T).getState().patchLocal(patch);
	return render(
		<MemoryRouter>
			<QueryClientProvider client={new QueryClient()}>
				<SubagentDialog threadId={T} id="0-Scout" onClose={() => undefined} />
			</QueryClientProvider>
		</MemoryRouter>,
	);
}

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
	resetThreadStores();
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
});

describe("信息面板子 agent 列表", () => {
	it("agent 离线、无实时与快照：列出已入库的子 agent，点开即对话框", () => {
		getThreadStore(T)
			.getState()
			.patchLocal({ agentStatus: "none", entries: ENTRIES });
		render(
			<QueryClientProvider client={new QueryClient()}>
				<InfoPanel threadId={T} />
			</QueryClientProvider>,
		);
		const row = screen.getByRole("button", { name: /scout/ });
		expect(row).toHaveTextContent("找 foo");
		fireEvent.click(row);
		expect(uiStore.getState().dialog).toEqual({
			kind: "subagent",
			id: "0-Scout",
		});
		expect(cmd).not.toHaveBeenCalled();
	});
});
