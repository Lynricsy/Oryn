/** 设置 → 隐私与数据：列出接收方与同意时间，撤回先确认再 DELETE；没有接收方时给空态；隐私政策链接。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AiDataConsent } from "../../api/ai-consent-queries";
import { answerConfirm } from "../../test/confirm";
import PrivacySettings from "./PrivacySettings";

const CONSENTED: AiDataConsent = {
	recipients: [
		{
			id: "model:openai",
			name: "OpenAI",
			host: "api.openai.com",
			purposes: ["chat"],
		},
	],
	consentedAt: Date.UTC(2026, 10, 27, 8),
	consentedRecipientIds: ["model:openai"],
	required: false,
};

let deletes: number;

function renderWith(state: AiDataConsent) {
	deletes = 0;
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: Request) => {
			if (input.method === "DELETE") {
				deletes++;
				return Response.json({
					...state,
					consentedAt: null,
					consentedRecipientIds: [],
					required: state.recipients.length > 0,
				});
			}
			return Response.json(state);
		}),
	);
	render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<MemoryRouter>
				<PrivacySettings />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("PrivacySettings", () => {
	it("列出接收方与同意时间；撤回先确认再 DELETE，之后显示尚未同意", async () => {
		renderWith(CONSENTED);
		expect(await screen.findByText("OpenAI")).toBeInTheDocument();
		expect(screen.getByText("api.openai.com")).toBeInTheDocument();
		expect(screen.getByText(/2026.* 同意$/)).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "隐私政策" })).toHaveAttribute(
			"href",
			"https://docs.oryn.best/privacy/",
		);
		fireEvent.click(screen.getByRole("button", { name: "撤回同意" }));
		expect((await answerConfirm(true)).title).toBe("撤回 AI 数据共享的同意？");
		await waitFor(() => expect(deletes).toBe(1));
		expect(await screen.findByText("尚未同意")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "撤回同意" })).toBeNull();
	});

	it("没有接收方：空态说明", async () => {
		renderWith({
			recipients: [],
			consentedAt: null,
			consentedRecipientIds: [],
			required: false,
		});
		expect(
			await screen.findByText("当前配置不会把数据发给第三方 AI 服务。"),
		).toBeInTheDocument();
	});
});
