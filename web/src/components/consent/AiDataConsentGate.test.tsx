/** AI 数据共享同意框：required 时整页拦住（不能 Esc / 点遮罩关闭）、按用途列出接收方、同意即 PUT 当前全部 id；不需要时不出现；不同意给出退出路径。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AiDataConsent } from "../../api/ai-consent-queries";
import { AiDataConsentGate } from "./AiDataConsentGate";

const REQUIRED: AiDataConsent = {
	recipients: [
		{
			id: "model:anthropic",
			name: "Anthropic",
			host: "api.anthropic.com",
			purposes: ["chat", "memory"],
		},
		{
			id: "memory-embed:api.voyageai.com",
			name: "Voyage AI",
			host: "api.voyageai.com",
			purposes: ["memory"],
		},
		{ id: "web:public", name: "Web", purposes: ["web"] },
	],
	consentedAt: null,
	consentedRecipientIds: [],
	required: true,
};

let puts: unknown[];
let posts: string[];

function renderWith(state: AiDataConsent) {
	puts = [];
	posts = [];
	let current = state;
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: Request) => {
			const u = new URL(input.url);
			if (input.method === "PUT") {
				const body = (await input.json()) as { recipientIds: string[] };
				puts.push(body);
				current = {
					...current,
					consentedAt: 1,
					consentedRecipientIds: body.recipientIds,
					required: false,
				};
				return Response.json(current);
			}
			if (input.method === "POST") {
				posts.push(u.pathname);
				return new Response(null, { status: 204 });
			}
			return Response.json(current);
		}),
	);
	render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<MemoryRouter>
				<AiDataConsentGate />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("AiDataConsentGate", () => {
	it("required：按用途列出接收方，链接隐私政策，不能关闭；同意后 PUT 全部 id 并收起", async () => {
		renderWith(REQUIRED);
		const dialog = await screen.findByRole("dialog", {
			name: "AI 功能会把你的数据发给这些服务",
		});
		const chat = dialog.querySelector('[data-purpose="chat"]') as HTMLElement;
		const memory = dialog.querySelector(
			'[data-purpose="memory"]',
		) as HTMLElement;
		const web = dialog.querySelector('[data-purpose="web"]') as HTMLElement;
		expect(chat).toHaveTextContent("Anthropic");
		expect(chat).toHaveTextContent("api.anthropic.com");
		expect(chat).not.toHaveTextContent("Voyage AI");
		expect(memory).toHaveTextContent("Anthropic");
		expect(memory).toHaveTextContent("Voyage AI");
		expect(web).toHaveTextContent("公网网站");
		expect(screen.getByRole("link", { name: "隐私政策" })).toHaveAttribute(
			"href",
			"https://docs.oryn.best/privacy/",
		);
		expect(screen.queryByRole("button", { name: "关闭" })).toBeNull();
		fireEvent.keyDown(window, { key: "Escape" });
		expect(screen.getByRole("dialog")).toBeInTheDocument();

		fireEvent.click(screen.getByRole("button", { name: "同意并继续" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(puts).toEqual([
			{
				recipientIds: [
					"model:anthropic",
					"memory-embed:api.voyageai.com",
					"web:public",
				],
			},
		]);
	});

	it("不需要同意时不出现", async () => {
		renderWith({
			...REQUIRED,
			consentedAt: 1,
			consentedRecipientIds: REQUIRED.recipients.map((r) => r.id),
			required: false,
		});
		await waitFor(() => expect(fetch).toHaveBeenCalled());
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	it("不同意：说明 AI 功能不可用，浏览器里给退出登录，返回可再次同意", async () => {
		renderWith(REQUIRED);
		fireEvent.click(await screen.findByRole("button", { name: "不同意" }));
		expect(
			screen.getByRole("dialog", { name: "不同意就无法使用 AI 功能" }),
		).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "退出登录" }));
		await waitFor(() => expect(posts).toEqual(["/api/v1/auth/logout"]));
		fireEvent.click(screen.getByRole("button", { name: "返回" }));
		expect(
			screen.getByRole("button", { name: "同意并继续" }),
		).toBeInTheDocument();
		expect(puts).toEqual([]);
	});
});
