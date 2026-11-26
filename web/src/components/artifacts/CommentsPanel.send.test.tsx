/**
 * 把评论发给线程（计划 M14 3.4）：候选线程与排序、发给已有线程（发消息后记已发送）、发给新线程（交给首屏，
 * 不记已发送）、全部发过时禁用；以及 `useArtifactActivity` 取在场信息。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	renderHook,
	screen,
	waitFor,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas, Thread } from "../../api/client";
import i18n from "../../i18n";
import type {
	ArtifactActivityEvent,
	ArtifactEvent,
} from "../../realtime/socket";
import { sendGeneratedPrompt } from "../../state/actions";
import { indexStore } from "../../state/stores";
import { toastsStore } from "../../state/toasts";
import { uiStore } from "../../state/ui";
import { pickOption } from "../../test/select";
import { CommentsPanel, sendTargets } from "./CommentsPanel";
import { useArtifactActivity } from "./use-activity";

vi.mock("../../state/actions", () => ({
	sendGeneratedPrompt: vi.fn(async () => undefined),
}));

const ID = "art_01JDNAWEB0000000000000000A";
const t = i18n.t.bind(i18n);

type CommentThread = Schemas["ArtifactCommentThread"];

function thread(
	id: string,
	createdAt: number,
	patch: Partial<CommentThread> = {},
): CommentThread {
	return {
		id,
		createdAt,
		anchor: {
			version: 3,
			path: "index.html",
			page: null,
			offset: 40,
			tag: "h1",
			text: "本周习惯打卡",
			missing: false,
		},
		comments: [{ n: 1, authorLabel: "", body: `正文 ${id}`, createdAt }],
		...patch,
	};
}

function th(id: string, patch: Partial<Thread> = {}): Thread {
	return {
		id,
		title: `线程 ${id}`,
		titleSource: "user",
		state: "idle",
		stateChangedAt: 1,
		pinned: false,
		labels: [],
		unread: false,
		createdAt: 1,
		updatedAt: 1,
		...patch,
	} as Thread;
}

let comments: CommentThread[];
let requests: { method: string; path: string; body: unknown }[];

function wrap(node: ReactNode) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={["/artifacts/x"]}>
				<Routes>
					<Route path="/artifacts/x" element={node} />
					<Route path="/" element={<p data-testid="home">home</p>} />
				</Routes>
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const DETAIL_THREADS = [
	{ threadId: "T-maker", title: "做报告", archived: false },
	{ threadId: "T-old", title: "已归档的", archived: true },
];

beforeEach(() => {
	requests = [];
	toastsStore.setState({ toasts: [] });
	uiStore.setState({ homeSeed: null });
	indexStore.setState({ threads: {} });
	vi.mocked(sendGeneratedPrompt).mockClear();
	comments = [
		thread("act_A", 1000),
		thread("act_B", 2000),
		thread("act_C", 3000, { resolvedAt: 3500 }),
	];
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
						projectId: "P1",
						origin: "agent",
						kind: "web",
						title: "本周习惯打卡",
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
					threads: DETAIL_THREADS,
				});
			if (path === `/api/v1/artifacts/${ID}/versions`)
				return Response.json({ versions: [] });
			if (path === `/api/v1/artifacts/${ID}/comments`)
				return Response.json({ version: 3, threads: comments });
			return new Response(null, { status: 204 });
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

async function sendBar() {
	const bar = await screen.findByTestId("comments-send");
	const select = screen.getByRole("combobox", {
		name: t("artifacts.comments.sendTarget"),
	});
	const button = screen.getByRole("button", {
		name: t("artifacts.comments.send"),
	});
	// 等详情到达（标题与项目），按钮才可用
	await waitFor(() => expect(button).toBeEnabled());
	return { bar, select, button };
}

describe("把评论发给线程", () => {
	it("发给已有线程：发消息、记已发送、提示", async () => {
		wrap(<CommentsPanel artifactId={ID} threads={DETAIL_THREADS} canComment />);
		const { bar, select, button } = await sendBar();
		expect(bar).toHaveTextContent(
			t("artifacts.comments.sendLead", { count: 2 }),
		);
		fireEvent.click(select);
		expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
			"做报告",
			t("artifacts.comments.sendNewThread"),
		]);
		fireEvent.click(select);
		expect(select).toHaveTextContent("做报告");
		fireEvent.click(button);
		await waitFor(() =>
			expect(requests).toContainEqual({
				method: "POST",
				path: `/api/v1/artifacts/${ID}/comments/sent`,
				body: { threadIds: ["act_A", "act_B"], sentTo: "T-maker" },
			}),
		);
		expect(sendGeneratedPrompt).toHaveBeenCalledTimes(1);
		const [to, text] = vi.mocked(sendGeneratedPrompt).mock.calls[0] as [
			string,
			string,
		];
		expect(to).toBe("T-maker");
		expect(text).toContain("1. (act_A) 正文 act_A");
		expect(text).toContain("2. (act_B) 正文 act_B");
		expect(text).not.toContain("act_C");
		await waitFor(() =>
			expect(toastsStore.getState().toasts.map((x) => x.text)).toContain(
				t("artifacts.comments.sentToast", { count: 2, thread: "做报告" }),
			),
		);
	});

	it("发送失败时提示原因、不记已发送", async () => {
		vi.mocked(sendGeneratedPrompt).mockRejectedValueOnce(
			new Error("not connected"),
		);
		wrap(<CommentsPanel artifactId={ID} threads={DETAIL_THREADS} canComment />);
		const { button } = await sendBar();
		fireEvent.click(button);
		await waitFor(() =>
			expect(toastsStore.getState().toasts[0]?.tone).toBe("err"),
		);
		expect(requests.some((r) => r.path.endsWith("/comments/sent"))).toBe(false);
	});

	it("发给新线程：交给首屏输入框并跳到 /，不记已发送", async () => {
		wrap(<CommentsPanel artifactId={ID} threads={DETAIL_THREADS} canComment />);
		const { select, button } = await sendBar();
		pickOption(select, t("artifacts.comments.sendNewThread"));
		fireEvent.click(button);
		expect(await screen.findByTestId("home")).toBeVisible();
		const seed = uiStore.getState().homeSeed;
		expect(seed?.projectId).toBe("P1");
		expect(seed?.text).toContain("1. (act_A) 正文 act_A");
		expect(sendGeneratedPrompt).not.toHaveBeenCalled();
		expect(requests.some((r) => r.path.endsWith("/comments/sent"))).toBe(false);
	});

	it("未处理的都发过时显示说明并禁用", async () => {
		comments = [
			thread("act_A", 1000, { sentAt: 1500, sentTo: "T-maker" }),
			thread("act_B", 2000, { sentAt: 2500, sentTo: "T-maker" }),
		];
		wrap(<CommentsPanel artifactId={ID} threads={DETAIL_THREADS} canComment />);
		const bar = await screen.findByTestId("comments-send");
		expect(bar).toHaveTextContent(t("artifacts.comments.allSent"));
		expect(
			screen.getByRole("button", { name: t("artifacts.comments.send") }),
		).toBeDisabled();
		expect(screen.getAllByText(t("artifacts.comments.sent")).length).toBe(2);
	});

	it("评论都解决了就不显示发送条", async () => {
		comments = [thread("act_C", 3000, { resolvedAt: 3500 })];
		wrap(<CommentsPanel artifactId={ID} threads={DETAIL_THREADS} canComment />);
		await screen.findByTestId("comments-panel");
		expect(screen.queryByTestId("comments-send")).toBeNull();
	});
});

describe("sendTargets", () => {
	it("正在改的 → 最近写过版本的 → 最近活动的；排除归档、子线程与别的项目", () => {
		const index: Record<string, Thread> = {
			"T-maker": th("T-maker", { projectId: "P1", lastMessageAt: 10 }),
			"T-writer": th("T-writer", { projectId: "P1", lastMessageAt: 5 }),
			"T-busy": th("T-busy", { projectId: "P1", lastMessageAt: 1 }),
			"T-reader": th("T-reader", { projectId: "P1", lastMessageAt: 50 }),
			"T-recent": th("T-recent", { projectId: "P1", lastMessageAt: 99 }),
			"T-arch": th("T-arch", { projectId: "P1", archivedAt: 1 }),
			"T-child": th("T-child", { projectId: "P1", parentThreadId: "T-maker" }),
			"T-other": th("T-other", { projectId: "P2", lastMessageAt: 1000 }),
			"T-asst": th("T-asst", { projectId: "P1", kind: "assistant" }),
		};
		const targets = sendTargets({
			detailThreads: [
				{ threadId: "T-maker", title: "旧标题", archived: false },
				{ threadId: "T-writer", title: "写过", archived: false },
				{ threadId: "T-gone", title: "已归档", archived: true },
			],
			index,
			projectId: "P1",
			activity: [
				{ threadId: "T-busy", doing: "write" },
				{ threadId: "T-reader", doing: "read" },
			],
			versions: [
				{
					version: 1,
					createdAt: 100,
					authorThreadId: "T-maker",
					authorLabel: "",
					size: 1,
					pages: 1,
				},
				{
					version: 2,
					createdAt: 200,
					authorThreadId: "T-writer",
					authorLabel: "",
					size: 1,
					pages: 1,
				},
			],
		});
		expect(targets.map((x) => x.threadId)).toEqual([
			"T-busy",
			"T-writer",
			"T-maker",
			"T-recent",
			"T-reader",
		]);
		// 线程还在时用它现在的标题
		expect(targets.find((x) => x.threadId === "T-maker")?.title).toBe(
			"线程 T-maker",
		);
	});
});

describe("useArtifactActivity", () => {
	it("先取缓存的最近一条，之后随 artifact.activity 更新", () => {
		let last: ArtifactActivityEvent | undefined = {
			type: "artifact.activity",
			id: ID,
			viewers: 1,
			threads: [],
			clients: [],
			visitors: 0,
		};
		const listeners = new Set<(m: ArtifactEvent) => void>();
		const client = {
			onArtifact: (_id: string, cb: (m: ArtifactEvent) => void) => {
				listeners.add(cb);
				return () => listeners.delete(cb);
			},
			lastArtifactActivity: () => last,
		};
		const { result } = renderHook(() => useArtifactActivity(ID, client));
		expect(result.current?.viewers).toBe(1);
		const next: ArtifactActivityEvent = {
			...last,
			viewers: 2,
			threads: [{ threadId: "T-1", doing: "write" }],
		};
		act(() => {
			last = next;
			for (const cb of listeners) cb(next);
		});
		expect(result.current?.viewers).toBe(2);
		expect(result.current?.threads[0]?.doing).toBe("write");
	});
});
