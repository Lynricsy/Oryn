import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, unwrap } from "../../api/client";
import { MockServer } from "../../mock/server";
import { AgentAvatar } from "./AgentAvatar";

// 经 mock 后端走一遍「保存 → 查询 → 渲染」：头像契约与 orynd 一致（avatarIcon 为单个字形）
let server: MockServer;

beforeEach(() => {
	server = new MockServer();
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			return (
				(await server.handleFetch(req)) ?? new Response(null, { status: 404 })
			);
		},
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

function renderAvatar() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<div data-testid="host">
				<AgentAvatar size={20} />
			</div>
		</QueryClientProvider>,
	);
}

const host = () => screen.getByTestId("host").firstElementChild;

describe("AgentAvatar", () => {
	it("未设置时显示品牌标", async () => {
		renderAvatar();
		await waitFor(() =>
			expect(host()?.getAttribute("data-avatar")).toBe("default"),
		);
	});

	it("保存的字形图标原样显示", async () => {
		await unwrap(
			api.PUT("/api/v1/settings/agent-identity", {
				body: { avatarIcon: "🦊" },
			}),
		);
		renderAvatar();
		await waitFor(() =>
			expect(host()?.getAttribute("data-avatar")).toBe("icon"),
		);
		expect(host()?.textContent).toBe("🦊");
	});

	it("保存的图片以 img 显示", async () => {
		const png =
			"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";
		await unwrap(
			api.PUT("/api/v1/settings/agent-identity", {
				body: { avatarImage: png },
			}),
		);
		renderAvatar();
		await waitFor(() =>
			expect(host()?.getAttribute("data-avatar")).toBe("image"),
		);
		expect(host()?.getAttribute("src")).toBe(png);
	});
});
