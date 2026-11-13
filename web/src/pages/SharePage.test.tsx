/**
 * 匿名分享页：加载中 / 不存在 / 正常三种状态；只读（没有输入框、没有 fork 菜单项）；
 * 全程只请求 `/api/v1/public/…`，不碰 `/api/v1/me`、`/api/v1/ws-ticket` 等私有接口。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Entry } from "../api/client";
import i18n from "../i18n";
import type { PublicShare } from "../share/feed";
import { resetThreadStores } from "../state/stores";
import { SharePage } from "./SharePage";

const SLUG = "fedcba9876543210fedcba9876543210";
const T = (k: string) => i18n.t(k as "common.copy");

const SHARE: PublicShare = {
	mode: "snapshot",
	title: "重构重连",
	sharedAt: Date.UTC(2026, 10, 13, 2, 0),
	threadCreatedAt: Date.UTC(2026, 10, 12),
	lastSeq: 2,
	leafEntryId: "e2",
	transcriptRev: 0,
	attachments: [],
};

function entry(seq: number, role: string, text: string): Entry {
	return {
		seq,
		file: "s.jsonl",
		entryId: `e${seq}`,
		parentId: seq > 1 ? `e${seq - 1}` : undefined,
		type: "message",
		payload: {
			type: "message",
			id: `e${seq}`,
			message: { role, content: [{ type: "text", text }], timestamp: seq },
		},
	};
}

let urls: string[];

function stubFetch(handler: (path: string) => Response | Promise<Response>) {
	urls = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: Request | string) => {
			const u = new URL(
				typeof input === "string" ? input : input.url,
				location.origin,
			);
			urls.push(u.pathname);
			return handler(u.pathname);
		}),
	);
}

function renderAt(slug: string) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter initialEntries={[`/s/${slug}`]}>
				<Routes>
					<Route path="/s/:slug" element={<SharePage />} />
				</Routes>
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

beforeEach(() => {
	resetThreadStores();
	// jsdom 没有布局：给虚拟列表一个足够高的视口，所有行都挂载
	vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(4000);
	vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(800);
});
afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

describe("SharePage", () => {
	it("加载中显示转圈", () => {
		stubFetch(() => new Promise<Response>(() => {}));
		renderAt(SLUG);
		expect(screen.getByRole("status")).toBeInTheDocument();
	});

	it("链接不存在或已撤销：空态", async () => {
		stubFetch(() =>
			Response.json({ code: "not_found", message: "x" }, { status: 404 }),
		);
		renderAt(SLUG);
		expect(await screen.findByText(T("share.page.gone"))).toBeInTheDocument();
	});

	it("slug 格式不对时不发请求，直接显示空态", () => {
		stubFetch(() => Response.json({}));
		renderAt("not-a-slug");
		expect(screen.getByText(T("share.page.gone"))).toBeInTheDocument();
		expect(urls).toEqual([]);
	});

	it("正常：只读转录，没有输入框与 fork，只请求公开接口", async () => {
		stubFetch((path) => {
			if (path === `/api/v1/public/shares/${SLUG}`) return Response.json(SHARE);
			if (path === `/api/v1/public/shares/${SLUG}/entries`)
				return Response.json({
					entries: [
						entry(1, "user", "数到 5"),
						entry(2, "assistant", "1 2 3 4 5"),
					],
					hasMore: false,
					lastSeq: 2,
					leafEntryId: "e2",
				});
			return Response.json({ code: "unauthorized" }, { status: 401 });
		});
		renderAt(SLUG);
		expect(await screen.findByText("数到 5")).toBeInTheDocument();
		expect(screen.getByText("1 2 3 4 5")).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { level: 1, name: "重构重连" }),
		).toBeInTheDocument();
		expect(document.title).toBe("重构重连 · Oryn");
		expect(screen.getByRole("link", { name: /Markdown/ })).toHaveAttribute(
			"href",
			`/s/${SLUG}.md`,
		);
		expect(screen.queryByRole("textbox")).toBeNull();

		const menus = screen.getAllByRole("button", {
			name: T("transcript.messageActions"),
		});
		for (const m of menus) fireEvent.click(m);
		expect(
			screen.queryByRole("menuitem", { name: T("transcript.forkHere") }),
		).toBeNull();
		expect(
			screen.queryByRole("menuitem", { name: T("transcript.label") }),
		).toBeNull();

		expect(urls.length).toBeGreaterThan(0);
		for (const u of urls) expect(u.startsWith("/api/v1/public/")).toBe(true);
		expect(urls).not.toContain("/api/v1/me");
		expect(urls).not.toContain("/api/v1/ws-ticket");
	});
});
