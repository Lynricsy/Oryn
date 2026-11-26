/** 设置 → 分享与 Artifacts 的公开线程与文件两组；都为空时只有一组空态；撤销与撤回经 ⋯ 菜单并先确认。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { PublishedFile, ThreadShare } from "../../api/share-queries";
import i18n from "../../i18n";
import { answerConfirm } from "../../test/confirm";
import SharesSettings from "./SharesSettings";

const T = (k: string) => i18n.t(k as "common.copy");

const SHARE: ThreadShare = {
	id: "shr_01JCZ8M3W4X5Y6Z7A8B9C0D1E2",
	threadId: "T-0193a1b2-0000-7000-8000-0000000000e1",
	threadTitle: "🐳 DOCKER: 冻结后重连",
	mode: "snapshot",
	slug: "ab".repeat(16),
	url: `https://oryn.example/s/${"ab".repeat(16)}`,
	createdAt: Date.now() - 3_600_000,
	untilSeq: 12,
	findings: 1,
};
const FILE: PublishedFile = {
	id: "pub_01JCZ8M3W4X5Y6Z7A8B9C0D1E2",
	attachmentId: "att_01JCZ8M3W4X5Y6Z7A8B9C0D1E2",
	threadId: SHARE.threadId,
	name: "trace.log",
	mime: "text/plain",
	size: 2048,
	url: "https://oryn.example/f/cd/trace.log",
	createdAt: Date.now() - 60_000,
};

let deletes: string[];

function renderWith(overview: {
	shares: ThreadShare[];
	files: PublishedFile[];
}) {
	deletes = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: Request) => {
			const u = new URL(input.url);
			if (input.method === "DELETE") {
				deletes.push(u.pathname);
				return new Response(null, { status: 204 });
			}
			return Response.json(overview);
		}),
	);
	render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<MemoryRouter>
				<SharesSettings />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("SharesSettings", () => {
	it("都为空：只显示一组空态", async () => {
		renderWith({ shares: [], files: [] });
		const empty = await screen.findByRole("region", {
			name: T("share.settings.public"),
		});
		expect(empty).toHaveTextContent(T("share.settings.empty"));
		expect(screen.queryByText(T("share.settings.threads"))).toBeNull();
	});

	it("列出公开的线程与文件；线程标题点进线程，撤销与撤回经 ⋯ 菜单并先确认", async () => {
		renderWith({ shares: [SHARE], files: [FILE] });
		const title = await screen.findByRole("link", { name: "冻结后重连" });
		expect(title).toHaveAttribute("href", `/t/${SHARE.threadId}`);
		const threads = screen.getByRole("region", {
			name: T("share.settings.threads"),
		});
		const files = screen.getByRole("region", {
			name: T("share.settings.files"),
		});
		expect(
			within(files).getByRole("link", { name: "trace.log" }),
		).toHaveAttribute("href", FILE.url);
		expect(
			within(threads).getByRole("button", {
				name: T("share.page.copyLink"),
			}),
		).toBeInTheDocument();

		fireEvent.click(
			within(threads).getByRole("button", { name: "「冻结后重连」的更多操作" }),
		);
		fireEvent.click(
			await screen.findByRole("menuitem", { name: T("share.dialog.revoke") }),
		);
		expect((await answerConfirm(false)).title).toBe(
			"撤销「冻结后重连」的公开链接？",
		);
		expect(deletes).toEqual([]);

		fireEvent.click(
			within(files).getByRole("button", { name: "「trace.log」的更多操作" }),
		);
		fireEvent.click(
			await screen.findByRole("menuitem", { name: T("publish.unpublish") }),
		);
		await answerConfirm(true);
		await waitFor(() =>
			expect(deletes).toEqual([`/api/v1/published-files/${FILE.id}`]),
		);
	});
});
