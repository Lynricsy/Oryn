/**
 * 设计便签自动投递（计划 M29 I3）：设计画布的评论面板显示「自动发给设计线程」开关，切换发 PATCH；
 * 开着时未发出的未解决评论标「已排队」；不是设计画布时没有开关。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../../api/client";
import i18n from "../../i18n";
import { indexStore } from "../../state/stores";
import { CommentsPanel } from "./CommentsPanel";

const ID = "art_01JDNAWEB0000000000000000A";
const NOW = Date.now();
const t = i18n.t.bind(i18n);

type Thread = Schemas["ArtifactCommentThread"];

function thread(id: string, patch: Partial<Thread> = {}): Thread {
	return {
		id,
		createdAt: NOW,
		anchor: {
			version: 3,
			path: "dist/home.html",
			page: { number: 1, name: "首页" },
			offset: null,
			tag: "button",
			text: "立即开始",
			missing: false,
			loc: "src/frames/home.tsx:3:5",
		},
		comments: [{ n: 1, authorLabel: "", body: `正文 ${id}`, createdAt: NOW }],
		...patch,
	};
}

let kind: Schemas["Artifact"]["kind"];
let design: Schemas["ArtifactDesign"];
let requests: { method: string; path: string; body: unknown }[];

function wrap(node: ReactNode) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter>{node}</MemoryRouter>
		</QueryClientProvider>,
	);
}

beforeEach(() => {
	requests = [];
	kind = "canvas";
	design = { design: true, live: null, direction: null, autoNotes: true };
	indexStore.setState({ threads: {} });
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const path = new URL(req.url).pathname;
			const body = req.method === "GET" ? undefined : await req.json();
			requests.push({ method: req.method, path, body });
			if (path === `/api/v1/artifacts/${ID}` && req.method === "GET")
				return Response.json({
					artifact: {
						id: ID,
						origin: "agent",
						kind,
						title: "登录页设计",
						version: 3,
						size: 3,
						pages: 1,
						createdAt: 1,
						updatedAt: 2,
					},
					version: 3,
					head: 3,
					files: [],
					capabilities: [],
					threads: [],
				});
			if (path === `/api/v1/artifacts/${ID}/comments`)
				return Response.json({
					version: 3,
					threads: [
						thread("act_A"),
						thread("act_B", { sentAt: NOW, sentTo: "T-a" }),
					],
				});
			if (path === `/api/v1/artifacts/${ID}/design`) {
				if (req.method === "PATCH")
					design = { ...design, ...(body as { autoNotes: boolean }) };
				return Response.json(design);
			}
			return new Response(null, { status: 204 });
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

describe("CommentsPanel 设计便签自动投递", () => {
	it("开着时未发出的评论标「已排队」；关掉发 PATCH 并去掉标记", async () => {
		wrap(<CommentsPanel artifactId={ID} threads={[]} canComment />);
		const toggle = await screen.findByRole("checkbox", {
			name: t("artifacts.comments.autoNotes"),
		});
		expect(toggle).toBeChecked();
		expect(screen.getByTestId("comments-auto-notes")).toHaveTextContent(
			t("artifacts.comments.autoNotesOn"),
		);
		const queued = await screen.findAllByTestId("comment-queued");
		expect(queued).toHaveLength(1);
		expect(
			queued[0]?.closest("[data-thread]")?.getAttribute("data-thread"),
		).toBe("act_A");

		fireEvent.click(toggle);
		await waitFor(() =>
			expect(requests).toContainEqual({
				method: "PATCH",
				path: `/api/v1/artifacts/${ID}/design`,
				body: { autoNotes: false },
			}),
		);
		await waitFor(() => expect(toggle).not.toBeChecked());
		expect(screen.queryByTestId("comment-queued")).toBeNull();
		expect(screen.getByTestId("comments-auto-notes")).toHaveTextContent(
			t("artifacts.comments.autoNotesOff"),
		);
		// 手动发送仍在
		expect(screen.getByTestId("comments-send")).toBeInTheDocument();
	});

	it("HTML 画布与网页没有开关，也不标「已排队」", async () => {
		design = { design: false, live: null, direction: null, autoNotes: true };
		const { unmount } = wrap(
			<CommentsPanel artifactId={ID} threads={[]} canComment />,
		);
		await screen.findByTestId("comments-panel");
		await waitFor(() =>
			expect(requests.some((r) => r.path.endsWith("/design"))).toBe(true),
		);
		expect(screen.queryByTestId("comments-auto-notes")).toBeNull();
		expect(screen.queryByTestId("comment-queued")).toBeNull();
		unmount();

		kind = "web";
		requests = [];
		wrap(<CommentsPanel artifactId={ID} threads={[]} canComment />);
		await screen.findByTestId("comments-panel");
		await waitFor(() =>
			expect(requests.some((r) => r.path === `/api/v1/artifacts/${ID}`)).toBe(
				true,
			),
		);
		expect(requests.some((r) => r.path.endsWith("/design"))).toBe(false);
		expect(screen.queryByTestId("comments-auto-notes")).toBeNull();
	});
});
