/**
 * 首页新建对话框：role 胶囊里的思考强度按对象的模型列出；首屏默认对象与全局助手（M13 5.4：三个胶囊随对象收起 / 展开、
 * 托盘 `data-target`、助手发送新建助手对话、普通发送带 role、Ctrl+S 打开 role 胶囊）；系统分享预填。
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
import { MemoryRouter, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../i18n";
import type { Attachment } from "../lib/attachments";
import { shareIntakeStore } from "../state/share-intake";
import { resetThreadStores } from "../state/stores";
import { HomePage } from "./HomePage";

/** 中央模型目录里的一个对话模型。 */
const chat = (
	provider: string,
	id: string,
	name: string,
	thinking?: string[],
) => ({
	provider,
	id,
	selector: `${provider}/${id}`,
	name,
	kind: "chat",
	...(thinking ? { thinking } : {}),
});

/** 首屏偏好的默认对象（`GET settings/composer`）；null = 接口 404（按 default role 处理）。 */
let defaultTarget: "role" | "assistant" | null = null;
/** 收到的写请求：路径 + JSON 请求体。 */
let posts: { path: string; body: unknown }[] = [];
/** `GET providers` / `GET projects` 的响应。 */
let providerList: Record<string, unknown> = { providers: [] };
let projectList: unknown[] = [];

const THREAD = {
	id: "T-new",
	title: "",
	titleSource: "auto",
	state: "starting",
	stateChangedAt: 1,
	pinned: false,
	labels: [],
	unread: false,
	createdAt: 1,
	updatedAt: 1,
};

const fetchMock = vi.fn(
	async (input: RequestInfo | URL, init?: RequestInit) => {
		const req =
			input instanceof Request ? input : new Request(String(input), init);
		const url = new URL(req.url, "http://x");
		if (req.method === "POST") {
			const text = await req.text();
			posts.push({
				path: url.pathname,
				body: text ? JSON.parse(text) : undefined,
			});
			if (url.pathname === "/api/v1/assistant/conversations")
				return Response.json(
					{ thread: { ...THREAD, id: "T-asst", kind: "assistant" } },
					{ status: 201 },
				);
			if (url.pathname === "/api/v1/threads")
				return Response.json(
					{ thread: { ...THREAD, kind: "work" } },
					{ status: 201 },
				);
		}
		if (url.pathname === "/api/v1/providers")
			return Response.json(providerList);
		if (url.pathname === "/api/v1/projects")
			return Response.json({ projects: projectList });
		if (url.pathname === "/api/v1/orbs") return Response.json({ orbs: [] });
		if (url.pathname === "/api/v1/settings/composer" && defaultTarget)
			return Response.json({ defaultTarget });
		if (url.pathname === "/api/v1/models/catalog")
			return Response.json({
				models: [
					chat("openai", "gpt-x", "GPT X", ["low", "high"]),
					chat("openai", "gpt-lite", "GPT Lite", ["low"]),
					chat("acme", "plain", "Plain"),
				],
				fetchedAt: 0,
				stale: false,
			});
		return Response.json({ code: "not_found", message: "" }, { status: 404 });
	},
);

beforeEach(() => {
	defaultTarget = null;
	posts = [];
	providerList = { providers: [] };
	projectList = [];
	vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
	vi.unstubAllGlobals();
	resetThreadStores();
	shareIntakeStore.setState({ pending: null });
});

function Where() {
	return <span data-testid="where">{useLocation().pathname}</span>;
}

function renderHome() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<HomePage />
				<Where />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const capsule = () => screen.getByRole("button", { name: /^对象：/ });
const tray = () => document.querySelector(".home-tray") as HTMLElement;
const pills = () => screen.queryByTestId("home-pills");

/** 打开 role 胶囊（没开时），点「指定模型…」，在目录里点该模型（目录读到后才有这一行）。 */
async function pickModel(selector: string) {
	if (!screen.queryByTestId("role-capsule-popover")) fireEvent.click(capsule());
	fireEvent.click(screen.getByRole("button", { name: /^指定模型/ }));
	const row = await vi.waitFor(() => {
		const el = document.querySelector<HTMLElement>(
			`[data-model="${selector}"]`,
		);
		if (!el) throw new Error("模型列表未就绪");
		return el;
	});
	fireEvent.click(row);
}

/** 打开 role 胶囊，读思考强度的选项（没有这一节时为 null）。 */
function thinkingLevels(): (string | null)[] | null {
	if (!screen.queryByTestId("role-capsule-popover")) fireEvent.click(capsule());
	const group = screen.queryByRole("radiogroup", { name: "思考级别" });
	return group
		? within(group)
				.getAllByRole("radio")
				.map((r) => r.textContent)
		: null;
}

function prompt(): HTMLTextAreaElement {
	return screen.getByRole("textbox", {
		name: i18n.t("newThread.prompt"),
	}) as HTMLTextAreaElement;
}

describe("首页思考强度按对象的模型能力列出", () => {
	it("能力未知（role 没解析出模型）：只给「默认」，不假装全部级别可用", () => {
		renderHome();
		expect(thinkingLevels()).toEqual(["默认"]);
	});

	it("指定目录里的模型：列出它支持的级别（加自动）", async () => {
		renderHome();
		await pickModel("openai/gpt-x");
		expect(thinkingLevels()).toEqual(["默认", "自动", "低", "高"]);
	});

	it("模型不能调节思考：没有思考强度一节", async () => {
		renderHome();
		await pickModel("acme/plain");
		expect(thinkingLevels()).toBeNull();
	});

	it("换模型时退回「默认」", async () => {
		renderHome();
		await pickModel("openai/gpt-x");
		thinkingLevels();
		fireEvent.click(screen.getByRole("radio", { name: "高" }));
		expect(capsule()).toHaveTextContent("高");
		await pickModel("openai/gpt-lite");
		expect(capsule()).not.toHaveTextContent("高");
		expect(thinkingLevels()).toEqual(["默认", "自动", "低"]);
		expect(screen.getByRole("radio", { name: "默认" })).toHaveAttribute(
			"aria-checked",
			"true",
		);
	});

	it("首页没有单独的模型胶囊与思考级别圆圈", () => {
		renderHome();
		expect(screen.queryByRole("button", { name: "默认模型" })).toBeNull();
		expect(screen.queryByRole("button", { name: /思考级别：/ })).toBeNull();
	});
});

describe("首屏对象与全局助手（M13 5.4）", () => {
	it("默认对象为 assistant：执行位置、项目、Profile 胶囊不在 DOM 中，托盘 data-target=assistant", async () => {
		defaultTarget = "assistant";
		renderHome();
		await waitFor(() => expect(tray().dataset.target).toBe("assistant"));
		expect(pills()).toBeNull();
		expect(screen.queryByRole("button", { name: /^无项目/ })).toBeNull();
		expect(capsule()).toHaveAccessibleName("对象：全局助手");
	});

	it("切到 role 后三胶囊出现，data-target 随选择变化；切回助手后收起", async () => {
		vi.useFakeTimers({ shouldAdvanceTime: true });
		try {
			defaultTarget = "assistant";
			renderHome();
			await waitFor(() => expect(tray().dataset.target).toBe("assistant"));
			fireEvent.click(capsule());
			fireEvent.click(screen.getByRole("button", { name: /^快速/ }));
			expect(tray().dataset.target).toBe("role");
			expect(pills()).not.toBeNull();
			expect(pills()?.className).toContain("home-pills-in");
			expect(
				screen.getByRole("button", { name: "无项目" }),
			).toBeInTheDocument();

			fireEvent.click(capsule());
			fireEvent.click(screen.getByRole("button", { name: /^全局助手/ }));
			expect(tray().dataset.target).toBe("assistant");
			// leaving：先淡出，FADE_MS 后才离开 DOM
			expect(pills()?.className).toContain("home-pills-out");
			act(() => {
				vi.advanceTimersByTime(200);
			});
			expect(pills()).toBeNull();
		} finally {
			vi.useRealTimers();
		}
	});

	it("助手发送：POST assistant/conversations，请求体含 prompt，然后跳到 /assistant/:id", async () => {
		defaultTarget = "assistant";
		renderHome();
		await waitFor(() => expect(tray().dataset.target).toBe("assistant"));
		fireEvent.change(prompt(), { target: { value: "帮我整理下周计划" } });
		fireEvent.keyDown(prompt(), { key: "Enter" });
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent(
				"/assistant/T-asst",
			),
		);
		expect(posts).toEqual([
			{
				path: "/api/v1/assistant/conversations",
				body: { prompt: "帮我整理下周计划" },
			},
		]);
	});

	it("普通发送：新建线程带所选 role", async () => {
		renderHome();
		fireEvent.click(capsule());
		fireEvent.click(screen.getByRole("button", { name: /^思考/ }));
		fireEvent.change(prompt(), { target: { value: "修个 bug" } });
		fireEvent.keyDown(prompt(), { key: "Enter" });
		await waitFor(() => expect(posts).toHaveLength(1));
		expect(posts[0]).toMatchObject({
			path: "/api/v1/threads",
			body: { prompt: "修个 bug", role: "slow" },
		});
		expect(posts[0]?.body).not.toHaveProperty("model");
	});

	it("Ctrl+S 打开 role 胶囊，再按一次关上", () => {
		renderHome();
		fireEvent.keyDown(window, { key: "s", ctrlKey: true });
		expect(screen.getByTestId("role-capsule-popover")).toBeInTheDocument();
		expect(capsule()).toHaveAttribute("aria-expanded", "true");
		fireEvent.keyDown(window, { key: "s", ctrlKey: true });
		expect(screen.queryByTestId("role-capsule-popover")).toBeNull();
	});
});

describe("执行位置胶囊直接显示默认落点", () => {
	const provider = (name: string, types: string[], defaultType?: string) => ({
		id: `prov_${name}`,
		name,
		online: true,
		createdAt: 1,
		defaultType,
		orbTypes: types.map((n) => ({ name: n, cpus: 2, memoryMB: 2048 })),
	});
	const twoProviders = () => [
		provider("local", ["small", "medium"], "medium"),
		provider("remote", ["big"], "big"),
	];
	beforeEach(() => {
		providerList = {
			providers: twoProviders(),
			orbDefaults: { provider: "local" },
		};
		projectList = [
			{
				id: "prj_1",
				name: "oryn",
				shallowClone: false,
				defaultProvider: "remote",
				defaultOrbType: "big",
				createdAt: 1,
				updatedAt: 1,
			},
		];
	});
	const executorPill = () =>
		screen.findByRole("button", { name: /^(local|remote|新 orb)/ });
	async function expectFollowDefault() {
		fireEvent.change(prompt(), { target: { value: "跑一下" } });
		fireEvent.keyDown(prompt(), { key: "Enter" });
		await waitFor(() => expect(posts).toHaveLength(1));
		expect(posts[0]?.body).toHaveProperty("executor", { kind: "new_orb" });
	}

	it("没有项目：按 orb_defaults 的 provider 与它的 defaultType 显示，请求仍不带 provider", async () => {
		renderHome();
		await waitFor(async () =>
			expect(await executorPill()).toHaveAccessibleName("local · medium"),
		);
		await expectFollowDefault();
	});

	it("项目默认优先于 orb_defaults；选别的类型带上它，选回默认落点恢复跟随", async () => {
		renderHome();
		fireEvent.click(await screen.findByRole("button", { name: "无项目" }));
		fireEvent.click(await screen.findByRole("button", { name: /^oryn/ }));
		await waitFor(async () =>
			expect(await executorPill()).toHaveAccessibleName("remote · big"),
		);
		fireEvent.click(await executorPill());
		fireEvent.click(screen.getByRole("button", { name: /^local\s*small/ }));
		expect(await executorPill()).toHaveAccessibleName("local · small");
		fireEvent.click(await executorPill());
		fireEvent.click(screen.getByRole("button", { name: /^remote\s*big/ }));
		await expectFollowDefault();
	});

	it("多个 provider 又没有任何默认：只写「新 orb」", async () => {
		providerList = { providers: twoProviders() };
		renderHome();
		await screen.findByRole("button", { name: "无项目" });
		await act(async () => {});
		expect(await executorPill()).toHaveAccessibleName("新 orb");
	});
});

describe("系统分享预填新建线程框", () => {
	const shot: Attachment = {
		id: "att_s1",
		name: "shot.png",
		mime: "image/png",
		size: 2048,
		sha256: "0".repeat(64),
		createdAt: 0,
	};

	function prompt(): HTMLTextAreaElement {
		return screen.getByRole("textbox", {
			name: i18n.t("newThread.prompt"),
		}) as HTMLTextAreaElement;
	}

	it("待取的分享：文字进输入框、附件成为条目、导入失败给出提示，然后取走", () => {
		shareIntakeStore.getState().set({
			id: "s-1",
			text: "https://example.com/post",
			attachments: [shot],
			failed: [
				{ name: "big.mov", reason: "too_large" },
				{ name: "x.bin", reason: "io" },
			],
		});
		renderHome();
		expect(prompt().value).toBe("https://example.com/post");
		expect(screen.getByText("shot.png")).toBeInTheDocument();
		expect(screen.getByRole("alert")).toHaveTextContent(
			i18n.t("mobile.shareFailed", { count: 2 }),
		);
		expect(shareIntakeStore.getState().pending).toBeNull();
	});

	it("已有内容时换行追加；没有失败项不显示提示", () => {
		renderHome();
		fireEvent.change(prompt(), { target: { value: "看看这个" } });
		act(() => {
			shareIntakeStore.getState().set({
				id: "s-2",
				text: "分享的文字",
				attachments: [],
				failed: [],
			});
		});
		expect(prompt().value).toBe("看看这个\n分享的文字");
		expect(screen.queryByRole("alert")).toBeNull();
		expect(shareIntakeStore.getState().pending).toBeNull();
	});
});
