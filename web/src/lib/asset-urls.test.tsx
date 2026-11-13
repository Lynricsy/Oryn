/**
 * 资源地址注入：缺省（站内）与分享页覆盖两种 Provider 下，用户消息图片、附件芯片与 Markdown 图片的地址。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it } from "vitest";
import { Markdown } from "../components/markdown/Markdown";
import {
	TranscriptContext,
	type TranscriptContextValue,
} from "../components/transcript/context";
import { MessageView } from "../components/transcript/MessageView";
import i18n from "../i18n";
import { AssetUrlContext, type AssetUrls } from "./asset-urls";

const SHA = "ab".repeat(32);
const ATT = "att_01JCZ8M3W4X5Y6Z7A8B9C0D1E2";

const ctx: TranscriptContextValue = {
	threadId: "T-assets",
	toolResults: new Map(),
	liveTools: {},
	subagents: {},
	openSubagent: () => {},
	forkFrom: () => {},
	labelEntry: () => {},
};

const message = {
	role: "user",
	content: [
		{
			type: "text",
			text: `看这张图\n\n<oryn-attachments>\nThe user attached these files.\n- /home/user/.oryn/attachments/T-assets/${ATT}/trace.log (text/plain, 12 bytes)\n</oryn-attachments>`,
		},
		{ type: "image", data: `blob:sha256:${SHA}`, mimeType: "image/png" },
	],
};

function renderAll(assets?: AssetUrls): HTMLElement {
	const body: ReactNode = (
		<QueryClientProvider client={new QueryClient()}>
			<TranscriptContext.Provider value={ctx}>
				<MessageView message={message} live={false} entryId="E1" />
				<div data-testid="md">
					<Markdown text={`![图](blob:sha256:${SHA})`} />
				</div>
			</TranscriptContext.Provider>
		</QueryClientProvider>
	);
	const { container } = render(
		assets ? (
			<AssetUrlContext.Provider value={assets}>{body}</AssetUrlContext.Provider>
		) : (
			body
		),
	);
	return container;
}

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
});

describe("AssetUrlContext", () => {
	it("缺省走站内私有接口", () => {
		const root = renderAll();
		const imgs = [...root.querySelectorAll("img")].map((i) =>
			i.getAttribute("src"),
		);
		expect(imgs).toContain(`/api/v1/blobs/${SHA}`);
		expect(
			screen.getByTestId("md").querySelector("img")?.getAttribute("src"),
		).toBe(`/api/v1/blobs/${SHA}`);
		expect(
			screen.getByText("trace.log").closest("a")?.getAttribute("href"),
		).toBe(`/api/v1/attachments/${ATT}/content`);
	});

	it("分享页覆盖：图片走公开接口，未发布的附件芯片不可点", () => {
		const root = renderAll({
			blob: (sha) => `/api/v1/public/shares/s1/blobs/${sha}`,
			attachment: () => null,
		});
		const imgs = [...root.querySelectorAll("img")].map((i) =>
			i.getAttribute("src"),
		);
		expect(imgs).toContain(`/api/v1/public/shares/s1/blobs/${SHA}`);
		expect(imgs).not.toContain(`/api/v1/blobs/${SHA}`);
		expect(
			screen.getByTestId("md").querySelector("img")?.getAttribute("src"),
		).toBe(`/api/v1/public/shares/s1/blobs/${SHA}`);
		const chip = screen.getByText("trace.log");
		expect(chip.closest("a")).toBeNull();
		expect(chip.closest("span[title]")).not.toBeNull();
	});

	it("分享页覆盖：已发布的附件芯片指向公开 URL", () => {
		renderAll({
			blob: (sha) => `/api/v1/public/shares/s1/blobs/${sha}`,
			attachment: (id) =>
				id === ATT ? "https://oryn.example/f/abc/trace.log" : null,
		});
		expect(
			screen.getByText("trace.log").closest("a")?.getAttribute("href"),
		).toBe("https://oryn.example/f/abc/trace.log");
	});
});
