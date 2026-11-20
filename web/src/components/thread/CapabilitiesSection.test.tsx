/**
 * 「能力」卡：默认收起，展开才发五条只读查询；MCP 缺授权出徽标；非 work 线程不渲染。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import type { ThreadViewState } from "../../state/thread-reducer";
import { uiStore } from "../../state/ui";
import { CapabilitiesRow } from "./CapabilitiesSection";

const { cmd } = vi.hoisted(() => ({ cmd: vi.fn() }));
vi.mock("../../realtime/socket", () => ({
	realtime: { cmd: (...args: unknown[]) => cmd(...args) },
}));

const T = "T-capabilities";

const DATA: Record<string, unknown> = {
	get_skills: {
		skills: [
			{
				name: "rust",
				description: "Rust 指南",
				source: "native:user",
				enabled: true,
			},
		],
	},
	get_mcp_servers: {
		servers: [
			{
				name: "github",
				transport: "http",
				status: "disconnected",
				toolCount: 0,
				enabled: true,
				authed: false,
				authState: "required",
			},
			{
				name: "fs",
				transport: "stdio",
				status: "connected",
				toolCount: 4,
				enabled: true,
				authed: false,
				authState: "none",
			},
		],
	},
	get_agent_definitions: { agents: [] },
	get_hooks: { hooks: [] },
	get_config_warnings: { warnings: ["bad config"] },
};

function setup(kind: "work" | "space" = "work") {
	getThreadStore(T)
		.getState()
		.patchLocal({
			agentStatus: "ready",
			thread: { id: T, kind },
		} as unknown as Partial<ThreadViewState>);
	return render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<CapabilitiesRow threadId={T} />
		</QueryClientProvider>,
	);
}

const sentTypes = () =>
	cmd.mock.calls.map((c) => (c[1] as { type: string }).type).sort();

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
	resetThreadStores();
	uiStore.setState({ collapsedCards: {} });
	cmd.mockReset();
	cmd.mockImplementation(
		async (_t: string, f: { type: string }) => DATA[f.type],
	);
});

describe("CapabilitiesRow", () => {
	it("收起时不发查询；展开后五条命令各 1 次", async () => {
		setup();
		expect(screen.getByRole("button", { name: "能力" })).toHaveAttribute(
			"aria-expanded",
			"false",
		);
		expect(cmd).not.toHaveBeenCalled();

		fireEvent.click(screen.getByRole("button", { name: "能力" }));
		await screen.findByText("rust");
		expect(sentTypes()).toEqual([
			"get_agent_definitions",
			"get_config_warnings",
			"get_hooks",
			"get_mcp_servers",
			"get_skills",
		]);
		expect(screen.getByText("bad config")).toBeInTheDocument();
		// 只读：除「刷新」外没有别的控件
		expect(
			screen
				.getAllByRole("button")
				.map((b) => b.getAttribute("aria-label") ?? b.textContent),
		).toEqual(["能力", "刷新"]);
		expect(screen.queryByRole("switch")).toBeNull();
		expect(screen.queryByRole("checkbox")).toBeNull();
	});

	it("MCP authState=required 出「需要授权」徽标；收起后摘要计数", async () => {
		setup();
		fireEvent.click(screen.getByRole("button", { name: "能力" }));
		const row = (await screen.findByText("github")).closest("li");
		expect(row).toHaveTextContent("需要授权");
		expect((await screen.findByText("fs")).closest("li")).not.toHaveTextContent(
			"需要授权",
		);

		fireEvent.click(screen.getByRole("button", { name: "能力" }));
		await waitFor(() =>
			expect(screen.getByText("技能 1 · MCP 1/2")).toBeInTheDocument(),
		);
	});

	it("kind 不是 work 时不渲染（即便已展开过）", () => {
		uiStore.setState({ collapsedCards: { "capabilities:expanded": true } });
		const { container } = setup("space");
		expect(container).toBeEmptyDOMElement();
		expect(cmd).not.toHaveBeenCalled();
	});
});
