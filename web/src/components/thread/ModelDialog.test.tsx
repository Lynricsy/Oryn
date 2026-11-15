/**
 * 模型对话框（M9.6 §7.2）：模型列表里直接选 = 手动选择，发 oryn_select_model{provider, modelId}；
 * 旧 Core（Unknown command）回退 set_model。模型行带 `data-model`，角色行带 `data-role`。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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

const sent = () =>
	cmd.mock.calls
		.map((c) => c[1] as Record<string, unknown>)
		.filter((f) => !String(f.type).startsWith("get_"));

beforeEach(() => {
	cmd.mockReset();
	resetThreadStores();
	resetLegacyCore();
	legacy = false;
	cmd.mockImplementation(async (_t: string, f: Record<string, unknown>) => {
		if (f.type === "get_available_models")
			return {
				models: [
					{ provider: "openai", id: "gpt-6.1-sol", name: "GPT-6.1 Sol" },
					{
						provider: "anthropic",
						id: "claude-opus-5-5",
						name: "Claude Opus 5.5",
					},
				],
			};
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
		cmd.mockImplementation(async (_t: string, f: Record<string, unknown>) =>
			f.type === "get_available_models" ? new Promise(() => undefined) : {},
		);
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

	it("没有任何模型：说明需要先登录供应商", async () => {
		cmd.mockImplementation(async (_t: string, f: Record<string, unknown>) =>
			f.type === "get_available_models" ? { models: [] } : {},
		);
		openDialog();
		expect(await screen.findByText(/没有可用的模型/)).toBeTruthy();
	});

	it("带 query 打开（/model <选择器> 解析不出时）：预填搜索框并按它筛选", async () => {
		openThreadDialog(T, { kind: "model", query: "sol" });
		await pickReady();
		expect(
			(screen.getByPlaceholderText("搜索模型…") as HTMLInputElement).value,
		).toBe("sol");
		expect(
			[...document.querySelectorAll("[data-model]")].map((e) =>
				e.getAttribute("data-model"),
			),
		).toEqual(["openai/gpt-6.1-sol"]);
	});
});

/** 等模型列表读完（至少一行或空态出现）。 */
function pickReady() {
	return vi.waitFor(() => {
		if (screen.queryByText("正在读取模型列表…"))
			throw new Error("模型列表未就绪");
	});
}
