/**
 * 设置 → 全局助手（M19 步骤 1.6，M26 步骤 1.6 / 4.2）：联网开关保存、MCP 逐个关闭、不可用原因显示；
 * 后台研究卡片的总开关、今天已花、每日上限与研究偏好整体 PUT；MCP 服务器的研究可用工具（列出勾选、列不出时手动输入）。
 * 走 mock 后端。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MockServer } from "../../mock/server";
import AssistantSettings from "./AssistantSettings";

let server: MockServer;
let puts: unknown[];
let researchPuts: unknown[];

beforeEach(() => {
	server = new MockServer();
	puts = [];
	researchPuts = [];
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			const u = new URL(req.url);
			if (req.method === "PUT" && u.pathname === "/api/v1/settings/assistant")
				puts.push(await req.clone().json());
			if (
				req.method === "PUT" &&
				u.pathname === "/api/v1/assistant/research/settings"
			)
				researchPuts.push(await req.clone().json());
			return (
				(await server.handleFetch(req)) ?? new Response(null, { status: 404 })
			);
		},
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

function mount() {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={["/settings/assistant"]}>
				<AssistantSettings />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

/** MCP 服务器所在的行（定位钩子挂在行标题上）。 */
const row = (name: string) =>
	document
		.querySelector(`[data-mcp-server="${name}"]`)
		?.closest("li") as HTMLElement;

/** 自绘下拉：点开后点选项。 */
async function choose(combo: HTMLElement, option: string | RegExp) {
	fireEvent.click(combo);
	fireEvent.click(await screen.findByRole("option", { name: option }));
}

describe("设置 → 全局助手", () => {
	it("联网开关保存整份设置", async () => {
		mount();
		const web = await screen.findByRole("switch", { name: "允许助手联网" });
		expect(web).toBeChecked();
		fireEvent.click(web);
		await waitFor(() =>
			expect(server.assistant.settings.webAccess).toBe(false),
		);
		expect(puts).toEqual([
			{ webAccess: false, disabledMcpServers: [], computer: {} },
		]);
		await waitFor(() => expect(web).not.toBeChecked());
		expect(web).toBeEnabled();
	});

	it("可用的服务器逐个关闭与重新打开", async () => {
		mount();
		const github = await screen.findByRole("switch", {
			name: "助手使用 github",
		});
		const linear = screen.getByRole("switch", { name: "助手使用 linear" });
		expect(github).toBeChecked();
		expect(linear).toBeChecked();
		expect(row("github")).toHaveTextContent("api.githubcopilot.com");
		expect(row("linear")).toHaveTextContent("SSE");

		fireEvent.click(github);
		await waitFor(() =>
			expect(server.assistant.settings.disabledMcpServers).toEqual(["github"]),
		);
		await waitFor(() => expect(github).not.toBeChecked());
		expect(linear).toBeChecked();
		await waitFor(() => expect(github).toBeEnabled());

		fireEvent.click(github);
		await waitFor(() =>
			expect(server.assistant.settings.disabledMcpServers).toEqual([]),
		);
		await waitFor(() => expect(github).toBeChecked());
		expect(puts).toHaveLength(2);
	});

	it("不可用的服务器显示原因、没有开关", async () => {
		mount();
		await screen.findByRole("switch", { name: "助手使用 github" });
		expect(row("filesystem")).toHaveTextContent(
			"本地进程型服务器，助手的沙箱里无法运行",
		);
		expect(row("notion")).toHaveTextContent("需要 OAuth 登录，暂不支持");
		expect(row("notion")).toHaveTextContent("不可用");
		expect(
			screen.queryByRole("switch", { name: "助手使用 notion" }),
		).toBeNull();
		expect(
			screen.queryByRole("switch", { name: "助手使用 filesystem" }),
		).toBeNull();
	});

	it("助手的电脑：provider 与类型进设置，换 provider 时类型回到默认", async () => {
		mount();
		const group = await screen.findByRole("region", { name: "助手的电脑" });
		const providerSel = within(group).getByRole("combobox", {
			name: "Provider",
		});
		await waitFor(() => expect(providerSel).toBeEnabled());
		await choose(providerSel, "local");
		await waitFor(() =>
			expect(server.assistant.settings.computer).toEqual({ provider: "local" }),
		);
		const typeSel = within(group).getByRole("combobox", { name: "类型" });
		await waitFor(() => expect(typeSel).toBeEnabled());
		await choose(typeSel, /^large/);
		await waitFor(() =>
			expect(server.assistant.settings.computer).toEqual({
				provider: "local",
				orbType: "large",
			}),
		);
		await waitFor(() => expect(providerSel).toBeEnabled());
		await choose(providerSel, "默认");
		await waitFor(() => expect(server.assistant.settings.computer).toEqual({}));
	});

	it("页内按顺序是助手身份、模型、提示词、联网与 MCP、后台研究、助手的电脑、助手记忆", async () => {
		mount();
		await screen.findByRole("switch", { name: "允许助手联网" });
		const titles = screen
			.getAllByRole("heading", { level: 3 })
			.map((h) => h.textContent);
		expect(titles.slice(0, 6)).toEqual([
			"全局助手",
			"模型与思考档位",
			"自定义提示词",
			"联网与 MCP",
			"后台研究",
			"助手的电脑",
		]);
		expect(
			await screen.findByTestId("owner-memory-section"),
		).toBeInTheDocument();
	});

	it("后台研究（M26）：总开关整体 PUT，带今天已花；上限与偏好各自保存", async () => {
		mount();
		const master = await screen.findByRole("switch", {
			name: "开启后台研究",
		});
		expect(master).not.toBeChecked();
		expect(screen.getByTestId("research-spent")).toHaveTextContent(
			"今天已花 $0.42，上限 $20.00",
		);
		fireEvent.click(master);
		await waitFor(() => expect(server.assistant.research.enabled).toBe(true));
		expect(researchPuts).toEqual([
			{
				enabled: true,
				web: true,
				mine: true,
				internal: true,
				mcp: false,
				dailyUsd: 20,
				preferences: "",
				mcpTools: {},
			},
		]);
		await waitFor(() => expect(master).toBeChecked());
		const cap = screen.getByRole("spinbutton", {
			name: /^每日花费上限（美元）/,
		});
		await waitFor(() => expect(cap).toBeEnabled());
		fireEvent.change(cap, { target: { value: "0.01" } });
		expect(screen.getByRole("alert")).toHaveTextContent(
			"请输入 0.1 到 1000 之间的数",
		);
		fireEvent.blur(cap);
		expect(researchPuts).toHaveLength(1);
		fireEvent.change(cap, { target: { value: "5" } });
		fireEvent.blur(cap);
		await waitFor(() => expect(server.assistant.research.dailyUsd).toBe(5));
		const prefs = screen.getByRole("textbox", { name: /^研究偏好/ });
		await waitFor(() => expect(prefs).toBeEnabled());
		fireEvent.change(prefs, { target: { value: "只看一手来源" } });
		expect(screen.getByTestId("research-preferences-count")).toHaveTextContent(
			"6 / 1500",
		);
		const savePrefs = screen.getByRole("button", { name: "保存偏好" });
		await waitFor(() => expect(savePrefs).toBeEnabled());
		fireEvent.click(savePrefs);
		await waitFor(() =>
			expect(server.assistant.research.preferences).toBe("只看一手来源"),
		);
		expect(server.assistant.research).toMatchObject({
			enabled: true,
			dailyUsd: 5,
		});
	});

	it("研究可用工具（M26）：列出工具、不预先勾选，勾一个保存后 PUT 只带它", async () => {
		mount();
		await screen.findByRole("switch", { name: "助手使用 github" });
		const open = within(row("github")).getByRole("button", {
			name: "研究可用工具（0）",
		});
		fireEvent.click(open);
		const dialog = await screen.findByRole("dialog", {
			name: "研究可用工具 · github",
		});
		expect(dialog).toHaveTextContent(
			"只勾选不会对外发送数据、不会改动任何东西的读取工具。「自报只读」是服务器自己的说法，不是保证。",
		);
		const boxes = await within(dialog).findAllByRole("checkbox");
		expect(boxes).toHaveLength(3);
		for (const b of boxes) expect(b).not.toBeChecked();
		const tool = (name: string) =>
			dialog.querySelector(`[data-mcp-tool="${name}"]`) as HTMLElement;
		expect(tool("list_issues")).toHaveTextContent("自报只读");
		expect(tool("create_issue")).not.toHaveTextContent("自报只读");
		expect(tool("search_code")).not.toHaveTextContent("自报只读");

		fireEvent.click(within(tool("list_issues")).getByRole("checkbox"));
		fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
		await waitFor(() =>
			expect(server.assistant.research.mcpTools).toEqual({
				github: ["list_issues"],
			}),
		);
		expect(researchPuts).toEqual([
			expect.objectContaining({ mcpTools: { github: ["list_issues"] } }),
		]);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		await waitFor(() =>
			expect(
				within(row("github")).getByRole("button", {
					name: "研究可用工具（1）",
				}),
			).toBeInTheDocument(),
		);
	});

	it("研究可用工具（M26）：列不出工具（SSE）时改为手动输入", async () => {
		mount();
		await screen.findByRole("switch", { name: "助手使用 linear" });
		fireEvent.click(
			within(row("linear")).getByRole("button", { name: /^研究可用工具/ }),
		);
		const dialog = await screen.findByRole("dialog", {
			name: "研究可用工具 · linear",
		});
		const input = await within(dialog).findByRole("textbox", {
			name: "工具名（每行一个）",
		});
		expect(dialog).toHaveTextContent("这是 SSE 服务器");
		expect(within(dialog).queryByRole("checkbox")).toBeNull();
		fireEvent.change(input, {
			target: { value: " list_issues \n\nget_issue\nlist_issues" },
		});
		fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
		await waitFor(() =>
			expect(server.assistant.research.mcpTools).toEqual({
				linear: ["list_issues", "get_issue"],
			}),
		);
	});

	it("模型与思考档位：默认跟随系统默认，支持选择、修改并恢复默认", async () => {
		mount();
		const card = await screen.findByTestId("assistant-model-card");
		expect(
			within(card).getByTestId("assistant-model-default"),
		).toHaveTextContent("跟随默认");
		expect(within(card).getByTestId("assistant-thinking")).toHaveTextContent(
			"跟随默认",
		);

		// 选择模型（自由输入）
		fireEvent.click(within(card).getByRole("button", { name: "更改" }));
		const input = await screen.findByRole("textbox", {
			name: "搜索或输入 provider/模型 id",
		});
		fireEvent.change(input, {
			target: { value: "anthropic/claude-opus-4-6" },
		});
		fireEvent.click(
			// 高亮行带 ⌘/Ctrl+数字键帽，无障碍名以「使用 …」开头
			screen.getByRole("button", { name: /^使用 anthropic\/claude-opus-4-6/ }),
		);
		await waitFor(() =>
			expect(within(card).getByTestId("assistant-model")).toHaveTextContent(
				"anthropic/claude-opus-4-6",
			),
		);
		expect(server.assistant.settings.model).toEqual({
			provider: "anthropic",
			id: "claude-opus-4-6",
		});

		// 选择思考档位
		fireEvent.click(within(card).getByTestId("assistant-thinking"));
		fireEvent.click(await screen.findByRole("menuitemradio", { name: "高" }));
		await waitFor(() =>
			expect(within(card).getByTestId("assistant-thinking")).toHaveTextContent(
				"高",
			),
		);
		expect(server.assistant.settings.thinkingLevel).toBe("high");
		expect(puts.at(-1)).toEqual(
			expect.objectContaining({
				model: { provider: "anthropic", id: "claude-opus-4-6" },
				thinkingLevel: "high",
			}),
		);

		// 分别恢复默认
		const resets = within(card).getAllByRole("button", { name: "恢复默认" });
		expect(resets).toHaveLength(2);
		const [resetModel] = resets;
		if (resetModel) fireEvent.click(resetModel);
		await waitFor(() =>
			expect(server.assistant.settings.model).toBeUndefined(),
		);
		fireEvent.click(within(card).getByRole("button", { name: "恢复默认" }));
		await waitFor(() =>
			expect(server.assistant.settings.thinkingLevel).toBeUndefined(),
		);
		expect(within(card).getByTestId("assistant-thinking")).toHaveTextContent(
			"跟随默认",
		);
	});

	it("自定义提示词：编辑保存、超长校验与清空", async () => {
		mount();
		const card = await screen.findByTestId("assistant-prompt-card");
		const textarea = within(card).getByRole("textbox", {
			name: "自定义提示词",
		});
		const saveBtn = within(card).getByRole("button", { name: "保存" });
		expect(saveBtn).toBeDisabled();
		expect(within(card).queryByRole("button", { name: "清空" })).toBeNull();

		// 超长拦截
		fireEvent.change(textarea, { target: { value: "字".repeat(8001) } });
		expect(saveBtn).toBeDisabled();
		expect(within(card).getByRole("alert")).toHaveTextContent("8000");

		// 正常输入并保存
		fireEvent.change(textarea, {
			target: { value: "  始终用简体中文回答，先给结论。  " },
		});
		expect(saveBtn).toBeEnabled();
		fireEvent.click(saveBtn);
		await waitFor(() =>
			expect(server.assistant.settings.systemPrompt).toBe(
				"始终用简体中文回答，先给结论。",
			),
		);
		expect(puts.at(-1)).toEqual(
			expect.objectContaining({
				systemPrompt: "始终用简体中文回答，先给结论。",
			}),
		);
		await waitFor(() => expect(saveBtn).toBeDisabled());

		// 一键清空
		const clearBtn = within(card).getByRole("button", { name: "清空" });
		fireEvent.click(clearBtn);
		await waitFor(() =>
			expect(server.assistant.settings.systemPrompt).toBeUndefined(),
		);
		expect(textarea).toHaveValue("");
	});
});
