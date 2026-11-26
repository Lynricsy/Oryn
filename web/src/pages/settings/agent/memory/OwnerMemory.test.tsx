/**
 * 设置 → 全局助手 › 助手记忆（M17 步骤 5.1）：走 mock 服务端（与 orynd 同格式）。覆盖 Agent 记忆不可用时只显示提示、
 * 开关即改即存、名字与别名保存、判断模型 key 只写（保存后不回显、留空保留、可清除）、world 事实失效（observation 不能失效）
 * 与之后主题进入等待刷新、按来源遗忘（确认对话框）、待确认的明确记忆改写后批准。
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
import i18n from "../../../../i18n";
import { MAIN_THREAD } from "../../../../mock/fixtures";
import { MockServer } from "../../../../mock/server";
import { OwnerExplicitCard } from "./OwnerExplicitCard";
import { OwnerFactsCard } from "./OwnerFactsCard";
import { OwnerMemorySection } from "./OwnerMemorySection";
import { OwnerSourcesCard } from "./OwnerSourcesCard";

const T = (k: string, o?: Record<string, unknown>): string =>
	String(i18n.t(k as "common.copy", { ...o }));

const MOCK_KEY = "oryn.mock.owner-memory";

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

/** 最后一次发往 `path` 的 `method` 请求的 JSON 请求体。 */
function lastBody(method: string, path: string): unknown {
	const r = requests
		.filter((x) => x.method === method && x.path === path)
		.pop();
	if (!r) throw new Error(`no ${method} ${path}`);
	return JSON.parse(r.body);
}

/** 直接读 mock 服务端的当前数据（不经过页面缓存）。 */
async function serverGet<R>(path: string): Promise<R> {
	const res = await server.handleFetch(new Request(`http://x${path}`));
	return (await res?.json()) as R;
}

async function ownerSection(): Promise<HTMLElement> {
	mount(<OwnerMemorySection />);
	const section = await screen.findByTestId("owner-memory-section");
	await within(section).findByRole("switch", {
		name: T("memoryPage.owner.settings.enabled"),
	});
	return section;
}

describe("助手记忆分区", () => {
	it("工程记忆未启用或未配置：分区只显示提示", async () => {
		localStorage.setItem(MOCK_KEY, "unavailable");
		mount(<OwnerMemorySection />);
		const section = await screen.findByTestId("owner-memory-section");
		expect(
			await within(section).findByText(T("memoryPage.owner.unavailable")),
		).toBeInTheDocument();
		expect(
			within(section).queryByRole("switch", {
				name: T("memoryPage.owner.settings.enabled"),
			}),
		).toBeNull();
		expect(
			within(section).queryByText(T("memoryPage.owner.topics.title")),
		).toBeNull();
	});

	it("开关即改即存：带上已保存的名字", async () => {
		const section = await ownerSection();
		const toggle = within(section).getByRole("switch", {
			name: T("memoryPage.owner.settings.enabled"),
		});
		expect(toggle).toBeChecked();
		fireEvent.click(toggle);
		await waitFor(() => expect(toggle).not.toBeChecked());
		expect(lastBody("PUT", "/api/v1/owner-memory/settings")).toEqual({
			enabled: false,
			ownerNames: ["Lynricsy", "林"],
		});
		// 关闭后事实不再读取
		expect(
			await within(section).findByText(T("memoryPage.owner.facts.disabled")),
		).toBeInTheDocument();
	});

	it("名字与别名：增删后点保存才提交，重复与超长的不能添加", async () => {
		const section = await ownerSection();
		const save = within(section).getByRole("button", {
			name: T("memoryPage.owner.settings.saveNames"),
		});
		expect(save).toBeDisabled();
		const input = within(section).getByPlaceholderText(
			T("memoryPage.owner.settings.namePlaceholder"),
		);
		fireEvent.change(input, { target: { value: "林" } });
		expect(
			within(section).getByText(
				T("memoryPage.owner.settings.nameProblem.duplicate"),
			),
		).toBeInTheDocument();
		fireEvent.change(input, { target: { value: "x".repeat(65) } });
		expect(
			within(section).getByText(
				T("memoryPage.owner.settings.nameProblem.tooLong", { chars: 64 }),
			),
		).toBeInTheDocument();
		fireEvent.change(input, { target: { value: " 小林 " } });
		fireEvent.keyDown(input, { key: "Enter" });
		expect(within(section).getByText("小林")).toBeInTheDocument();
		fireEvent.click(
			within(section).getByRole("button", {
				name: T("memoryPage.owner.settings.removeName", { name: "林" }),
			}),
		);
		expect(save).toBeEnabled();
		fireEvent.click(save);
		await waitFor(() => expect(save).toBeDisabled());
		expect(lastBody("PUT", "/api/v1/owner-memory/settings")).toEqual({
			enabled: true,
			ownerNames: ["Lynricsy", "小林"],
		});
		const saved = await serverGet<{ ownerNames: string[] }>(
			"/api/v1/owner-memory/settings",
		);
		expect(saved.ownerNames).toEqual(["Lynricsy", "小林"]);
	});

	it("判断模型 key 只写：留空保存保留原 key，新 key 保存后不回显，可以清除", async () => {
		const section = await ownerSection();
		const form = within(section).getByTestId("owner-decide-form");
		const [, model, key] = Array.from(form.querySelectorAll("input"));
		if (!model || !key) throw new Error("decide inputs missing");
		expect(key).toHaveAttribute("type", "password");
		expect(key).toHaveValue("");
		expect(
			within(form).getByText(T("memoryPage.owner.decide.keySet")),
		).toBeInTheDocument();
		const save = within(form).getByRole("button", { name: T("common.save") });

		// 只改模型、key 留空：请求里没有 apiKey，key 仍是已设置
		fireEvent.change(model, { target: { value: "gemini-decision-2" } });
		fireEvent.click(save);
		await waitFor(() =>
			expect(
				requests.filter(
					(r) =>
						r.method === "PUT" &&
						r.path === "/api/v1/owner-memory/decide-endpoint",
				),
			).toHaveLength(1),
		);
		expect(lastBody("PUT", "/api/v1/owner-memory/decide-endpoint")).toEqual({
			baseUrl: "https://generativelanguage.googleapis.com",
			model: "gemini-decision-2",
		});
		await waitFor(() => expect(save).toBeDisabled());
		expect(
			within(form).getByText(T("memoryPage.owner.decide.keySet")),
		).toBeInTheDocument();

		// 填新 key：提交后输入框清空，设置里只有 hasKey
		fireEvent.change(key, { target: { value: "AIza-new-secret" } });
		fireEvent.click(save);
		await waitFor(() => expect(key).toHaveValue(""));
		expect(lastBody("PUT", "/api/v1/owner-memory/decide-endpoint")).toEqual({
			baseUrl: "https://generativelanguage.googleapis.com",
			model: "gemini-decision-2",
			apiKey: "AIza-new-secret",
		});
		const settings = await serverGet<Record<string, unknown>>(
			"/api/v1/owner-memory/settings",
		);
		expect(JSON.stringify(settings)).not.toContain("AIza-new-secret");
		expect(settings.decide).toEqual({
			baseUrl: "https://generativelanguage.googleapis.com",
			model: "gemini-decision-2",
			hasKey: true,
		});
		expect(screen.queryByDisplayValue("AIza-new-secret")).toBeNull();

		// 清除 key：两步确认，发空串
		fireEvent.click(
			within(form).getByRole("button", {
				name: T("memoryPage.owner.decide.clearKey"),
			}),
		);
		fireEvent.click(
			within(form).getByRole("button", {
				name: T("memoryPage.owner.decide.clearKeyConfirm"),
			}),
		);
		expect(
			await within(form).findByText(T("memoryPage.owner.decide.keyUnset")),
		).toBeInTheDocument();
		expect(lastBody("PUT", "/api/v1/owner-memory/decide-endpoint")).toEqual({
			baseUrl: "https://generativelanguage.googleapis.com",
			model: "gemini-decision-2",
			apiKey: "",
		});
	});
});

describe("OwnerFactsCard", () => {
	it("world 事实设为失效（带原因）后可恢复；observation 没有该操作；主题进入等待刷新", async () => {
		const { container } = mount(<OwnerFactsCard enabled />);
		await screen.findByText("主人周末会集中处理 Oryn 的大改动。");
		const observation = container.querySelector<HTMLElement>(
			'[data-fact="omf_05"]',
		);
		if (!observation) throw new Error("observation row missing");
		expect(
			within(observation).queryByRole("button", {
				name: T("memoryPage.owner.facts.invalidate"),
			}),
		).toBeNull();

		const row = container.querySelector<HTMLElement>('[data-fact="omf_02"]');
		if (!row) throw new Error("fact row missing");
		// 出处：含助手消息、可跳到来源线程、消息范围
		expect(
			within(row).getByText(T("memoryPage.owner.facts.provenance.mixed.label")),
		).toBeInTheDocument();
		expect(
			within(row).getByText(
				T("memoryPage.owner.facts.seqRange", { from: 12, to: 30 }),
			),
		).toBeInTheDocument();
		expect(row.querySelector(`a[href="/t/${MAIN_THREAD}"]`)).not.toBeNull();

		fireEvent.click(
			within(row).getByRole("button", {
				name: T("memoryPage.owner.facts.invalidate"),
			}),
		);
		fireEvent.change(
			within(row).getByRole("textbox", {
				name: T("memoryPage.owner.facts.reason"),
			}),
			{ target: { value: "已经不这么安排了" } },
		);
		fireEvent.click(
			within(row).getByRole("button", {
				name: T("memoryPage.owner.facts.invalidateConfirm"),
			}),
		);
		expect(
			await within(row).findByText(T("memoryPage.owner.facts.invalidated")),
		).toBeInTheDocument();
		expect(lastBody("PUT", "/api/v1/owner-memory/facts/omf_02/state")).toEqual({
			state: "invalidated",
			reason: "已经不这么安排了",
		});
		const topics = await serverGet<{
			items: { status: string; stale: boolean }[];
		}>("/api/v1/owner-memory/topics");
		expect(
			topics.items.filter((t) => t.status === "active").every((t) => t.stale),
		).toBe(true);

		fireEvent.click(
			within(row).getByRole("button", {
				name: T("memoryPage.owner.facts.restore"),
			}),
		);
		await waitFor(() =>
			expect(
				within(row).queryByText(T("memoryPage.owner.facts.invalidated")),
			).toBeNull(),
		);
		expect(lastBody("PUT", "/api/v1/owner-memory/facts/omf_02/state")).toEqual({
			state: "valid",
		});
	});
});

describe("OwnerSourcesCard", () => {
	it("按来源遗忘：确认对话框，取消不发请求，确认后显示删除数", async () => {
		const { container } = mount(<OwnerSourcesCard />);
		await waitFor(() =>
			expect(
				container.querySelector(`[data-source="${MAIN_THREAD}"]`),
			).not.toBeNull(),
		);
		const row = container.querySelector<HTMLElement>(
			`[data-source="${MAIN_THREAD}"]`,
		);
		if (!row) throw new Error("source row missing");
		const forget = within(row).getByRole("button", {
			name: T("memoryPage.owner.sources.forget"),
		});
		fireEvent.click(forget);
		let dialog = await screen.findByRole("dialog");
		fireEvent.click(
			within(dialog).getByRole("button", { name: T("common.cancel") }),
		);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(requests.some((r) => r.method === "DELETE")).toBe(false);

		fireEvent.click(forget);
		dialog = await screen.findByRole("dialog");
		fireEvent.click(
			within(dialog).getByRole("button", {
				name: T("memoryPage.owner.sources.forgetConfirm"),
			}),
		);
		expect(
			await screen.findByText(
				T("memoryPage.owner.sources.forgotten", { n: 5 }),
			),
		).toBeInTheDocument();
		expect(
			requests.filter((r) => r.method === "DELETE").map((r) => r.path),
		).toEqual([`/api/v1/owner-memory/sources/${MAIN_THREAD}`]);
		// 线程还在：来源保留、块数清零
		await waitFor(() =>
			expect(
				within(row).getByText(T("memoryPage.owner.sources.chunks", { n: 0 })),
			).toBeInTheDocument(),
		);
		// 已删除的线程显示「线程已删除」
		expect(
			screen.getByText(T("memoryPage.owner.sources.missing")),
		).toBeInTheDocument();
	});
});

describe("OwnerExplicitCard", () => {
	it("待确认的明确记忆改写后批准：按改写的内容提交，变为已生效", async () => {
		const { container } = mount(<OwnerExplicitCard />);
		const first = "主人的生日是 3 月 14 日。";
		await screen.findByText(first);
		const row = container.querySelector<HTMLElement>(
			'[data-explicit="ome_01"]',
		);
		if (!row) throw new Error("explicit row missing");
		expect(row).toHaveAttribute("data-status", "pending");
		expect(
			within(row).getByText(
				T("memoryPage.owner.explicit.quote", {
					text: "记一下，我生日是 3 月 14 号",
				}),
			),
		).toBeInTheDocument();
		fireEvent.click(
			within(row).getByRole("button", { name: T("common.edit") }),
		);
		fireEvent.change(
			within(row).getByRole("textbox", {
				name: T("memoryPage.owner.explicit.editLabel"),
			}),
			{ target: { value: "主人生日：3 月 14 日。" } },
		);
		fireEvent.click(
			within(row).getByRole("button", {
				name: T("memoryPage.owner.explicit.approve"),
			}),
		);
		await waitFor(() =>
			expect(
				container.querySelector('[data-explicit="ome_01"]'),
			).toHaveAttribute("data-status", "active"),
		);
		expect(
			lastBody("POST", "/api/v1/owner-memory/explicit/ome_01/approve"),
		).toEqual({ content: "主人生日：3 月 14 日。" });
		expect(screen.getByText("主人生日：3 月 14 日。")).toBeInTheDocument();
		expect(
			screen.queryByText(T("memoryPage.owner.explicit.pending")),
		).toBeNull();
	});
});
