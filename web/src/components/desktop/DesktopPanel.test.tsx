/**
 * Desktop 面板：连接条件（ready、displayReady、前台，缺一不连；后台满 60 秒断开）、休眠遮罩与唤醒、
 * 意外断开重试三次后出现「重新连接」、4429 不重试、浏览器未运行时的「启动浏览器」、
 * Profile 横幅（被占用 → 冲突对话框 → 接管；装载失败 → 重试；被接管 → 只读，随 orb 推送实时出现）、
 * Profile 菜单（被占用 / 409 → 对话框 → 只读使用；只读副本换另一个被占用的 Profile 仍可只读）、可控制 / 仅观看开关、全屏与回退、
 * 迟到的 REST 应答不盖掉更新的推送、键盘进出舞台、软键盘期间点画面不抢焦点、剪贴板只在显式操作时收发。
 * 假会话照 noVNC 1.7 的画布行为：tabIndex=-1、focusOnClick 时 mousedown / touchstart 抢焦点、keydown 全部 preventDefault。
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
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Orb, Schemas, Thread } from "../../api/client";
import i18n from "../../i18n";
import { acceptOrbPush, getThreadStore, indexStore } from "../../state/stores";
import { uiStore } from "../../state/ui";
import DesktopPanel, { AWAY_MS, RETRY_MS } from "./DesktopPanel";
import type { DesktopHandlers } from "./session";

interface FakeSession {
	target: HTMLElement;
	orbId: string;
	h: DesktopHandlers;
	setViewOnly: ReturnType<typeof vi.fn>;
	setScale: ReturnType<typeof vi.fn>;
	paste: ReturnType<typeof vi.fn>;
	sendKey: ReturnType<typeof vi.fn>;
	focus: ReturnType<typeof vi.fn>;
	setFocusOnClick: ReturnType<typeof vi.fn>;
	close: ReturnType<typeof vi.fn>;
	canvas: HTMLCanvasElement;
	/** noVNC 收到（并拦下）的按键 */
	novncKeys: string[];
}

const sessions: FakeSession[] = [];

vi.mock("./session", () => ({
	openDesktopSession: (
		target: HTMLElement,
		orbId: string,
		h: DesktopHandlers,
	) => {
		// 与 noVNC 1.7 的画布一致（core/rfb.js:231、584-585、693-699；input/keyboard.js 的 stopEvent）
		const canvas = document.createElement("canvas");
		canvas.tabIndex = -1;
		target.appendChild(canvas);
		let focusOnClick = true;
		const grab = () => {
			if (focusOnClick) canvas.focus({ preventScroll: true });
		};
		canvas.addEventListener("mousedown", grab);
		canvas.addEventListener("touchstart", grab);
		const novncKeys: string[] = [];
		canvas.addEventListener("keydown", (e) => {
			novncKeys.push(e.key);
			e.stopPropagation();
			e.preventDefault();
		});
		const s: FakeSession = {
			target,
			orbId,
			h,
			setViewOnly: vi.fn(),
			setScale: vi.fn(),
			paste: vi.fn(),
			sendKey: vi.fn(),
			focus: vi.fn(() => canvas.focus({ preventScroll: true })),
			setFocusOnClick: vi.fn((on: boolean) => {
				focusOnClick = on;
			}),
			close: vi.fn(),
			canvas,
			novncKeys,
		};
		sessions.push(s);
		return s;
	},
}));

const PROFILE_WORK = "bpf_01JCZ8M3W4X5Y6Z7A8B9C0DWRK";
const PROFILE_GH = "bpf_01JCZ8M3W4X5Y6Z7A8B9C0DGHB";
const HOLDER: Schemas["BrowserProfileHolder"] = {
	orbId: "orb-holder000000000000000001",
	threadIds: ["T-holder"],
	threadId: "T-holder",
	threadTitle: "🐳 DOCKER: 冻结后重连",
	since: Date.UTC(2026, 10, 14, 2, 0),
};

let seq = 0;
let requests: { method: string; path: string; body: unknown }[];
let putReply: (
	body: Schemas["OrbDesktopProfilePut"],
) => Response | Promise<Response>;
let startReply: () => Response | Promise<Response>;
let currentOrb: Orb;

function conflictBody(id: string, name: string) {
	return {
		code: "profile_in_use",
		message: "held",
		profileId: id,
		profileName: name,
		holder: HOLDER,
	};
}

function setup(
	desktop: Schemas["OrbDesktop"] | undefined,
	state: Orb["state"] = "ready",
) {
	seq++;
	const threadId = `T-desk-${seq}`;
	const orbId = `orb-desk${String(seq).padStart(18, "0")}`;
	currentOrb = {
		id: orbId,
		state,
		stateChangedAt: seq,
		threadIds: [threadId],
		...(desktop ? { desktop } : {}),
	} as Orb;
	getThreadStore(threadId)
		.getState()
		.patchLocal({ thread: { id: threadId, orbId } as Thread, orb: currentOrb });
	indexStore.getState().upsertOrb(currentOrb);
	return { threadId, orbId };
}

/** 模拟 WS 推送新的 orb。 */
function push(patch: Partial<Orb>) {
	currentOrb = { ...currentOrb, ...patch };
	act(() => acceptOrbPush(currentOrb));
}

function mount(threadId: string) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<DesktopPanel threadId={threadId} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const READY: Schemas["OrbDesktop"] = { displayReady: true, browser: "running" };

function setVisibility(v: "visible" | "hidden") {
	Object.defineProperty(document, "visibilityState", {
		configurable: true,
		get: () => v,
	});
}

const last = () => sessions.at(-1) as FakeSession;
const status = () => screen.getByTestId("desktop-status");

beforeEach(() => {
	sessions.length = 0;
	requests = [];
	localStorage.clear();
	uiStore.setState({
		desktopControl: true,
		desktopScale: null,
		panelMax: false,
	});
	setVisibility("visible");
	putReply = () => Response.json(currentOrb);
	startReply = () =>
		Response.json({
			...currentOrb,
			desktop: { ...currentOrb.desktop, browser: "running" },
		});
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const path = new URL(req.url).pathname;
			const text = req.method === "GET" ? "" : await req.text();
			const body = text ? JSON.parse(text) : undefined;
			requests.push({ method: req.method, path, body });
			if (path === "/api/v1/browser-profiles")
				return Response.json({
					profiles: [
						{
							id: PROFILE_GH,
							name: "GitHub 测试号",
							revision: 1,
							sizeBytes: 1,
							holder: HOLDER,
							createdAt: 1,
							updatedAt: 1,
						},
						{
							id: PROFILE_WORK,
							name: "工作账号",
							revision: 3,
							sizeBytes: 1,
							createdAt: 1,
							updatedAt: 1,
						},
					],
				});
			if (path.endsWith("/desktop/profile") && req.method === "PUT")
				return putReply(body);
			if (path.endsWith("/desktop/browser/start")) return startReply();
			if (path.endsWith("/wake"))
				return Response.json({ ok: true }, { status: 202 });
			return new Response(null, { status: 404 });
		}),
	);
});

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

describe("DesktopPanel 连接", () => {
	it("ready + displayReady + 前台：连接到舞台里的容器，初始为可控制、按断点取缩放", () => {
		const { threadId, orbId } = setup(READY);
		mount(threadId);
		expect(sessions).toHaveLength(1);
		expect(last().orbId).toBe(orbId);
		expect(screen.getByTestId("desktop-stage")).toContainElement(last().target);
		expect(last().setViewOnly).toHaveBeenCalledWith(false);
		expect(last().setScale).toHaveBeenCalledWith("follow");
		act(() => last().h.onStatus("connecting"));
		expect(status()).toHaveAttribute("data-state", "connecting");
		act(() => last().h.onStatus("connected"));
		expect(status()).toHaveAttribute("data-state", "connected");
	});

	it.each([
		["orb 不是 ready", READY, "frozen" as const, "visible" as const],
		[
			"显示未就绪",
			{ displayReady: false, browser: "stopped" as const },
			"ready" as const,
			"visible" as const,
		],
		["页面在后台", READY, "ready" as const, "hidden" as const],
	])("缺一不连：%s", (_name, desktop, state, vis) => {
		setVisibility(vis);
		const { threadId } = setup(desktop, state);
		mount(threadId);
		expect(sessions).toHaveLength(0);
		expect(status()).toHaveAttribute("data-state", "closed");
	});

	it("没有桌面的 orb：空态，不连接", () => {
		const { threadId } = setup(undefined);
		mount(threadId);
		expect(
			screen.getByText(i18n.t("deskPanel.unsupported")),
		).toBeInTheDocument();
		expect(sessions).toHaveLength(0);
	});

	it("页面转入后台满 60 秒断开，回到前台再连", () => {
		vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
		const { threadId } = setup(READY);
		mount(threadId);
		const first = last();
		setVisibility("hidden");
		act(() => document.dispatchEvent(new Event("visibilitychange")));
		act(() => vi.advanceTimersByTime(AWAY_MS - 1));
		expect(first.close).not.toHaveBeenCalled();
		act(() => vi.advanceTimersByTime(1));
		expect(first.close).toHaveBeenCalled();
		setVisibility("visible");
		act(() => document.dispatchEvent(new Event("visibilitychange")));
		expect(sessions).toHaveLength(2);
	});

	it("orb 冻结：断开并显示休眠遮罩；「唤醒」调 POST threads/{id}/wake", async () => {
		const { threadId } = setup(READY);
		mount(threadId);
		const first = last();
		push({ state: "frozen", stateChangedAt: 99 });
		expect(first.close).toHaveBeenCalled();
		const cover = screen.getByTestId("desktop-asleep");
		await act(async () => {
			fireEvent.click(
				within(cover).getByRole("button", { name: i18n.t("deskPanel.wake") }),
			);
		});
		expect(requests).toContainEqual(
			expect.objectContaining({
				method: "POST",
				path: `/api/v1/threads/${threadId}/wake`,
			}),
		);
	});

	it("意外断开按 1/2/5 秒重试三次，之后显示「重新连接」；点它再连", () => {
		vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
		const { threadId } = setup(READY);
		mount(threadId);
		for (const delay of RETRY_MS) {
			const n = sessions.length;
			act(() => last().h.onStatus("closed", 1006));
			expect(screen.getByTestId("desktop-connecting")).toBeInTheDocument();
			act(() => vi.advanceTimersByTime(delay - 1));
			expect(sessions).toHaveLength(n);
			act(() => vi.advanceTimersByTime(1));
			expect(sessions).toHaveLength(n + 1);
		}
		act(() => last().h.onStatus("closed", 4502));
		act(() => vi.advanceTimersByTime(60_000));
		expect(sessions).toHaveLength(4);
		const cover = screen.getByTestId("desktop-closed");
		expect(cover).toHaveTextContent(i18n.t("deskPanel.closed.unreachableHint"));
		fireEvent.click(
			within(cover).getByRole("button", {
				name: i18n.t("deskPanel.reconnect"),
			}),
		);
		expect(sessions).toHaveLength(5);
	});

	it("连上之后重新计数：断开一次后又连上，下次断开仍从 1 秒开始", () => {
		vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
		const { threadId } = setup(READY);
		mount(threadId);
		act(() => last().h.onStatus("closed", 1006));
		act(() => vi.advanceTimersByTime(RETRY_MS[0]));
		act(() => last().h.onStatus("connected"));
		act(() => last().h.onStatus("closed", 1006));
		act(() => vi.advanceTimersByTime(RETRY_MS[0]));
		expect(sessions).toHaveLength(3);
	});

	it("4429 观看者已满：不重试，提示关掉其它窗口后重新连接", () => {
		vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
		const { threadId } = setup(READY);
		mount(threadId);
		act(() => last().h.onStatus("closed", 4429));
		act(() => vi.advanceTimersByTime(30_000));
		expect(sessions).toHaveLength(1);
		const cover = screen.getByTestId("desktop-closed");
		expect(cover).toHaveTextContent(i18n.t("deskPanel.closed.full"));
		expect(
			within(cover).getByRole("button", {
				name: i18n.t("deskPanel.reconnect"),
			}),
		).toBeInTheDocument();
	});

	it("4410 orb 已删除：不重试，也不给重新连接", () => {
		const { threadId } = setup(READY);
		mount(threadId);
		act(() => last().h.onStatus("closed", 4410));
		const cover = screen.getByTestId("desktop-closed");
		expect(cover).toHaveTextContent(i18n.t("deskPanel.closed.deleted"));
		expect(within(cover).queryByRole("button")).toBeNull();
	});
});

describe("DesktopPanel 工具条与浏览器", () => {
	it("浏览器未运行且已连接：舞台中央「启动浏览器」，点后 POST browser/start", async () => {
		const { threadId, orbId } = setup({
			displayReady: true,
			browser: "stopped",
		});
		mount(threadId);
		expect(screen.queryByTestId("desktop-start-browser")).toBeNull();
		act(() => last().h.onStatus("connected"));
		await act(async () => {
			fireEvent.click(screen.getByTestId("desktop-start-browser"));
		});
		expect(requests).toContainEqual(
			expect.objectContaining({
				method: "POST",
				path: `/api/v1/orbs/${orbId}/desktop/browser/start`,
			}),
		);
		await waitFor(() =>
			expect(screen.queryByTestId("desktop-start-browser")).toBeNull(),
		);
	});

	it("可控制 / 仅观看开关：默认可控制，切换后 setViewOnly(true) 并记住", () => {
		const { threadId } = setup(READY);
		mount(threadId);
		act(() => last().h.onStatus("connected"));
		const btn = screen.getByTestId("desktop-control");
		expect(btn).toHaveAccessibleName(i18n.t("deskPanel.toWatch"));
		fireEvent.click(btn);
		expect(last().setViewOnly).toHaveBeenLastCalledWith(true);
		expect(localStorage.getItem("oryn.desktop.control")).toBe("0");
		expect(btn).toHaveAccessibleName(i18n.t("deskPanel.toControl"));
		expect(screen.getByText(i18n.t("deskPanel.watching"))).toBeInTheDocument();
	});

	it("全屏：对舞台 requestFullscreen；不支持时退到面板最大化", async () => {
		const { threadId } = setup(READY);
		mount(threadId);
		const stage = screen.getByTestId("desktop-stage");
		const req = vi.fn().mockResolvedValue(undefined);
		stage.requestFullscreen = req;
		await act(async () => {
			fireEvent.click(screen.getByTestId("desktop-fullscreen"));
		});
		expect(req).toHaveBeenCalled();
		expect(uiStore.getState().panelMax).toBe(false);

		req.mockRejectedValue(new Error("denied"));
		await act(async () => {
			fireEvent.click(screen.getByTestId("desktop-fullscreen"));
		});
		expect(uiStore.getState().panelMax).toBe(true);
	});

	it("「更多」里切缩放：适应 → setScale(fit) 并记住", () => {
		const { threadId } = setup(READY);
		mount(threadId);
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("deskPanel.more") }),
		);
		fireEvent.click(
			screen.getByRole("menuitemradio", {
				name: i18n.t("deskPanel.scale.fit"),
			}),
		);
		expect(last().setScale).toHaveBeenLastCalledWith("fit");
		expect(localStorage.getItem("oryn.desktop.scale")).toBe("fit");
	});
});

describe("DesktopPanel Profile", () => {
	it("被占用横幅 → 冲突对话框（持有者线程与开始时间）→ 接管：PUT profileMode=takeover", async () => {
		const { threadId, orbId } = setup({
			displayReady: true,
			browser: "stopped",
			profile: {
				id: PROFILE_GH,
				name: "GitHub 测试号",
				status: "in_use",
				mode: "exclusive",
				revision: 0,
				heldBy: HOLDER,
			},
		});
		mount(threadId);
		expect(
			screen.getByText(
				i18n.t("deskPanel.profile.inUseBy", { thread: "冻结后重连" }),
			),
		).toBeInTheDocument();
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("deskPanel.banner.choose") }),
		);
		const dialog = screen.getByTestId("profile-conflict");
		expect(dialog).toHaveTextContent("冻结后重连");
		expect(dialog).toHaveTextContent(i18n.t("deskPanel.conflict.readonlyHint"));
		expect(dialog).toHaveTextContent(i18n.t("deskPanel.conflict.takeoverHint"));
		await act(async () => {
			fireEvent.click(screen.getByTestId("profile-conflict-takeover"));
		});
		expect(requests).toContainEqual({
			method: "PUT",
			path: `/api/v1/orbs/${orbId}/desktop/profile`,
			body: { profileId: PROFILE_GH, profileMode: "takeover" },
		});
		await waitFor(() =>
			expect(screen.queryByTestId("profile-conflict")).toBeNull(),
		);
	});

	it("装载失败横幅：显示原因，「重试」以同一 id 重新 PUT", async () => {
		const { threadId, orbId } = setup({
			displayReady: true,
			browser: "stopped",
			profile: {
				id: PROFILE_WORK,
				name: "工作账号",
				status: "error",
				mode: "exclusive",
				revision: 0,
				error: "no_space",
			},
		});
		mount(threadId);
		expect(
			screen.getByText(i18n.t("deskPanel.profileError.no_space")),
		).toBeInTheDocument();
		await act(async () => {
			fireEvent.click(
				screen.getByRole("button", { name: i18n.t("deskPanel.banner.retry") }),
			);
		});
		expect(requests).toContainEqual({
			method: "PUT",
			path: `/api/v1/orbs/${orbId}/desktop/profile`,
			body: { profileId: PROFILE_WORK },
		});
	});

	it("被接管：随 orb 推送实时显示「只读 Profile · 已被〈线程〉接管」，菜单按钮带「只读」", () => {
		const attached: Schemas["OrbDesktop"] = {
			displayReady: true,
			browser: "running",
			profile: {
				id: PROFILE_WORK,
				name: "工作账号",
				status: "attached",
				mode: "exclusive",
				revision: 3,
			},
		};
		const { threadId } = setup(attached);
		mount(threadId);
		const taken = i18n.t("deskPanel.profile.takenOver", { thread: "WS: 重构" });
		expect(screen.queryByText(taken)).toBeNull();
		push({
			desktop: {
				...attached,
				profile: {
					id: PROFILE_WORK,
					name: "工作账号",
					status: "readonly",
					mode: "readonly",
					revision: 3,
					readonlyReason: "taken_over",
					takenOverBy: { threadId: "T-x", threadTitle: "WS: 重构", at: 1 },
				},
			},
		});
		expect(screen.getByText(taken)).toBeInTheDocument();
		expect(screen.getByTestId("desktop-profile-menu")).toHaveTextContent(
			i18n.t("deskPanel.profile.readonlyShort"),
		);
		// 浏览器照常运行：不断开桌面
		expect(sessions).toHaveLength(1);
	});

	it("菜单：选空闲的 Profile 先确认，PUT 回 409 时弹对话框，选只读后以 readonly 重发", async () => {
		vi.spyOn(window, "confirm").mockReturnValue(true);
		putReply = (b) =>
			b.profileMode
				? Response.json(currentOrb)
				: Response.json(conflictBody(PROFILE_WORK, "工作账号"), {
						status: 409,
					});
		const { threadId, orbId } = setup(READY);
		mount(threadId);
		fireEvent.click(screen.getByTestId("desktop-profile-menu"));
		const item = await screen.findByRole("menuitemradio", { name: /工作账号/ });
		await act(async () => {
			fireEvent.click(item);
		});
		expect(window.confirm).toHaveBeenCalledWith(
			i18n.t("deskPanel.menu.switchConfirm"),
		);
		await screen.findByTestId("profile-conflict");
		await act(async () => {
			fireEvent.click(screen.getByTestId("profile-conflict-readonly"));
		});
		const puts = requests.filter((r) => r.method === "PUT");
		expect(puts.map((r) => r.body)).toEqual([
			{ profileId: PROFILE_WORK },
			{ profileId: PROFILE_WORK, profileMode: "readonly" },
		]);
		expect(puts[0]?.path).toBe(`/api/v1/orbs/${orbId}/desktop/profile`);
	});

	it("菜单：列表里已被别的 orb 持有的 Profile 标「使用中」，选它直接弹对话框（不先发请求）", async () => {
		vi.spyOn(window, "confirm").mockReturnValue(true);
		const { threadId } = setup(READY);
		mount(threadId);
		fireEvent.click(screen.getByTestId("desktop-profile-menu"));
		const item = await screen.findByRole("menuitemradio", {
			name: /GitHub 测试号/,
		});
		expect(item).toHaveTextContent(i18n.t("deskPanel.menu.inUse"));
		fireEvent.click(item);
		expect(screen.getByTestId("profile-conflict")).toBeInTheDocument();
		expect(requests.filter((r) => r.method === "PUT")).toHaveLength(0);
	});
});

const ATTACHED_WORK: Schemas["OrbDesktop"] = {
	displayReady: true,
	browser: "stopped",
	profile: {
		id: PROFILE_WORK,
		name: "工作账号",
		status: "attached",
		mode: "exclusive",
		revision: 3,
	},
};

const READONLY_WORK: Schemas["OrbDesktop"] = {
	displayReady: true,
	browser: "running",
	profile: {
		id: PROFILE_WORK,
		name: "工作账号",
		status: "readonly",
		mode: "readonly",
		revision: 3,
	},
};

describe("DesktopPanel Profile：只读副本与乱序", () => {
	it("只读使用 A 时换到被占用的 B：对话框仍给「只读使用」，以 readonly 发 PUT", async () => {
		vi.spyOn(window, "confirm").mockReturnValue(true);
		const { threadId, orbId } = setup(READONLY_WORK);
		mount(threadId);
		fireEvent.click(screen.getByTestId("desktop-profile-menu"));
		fireEvent.click(
			await screen.findByRole("menuitemradio", { name: /GitHub 测试号/ }),
		);
		await act(async () => {
			fireEvent.click(screen.getByTestId("profile-conflict-readonly"));
		});
		expect(requests).toContainEqual({
			method: "PUT",
			path: `/api/v1/orbs/${orbId}/desktop/profile`,
			body: { profileId: PROFILE_GH, profileMode: "readonly" },
		});
	});

	it("只读使用 A 时恢复写回 A 撞上 409：只给「接管」", async () => {
		vi.spyOn(window, "confirm").mockReturnValue(true);
		putReply = () =>
			Response.json(conflictBody(PROFILE_WORK, "工作账号"), { status: 409 });
		const { threadId } = setup(READONLY_WORK);
		mount(threadId);
		await act(async () => {
			fireEvent.click(
				screen.getByRole("button", {
					name: i18n.t("deskPanel.banner.writeBack"),
				}),
			);
		});
		await screen.findByTestId("profile-conflict");
		expect(screen.queryByTestId("profile-conflict-readonly")).toBeNull();
		expect(screen.getByTestId("profile-conflict-takeover")).toBeInTheDocument();
	});

	it("在途期间推来「已被接管」，迟到的 REST 应答（旧的独占快照）不覆盖它", async () => {
		let release: (r: Response) => void = () => {};
		startReply = () =>
			new Promise<Response>((r) => {
				release = r;
			});
		const { threadId } = setup(ATTACHED_WORK);
		mount(threadId);
		act(() => last().h.onStatus("connected"));
		// 服务端在接管之前生成的应答：浏览器已启动、Profile 仍独占（生命周期时间戳不变）
		const staleOrb = {
			...currentOrb,
			desktop: { ...ATTACHED_WORK, browser: "running" as const },
		};
		await act(async () => {
			fireEvent.click(screen.getByTestId("desktop-start-browser"));
		});
		const taken = i18n.t("deskPanel.profile.takenOver", { thread: "WS: 重构" });
		push({
			desktop: {
				...READONLY_WORK,
				profile: {
					...(READONLY_WORK.profile as NonNullable<
						Schemas["OrbDesktop"]["profile"]
					>),
					readonlyReason: "taken_over",
					takenOverBy: { threadId: "T-x", threadTitle: "WS: 重构", at: 1 },
				},
			},
		});
		expect(screen.getByText(taken)).toBeInTheDocument();
		await act(async () => {
			release(Response.json(staleOrb));
		});
		// 「关闭浏览器」在启动请求在途时禁用：等它可用 = 应答已处理完
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("deskPanel.more") }),
		);
		await waitFor(() =>
			expect(
				screen.getByRole("menuitem", { name: i18n.t("deskPanel.stopBrowser") }),
			).toBeEnabled(),
		);
		expect(screen.getByText(taken)).toBeInTheDocument();
		expect(
			indexStore.getState().orbs[currentOrb.id]?.desktop?.profile?.mode,
		).toBe("readonly");
	});

	it("在途期间没有推送：REST 应答照常写入", async () => {
		const { threadId, orbId } = setup({
			displayReady: true,
			browser: "stopped",
		});
		mount(threadId);
		act(() => last().h.onStatus("connected"));
		await act(async () => {
			fireEvent.click(screen.getByTestId("desktop-start-browser"));
		});
		await waitFor(() =>
			expect(indexStore.getState().orbs[orbId]?.desktop?.browser).toBe(
				"running",
			),
		);
	});
});

describe("DesktopPanel 键盘", () => {
	it("舞台可 Tab 聚焦（画布 tabIndex=-1 不行）；Enter 把焦点交给画布，单独按一下 Ctrl+Alt 交还舞台", () => {
		const { threadId } = setup(READY);
		mount(threadId);
		act(() => last().h.onStatus("connected"));
		const stage = screen.getByRole("application", {
			name: i18n.t("deskPanel.stage"),
		});
		const { canvas } = last();
		expect(canvas.tabIndex).toBe(-1);
		expect(stage.tabIndex).toBe(0);
		expect(stage).toHaveAccessibleDescription(i18n.t("deskPanel.stageHint"));
		act(() => stage.focus());
		expect(screen.getByTestId("desktop-stage-hint")).toBeInTheDocument();
		fireEvent.keyDown(stage, { key: "Enter" });
		expect(document.activeElement).toBe(canvas);
		expect(screen.queryByTestId("desktop-stage-hint")).toBeNull();
		// Ctrl+Alt+T 是发给桌面的快捷键，不退出
		fireEvent.keyDown(canvas, { key: "Control", ctrlKey: true });
		fireEvent.keyDown(canvas, { key: "Alt", ctrlKey: true, altKey: true });
		fireEvent.keyDown(canvas, { key: "t", ctrlKey: true, altKey: true });
		fireEvent.keyUp(canvas, { key: "Alt", ctrlKey: true });
		fireEvent.keyUp(canvas, { key: "Control" });
		expect(document.activeElement).toBe(canvas);
		expect(last().novncKeys).toEqual(["Control", "Alt", "t"]);
		// 单独按一下 Ctrl+Alt：交还舞台
		fireEvent.keyDown(canvas, { key: "Control", ctrlKey: true });
		fireEvent.keyDown(canvas, { key: "Alt", ctrlKey: true, altKey: true });
		fireEvent.keyUp(canvas, { key: "Alt", ctrlKey: true });
		expect(document.activeElement).toBe(stage);
		fireEvent.keyUp(stage, { key: "Control" });
		expect(document.activeElement).toBe(stage);
	});

	it("软键盘打开期间点画面不抢焦点：点远端另一处后仍能继续输入", () => {
		vi.stubGlobal(
			"matchMedia",
			vi.fn((q: string) => ({
				matches: q === "(pointer: coarse)",
				addEventListener: () => {},
				removeEventListener: () => {},
			})),
		);
		const { threadId } = setup(READY);
		mount(threadId);
		act(() => last().h.onStatus("connected"));
		const s = last();
		expect(s.setFocusOnClick).toHaveBeenLastCalledWith(true);
		fireEvent.click(screen.getByTestId("desktop-keyboard"));
		const input = screen.getByTestId("desktop-kb-input");
		expect(document.activeElement).toBe(input);
		expect(s.setFocusOnClick).toHaveBeenLastCalledWith(false);
		fireEvent.touchStart(s.canvas);
		fireEvent.mouseDown(s.canvas);
		expect(document.activeElement).toBe(input);
		act(() => {
			input.dispatchEvent(
				new InputEvent("beforeinput", {
					inputType: "insertText",
					data: "a",
					cancelable: true,
					bubbles: true,
				}),
			);
		});
		expect(s.sendKey).toHaveBeenLastCalledWith(0x61, undefined);
		fireEvent.click(screen.getByTestId("desktop-keyboard"));
		expect(s.setFocusOnClick).toHaveBeenLastCalledWith(true);
		fireEvent.mouseDown(s.canvas);
		expect(document.activeElement).toBe(s.canvas);
	});
});

describe("DesktopPanel 剪贴板", () => {
	const clip = { readText: vi.fn(), writeText: vi.fn() };
	beforeEach(() => {
		clip.readText.mockReset().mockResolvedValue("本机密码");
		clip.writeText.mockReset().mockResolvedValue(undefined);
		Object.defineProperty(navigator, "clipboard", {
			configurable: true,
			value: clip,
		});
		vi.spyOn(document, "hasFocus").mockReturnValue(true);
	});

	function paste(el: HTMLElement, text: string) {
		const ev = new Event("paste", { bubbles: true, cancelable: true });
		Object.defineProperty(ev, "clipboardData", {
			value: { getData: (t: string) => (t === "text/plain" ? text : "") },
		});
		act(() => {
			el.dispatchEvent(ev);
		});
		return ev;
	}

	it("桌面剪贴板变化只显示在弹层里，不写本机剪贴板；窗口 / 舞台获得焦点不读本机剪贴板", async () => {
		const { threadId } = setup(READY);
		mount(threadId);
		act(() => last().h.onStatus("connected"));
		act(() => last().h.onClipboard("echo pwned | sh"));
		await act(async () => {
			window.dispatchEvent(new Event("focus"));
			screen.getByTestId("desktop-stage").focus();
		});
		expect(clip.writeText).not.toHaveBeenCalled();
		expect(clip.readText).not.toHaveBeenCalled();
		expect(last().paste).not.toHaveBeenCalled();
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("deskPanel.clip.title") }),
		);
		expect(screen.getByTestId("desktop-clip-remote")).toHaveTextContent(
			"echo pwned | sh",
		);
		await act(async () => {
			fireEvent.click(screen.getByTestId("desktop-clip-copy"));
		});
		expect(clip.writeText).toHaveBeenCalledWith("echo pwned | sh");
	});

	it("在桌面上按 Ctrl+V：不交给 noVNC（浏览器才会派发 paste），paste 事件把本机文本发到桌面并补发 V", () => {
		const { threadId } = setup(READY);
		mount(threadId);
		act(() => last().h.onStatus("connected"));
		const s = last();
		act(() => s.canvas.focus());
		const down = fireEvent.keyDown(s.canvas, {
			key: "v",
			code: "KeyV",
			ctrlKey: true,
		});
		expect(down).toBe(true);
		expect(s.novncKeys).toEqual([]);
		const ev = paste(s.canvas, "本机文本");
		expect(ev.defaultPrevented).toBe(true);
		expect(s.paste).toHaveBeenCalledWith("本机文本");
		expect(s.sendKey.mock.calls).toEqual([[0x76]]);
		expect(clip.readText).not.toHaveBeenCalled();
	});

	it("macOS ⌘+V：远端的 ⌘ 是 Alt，先松开再按 Ctrl+V", () => {
		const { threadId } = setup(READY);
		mount(threadId);
		act(() => last().h.onStatus("connected"));
		const s = last();
		fireEvent.keyDown(s.canvas, { key: "v", code: "KeyV", metaKey: true });
		paste(s.canvas, "x");
		expect(s.sendKey.mock.calls).toEqual([
			[0xffe9, false],
			[0xffe3, true],
			[0x76],
			[0xffe3, false],
		]);
	});

	it("仅观看时不截获 Ctrl+V，也不发送粘贴内容", () => {
		uiStore.setState({ desktopControl: false });
		const { threadId } = setup(READY);
		mount(threadId);
		act(() => last().h.onStatus("connected"));
		const s = last();
		fireEvent.keyDown(s.canvas, { key: "v", code: "KeyV", ctrlKey: true });
		expect(s.novncKeys).toEqual(["v"]);
		paste(s.canvas, "本机文本");
		expect(s.paste).not.toHaveBeenCalled();
	});
});
