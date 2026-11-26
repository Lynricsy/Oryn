/**
 * 设置 → 密钥：名称列表（方式、允许的主机）；「写入」对话框（env 名称校验、proxy 才有主机）；⋯ → 更新值（名称只读）与删除（先确认）。
 * 走 mock 后端，记录写请求。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MockServer } from "../../mock/server";
import { answerConfirm } from "../../test/confirm";
import SecretsSettings from "./SecretsSettings";

let writes: { method: string; path: string; body: unknown }[];

beforeEach(() => {
	const server = new MockServer();
	writes = [];
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			if (req.method !== "GET") {
				const text = await req.clone().text();
				writes.push({
					method: req.method,
					path: decodeURIComponent(new URL(req.url).pathname),
					body: text ? JSON.parse(text) : undefined,
				});
			}
			return (
				(await server.handleFetch(req)) ?? new Response(null, { status: 404 })
			);
		},
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

function mount() {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(
		<QueryClientProvider client={qc}>
			<MemoryRouter>
				<SecretsSettings />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

describe("设置 → 密钥", () => {
	it("列出名称、方式与允许的主机", async () => {
		mount();
		const row = (await screen.findByText("git/github.com/token")).closest(
			"li",
		) as HTMLElement;
		expect(row).toHaveTextContent("proxy");
		expect(row).toHaveTextContent("github.com");
		expect(
			(screen.getByText("NPM_TOKEN").closest("li") as HTMLElement).textContent,
		).toContain("env");
	});

	it("写入：env 名称不合法时不能保存；proxy 才填主机；保存发 PUT 后关闭", async () => {
		mount();
		await screen.findByText("NPM_TOKEN");
		fireEvent.click(screen.getByRole("button", { name: "写入" }));
		const dialog = await screen.findByRole("dialog", { name: "写入密钥" });
		const save = within(dialog).getByRole("button", { name: "保存" });
		fireEvent.change(within(dialog).getByRole("textbox", { name: "名称" }), {
			target: { value: "lower-case" },
		});
		fireEvent.change(within(dialog).getByLabelText("值"), {
			target: { value: "s3cret" },
		});
		expect(save).toBeEnabled();
		// 切到 env：名称不是合法环境变量名，保存不可用，主机字段消失
		const mode = within(dialog).getByRole("combobox", { name: /注入方式/ });
		fireEvent.click(mode);
		fireEvent.click(screen.getByRole("option", { name: /^env/ }));
		expect(save).toBeDisabled();
		expect(
			within(dialog).queryByRole("textbox", { name: /允许注入的主机/ }),
		).toBeNull();
		fireEvent.change(within(dialog).getByRole("textbox", { name: "名称" }), {
			target: { value: "API_TOKEN" },
		});
		expect(save).toBeEnabled();
		fireEvent.click(save);
		await waitFor(() =>
			expect(writes).toEqual([
				{
					method: "PUT",
					path: "/api/v1/secrets/API_TOKEN",
					body: { value: "s3cret", mode: "env" },
				},
			]),
		);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	it("⋯ → 更新值：名称只读，沿用方式与主机", async () => {
		mount();
		await screen.findByText("git/github.com/token");
		fireEvent.click(
			screen.getByRole("button", { name: "git/github.com/token 的更多操作" }),
		);
		fireEvent.click(await screen.findByRole("menuitem", { name: "更新值" }));
		const dialog = await screen.findByRole("dialog", {
			name: "更新密钥 git/github.com/token",
		});
		expect(
			within(dialog).getByRole("textbox", { name: "名称" }),
		).toHaveAttribute("readonly");
		fireEvent.change(within(dialog).getByLabelText("值"), {
			target: { value: "ghp_new" },
		});
		fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
		await waitFor(() =>
			expect(writes).toEqual([
				{
					method: "PUT",
					path: "/api/v1/secrets/git/github.com/token",
					body: { value: "ghp_new", mode: "proxy", hosts: ["github.com"] },
				},
			]),
		);
	});

	it("⋯ → 删除：确认后发 DELETE", async () => {
		mount();
		await screen.findByText("NPM_TOKEN");
		fireEvent.click(
			screen.getByRole("button", { name: "NPM_TOKEN 的更多操作" }),
		);
		fireEvent.click(await screen.findByRole("menuitem", { name: "删除" }));
		expect((await answerConfirm(true)).title).toBe("删除密钥 NPM_TOKEN？");
		await waitFor(() =>
			expect(writes).toEqual([
				{
					method: "DELETE",
					path: "/api/v1/secrets/NPM_TOKEN",
					body: undefined,
				},
			]),
		);
	});
});
