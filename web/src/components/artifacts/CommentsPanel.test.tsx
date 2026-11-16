/**
 * 评论界面：面板（未解决在前编号、已解决折叠、作者、元素已不在、回复、解决 / 重新打开）、新建评论浮框，
 * 以及查看器里的评论模式（按 C 下发 `select-mode`、点元素弹浮框、提交带锚点、图钉下发）。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../../api/client";
import i18n from "../../i18n";
import { indexStore } from "../../state/stores";
import { ArtifactViewer } from "./ArtifactViewer";
import {
	CommentBox,
	commentBoxPlacement,
	MAX_COMMENT_RUNES,
} from "./CommentBox";
import { CommentsPanel } from "./CommentsPanel";

const ID = "art_01JDNAWEB0000000000000000A";
const NOW = Date.now();

type Thread = Schemas["ArtifactCommentThread"];

function thread(
	id: string,
	createdAt: number,
	patch: Omit<Partial<Thread>, "anchor"> & {
		anchor?: Partial<Thread["anchor"]>;
	} = {},
): Thread {
	const { anchor, ...rest } = patch;
	return {
		id,
		createdAt,
		comments: [
			{ n: 1, authorLabel: "", body: `正文 ${id}`, createdAt: createdAt },
		],
		...rest,
		anchor: {
			version: 3,
			path: "index.html",
			page: null,
			offset: 40,
			tag: "h1",
			text: "本周习惯打卡",
			missing: false,
			...anchor,
		},
	};
}

let threads: Thread[];
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
	indexStore.setState({ threads: {} });
	threads = [
		thread("act_B", NOW - 2000, {
			comments: [
				{ n: 1, authorLabel: "", body: "标题换个说法", createdAt: NOW - 2000 },
				{
					n: 2,
					authorThreadId: "T-live",
					authorLabel: "旧标题",
					body: "已改",
					createdAt: NOW - 1000,
				},
			],
		}),
		thread("act_A", NOW - 3000, { anchor: { missing: true } }),
		thread("act_C", NOW - 500, { resolvedAt: NOW - 100 }),
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
			const artifact: Schemas["Artifact"] = {
				id: ID,
				origin: "agent",
				kind: "web",
				title: "本周习惯打卡",
				version: 3,
				size: 3,
				pages: 1,
				createdAt: 1,
				updatedAt: 2,
			};
			if (path === `/api/v1/artifacts/${ID}` && req.method === "GET")
				return Response.json({
					artifact,
					version: 3,
					head: 3,
					files: [],
					capabilities: [],
					threads: [],
				});
			if (path === `/api/v1/artifacts/${ID}/comments` && req.method === "GET")
				return Response.json({ version: 3, threads });
			if (path === `/api/v1/artifacts/${ID}/comments` && req.method === "POST")
				return Response.json(thread("act_NEW", NOW), { status: 201 });
			if (path.startsWith(`/api/v1/artifacts/${ID}/comments/`))
				return Response.json(threads[0]);
			if (path === `/api/v1/artifacts/${ID}/frames`)
				return Response.json(
					{ url: "https://a-x.example.test/", token: "t".repeat(26) },
					{ status: 201 },
				);
			return new Response(null, { status: 204 });
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

const t = i18n.t.bind(i18n);

describe("CommentsPanel", () => {
	it("未解决的在前并按创建先后编号；已解决的折叠；作者与元素已不在", async () => {
		indexStore.setState({
			threads: { "T-live": { title: "现在的标题" } as never },
		});
		wrap(<CommentsPanel artifactId={ID} threads={[]} canComment />);
		await screen.findByTestId("comments-panel");
		const cards = screen.getAllByTestId("comment-thread");
		expect(cards.map((c) => [c.dataset.thread, c.dataset.n])).toEqual([
			["act_A", "1"],
			["act_B", "2"],
		]);
		expect(cards[0]).toHaveTextContent(t("artifacts.comments.missing"));
		expect(cards[1]).not.toHaveTextContent(t("artifacts.comments.missing"));
		expect(cards[1]).toHaveTextContent(t("artifacts.history.you"));
		expect(cards[1]).toHaveTextContent("现在的标题");
		expect(cards[1]).toHaveTextContent("已改");
		expect(screen.queryByTestId("comments-resolved")).toBeNull();
		const toggle = screen.getByTestId("comments-resolved-toggle");
		expect(toggle).toHaveTextContent(
			t("artifacts.comments.showResolved", { count: 1 }),
		);
		fireEvent.click(toggle);
		const resolved = screen.getByTestId("comments-resolved");
		const card = within(resolved).getByTestId("comment-thread");
		expect(card.dataset.thread).toBe("act_C");
		expect(within(card).queryByTestId("comment-number")).toBeNull();
		// 已解决的串不能回复，只能重新打开
		expect(
			within(card).queryByRole("textbox", {
				name: t("artifacts.comments.reply"),
			}),
		).toBeNull();
		fireEvent.click(
			within(card).getByRole("button", {
				name: t("artifacts.comments.reopen"),
			}),
		);
		await waitFor(() =>
			expect(requests).toContainEqual({
				method: "PATCH",
				path: `/api/v1/artifacts/${ID}/comments/act_C`,
				body: { resolved: false },
			}),
		);
	});

	it("回复用 Mod+Enter 提交；标为已解决发 PATCH", async () => {
		wrap(<CommentsPanel artifactId={ID} threads={[]} canComment />);
		const [first] = await screen.findAllByTestId("comment-thread");
		const card = first as HTMLElement;
		const box = within(card).getByRole("textbox", {
			name: t("artifacts.comments.reply"),
		});
		fireEvent.change(box, { target: { value: "  再改短一点 " } });
		fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
		await waitFor(() =>
			expect(requests).toContainEqual({
				method: "POST",
				path: `/api/v1/artifacts/${ID}/comments/act_A/replies`,
				body: { body: "再改短一点" },
			}),
		);
		await waitFor(() => expect(box).toHaveValue(""));
		fireEvent.click(
			within(card).getByRole("button", {
				name: t("artifacts.comments.resolve"),
			}),
		);
		await waitFor(() =>
			expect(requests).toContainEqual({
				method: "PATCH",
				path: `/api/v1/artifacts/${ID}/comments/act_A`,
				body: { resolved: true },
			}),
		);
	});

	it("没有评论时给出空态与操作提示", async () => {
		threads = [];
		wrap(<CommentsPanel artifactId={ID} threads={[]} canComment />);
		expect(
			await screen.findByText(t("artifacts.comments.empty")),
		).toBeVisible();
		expect(screen.getByText(t("artifacts.comments.emptyHint"))).toBeVisible();
	});
});

describe("CommentBox", () => {
	const anchor: Schemas["ArtifactCommentAnchor"] = {
		version: 3,
		path: "index.html",
		page: null,
		offset: 40,
		tag: "h1",
		text: "本周习惯打卡",
		missing: false,
	};

	it("Mod+Enter 提交锚点与去掉首尾空白的正文；Esc 取消；超过 4000 字禁用提交", async () => {
		const onDone = vi.fn();
		const onCancel = vi.fn();
		wrap(
			<CommentBox
				artifactId={ID}
				anchor={anchor}
				at={{ x: 10, y: 10 }}
				bounds={{ width: 800, height: 600 }}
				onDone={onDone}
				onCancel={onCancel}
			/>,
		);
		const field = screen.getByRole("textbox");
		expect(field).toHaveFocus();
		const submit = screen.getByTestId("comment-submit");
		expect(submit).toBeDisabled();
		fireEvent.change(field, {
			target: { value: "字".repeat(MAX_COMMENT_RUNES + 1) },
		});
		expect(submit).toBeDisabled();
		expect(screen.getByTestId("comment-count")).toHaveTextContent(
			`${MAX_COMMENT_RUNES + 1} / ${MAX_COMMENT_RUNES}`,
		);
		fireEvent.change(field, { target: { value: " 换个颜色 " } });
		expect(submit).toBeEnabled();
		fireEvent.keyDown(field, { key: "Enter", metaKey: true });
		await waitFor(() => expect(onDone).toHaveBeenCalled());
		expect(requests).toContainEqual({
			method: "POST",
			path: `/api/v1/artifacts/${ID}/comments`,
			body: { anchor, body: "换个颜色" },
		});
		fireEvent.keyDown(field, { key: "Escape" });
		expect(onCancel).toHaveBeenCalled();
	});

	it("浮框夹在容器之内，下方放不下时翻到点击处上方", () => {
		expect(
			commentBoxPlacement({ x: 700, y: 20 }, { width: 800, height: 600 }),
		).toEqual({ left: 452, top: 28, width: 340 });
		const up = commentBoxPlacement(
			{ x: 0, y: 580 },
			{ width: 300, height: 600 },
		);
		expect(up.left).toBe(8);
		expect(up.width).toBe(284);
		expect(up.top).toBeLessThan(580);
	});
});

describe("查看器里的评论模式", () => {
	async function openFrame() {
		const frame = (await screen.findByTestId(
			"artifact-frame",
		)) as HTMLIFrameElement;
		const post = vi.fn();
		vi.spyOn(frame.contentWindow as Window, "postMessage").mockImplementation(
			post,
		);
		act(() => {
			frame.dispatchEvent(new Event("load"));
		});
		const sent = () =>
			post.mock.calls.map((c) => c[0]) as {
				kind: string;
				channel: string;
				[k: string]: unknown;
			}[];
		const channel = String(sent().find((m) => m.kind === "hello")?.channel);
		const fromPage = (data: Record<string, unknown>) =>
			act(() => {
				window.dispatchEvent(
					new MessageEvent("message", {
						data: { v: 1, channel, ...data },
						source: frame.contentWindow,
					}),
				);
			});
		return { sent, fromPage };
	}

	it("按 C 进入评论模式并下发图钉；点元素弹浮框，提交带锚点；Esc 退出模式", async () => {
		wrap(<ArtifactViewer artifactId={ID} variant="page" />);
		const { sent, fromPage } = await openFrame();
		await screen.findByTestId("artifact-mode-comment");
		const last = (kind: string) => sent().findLast((m) => m.kind === kind);
		// 默认关闭，不画图钉
		expect(last("select-mode")).toMatchObject({ mode: null });
		expect(last("select-pins")).toMatchObject({ pins: [] });
		fireEvent.keyDown(window, { key: "c" });
		await waitFor(() =>
			expect(last("select-mode")).toMatchObject({ mode: "comment" }),
		);
		// 评论模式下：未解决、元素还在的串画图钉（act_A 已不在、act_C 已解决）
		await waitFor(() =>
			expect(last("select-pins")).toMatchObject({
				pins: [{ n: 2, src: 40, answered: true }],
			}),
		);
		// 整页里进入评论模式时打开评论面板，分段带未解决数
		expect(await screen.findByTestId("comments-panel")).toBeVisible();
		expect(screen.getByTestId("artifact-side-comments")).toHaveTextContent("2");

		fromPage({
			kind: "select",
			selection: {
				path: "index.html",
				src: 88,
				tag: "p",
				text: "11 月 10 日",
				textOnly: true,
				style: {
					color: "#000000",
					"background-color": "#ffffff",
					"font-size": "15px",
					"font-weight": "400",
					"text-align": "left",
					padding: "0px",
					"border-radius": "0px",
				},
			},
			at: { x: 30, y: 40 },
		});
		const box = await screen.findByTestId("comment-box");
		expect(box).toHaveTextContent("<p>");
		fireEvent.change(within(box).getByRole("textbox"), {
			target: { value: "日期写全" },
		});
		fireEvent.click(within(box).getByTestId("comment-submit"));
		await waitFor(() =>
			expect(requests).toContainEqual({
				method: "POST",
				path: `/api/v1/artifacts/${ID}/comments`,
				body: {
					anchor: {
						version: 3,
						path: "index.html",
						page: null,
						offset: 88,
						tag: "p",
						text: "11 月 10 日",
						missing: false,
					},
					body: "日期写全",
				},
			}),
		);
		await waitFor(() => expect(screen.queryByTestId("comment-box")).toBeNull());
		// 提交后清掉页面上的选中
		expect(last("select-source")).toMatchObject({ src: null });

		// 点空白处：整个页面的锚点
		fromPage({ kind: "select", selection: null, at: { x: 5, y: 5 } });
		expect(await screen.findByTestId("comment-box")).toHaveTextContent(
			t("artifacts.comments.wholePage"),
		);
		fireEvent.keyDown(
			within(screen.getByTestId("comment-box")).getByRole("textbox"),
			{
				key: "Escape",
			},
		);
		expect(screen.queryByTestId("comment-box")).toBeNull();

		fireEvent.keyDown(window, { key: "Escape" });
		await waitFor(() =>
			expect(last("select-mode")).toMatchObject({ mode: null }),
		);
		// 评论面板仍开着：图钉继续显示
		expect(last("select-pins")).toMatchObject({
			pins: [{ n: 2, src: 40, answered: true }],
		});
	});

	it("V 进入选择模式；输入框里的按键不切换；右面板没有评论面板", async () => {
		wrap(<ArtifactViewer artifactId={ID} variant="panel" />);
		const { sent } = await openFrame();
		await screen.findByTestId("artifact-mode-select");
		const last = (kind: string) => sent().findLast((m) => m.kind === kind);
		fireEvent.keyDown(window, { key: "v" });
		await waitFor(() =>
			expect(last("select-mode")).toMatchObject({ mode: "select" }),
		);
		const input = document.createElement("input");
		document.body.append(input);
		fireEvent.keyDown(input, { key: "c" });
		expect(last("select-mode")).toMatchObject({ mode: "select" });
		input.remove();
		fireEvent.click(screen.getByTestId("artifact-mode-comment"));
		await waitFor(() =>
			expect(last("select-mode")).toMatchObject({ mode: "comment" }),
		);
		expect(screen.queryByTestId("comments-panel")).toBeNull();
		await waitFor(() =>
			expect(last("select-pins")).toMatchObject({
				pins: [{ n: 2, src: 40, answered: true }],
			}),
		);
	});
});
