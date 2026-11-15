/**
 * 匿名分享页：加载中 / 不存在 / 正常三种状态；只读（没有输入框、没有 fork 菜单项）；
 * 全程只请求 `/api/v1/public/…`，不碰 `/api/v1/me`、`/api/v1/ws-ticket` 等私有接口。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Entry } from "../api/client";
import i18n from "../i18n";
import type { PublicShare } from "../share/feed";
import { resetThreadStores } from "../state/stores";
import { ATTACHMENT_POLL_MS, SharePage, usedAgentNames } from "./SharePage";

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

	it("读取元数据失败（非 404）：说明哪一步、为什么，并给出重试", async () => {
		stubFetch(() =>
			Response.json(
				{ code: "internal", message: "db locked" },
				{ status: 500 },
			),
		);
		renderAt(SLUG);
		expect(
			await screen.findByText(
				"读取分享失败：服务器出错",
				{},
				{ timeout: 8000 },
			),
		).toBeInTheDocument();
		expect(
			screen.getByText("稍后重试；一直失败时查看 orynd 日志。"),
		).toBeInTheDocument();
		expect(screen.getByText("db locked")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: T("share.page.retry") }),
		).toBeInTheDocument();
	}, 10_000);

	it("实时分享工作中：状态用中性的转圈，不用成功绿", () => {
		stubFetch(() => new Promise<Response>(() => {}));
		vi.stubGlobal(
			"WebSocket",
			class {
				static OPEN = 1;
				close() {}
			},
		);
		const client = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		client.setQueryData(["public-share", SLUG], {
			...SHARE,
			mode: "live",
			state: "working",
		});
		render(
			<QueryClientProvider client={client}>
				<MemoryRouter initialEntries={[`/s/${SLUG}`]}>
					<Routes>
						<Route path="/s/:slug" element={<SharePage />} />
					</Routes>
				</MemoryRouter>
			</QueryClientProvider>,
		);
		const state = screen.getByRole("img", { name: "工作中" });
		expect(state.className).toContain("text-muted");
		expect(state.querySelector("[class*='bg-ok']")).toBeNull();
	});
});

describe("SharePage 附件的公开地址", () => {
	const ATT = "att_01ABCDEFGHJKMNPQRSTVWXYZ01";
	const withAttachment: Entry = {
		...entry(1, "user", ""),
		payload: {
			type: "message",
			id: "e1",
			message: {
				role: "user",
				timestamp: 1,
				content: [
					{
						type: "text",
						text: `看附件\n\n<oryn-attachments>\nThe user attached these files.\n- /home/user/.oryn/attachments/${ATT}/a.txt (text/plain, 12 bytes)\n</oryn-attachments>`,
					},
				],
			},
		},
	};
	const page = {
		entries: [withAttachment],
		hasMore: false,
		lastSeq: 1,
		leafEntryId: "e1",
	};
	const published = {
		id: ATT,
		name: "a.txt",
		size: 12,
		mime: "text/plain",
		url: `/f/${ATT}`,
	};

	it("打开后才出现的附件：元数据里没有 → 立即复查，拿到公开地址后可下载", async () => {
		let metaCalls = 0;
		stubFetch((path) => {
			if (path === `/api/v1/public/shares/${SLUG}`) {
				metaCalls++;
				return Response.json(
					metaCalls === 1 ? SHARE : { ...SHARE, attachments: [published] },
				);
			}
			if (path === `/api/v1/public/shares/${SLUG}/entries`)
				return Response.json(page);
			return Response.json({ code: "unauthorized" }, { status: 401 });
		});
		renderAt(SLUG);
		expect(await screen.findByText("看附件")).toBeInTheDocument();
		const link = await screen.findByRole("link", { name: /a\.txt/ });
		expect(link).toHaveAttribute("href", `/f/${ATT}`);
		expect(metaCalls).toBe(2);
	});

	it("之后才被发布的附件：定期复查元数据；未发布前只显示名称", async () => {
		vi.useFakeTimers({ shouldAdvanceTime: true });
		try {
			let isPublic = false;
			stubFetch((path) => {
				if (path === `/api/v1/public/shares/${SLUG}`)
					return Response.json({
						...SHARE,
						attachments: [
							isPublic ? published : { ...published, url: undefined },
						],
					});
				if (path === `/api/v1/public/shares/${SLUG}/entries`)
					return Response.json(page);
				return Response.json({ code: "unauthorized" }, { status: 401 });
			});
			renderAt(SLUG);
			expect(await screen.findByText("a.txt")).toBeInTheDocument();
			expect(screen.queryByRole("link", { name: /a\.txt/ })).toBeNull();
			isPublic = true;
			await vi.advanceTimersByTimeAsync(ATTACHMENT_POLL_MS);
			await waitFor(() =>
				expect(screen.getByRole("link", { name: /a\.txt/ })).toHaveAttribute(
					"href",
					`/f/${ATT}`,
				),
			);
		} finally {
			vi.useRealTimers();
		}
	});
});

describe("SharePage 回合头身份（M9.6）", () => {
	const KEY_MARE = "a1b2c3d4e5f60718";
	const KEY_NEW = "ffeeddccbbaa9988";
	/** 主会话里的一串条目：parentId 依次接上。 */
	function chainEntries(
		items: { type: string; payload: Record<string, unknown> }[],
	): Entry[] {
		return items.map((it, i) => ({
			seq: i + 1,
			file: "s.jsonl",
			entryId: `e${i + 1}`,
			parentId: i > 0 ? `e${i}` : undefined,
			type: it.type,
			payload: { type: it.type, id: `e${i + 1}`, ...it.payload },
		}));
	}
	const rec = (data: Record<string, unknown>) => ({
		type: "custom",
		payload: {
			customType: "oryn-model-source",
			data: { v: 1, ...data },
		},
	});
	const msg = (role: string, text: string, ts: number) => ({
		type: "message",
		payload: {
			message: {
				role,
				content: [{ type: "text", text }],
				timestamp: ts,
				...(role === "assistant"
					? { provider: "openai", model: "gpt-6.1-sol" }
					: {}),
			},
		},
	});
	const SOL = "openai/gpt-6.1-sol";
	/** 回合 1：点了 Mare（键 KEY_MARE）；回合 2：手动选了同一模型。 */
	const ENTRIES = chainEntries([
		{ type: "model_change", payload: { model: SOL, role: KEY_MARE } },
		rec({ source: "role", role: KEY_MARE, model: SOL }),
		msg("user", "切到 Mare", 1),
		msg("assistant", "我是 Mare", 2),
		{ type: "model_change", payload: { model: SOL } },
		rec({ source: "manual", model: SOL }),
		msg("user", "手动选同一模型", 3),
		msg("assistant", "回落", 4),
	]);
	const IDENTITY = {
		agent: { name: "Oryn", avatarIcon: "🦊" },
		roles: [{ key: KEY_MARE, role: "Mare", avatarIcon: "🌊" }],
	};

	function serve(identity: () => Response) {
		stubFetch((path) => {
			if (path === `/api/v1/public/shares/${SLUG}`)
				return Response.json({ ...SHARE, lastSeq: 8, leafEntryId: "e8" });
			if (path === `/api/v1/public/shares/${SLUG}/entries`)
				return Response.json({
					entries: ENTRIES,
					hasMore: false,
					lastSeq: 8,
					leafEntryId: "e8",
				});
			if (path === `/api/v1/public/shares/${SLUG}/identity`) return identity();
			return Response.json({ code: "unauthorized" }, { status: 401 });
		});
	}

	const heads = () =>
		[...document.querySelectorAll<HTMLElement>("[data-turn-identity]")].map(
			(h) => ({
				identity: h.dataset.turnIdentity,
				role: h.dataset.role,
				name: h.querySelector('[data-testid="turn-name"]')?.textContent,
			}),
		);

	it("身份取自匿名接口：按键命中 role，手动选的回合回落为快照里的 agent 身份；没有任何设置请求", async () => {
		serve(() => Response.json(IDENTITY));
		renderAt(SLUG);
		expect(await screen.findByText("我是 Mare")).toBeInTheDocument();
		await waitFor(() =>
			expect(heads()).toEqual([
				{ identity: "role", role: "Mare", name: "Mare" },
				{ identity: "agent", role: undefined, name: "Oryn" },
			]),
		);
		const [mare, fallback] = [
			...document.querySelectorAll<HTMLElement>("[data-turn-identity]"),
		];
		expect(mare?.querySelector('[data-avatar-of="role"]')?.textContent).toBe(
			"🌊",
		);
		// 负向：手动选的回合没有 Mare 的头像，尽管模型相同
		expect(fallback?.querySelector('[data-avatar-of="role"]')).toBeNull();
		expect(
			fallback?.querySelector('[data-avatar-of="agent"]')?.textContent,
		).toBe("🦊");
		expect(urls).toContain(`/api/v1/public/shares/${SLUG}/identity`);
		expect(urls.filter((u) => u.includes("/api/v1/settings/"))).toEqual([]);
		for (const u of urls) expect(u.startsWith("/api/v1/public/")).toBe(true);
	});

	it("接口 404 或 {roles: []} → 默认品牌标", async () => {
		for (const res of [
			() => Response.json({ code: "not_found", message: "x" }, { status: 404 }),
			() => Response.json({ roles: [] }),
		]) {
			resetThreadStores();
			serve(res);
			const view = renderAt(SLUG);
			expect(await screen.findByText("我是 Mare")).toBeInTheDocument();
			await waitFor(() =>
				expect(urls.includes(`/api/v1/public/shares/${SLUG}/identity`)).toBe(
					true,
				),
			);
			await waitFor(() =>
				expect(heads()).toEqual([
					{ identity: "default", role: undefined, name: undefined },
					{ identity: "default", role: undefined, name: undefined },
				]),
			);
			expect(
				document.querySelectorAll(
					'[data-turn-identity] [data-avatar="default"][data-avatar-of="agent"]',
				).length,
			).toBe(2);
			view.unmount();
		}
	});

	it("实时分享：转录里出现身份表没有的键时重取身份，同一个键只重取一次", async () => {
		let identityCalls = 0;
		serve(() => {
			identityCalls++;
			return Response.json(
				identityCalls === 1
					? IDENTITY
					: {
							...IDENTITY,
							roles: [
								...IDENTITY.roles,
								{ key: KEY_NEW, role: "Cyrene", avatarIcon: "🌸" },
							],
						},
			);
		});
		const sockets: { onmessage?: (ev: MessageEvent) => void }[] = [];
		vi.stubGlobal(
			"WebSocket",
			class {
				static OPEN = 1;
				readyState = 1;
				onopen?: () => void;
				onmessage?: (ev: MessageEvent) => void;
				onclose?: () => void;
				constructor() {
					sockets.push(this);
					setTimeout(() => this.onopen?.(), 0);
				}
				send() {}
				close() {}
			},
		);
		const deliver = (data: unknown) =>
			act(() => {
				for (const s of sockets)
					s.onmessage?.(
						new MessageEvent("message", { data: JSON.stringify(data) }),
					);
			});
		const client = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		client.setQueryData(["public-share", SLUG], {
			...SHARE,
			mode: "live",
			state: "idle",
		});
		render(
			<QueryClientProvider client={client}>
				<MemoryRouter initialEntries={[`/s/${SLUG}`]}>
					<Routes>
						<Route path="/s/:slug" element={<SharePage />} />
					</Routes>
				</MemoryRouter>
			</QueryClientProvider>,
		);
		await waitFor(() => expect(sockets.length).toBe(1));
		await waitFor(() => expect(identityCalls).toBe(1));
		deliver({
			type: "share.snapshot",
			title: "重构重连",
			state: "idle",
			leafEntryId: "e8",
			lastSeq: 8,
			transcriptRev: 0,
			fullReplay: false,
			live: [],
			subagents: [],
		});
		deliver({
			type: "entries",
			entries: ENTRIES,
			leafEntryId: "e8",
			lastSeq: 8,
		});
		expect(await screen.findByText("我是 Mare")).toBeInTheDocument();
		// 已知键：不重取
		await act(async () => {});
		expect(identityCalls).toBe(1);

		// 点了一个新 role：键不在身份表里 → 重取一次，新回合显示它
		const more = chainEntries([
			...ENTRIES.map((e) => ({
				type: e.type,
				payload: e.payload as Record<string, unknown>,
			})),
			{
				type: "model_change",
				payload: { model: "anthropic/claude-opus-5-5", role: KEY_NEW },
			},
			rec({
				source: "role",
				role: KEY_NEW,
				model: "anthropic/claude-opus-5-5",
			}),
			msg("user", "切到新 role", 5),
			{
				type: "message",
				payload: {
					message: {
						role: "assistant",
						content: [{ type: "text", text: "新 role 的回答" }],
						timestamp: 6,
						provider: "anthropic",
						model: "claude-opus-5-5",
					},
				},
			},
		]).slice(8);
		deliver({
			type: "entries",
			entries: more,
			leafEntryId: "e12",
			lastSeq: 12,
		});
		expect(await screen.findByText("新 role 的回答")).toBeInTheDocument();
		await waitFor(() => expect(identityCalls).toBe(2));
		await waitFor(() =>
			expect(heads().at(-1)).toEqual({
				identity: "role",
				role: "Cyrene",
				name: "Cyrene",
			}),
		);
		// 同一个键再出现：不再重取
		deliver({
			type: "entries",
			entries: chainEntries([
				...ENTRIES.map((e) => ({
					type: e.type,
					payload: e.payload as Record<string, unknown>,
				})),
				...more.map((e) => ({
					type: e.type,
					payload: e.payload as Record<string, unknown>,
				})),
				rec({
					source: "role",
					role: KEY_NEW,
					model: "anthropic/claude-opus-5-5",
				}),
			]).slice(12),
			leafEntryId: "e13",
			lastSeq: 13,
		});
		await act(async () => {});
		expect(identityCalls).toBe(2);
		expect(urls.filter((u) => u.includes("/api/v1/settings/"))).toEqual([]);
	});

	it("实时分享：来源记录先于消息到达时不重取，消息到达后才为该键重取（新 Core 提前落盘记录）", async () => {
		let identityCalls = 0;
		let messageSent = false;
		// 后端规则：role 只有在生效期间出现过消息才进身份表
		serve(() => {
			identityCalls++;
			return Response.json(
				messageSent
					? {
							...IDENTITY,
							roles: [
								...IDENTITY.roles,
								{ key: KEY_NEW, role: "Cyrene", avatarIcon: "🌸" },
							],
						}
					: IDENTITY,
			);
		});
		const sockets: { onmessage?: (ev: MessageEvent) => void }[] = [];
		vi.stubGlobal(
			"WebSocket",
			class {
				static OPEN = 1;
				readyState = 1;
				onopen?: () => void;
				onmessage?: (ev: MessageEvent) => void;
				onclose?: () => void;
				constructor() {
					sockets.push(this);
					setTimeout(() => this.onopen?.(), 0);
				}
				send() {}
				close() {}
			},
		);
		const deliver = (data: unknown) =>
			act(() => {
				for (const s of sockets)
					s.onmessage?.(
						new MessageEvent("message", { data: JSON.stringify(data) }),
					);
			});
		const client = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		client.setQueryData(["public-share", SLUG], {
			...SHARE,
			mode: "live",
			state: "idle",
		});
		render(
			<QueryClientProvider client={client}>
				<MemoryRouter initialEntries={[`/s/${SLUG}`]}>
					<Routes>
						<Route path="/s/:slug" element={<SharePage />} />
					</Routes>
				</MemoryRouter>
			</QueryClientProvider>,
		);
		await waitFor(() => expect(sockets.length).toBe(1));
		await waitFor(() => expect(identityCalls).toBe(1));
		deliver({
			type: "share.snapshot",
			title: "重构重连",
			state: "idle",
			leafEntryId: "e8",
			lastSeq: 8,
			transcriptRev: 0,
			fullReplay: false,
			live: [],
			subagents: [],
		});
		deliver({
			type: "entries",
			entries: ENTRIES,
			leafEntryId: "e8",
			lastSeq: 8,
		});
		expect(await screen.findByText("我是 Mare")).toBeInTheDocument();

		const all = chainEntries([
			...ENTRIES.map((e) => ({
				type: e.type,
				payload: e.payload as Record<string, unknown>,
			})),
			{
				type: "model_change",
				payload: { model: "anthropic/claude-opus-5-5", role: KEY_NEW },
			},
			rec({
				source: "role",
				role: KEY_NEW,
				model: "anthropic/claude-opus-5-5",
			}),
			msg("user", "切到新 role", 5),
			{
				type: "message",
				payload: {
					message: {
						role: "assistant",
						content: [{ type: "text", text: "新 role 的回答" }],
						timestamp: 6,
						provider: "anthropic",
						model: "claude-opus-5-5",
					},
				},
			},
		]);
		// 1. 只有来源记录（Core 已落盘、还没有消息）：不重取
		deliver({
			type: "entries",
			entries: all.slice(8, 10),
			leafEntryId: "e10",
			lastSeq: 10,
		});
		await act(async () => {});
		expect(identityCalls).toBe(1);

		// 2. 消息到达：该键已用到 → 重取一次，新回合显示 Cyrene
		messageSent = true;
		deliver({
			type: "entries",
			entries: all.slice(10),
			leafEntryId: "e12",
			lastSeq: 12,
		});
		expect(await screen.findByText("新 role 的回答")).toBeInTheDocument();
		await waitFor(() => expect(identityCalls).toBe(2));
		await waitFor(() =>
			expect(heads().at(-1)).toEqual({
				identity: "role",
				role: "Cyrene",
				name: "Cyrene",
			}),
		);
	});
});

describe("SharePage task 卡片的 agent 头像（M13 R9）", () => {
	const IMAGE = "data:image/png;base64,iVBORw0KGgo=";
	function toolCall(id: string, args: Record<string, unknown>) {
		return { type: "toolCall", id, name: "task", arguments: args };
	}
	function assistant(seq: number, content: unknown[]): Entry {
		return {
			seq,
			file: "s.jsonl",
			entryId: `e${seq}`,
			parentId: `e${seq - 1}`,
			type: "message",
			payload: {
				type: "message",
				id: `e${seq}`,
				message: { role: "assistant", content, timestamp: seq },
			},
		};
	}
	const ENTRIES: Entry[] = [
		entry(1, "user", "派两个子 agent"),
		assistant(2, [
			toolCall("c1", { tasks: [{ name: "Alpha", agent: "scout", task: "a" }] }),
			toolCall("c2", {
				tasks: [{ name: "Beta", agent: "reviewer", task: "b" }],
			}),
		]),
	];

	it("公开身份里设了头像的 agent 显示该头像，没有的用默认字形；不发任何设置请求", async () => {
		stubFetch((path) => {
			if (path === `/api/v1/public/shares/${SLUG}`)
				return Response.json({ ...SHARE, lastSeq: 2, leafEntryId: "e2" });
			if (path === `/api/v1/public/shares/${SLUG}/entries`)
				return Response.json({
					entries: ENTRIES,
					hasMore: false,
					lastSeq: 2,
					leafEntryId: "e2",
				});
			if (path === `/api/v1/public/shares/${SLUG}/identity`)
				return Response.json({
					roles: [],
					agents: [{ name: "scout", avatarImage: IMAGE }],
				});
			return Response.json({ code: "unauthorized" }, { status: 401 });
		});
		renderAt(SLUG);
		const img = await waitFor(() => {
			const el = document.querySelector('img[data-avatar="image"]');
			if (!el) throw new Error("还没有 agent 头像图片");
			return el;
		});
		expect(img).toHaveAttribute("src", IMAGE);
		expect(img.closest("[data-state]")).toHaveAttribute("title", "scout");
		const reviewer = [
			...document.querySelectorAll<HTMLElement>("[role=img][data-state]"),
		].find((el) => el.title === "reviewer");
		expect(reviewer?.querySelector('[data-avatar="default"]')).not.toBeNull();
		expect(urls.filter((u) => u.includes("/api/v1/settings/"))).toEqual([]);
	});

	it("usedAgentNames 与后端 UsedAgents 同一规则：两种参数形状、条目退回顶层 agent、都没写算 task", () => {
		expect(
			usedAgentNames([
				entry(1, "user", "hi"),
				assistant(2, [
					toolCall("c1", { agent: "scout", tasks: [{ agent: "" }, {}] }),
					toolCall("c2", { tasks: [{ agent: "reviewer" }] }),
					toolCall("c3", { task: "flat" }),
					{ type: "toolCall", id: "c4", name: "bash", arguments: {} },
				]),
				{
					...assistant(3, [toolCall("c5", { agent: "foo" })]),
					file: "s/0-X.jsonl",
				},
			]),
		).toEqual(["scout", "reviewer", "task"]);
	});
});
