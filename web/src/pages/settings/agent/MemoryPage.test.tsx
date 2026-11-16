/**
 * 设置 → Agent → 记忆（M16 阶段 3）：走 mock 服务端（与 orynd 同格式）。覆盖状态的各种显示、.env 只显示一次、
 * 开关变化后出现待重启提示、换协议后的重启流程（R16）、嵌入锁定只读（R17）、候选改写后批准（R20）与破坏性操作两步确认（R19）。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../../i18n";
import { projects } from "../../../mock/fixtures";
import { MockServer } from "../../../mock/server";
import MemoryPage from "./MemoryPage";
import { BrowseCard } from "./memory/BrowseCard";
import { CandidatesCard } from "./memory/CandidatesCard";
import { ConnectionCard } from "./memory/ConnectionCard";
import { EndpointsCard } from "./memory/EndpointsCard";
import { ModelsCard } from "./memory/ModelsCard";
import { StatusCard } from "./memory/StatusCard";

const T = (k: string, o?: Record<string, unknown>): string =>
	String(i18n.t(k as "common.copy", { ...o }));

const MOCK_KEY = "oryn.mock.memory";

let server: MockServer;
let requests: { method: string; path: string; body: string }[];

beforeEach(() => {
	server = new MockServer();
	requests = [];
	vi.stubGlobal("fetch", async (input: Request) => {
		const u = new URL(input.url);
		requests.push({
			method: input.method,
			path: u.pathname,
			body: input.method === "GET" ? "" : await input.clone().text(),
		});
		return (
			(await server.handleFetch(input)) ?? new Response(null, { status: 404 })
		);
	});
	// 生成 .env 后的复制按钮
	Object.assign(navigator, {
		clipboard: { writeText: vi.fn(async () => {}) },
	});
});

afterEach(() => {
	vi.unstubAllGlobals();
	localStorage.removeItem(MOCK_KEY);
});

function mount(node: ReactNode) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter>{node}</MemoryRouter>
		</QueryClientProvider>,
	);
}

describe("StatusCard", () => {
	it("正常：徽标、版本、记忆数与计数，不出失败提示", async () => {
		mount(<StatusCard />);
		expect(
			await screen.findByText(T("memoryPage.state.ok.label")),
		).toBeInTheDocument();
		expect(screen.getByText("1.2.1")).toBeInTheDocument();
		expect(screen.getByText("49")).toBeInTheDocument();
		expect(screen.queryByText(T("memoryPage.state.ok.action"))).toBeNull();
		expect(
			screen.queryByText(T("memoryPage.status.versionMismatch")),
		).toBeNull();
		// 删除重试队列非空时列出失败的条目
		expect(
			await screen.findByText("019a2f90-1b3c-7e4d-8f50-6a7b8c9d0e1f"),
		).toBeInTheDocument();
	});

	it.each([
		"unreachable",
		"unauthorized",
		"not_ready",
		"suspect_empty",
	] as const)("%s：给出原因与下一步", async (state) => {
		localStorage.setItem(MOCK_KEY, state);
		const { container } = mount(<StatusCard />);
		expect(
			await screen.findByText(T(`memoryPage.state.${state}.title`)),
		).toBeInTheDocument();
		expect(
			screen.getByText(T(`memoryPage.state.${state}.action`)),
		).toBeInTheDocument();
		expect(container.querySelector(`[data-memory-state="${state}"]`)).not.toBe(
			null,
		);
		if (state === "suspect_empty")
			expect(
				screen.getByRole("link", { name: T("memoryPage.status.deployDoc") }),
			).toBeInTheDocument();
	});

	it("版本与已验证的小版本不同：显示警告", async () => {
		localStorage.setItem(MOCK_KEY, "version");
		mount(<StatusCard />);
		expect(
			await screen.findByText(T("memoryPage.status.versionMismatch")),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				T("memoryPage.status.versionMismatchHint", {
					version: "1.3.0",
					tested: "1.2",
				}),
			),
		).toBeInTheDocument();
	});
});

describe("ConnectionCard", () => {
	it(".env 只显示一次：关闭后再打开只能重新生成", async () => {
		mount(<ConnectionCard />);
		fireEvent.click(
			await screen.findByRole("button", {
				name: T("memoryPage.env.regenerate"),
			}),
		);
		const dialog = await screen.findByRole("dialog");
		expect(
			within(dialog).getByText(T("memoryPage.env.rotateTitle")),
		).toBeInTheDocument();
		fireEvent.click(
			within(dialog).getByRole("button", {
				name: T("memoryPage.env.regenerate"),
			}),
		);
		const content = await within(dialog).findByTestId("memory-env-content");
		expect(content.textContent).toMatch(/HINDSIGHT_API_TENANT_API_KEY=hs_/);
		expect(
			within(dialog).getByText(T("memoryPage.env.onceTitle")),
		).toBeInTheDocument();
		fireEvent.click(
			within(dialog).getByRole("button", { name: T("memoryPage.env.done") }),
		);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

		fireEvent.click(
			screen.getByRole("button", { name: T("memoryPage.env.regenerate") }),
		);
		const again = await screen.findByRole("dialog");
		expect(within(again).queryByTestId("memory-env-content")).toBeNull();
		expect(
			within(again).getByRole("button", {
				name: T("memoryPage.env.regenerate"),
			}),
		).toBeInTheDocument();
		expect(
			requests.filter((r) => r.path === "/api/v1/memory/env-file"),
		).toHaveLength(1);
	});

	it("远端 http 地址被拒：说明哪一步失败、为什么、怎么办", async () => {
		mount(<ConnectionCard />);
		const input = await screen.findByDisplayValue("http://127.0.0.1:8888");
		fireEvent.change(input, {
			target: { value: "http://hindsight.example.com" },
		});
		fireEvent.click(screen.getByRole("button", { name: T("common.save") }));
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent(
			T("memoryPage.failed", { step: T("memoryPage.steps.saveSettings") }),
		);
		expect(alert).toHaveTextContent(T("memoryPage.errors.insecure_url.why"));
		expect(alert).toHaveTextContent(T("memoryPage.errors.insecure_url.next"));
	});
});

describe("MemoryPage", () => {
	it("说明作用范围；项目开关变化后出现待重启提示", async () => {
		mount(<MemoryPage />);
		expect(screen.getByText(T("memoryPage.scope"))).toBeInTheDocument();
		const name = projects[0]?.name ?? "";
		const toggle = await screen.findByRole("checkbox", {
			name: T("memoryPage.projects.toggle", { name }),
		});
		expect(toggle).toBeChecked();
		expect(
			screen.queryByRole("button", { name: T("agentPages.apply.restart") }),
		).toBeNull();
		fireEvent.click(toggle);
		expect(
			await screen.findByRole("button", {
				name: T("agentPages.apply.restart"),
			}),
		).toBeInTheDocument();
		expect(screen.getAllByText("memory").length).toBeGreaterThan(0);
		await waitFor(() => expect(toggle).not.toBeChecked());
	});
});

/** 用途的选择器（accessible name 里混着选中项的文字，按 data-purpose 取更稳）。 */
async function purposeSelect(
	container: HTMLElement,
	purpose: string,
): Promise<HTMLSelectElement> {
	const selector = `select[data-purpose="${purpose}"]`;
	await waitFor(() => expect(container.querySelector(selector)).not.toBeNull());
	return container.querySelector(selector) as HTMLSelectElement;
}

describe("ModelsCard", () => {
	it("不可用的模型置灰并写明原因", async () => {
		const { container } = mount(<ModelsCard />);
		const select = await purposeSelect(container, "default");
		const codex = within(select).getByRole("option", {
			name: /GPT-5\.1 Codex/,
		});
		expect(codex).toBeDisabled();
		expect(codex.textContent).toMatch(/openai-codex-responses/);
		// 其余用途第一项是「跟随默认」
		const retain = await purposeSelect(container, "retain");
		expect(retain.querySelector("option")?.textContent).toBe(
			T("memoryPage.models.followNamed", { name: "Claude Haiku 4.5" }),
		);
	});

	it("换了协议：显示 .env 片段与「需要重启 Hindsight」，点「我已重启」后消失", async () => {
		const { container } = mount(<ModelsCard />);
		const retain = await purposeSelect(container, "retain");
		expect(screen.queryByText(T("memoryPage.models.needsRestart"))).toBeNull();
		fireEvent.change(retain, {
			target: {
				value: JSON.stringify(["openrouter", "deepseek/deepseek-v3.2"]),
			},
		});
		fireEvent.click(screen.getByRole("button", { name: T("common.save") }));
		expect(
			await screen.findByText(T("memoryPage.models.needsRestart")),
		).toBeInTheDocument();
		expect(screen.getByTestId("memory-env-snippet").textContent).toMatch(
			/HINDSIGHT_API_RETAIN_LLM_PROVIDER=openai/,
		);
		const put = requests.find(
			(r) => r.method === "PUT" && r.path === "/api/v1/memory/models",
		);
		expect(JSON.parse(put?.body ?? "{}").items).toContainEqual({
			purpose: "retain",
			providerKey: "openrouter",
			modelId: "deepseek/deepseek-v3.2",
		});
		fireEvent.click(
			screen.getByRole("button", { name: T("memoryPage.models.restarted") }),
		);
		await waitFor(() =>
			expect(
				screen.queryByText(T("memoryPage.models.needsRestart")),
			).toBeNull(),
		);
		expect(
			requests.some(
				(r) =>
					r.method === "POST" && r.path === "/api/v1/memory/models/deployed",
			),
		).toBe(true);
	});
});

describe("EndpointsCard", () => {
	it("嵌入锁定：模型与维度只读并给 reembed 文档；重排不受影响", async () => {
		const { container } = mount(<EndpointsCard />);
		expect(
			await screen.findByRole("link", {
				name: T("memoryPage.endpoints.reembedDoc"),
			}),
		).toBeInTheDocument();
		const embed = container.querySelector<HTMLElement>(
			'[data-endpoint="embed"]',
		);
		const rerank = container.querySelector<HTMLElement>(
			'[data-endpoint="rerank"]',
		);
		if (!embed || !rerank) throw new Error("endpoint forms missing");
		expect(
			within(embed).getByRole("textbox", {
				name: T("memoryPage.endpoints.model"),
			}),
		).toHaveAttribute("readonly");
		expect(
			within(embed).getByRole("textbox", {
				name: T("memoryPage.endpoints.dimensions"),
			}),
		).toHaveAttribute("readonly");
		expect(
			within(embed).getByRole("textbox", {
				name: T("memoryPage.endpoints.baseUrl"),
			}),
		).not.toHaveAttribute("readonly");
		expect(
			within(rerank).getByRole("textbox", {
				name: T("memoryPage.endpoints.model"),
			}),
		).not.toHaveAttribute("readonly");
	});

	async function embedForm(container: HTMLElement): Promise<HTMLElement> {
		await screen.findByRole("checkbox", {
			name: T("memoryPage.endpoints.reembed"),
		});
		const form = container.querySelector<HTMLElement>(
			'[data-endpoint="embed"]',
		);
		if (!form) throw new Error("embed form missing");
		return form;
	}

	it("勾选 reembed 才能改嵌入模型；Hindsight 仍在运行时提示先停止 API", async () => {
		const { container } = mount(<EndpointsCard />);
		const embed = await embedForm(container);
		const model = within(embed).getByRole("textbox", {
			name: T("memoryPage.endpoints.model"),
		});
		expect(model).toHaveAttribute("readonly");
		expect(
			screen.queryByText(T("memoryPage.endpoints.reembedHint")),
		).toBeNull();
		fireEvent.click(
			within(embed).getByRole("checkbox", {
				name: T("memoryPage.endpoints.reembed"),
			}),
		);
		expect(model).not.toHaveAttribute("readonly");
		expect(
			screen.getByText(T("memoryPage.endpoints.reembedHint")),
		).toBeInTheDocument();
		fireEvent.change(model, { target: { value: "Qwen/Qwen3-Embedding-8B" } });
		fireEvent.click(
			within(embed).getByRole("button", { name: T("common.save") }),
		);
		const alert = await within(embed).findByRole("alert");
		expect(alert).toHaveTextContent(
			T("memoryPage.errors.hindsight_running.why"),
		);
		expect(alert).toHaveTextContent(
			T("memoryPage.errors.hindsight_running.next"),
		);
		const put = requests.find(
			(r) => r.path === "/api/v1/memory/endpoints/embed",
		);
		expect(JSON.parse(put?.body ?? "{}")).toMatchObject({
			model: "Qwen/Qwen3-Embedding-8B",
			reembed: true,
		});
	});

	it("Hindsight 已停止时 reembed 保存成功", async () => {
		localStorage.setItem(MOCK_KEY, "unreachable");
		const { container } = mount(<EndpointsCard />);
		const embed = await embedForm(container);
		fireEvent.click(
			within(embed).getByRole("checkbox", {
				name: T("memoryPage.endpoints.reembed"),
			}),
		);
		fireEvent.change(
			within(embed).getByRole("textbox", {
				name: T("memoryPage.endpoints.model"),
			}),
			{ target: { value: "Qwen/Qwen3-Embedding-8B" } },
		);
		fireEvent.click(
			within(embed).getByRole("button", { name: T("common.save") }),
		);
		await waitFor(() =>
			expect(
				within(embed).getByRole("textbox", {
					name: T("memoryPage.endpoints.model"),
				}),
			).toHaveAttribute("readonly"),
		);
		expect(
			within(embed).getByDisplayValue("Qwen/Qwen3-Embedding-8B"),
		).toBeInTheDocument();
		expect(within(embed).queryByRole("alert")).toBeNull();
	});

	it("删除重排接口：两步确认后改用 rrf，给出 .env 片段与「需要重启 Hindsight」，点「我已重启」后消失", async () => {
		mount(<EndpointsCard />);
		const del = await screen.findByRole("button", {
			name: T("memoryPage.endpoints.rerank.delete"),
		});
		expect(screen.queryByText(T("memoryPage.models.needsRestart"))).toBeNull();
		fireEvent.click(del);
		expect(requests.some((r) => r.method === "DELETE")).toBe(false);
		fireEvent.click(
			screen.getByRole("button", {
				name: T("memoryPage.endpoints.rerank.deleteConfirm"),
			}),
		);
		expect(
			await screen.findByText(T("memoryPage.models.needsRestart")),
		).toBeInTheDocument();
		expect(requests).toContainEqual(
			expect.objectContaining({
				method: "DELETE",
				path: "/api/v1/memory/endpoints/rerank",
			}),
		);
		expect(screen.getByTestId("memory-rerank-env-snippet").textContent).toBe(
			"HINDSIGHT_API_RERANKER_PROVIDER=rrf\n",
		);
		expect(
			screen.getByText(T("memoryPage.endpoints.rerank.restartHint.rrf")),
		).toBeInTheDocument();
		expect(
			screen.getByText(T("memoryPage.endpoints.rerank.unsetHint")),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", {
				name: T("memoryPage.endpoints.rerank.delete"),
			}),
		).toBeNull();
		fireEvent.click(
			screen.getByRole("button", { name: T("memoryPage.models.restarted") }),
		);
		await waitFor(() =>
			expect(
				screen.queryByText(T("memoryPage.models.needsRestart")),
			).toBeNull(),
		);
		expect(
			requests.some(
				(r) =>
					r.method === "POST" && r.path === "/api/v1/memory/models/deployed",
			),
		).toBe(true);

		// 重新配置重排接口：改回 cohere，片段带三行 cohere 配置，密钥保持原值
		const rerank = document.querySelector<HTMLElement>(
			'[data-endpoint="rerank"]',
		);
		if (!rerank) throw new Error("rerank form missing");
		fireEvent.change(
			within(rerank).getByRole("textbox", {
				name: T("memoryPage.endpoints.baseUrl"),
			}),
			{ target: { value: "https://api.siliconflow.cn/v1/rerank" } },
		);
		fireEvent.change(
			within(rerank).getByRole("textbox", {
				name: T("memoryPage.endpoints.model"),
			}),
			{ target: { value: "BAAI/bge-reranker-v2-m3" } },
		);
		fireEvent.click(
			within(rerank).getByRole("button", { name: T("common.save") }),
		);
		expect(
			await screen.findByText(
				T("memoryPage.endpoints.rerank.restartHint.cohere"),
			),
		).toBeInTheDocument();
		const snippet =
			screen.getByTestId("memory-rerank-env-snippet").textContent ?? "";
		expect(snippet).toMatch(/HINDSIGHT_API_RERANKER_PROVIDER=cohere/);
		expect(snippet).toMatch(/HINDSIGHT_API_RERANKER_COHERE_API_KEY=<保持原值>/);
	});

	it("协议与重排同时待部署：接口卡的片段也带上 LLM 行（「我已重启」会一起确认）", async () => {
		const { container } = mount(
			<>
				<ModelsCard />
				<EndpointsCard />
			</>,
		);
		const retain = await purposeSelect(container, "retain");
		fireEvent.change(retain, {
			target: {
				value: JSON.stringify(["openrouter", "deepseek/deepseek-v3.2"]),
			},
		});
		expect(screen.queryByTestId("memory-env-snippet")).toBeNull();
		const [saveModels] = screen.getAllByRole("button", {
			name: T("common.save"),
		});
		if (!saveModels) throw new Error("models save button missing");
		fireEvent.click(saveModels);
		expect(
			(await screen.findByTestId("memory-env-snippet")).textContent,
		).toMatch(/HINDSIGHT_API_RETAIN_LLM_PROVIDER=openai/);
		fireEvent.click(
			await screen.findByRole("button", {
				name: T("memoryPage.endpoints.rerank.delete"),
			}),
		);
		fireEvent.click(
			screen.getByRole("button", {
				name: T("memoryPage.endpoints.rerank.deleteConfirm"),
			}),
		);
		const rerankSnippet = await screen.findByTestId(
			"memory-rerank-env-snippet",
		);
		expect(rerankSnippet.textContent).toMatch(
			/HINDSIGHT_API_RETAIN_LLM_PROVIDER=openai/,
		);
		expect(rerankSnippet.textContent).toMatch(
			/HINDSIGHT_API_RERANKER_PROVIDER=rrf/,
		);
		// 删除重排后模型卡重新取：它的片段同样带上重排行
		await waitFor(() =>
			expect(screen.getByTestId("memory-env-snippet").textContent).toMatch(
				/HINDSIGHT_API_RERANKER_PROVIDER=rrf/,
			),
		);
	});
});

describe("CandidatesCard", () => {
	it("改写后批准：按改写的内容提交，候选从列表消失", async () => {
		mount(<CandidatesCard />);
		const first = "回复时先给结论，再列理由；不要用表情符号。";
		expect(await screen.findByText(first)).toBeInTheDocument();
		const row = screen.getByText(first).closest("li");
		if (!row) throw new Error("candidate row missing");
		fireEvent.click(
			within(row).getByRole("button", {
				name: T("memoryPage.candidates.edit"),
			}),
		);
		const box = within(row).getByRole("textbox", {
			name: T("memoryPage.candidates.editLabel"),
		});
		fireEvent.change(box, { target: { value: "回复先给结论再列理由。" } });
		fireEvent.click(
			within(row).getByRole("button", {
				name: T("memoryPage.candidates.approve"),
			}),
		);
		await waitFor(() => expect(screen.queryByText(first)).toBeNull());
		const approve = requests.find((r) => r.path.endsWith("/approve"));
		expect(JSON.parse(approve?.body ?? "{}")).toEqual({
			content: "回复先给结论再列理由。",
		});
	});

	it("拒绝要两步确认", async () => {
		mount(<CandidatesCard />);
		const first = "回复时先给结论，再列理由；不要用表情符号。";
		const row = (await screen.findByText(first)).closest("li");
		if (!row) throw new Error("candidate row missing");
		fireEvent.click(
			within(row).getByRole("button", {
				name: T("memoryPage.candidates.reject"),
			}),
		);
		expect(requests.some((r) => r.method === "DELETE")).toBe(false);
		fireEvent.click(
			within(row).getByRole("button", {
				name: T("memoryPage.candidates.rejectConfirm"),
			}),
		);
		await waitFor(() => expect(screen.queryByText(first)).toBeNull());
		expect(
			requests.filter((r) => r.method === "DELETE").map((r) => r.path),
		).toEqual(["/api/v1/memory/candidates/mcd_01JD5A0B1C2D3E4F5G6H7J8K9N"]);
	});
});

describe("BrowseCard", () => {
	it("删除 document 与按线程遗忘都要两步确认", async () => {
		const { container } = mount(<BrowseCard />);
		await waitFor(() =>
			expect(
				container.querySelector('[data-document="omp-retain-7f3c9a1e2b"]'),
			).not.toBeNull(),
		);
		const row = container.querySelector<HTMLElement>(
			'[data-document="omp-retain-7f3c9a1e2b"]',
		);
		if (!row) throw new Error("document row missing");
		fireEvent.click(
			within(row).getByRole("button", { name: T("common.delete") }),
		);
		expect(requests.some((r) => r.method === "DELETE")).toBe(false);
		fireEvent.click(
			within(row).getByRole("button", {
				name: T("memoryPage.browse.deleteConfirm"),
			}),
		);
		await waitFor(() =>
			expect(
				container.querySelector('[data-document="omp-retain-7f3c9a1e2b"]'),
			).toBeNull(),
		);
		expect(
			requests.filter((r) => r.method === "DELETE").map((r) => r.path),
		).toEqual(["/api/v1/memory/documents/omp-retain-7f3c9a1e2b"]);

		const other = container.querySelector<HTMLElement>(
			'[data-document="019a3e11-02bd-7a55-8c3e-0f9d7e6b5a41"]',
		);
		if (!other) throw new Error("document row missing");
		fireEvent.click(
			within(other).getByRole("button", {
				name: T("memoryPage.browse.forget"),
			}),
		);
		expect(requests.some((r) => r.path.endsWith("/forget"))).toBe(false);
		fireEvent.click(
			within(other).getByRole("button", {
				name: T("memoryPage.browse.forgetConfirm"),
			}),
		);
		expect(
			await screen.findByText(T("memoryPage.browse.forgotten", { n: 2 })),
		).toBeInTheDocument();
	});

	it("搜索走召回接口并标出全局记忆", async () => {
		mount(<BrowseCard />);
		const box = await screen.findByRole("textbox", {
			name: T("memoryPage.browse.search"),
		});
		fireEvent.change(box, { target: { value: "简体中文" } });
		fireEvent.click(
			screen.getByRole("button", { name: T("memoryPage.browse.search") }),
		);
		expect(
			await screen.findByText("代码注释、文档和提交信息一律用简体中文。"),
		).toBeInTheDocument();
		expect(screen.getByText(T("memoryPage.browse.global"))).toBeInTheDocument();
		const search = requests.find((r) => r.path === "/api/v1/memory/search");
		expect(search).toBeDefined();
	});
});
