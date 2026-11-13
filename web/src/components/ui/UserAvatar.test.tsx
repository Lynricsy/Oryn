import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, unwrap } from "../../api/client";
import { qk } from "../../api/queries";
import { MockServer } from "../../mock/server";
import { UserAvatar } from "./UserAvatar";

// 经 mock 后端走一遍「保存 → 重新查询 → 渲染」：契约与 orynd 一致（与 agent 头像同一套规则）
let server: MockServer;
/** 经过 fetch 的 GET 路径，用来数请求次数。 */
let gets: string[] = [];

beforeEach(() => {
	server = new MockServer();
	gets = [];
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			if (req.method === "GET") gets.push(new URL(req.url).pathname);
			return (
				(await server.handleFetch(req)) ?? new Response(null, { status: 404 })
			);
		},
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

function renderAvatar(
	client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	}),
) {
	return render(
		<QueryClientProvider client={client}>
			<div data-testid="host">
				<UserAvatar size={20} />
			</div>
		</QueryClientProvider>,
	);
}

const GIT = "/api/v1/settings/git-identity";

const host = () => screen.getByTestId("host").firstElementChild;

describe("UserAvatar", () => {
	it("设置了头像时不请求 git 身份（名字只给默认头像用）", async () => {
		server.userIdentity = { avatarIcon: "🐱" };
		renderAvatar();
		await waitFor(() => expect(host()?.textContent).toBe("🐱"));
		// 查询在挂载时就发出：头像渲染出来时若要请求 git 身份早已记录在案
		expect(gets).not.toContain(GIT);
	});

	it("长转录里头像反复挂载不重复请求 git 身份；保存后的缓存立即反映到名字首字", async () => {
		server.userIdentity = {};
		const client = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		for (let i = 0; i < 3; i++) {
			const view = renderAvatar(client);
			await waitFor(() => expect(host()?.textContent).toBe("O"));
			view.unmount();
		}
		expect(gets.filter((p) => p === GIT)).toHaveLength(1);
		// 设置页保存 git 身份后写回同一个缓存键：已挂载的头像跟着换字，不必重新请求
		renderAvatar(client);
		await waitFor(() => expect(host()?.textContent).toBe("O"));
		client.setQueryData(qk.gitIdentity, {
			identity: { name: "Zed", email: "z@example.com" },
		});
		await waitFor(() => expect(host()?.textContent).toBe("Z"));
		expect(gets.filter((p) => p === GIT)).toHaveLength(1);
	});

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
