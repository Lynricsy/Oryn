/**
 * 设置 → 全局助手（M19 步骤 1.6）：联网开关保存、MCP 逐个关闭、不可用原因显示。走 mock 后端。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MockServer } from "../../mock/server";
import AssistantSettings from "./AssistantSettings";

let server: MockServer;
let puts: unknown[];

beforeEach(() => {
	server = new MockServer();
	puts = [];
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			const u = new URL(req.url);
			if (req.method === "PUT" && u.pathname === "/api/v1/settings/assistant")
				puts.push(await req.clone().json());
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

const row = (name: string) =>
	document.querySelector(`[data-mcp-server="${name}"]`) as HTMLElement;

describe("设置 → 全局助手", () => {
	it("联网开关保存整份设置", async () => {
		mount();
		const web = await screen.findByRole("checkbox", { name: "允许助手联网" });
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
		const github = await screen.findByRole("checkbox", {
			name: "助手使用 github",
		});
		const linear = screen.getByRole("checkbox", { name: "助手使用 linear" });
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
		await screen.findByRole("checkbox", { name: "助手使用 github" });
		expect(row("filesystem")).toHaveTextContent(
			"本地进程型服务器，助手的沙箱里无法运行",
		);
		expect(row("notion")).toHaveTextContent("需要 OAuth 登录，暂不支持");
		expect(row("notion")).toHaveTextContent("不可用");
		expect(
			screen.queryByRole("checkbox", { name: "助手使用 notion" }),
		).toBeNull();
		expect(
			screen.queryByRole("checkbox", { name: "助手使用 filesystem" }),
		).toBeNull();
	});
	it("助手的电脑：provider 与类型进设置，换 provider 时类型回到默认", async () => {
		mount();
		const providerSel = (await screen.findByTestId(
			"computer-provider",
		)) as HTMLSelectElement;
		await screen.findByRole("option", { name: "local" });
		fireEvent.change(providerSel, { target: { value: "local" } });
		await waitFor(() =>
			expect(server.assistant.settings.computer).toEqual({ provider: "local" }),
		);
		const typeSel = screen.getByTestId(
			"computer-orb-type",
		) as HTMLSelectElement;
		await waitFor(() => expect(typeSel).toBeEnabled());
		fireEvent.change(typeSel, { target: { value: "large" } });
		await waitFor(() =>
			expect(server.assistant.settings.computer).toEqual({
				provider: "local",
				orbType: "large",
			}),
		);
		await waitFor(() => expect(providerSel).toBeEnabled());
		fireEvent.change(providerSel, { target: { value: "" } });
		await waitFor(() => expect(server.assistant.settings.computer).toEqual({}));
	});
});
