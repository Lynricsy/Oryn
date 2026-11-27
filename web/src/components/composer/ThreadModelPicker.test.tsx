/**
 * 线程的模型选择（M9.6 §7.2、M13，统一选择器 `ModelPicker`，锚在输入坞模型按钮上的弹层「模型」）：模型列表读中央模型目录
 * （`GET /api/v1/models/catalog`，只列 chat），列表里直接选 = 手动选择，发 oryn_select_model{provider, modelId}；
 * 旧 Core（Unknown command）回退 set_model。模型行带 `data-model`，角色行带 `data-role`；选择器内的页脚「刷新」带
 * `refresh=true`，旧结果给淡色提示。思考级别：改已选中模型的级别就地 set_thinking_level（不关）；点别的模型的级别 =
 * 选它 + set_thinking_level 后关闭。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../../api/client";
import { resetLegacyCore } from "../../lib/model-select";
import { OmpCommandError } from "../../realtime/socket";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import { type DialogState, uiStore } from "../../state/ui";
import { ModelChip } from "./ComposerMeta";

const { cmd } = vi.hoisted(() => ({ cmd: vi.fn() }));
vi.mock("../../realtime/socket", () => ({
	realtime: {
		cmd: (...args: unknown[]) => cmd(...args),
		subscribe: () => () => undefined,
		focus: () => undefined,
	},
	OmpCommandError: class extends Error {
		constructor(_command: string, _code: string, message: string) {
			super(message);
		}
	},
}));

const T = "T-model-dialog";
let legacy = false;
/** oryn_select_model 应答一个普通错误（不是旧 Core） */
let failSelect = false;

function catalogModel(
	provider: string,
	id: string,
	name: string,
	kind = "chat",
): Schemas["CatalogModel"] {
	return {
		provider,
		id,
		selector: `${provider}/${id}`,
		name,
		kind,
		reasoning: true,
		thinking: ["low", "medium", "high"],
	};
}

const MODELS = [
	catalogModel("openai", "gpt-6.1-sol", "GPT-6.1 Sol"),
	catalogModel("anthropic", "claude-opus-5-5", "Claude Opus 5.5"),
	// 非对话模型不进选择器
	catalogModel("openai", "text-embedding-3-small", "Embedding", "embedding"),
];

/** get_model_roles 回读形状：default 配成 Sol。 */
const DEFAULT_ROLE = {
	id: "default",
	name: "Default",
	section: "chat",
	model: "openai/gpt-6.1-sol",
	selected: { provider: "openai", id: "gpt-6.1-sol" },
};

/** 目录请求的 URL；`catalog` 决定下一次应答（返回 Promise 可挂起）。 */
let catalogUrls: URL[];
let catalog: (url: URL) => unknown;

const sent = () =>
	cmd.mock.calls
		.map((c) => c[1] as Record<string, unknown>)
		.filter((f) => !String(f.type).startsWith("get_"));

beforeEach(() => {
	cmd.mockReset();
	resetThreadStores();
	resetLegacyCore();
	localStorage.clear();
	legacy = false;
	failSelect = false;
	catalogUrls = [];
	catalog = () => ({ models: MODELS, fetchedAt: 0, stale: false });
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			const url = new URL(req.url);
			if (url.pathname !== "/api/v1/models/catalog")
				return new Response(null, { status: 404 });
			catalogUrls.push(url);
			return Response.json(await catalog(url));
		},
	);
	cmd.mockImplementation(async (_t: string, f: Record<string, unknown>) => {
		if (f.type === "get_state") return {};
		// 只有一个角色 default（指向 Sol）：对象区一行，当前模型不是 Sol 时没有当前角色
		if (f.type === "get_settings")
			return { values: { cycleOrder: ["default"] } };
		if (f.type === "get_model_roles") return { roles: [DEFAULT_ROLE] };
		if (failSelect && f.type === "oryn_select_model")
			throw new Error("选择失败：boom");
		if (legacy && String(f.type).startsWith("oryn_"))
			throw new OmpCommandError(
				String(f.type),
				"command_failed",
				`Unknown command: ${String(f.type)}`,
			);
		return {};
	});
});

afterEach(() => {
	uiStore.getState().close();
	vi.unstubAllGlobals();
});

/** 打开选择器（`/model` 与点模型按钮都走 uiStore），渲染输入坞的模型按钮（弹层锚在它上面）。 */
function openDialog(dialog: DialogState = { kind: "model" }) {
	uiStore.getState().open(dialog);
	render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<MemoryRouter>
				<ModelChip threadId={T} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const picker = () => screen.getByRole("dialog", { name: "模型" });

async function pickSol() {
	const row = await vi.waitFor(() => {
		const el = document.querySelector<HTMLElement>(
			'[data-model="openai/gpt-6.1-sol"]',
		);
		if (!el) throw new Error("模型列表未就绪");
		return el;
	});
	fireEvent.click(row);
}

const rows = () =>
	[...document.querySelectorAll("[data-model]")].map((e) =>
		e.getAttribute("data-model"),
	);

const radio = (name: RegExp) =>
	within(screen.getByRole("radiogroup", { name: "思考级别" })).getByRole(
		"radio",
		{ name },
	);

/** 线程当前是 Claude Opus（不是任何角色的模型，选中项落在模型行）。 */
function onClaude(level = "high") {
	getThreadStore(T)
		.getState()
		.apply([
			{
				type: "state",
				state: {
					model: {
						provider: "anthropic",
						id: "claude-opus-5-5",
						name: "Claude Opus 5.5",
					},
					thinkingLevel: level,
					thinkingConfigured: level,
				} as never,
			},
		]);
}

describe("ThreadModelPicker", () => {
	it("模型按钮带 aria-expanded：开着时再点一下就关闭，关着时点开", async () => {
		openDialog();
		await pickReady();
		const chip = document.querySelector<HTMLElement>("button[aria-expanded]");
		expect(chip?.getAttribute("aria-expanded")).toBe("true");
		expect(picker()).toBeTruthy();
		act(() => {
			chip?.click();
		});
		expect(uiStore.getState().dialog).toBeNull();
		expect(screen.queryByRole("dialog", { name: "模型" })).toBeNull();
		expect(chip?.getAttribute("aria-expanded")).toBe("false");
		act(() => {
			chip?.click();
		});
		expect(uiStore.getState().dialog).toEqual({ kind: "model" });
		expect(picker()).toBeTruthy();
	});

	it("点模型发 oryn_select_model{provider, modelId}，没有 set_model；之后刷新状态并关闭", async () => {
		openDialog();
		await pickSol();
		await vi.waitFor(() => expect(uiStore.getState().dialog).toBeNull());
		expect(sent()).toEqual([
			{ type: "oryn_select_model", provider: "openai", modelId: "gpt-6.1-sol" },
		]);
		expect(cmd.mock.calls.some((c) => c[1].type === "get_state")).toBe(true);
	});

	it("模型列表来自中央目录，只列 chat，不向线程要模型列表", async () => {
		openDialog();
		await pickReady();
		expect(within(picker()).getByPlaceholderText("搜索模型…")).toBeTruthy();
		expect(rows()).toEqual(["openai/gpt-6.1-sol", "anthropic/claude-opus-5-5"]);
		expect(catalogUrls).toHaveLength(1);
		expect(catalogUrls[0]?.searchParams.has("refresh")).toBe(false);
		// 线程只收到角色相关的读取（get_settings / get_model_roles），没有任何模型列表命令
		expect(
			cmd.mock.calls.filter((c) => String(c[1].type).includes("models")),
		).toEqual([]);
	});

	it("当前模型不属于任何角色：选中项是模型行，带 aria-current 与 font-medium", async () => {
		onClaude();
		openDialog();
		await pickReady();
		const on = document.querySelector("button[data-model].font-medium");
		expect(on?.getAttribute("data-model")).toBe("anthropic/claude-opus-5-5");
		expect(on?.getAttribute("aria-current")).toBe("true");
	});

	it("选模型失败：对话框不关，显示错误", async () => {
		failSelect = true;
		openDialog();
		await pickSol();
		expect(await screen.findByText(/选择失败：boom/)).toBeTruthy();
		expect(uiStore.getState().dialog).toEqual({ kind: "model" });
	});

	it("已选中模型改级别：就地发 set_thinking_level，不换模型，对话框不关", async () => {
		onClaude();
		openDialog();
		await pickReady();
		await vi.waitFor(() =>
			expect(radio(/^高/).getAttribute("aria-checked")).toBe("true"),
		);
		fireEvent.click(radio(/^低/));
		await vi.waitFor(() =>
			expect(sent()).toEqual([{ type: "set_thinking_level", level: "low" }]),
		);
		expect(uiStore.getState().dialog).toEqual({ kind: "model" });
	});

	it("点别的模型的级别：先选模型再 set_thinking_level，然后关闭", async () => {
		onClaude();
		openDialog();
		await pickReady();
		const sol = await vi.waitFor(() => {
			const el = document.querySelector<HTMLElement>(
				'[data-model="openai/gpt-6.1-sol"]',
			);
			if (!el) throw new Error("模型列表未就绪");
			return el;
		});
		fireEvent.mouseMove(sol);
		// 别的模型没有预设：一个都不勾
		await vi.waitFor(() =>
			expect(radio(/^中/).getAttribute("aria-checked")).toBe("false"),
		);
		fireEvent.click(radio(/^中/));
		await vi.waitFor(() => expect(uiStore.getState().dialog).toBeNull());
		expect(sent()).toEqual([
			{ type: "oryn_select_model", provider: "openai", modelId: "gpt-6.1-sol" },
			{ type: "set_thinking_level", level: "medium" },
		]);
	});

	it("旧 Core：收到 Unknown command 后回退 set_model，不显示错误", async () => {
		legacy = true;
		openDialog();
		await pickSol();
		await vi.waitFor(() => expect(uiStore.getState().dialog).toBeNull());
		expect(sent()).toEqual([
			{ type: "oryn_select_model", provider: "openai", modelId: "gpt-6.1-sol" },
			{ type: "set_model", provider: "openai", modelId: "gpt-6.1-sol" },
		]);
		expect(screen.queryByText(/Unknown command/)).toBeNull();
	});

	it("加载中：转圈带文案", async () => {
		catalog = () => new Promise(() => undefined);
		openDialog();
		expect(await screen.findByText("正在读取模型列表…")).toBeTruthy();
	});

	it("筛选无结果：说明没有匹配的模型", async () => {
		openDialog();
		await pickReady();
		fireEvent.change(screen.getByPlaceholderText("搜索模型…"), {
			target: { value: "zzz" },
		});
		expect(screen.getByText("没有匹配「zzz」的模型")).toBeTruthy();
		expect(document.querySelector("[data-model]")).toBeNull();
	});

	it("没有任何对话模型：说明需要先添加供应商", async () => {
		catalog = () => ({ models: [MODELS[2]], fetchedAt: 0, stale: false });
		openDialog();
		expect(await screen.findByText(/没有可用的模型/)).toBeTruthy();
	});

	it("带 query 打开（/model <选择器> 解析不出时）：预填搜索框并按它筛选", async () => {
		openDialog({ kind: "model", query: "sol" });
		await pickReady();
		expect(
			(screen.getByPlaceholderText("搜索模型…") as HTMLInputElement).value,
		).toBe("sol");
		expect(rows()).toEqual(["openai/gpt-6.1-sol"]);
	});

	it("旧结果给淡色提示；页脚「刷新」带 refresh=true，新结果替换列表、提示消失", async () => {
		catalog = (url) =>
			url.searchParams.get("refresh") === "true"
				? {
						models: [catalogModel("xai", "grok-5", "Grok 5")],
						fetchedAt: 1,
						stale: false,
					}
				: { models: MODELS, fetchedAt: 0, stale: true };
		openDialog();
		await pickReady();
		expect(await screen.findByTestId("catalog-stale")).toBeTruthy();
		// 目录页脚在选择器里（对话框内），不在对话框的按钮区
		fireEvent.click(within(picker()).getByRole("button", { name: "刷新" }));
		await vi.waitFor(() => expect(rows()).toEqual(["xai/grok-5"]));
		expect(catalogUrls.at(-1)?.searchParams.get("refresh")).toBe("true");
		expect(screen.queryByTestId("catalog-stale")).toBeNull();
	});
});

/** 等模型列表读完（至少一行或空态出现）。 */
function pickReady() {
	return vi.waitFor(() => {
		if (screen.queryByText("正在读取模型列表…"))
			throw new Error("模型列表未就绪");
	});
}
