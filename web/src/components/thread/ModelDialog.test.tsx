/**
 * 模型对话框（M9.6 §7.2、M13）：模型列表读中央模型目录（`GET /api/v1/models/catalog`，只列 chat），
 * 列表里直接选 = 手动选择，发 oryn_select_model{provider, modelId}；旧 Core（Unknown command）回退 set_model。
 * 模型行带 `data-model`，角色行带 `data-role`；页脚「刷新」带 `refresh=true`，旧结果给淡色提示。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../../api/client";
import { resetLegacyCore } from "../../lib/model-select";
import { OmpCommandError } from "../../realtime/socket";
import { resetThreadStores } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { ThreadDialogs } from "./Dialogs";
import { openThreadDialog } from "./reports/test-helpers";

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

function catalogModel(
	provider: string,
	id: string,
	name: string,
	kind = "chat",
): Schemas["CatalogModel"] {
	return { provider, id, selector: `${provider}/${id}`, name, kind };
}

const MODELS = [
	catalogModel("openai", "gpt-6.1-sol", "GPT-6.1 Sol"),
	catalogModel("anthropic", "claude-opus-5-5", "Claude Opus 5.5"),
	// 非对话模型不进选择器
	catalogModel("openai", "text-embedding-3-small", "Embedding", "embedding"),
];

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
	legacy = false;
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

function openDialog() {
	uiStore.getState().open({ kind: "model" });
	render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<MemoryRouter>
				<ThreadDialogs threadId={T} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

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

describe("ModelDialog", () => {
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
		expect(rows()).toEqual(["openai/gpt-6.1-sol", "anthropic/claude-opus-5-5"]);
		expect(catalogUrls).toHaveLength(1);
		expect(catalogUrls[0]?.searchParams.has("refresh")).toBe(false);
		// 线程只收到角色相关的读取（get_settings / get_model_roles），没有任何模型列表命令
		expect(
			cmd.mock.calls.filter((c) => String(c[1].type).includes("models")),
		).toEqual([]);
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
		openThreadDialog(T, { kind: "model", query: "sol" });
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
		fireEvent.click(screen.getByRole("button", { name: "刷新" }));
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
