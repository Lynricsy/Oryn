import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";

function renderAt(path: string) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter initialEntries={[path]}>
				<App />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

describe("路由与鉴权", () => {
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("未登录访问首页时跳转登录页", async () => {
		vi.stubGlobal(
			"fetch",
			vi.fn(async () =>
				Response.json(
					{ code: "unauthorized", message: "no session" },
					{ status: 401 },
				),
			),
		);
		renderAt("/");
		expect(
			await screen.findByRole("heading", { name: "登录" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "使用 Passkey 登录" }),
		).toBeInTheDocument();
	});

	it("配对页要求先登录", async () => {
		vi.stubGlobal(
			"fetch",
			vi.fn(async () =>
				Response.json(
					{ code: "unauthorized", message: "no session" },
					{ status: 401 },
				),
			),
		);
		renderAt("/pair");
		expect(await screen.findByText("请先登录再批准设备。")).toBeInTheDocument();
	});
});
