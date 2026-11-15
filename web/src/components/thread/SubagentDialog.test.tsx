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
import { modelBrand, shortModelName } from "../../lib/model-brand";
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

function setup(patch: Partial<ThreadViewState>, file?: string, id = "0-Scout") {
	getThreadStore(T).getState().patchLocal(patch);
	return render(
		<MemoryRouter>
			<QueryClientProvider client={new QueryClient()}>
				<SubagentDialog
					threadId={T}
					id={id}
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

/** 发给 agent 的命令类型（对话框会拉 `get_subagents` 快照组派发树）。 */
function sentTypes(): string[] {
	return cmd.mock.calls.map(([, f]) => f.type);
}

/** 对话框第一行的状态头像（转录里没有 task 卡片，对话框里第一个状态头像就是它）。 */
function headerAvatar(): HTMLElement {
	const el = screen
		.getByRole("dialog")
		.querySelector<HTMLElement>("[role=img][data-state]");
	if (!el) throw new Error("对话框里没有状态头像");
	return el;
}

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
	resetThreadStores();
	uiStore.getState().close();
	cmd.mockReset();
	cmd.mockResolvedValue({ messages: [] });
});

describe("SubagentDialog", () => {
	it("agent 不可查询：显示已入库的转录，离线说明在头像 tooltip 里（不再有说明条），不发命令，控制按钮禁用", async () => {
		setup({ agentStatus: "none", entries: ENTRIES });
		expect(await screen.findByText("子 agent 的任务")).toBeInTheDocument();
		expect(screen.getByText("子 agent 的回答")).toBeInTheDocument();
		expect(screen.queryByText("主线消息")).toBeNull();
		expect(screen.queryByRole("status")).toBeNull();
		const avatar = headerAvatar();
		expect(avatar).toHaveAttribute("data-state", "archived");
		expect(avatar.getAttribute("title")).toContain("已保存的转录");
		expect(avatar).toHaveAttribute("title", i18n.t("subagent.offline"));
		expect(cmd).not.toHaveBeenCalled();
		for (const name of ["取消", "终止", "复活", "发送"])
			expect(screen.getByRole("button", { name })).toBeDisabled();
		expect(screen.getByPlaceholderText("引导这个子 agent…")).toBeDisabled();
	});

	it("agent 在线但已有入库转录：仍用本地，不拉 get_subagent_messages", async () => {
		setup({ agentStatus: "ready", entries: ENTRIES });
		expect(await screen.findByText("子 agent 的回答")).toBeInTheDocument();
		expect(sentTypes()).not.toContain("get_subagent_messages");
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

	it("第一行：状态头像、类型 badge、等宽短名，右侧是所用 role 与模型短名；第二行是 description", () => {
		setup({
			agentStatus: "ready",
			entries: ENTRIES,
			subagents: liveSub({
				id: "0-Scout",
				status: "started",
				description: TITLE,
				progress: {
					status: "running",
					modelRole: "smol",
					resolvedModel: "anthropic/claude-sonnet-4-5",
				},
			}),
		});
		const avatar = headerAvatar();
		expect(avatar).toHaveAttribute("data-state", "running");
		const first = avatar.parentElement as HTMLElement;
		expect(within(first).getByText("scout")).toBeInTheDocument();
		const short = within(first).getByText("0-Scout");
		expect(short).toHaveClass("font-mono");
		expect(within(first).getByText("smol")).toBeInTheDocument();
		const model = "anthropic/claude-sonnet-4-5";
		expect(
			within(first).getByTitle(i18n.t("subagent.model", { model })),
		).toHaveTextContent(shortModelName("claude-sonnet-4-5", modelBrand(model)));
		// 状态由头像表示，不再有状态徽标与说明条
		expect(screen.queryByText("running")).toBeNull();
		expect(screen.queryByRole("status")).toBeNull();
		expect(first.nextElementSibling).toHaveTextContent(TITLE);
	});

	it("嵌套子 agent 的短名是 id 的最后一段，完整 id 进悬停提示", () => {
		setup(
			{
				agentStatus: "none",
				entries: [],
				subagents: liveSub({ id: "FixTest1.Leaf2", agent: "task" }),
			},
			undefined,
			"FixTest1.Leaf2",
		);
		const first = headerAvatar().parentElement as HTMLElement;
		expect(within(first).getByText("task")).toBeInTheDocument();
		expect(within(first).getByText("Leaf2")).toHaveAttribute(
			"title",
			"FixTest1.Leaf2",
		);
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
		expect(screen.queryByRole("status")).toBeNull();
		const avatar = headerAvatar();
		expect(avatar).toHaveAttribute("data-state", "archived");
		expect(avatar.getAttribute("title")).toContain("已保存的转录");
		expect(avatar).toHaveAttribute("title", i18n.t("subagent.archived"));
		for (const name of ["取消", "终止", "复活", "发送"])
			expect(screen.getByRole("button", { name })).toBeDisabled();
		expect(sentTypes()).toEqual(["get_subagents"]);
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

	it("行首是 agent 头像：运行中是中性色的旋转光环，不用强调色；标题栏汇总用同一种光环", () => {
		renderPanel({
			agentStatus: "none",
			entries: ENTRIES,
			subagents: liveSub({ status: "running", description: TITLE }),
		});
		const row = screen.getByRole("button", { name: /scout/ });
		expect(within(row).queryByText(TITLE)).not.toBeNull();
		const avatar = within(row).getByRole("img");
		expect(avatar).toHaveAttribute("data-state", "running");
		expect(avatar).toHaveAttribute("title", i18n.t("subagent.state.running"));
		expect(avatar.querySelector(".agent-ring-spin")).not.toBeNull();
		expect(row.querySelector(".bg-accent")).toBeNull();
		expect(row.querySelector(".animate-ping")).toBeNull();
		const summary = screen.getByText("1 个运行中");
		expect(
			summary.closest("span")?.querySelector(".agent-ring-spin"),
		).not.toBeNull();
	});

	it("只剩存档的行：头像是存档态，tooltip 说明只剩保存的转录", () => {
		renderPanel({ agentStatus: "none", entries: ENTRIES });
		const avatar = within(
			screen.getByRole("button", { name: /scout/ }),
		).getByRole("img");
		expect(avatar).toHaveAttribute("data-state", "archived");
		expect(avatar).toHaveAttribute("title", i18n.t("subagent.offline"));
	});
});

describe("子 agent 派发树", () => {
	/** 嵌套存档：子 agent 的会话在派发者会话去掉 `.jsonl` 的产物目录里。 */
	const WORKER = `${ART}/0-Scout/0-Scout.0-Worker.jsonl`;
	const LEAF = `${ART}/0-Scout/0-Scout.0-Worker/0-Scout.0-Worker.0-Leaf.jsonl`;
	const OTHER = `${ART}/1-Other.jsonl`;
	const NESTED: Entry[] = [
		entry(1, SUB, "session_init", { agent: "scout", task: "t" }),
		entry(2, WORKER, "session_init", { agent: "worker", task: "t" }),
		entry(3, LEAF, "session_init", { agent: "leaf", task: "t" }),
		entry(4, OTHER, "session_init", { agent: "other", task: "t" }),
	];
	const LIVE: Record<string, SubagentLive> = Object.fromEntries(
		[
			["0-Scout", "scout", undefined],
			["0-Scout.0-Worker", "worker", "0-Scout"],
			["0-Scout.0-Worker.0-Leaf", "leaf", "0-Scout.0-Worker"],
			["1-Other", "other", undefined],
		].map(([id, agent, parentSubagentId], index) => [
			id,
			{
				key: id as string,
				index,
				id,
				agent,
				parentSubagentId,
				status: "started",
				updatedAt: 0,
			},
		]),
	);

	/** 子 agent 卡片里每行：agent 名、嵌套深度，以及有没有连到上级的 L 形引导线。 */
	function panelRows(): [string, number, boolean][] {
		// closest 返回 Element；根节点的按钮最近的 <ul> 就是卡片的顶层列表
		const list = screen
			.getByRole("button", { name: /scout/ })
			.closest("ul") as HTMLElement;
		return within(list)
			.getAllByRole("listitem")
			.map((li) => {
				let depth = 0;
				for (let p = li.parentElement; p && p !== list; p = p.parentElement)
					if (p.tagName === "LI") depth++;
				// 引导线画在本行（li 的第一个子元素）里
				const elbow = li.firstElementChild?.querySelector(
					":scope > [data-guide=elbow]",
				);
				return [
					li.querySelector(".font-medium")?.textContent ?? "",
					depth,
					elbow != null,
				];
			});
	}

	function renderDialog(
		id: string,
		patch: Partial<ThreadViewState>,
		file?: string,
	) {
		getThreadStore(T).getState().patchLocal(patch);
		return render(
			<MemoryRouter>
				<QueryClientProvider client={new QueryClient()}>
					<SubagentDialog
						threadId={T}
						id={id}
						file={file}
						onClose={() => undefined}
					/>
				</QueryClientProvider>
			</MemoryRouter>,
		);
	}

	it("orb 冻结只剩存档：按存档路径推出层级，下级以引导线连到上级，摘要计数含所有层级", () => {
		renderPanel({ agentStatus: "none", entries: NESTED });
		expect(panelRows()).toEqual([
			["scout", 0, false],
			["worker", 1, true],
			["leaf", 2, true],
			["other", 0, false],
		]);
		expect(screen.getByText("共 4 个")).toBeInTheDocument();
	});

	it("热态（实时帧的 parentSubagentId）与冷态得到同一棵树", () => {
		const cold = renderPanel({ agentStatus: "none", entries: NESTED });
		const coldRows = panelRows();
		cold.unmount();
		resetThreadStores();
		renderPanel({ agentStatus: "none", entries: [], subagents: LIVE });
		expect(panelRows()).toEqual(coldRows);
		expect(screen.getByText("4 个运行中")).toBeInTheDocument();
	});

	it("刷新后（get_subagents 快照）也是同一棵树", async () => {
		cmd.mockImplementation(async (_t: string, f: { type: string }) =>
			f.type === "get_subagents"
				? {
						subagents: Object.values(LIVE).map(
							({ id, agent, parentSubagentId }) => ({
								id,
								agent,
								parentSubagentId,
								index: 0,
								status: "running",
								lastUpdate: 0,
							}),
						),
					}
				: {},
		);
		renderPanel({ agentStatus: "ready", entries: [] });
		await screen.findByText("4 个运行中");
		expect(panelRows()).toEqual([
			["scout", 0, false],
			["worker", 1, true],
			["leaf", 2, true],
			["other", 0, false],
		]);
	});

	it("孙 agent 对话框显示上级，点开换成上级的对话框；上级列出直接下级，可点开", () => {
		const leaf = renderDialog("0-Scout.0-Worker.0-Leaf", {
			agentStatus: "none",
			entries: NESTED,
		});
		const nav = screen.getByRole("navigation", { name: "派发层级" });
		expect(nav).toHaveTextContent("上级");
		expect(nav).not.toHaveTextContent("下级");
		fireEvent.click(within(nav).getByRole("button", { name: /worker/ }));
		expect(uiStore.getState().dialog).toEqual({
			kind: "subagent",
			id: "0-Scout.0-Worker",
			file: WORKER,
		});
		leaf.unmount();

		renderDialog(
			"0-Scout.0-Worker",
			{ agentStatus: "none", entries: NESTED },
			WORKER,
		);
		const nav2 = screen.getByRole("navigation", { name: "派发层级" });
		expect(nav2).toHaveTextContent("下级 1 个");
		expect(
			within(nav2)
				.getAllByRole("button")
				.map((b) => b.querySelector(".font-medium")?.textContent),
		).toEqual(["scout", "leaf"]);
		fireEvent.click(within(nav2).getByRole("button", { name: /leaf/ }));
		expect(uiStore.getState().dialog).toEqual({
			kind: "subagent",
			id: "0-Scout.0-Worker.0-Leaf",
			file: LEAF,
		});
		fireEvent.click(within(nav2).getByRole("button", { name: /scout/ }));
		expect(uiStore.getState().dialog).toEqual({
			kind: "subagent",
			id: "0-Scout",
			file: SUB,
		});
	});

	it("在线时上级入口按 id 打开（交给对话框取实时状态与控制）", () => {
		renderDialog("0-Scout.0-Worker.0-Leaf", {
			agentStatus: "none",
			entries: [],
			subagents: LIVE,
		});
		const nav = screen.getByRole("navigation", { name: "派发层级" });
		fireEvent.click(within(nav).getByRole("button", { name: /worker/ }));
		expect(uiStore.getState().dialog).toEqual({
			kind: "subagent",
			id: "0-Scout.0-Worker",
		});
	});

	it("根上、没有下级的子 agent 不显示层级区", () => {
		renderDialog("1-Other", { agentStatus: "none", entries: NESTED });
		expect(screen.queryByRole("navigation", { name: "派发层级" })).toBeNull();
	});
});
