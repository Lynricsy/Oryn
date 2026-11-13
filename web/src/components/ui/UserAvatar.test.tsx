import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, unwrap } from "../../api/client";
import { MockServer } from "../../mock/server";
import { UserAvatar } from "./UserAvatar";

// 经 mock 后端走一遍「保存 → 重新查询 → 渲染」：契约与 orynd 一致（与 agent 头像同一套规则）
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
				<UserAvatar size={20} />
			</div>
		</QueryClientProvider>,
	);
}

const host = () => screen.getByTestId("host").firstElementChild;

describe("UserAvatar", () => {
	it("未设置时用 git 身份名字的首字，没有名字时用 User 图标", async () => {
		server.userIdentity = {};
		const first = renderAvatar();
		await waitFor(() => expect(host()?.textContent).toBe("O"));
		expect(host()?.getAttribute("data-avatar")).toBe("default");
		expect(host()?.getAttribute("aria-hidden")).toBe("true");
		first.unmount();

		server.gitIdentity = undefined;
		renderAvatar();
		await waitFor(() => expect(host()?.querySelector("svg")).not.toBeNull());
		expect(host()?.getAttribute("data-avatar")).toBe("default");
		expect(host()?.textContent).toBe("");
	});

	it("PUT 保存的字形，重新查询后渲染出来", async () => {
		const saved = await unwrap(
			api.PUT("/api/v1/settings/user-identity", {
				body: { avatarIcon: " 🐱 " },
			}),
		);
		expect(saved).toEqual({ avatarIcon: "🐱" });
		renderAvatar();
		await waitFor(() =>
			expect(host()?.getAttribute("data-avatar")).toBe("icon"),
		);
		expect(host()?.getAttribute("data-avatar-of")).toBe("user");
		expect(host()?.textContent).toBe("🐱");
	});

	it("PUT 保存的图片以 img 显示，且不影响 agent 头像", async () => {
		const png =
			"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";
		await unwrap(
			api.PUT("/api/v1/settings/user-identity", {
				body: { avatarImage: png },
			}),
		);
		renderAvatar();
		await waitFor(() =>
			expect(host()?.getAttribute("data-avatar")).toBe("image"),
		);
		expect(host()?.getAttribute("src")).toBe(png);
		expect(await unwrap(api.GET("/api/v1/settings/agent-identity"))).toEqual(
			{},
		);
	});
});
