/**
 * 首页新建对话框：role 胶囊里的思考强度按对象的模型列出；首屏默认对象与全局助手（M13 5.4：三个胶囊随对象收起 / 展开、
 * 舞台 `data-target`、助手发送新建助手对话、普通发送带 role、Ctrl+S 打开 role 胶囊、Ctrl+P 轮换 role）；系统分享预填。
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

/**
 * 中央模型目录里的一个对话模型。`thinkingDefault`：档位 = 模型自带默认档位，null = 已知没有。
 */
const chat = (
	provider: string,
	id: string,
	name: string,
	thinking?: string[],
	thinkingDefault: string | null = null,
) => ({
	provider,
	id,
	selector: `${provider}/${id}`,
	name,
	kind: "chat",
	...(thinking ? { thinking } : {}),
	thinkingDefault: thinkingDefault ?? "",
});

/** 首屏偏好的默认对象（`GET settings/composer`）；null = 接口 404（按 default role 处理）。 */
let defaultTarget: "role" | "assistant" | null = null;
/** 收到的写请求：路径 + JSON 请求体。 */
let posts: { path: string; body: unknown }[] = [];
/** `GET providers` / `GET projects` 的响应。 */
let providerList: Record<string, unknown> = { providers: [] };
let projectList: unknown[] = [];
const IDENTITY_ROLES = [
	{ role: "smol", inCycle: true },
	{ role: "default", inCycle: true, selector: "openai/gpt-x" },
	{ role: "slow", inCycle: true },
];
/** `GET settings/role-identities` 的 role 列表。 */
let identityRoles: unknown[] = IDENTITY_ROLES;

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
		if (url.pathname === "/api/v1/settings/role-identities")
			return Response.json({
				roles: identityRoles,
				defaultThinkingLevel: "high",
			});
		if (url.pathname === "/api/v1/models/catalog")
			return Response.json({
				models: [
					chat("openai", "gpt-x", "GPT X", ["low", "high"]),
					chat("openai", "gpt-lite", "GPT Lite", ["low"]),
					chat("acme", "plain", "Plain"),
					// 模型自带默认档位 medium：切模型时沿用起始档位，自带默认不覆盖
					chat(
						"factory-droid",
						"claude-opus-5-5",
						"Claude Opus 5.5",
						["low", "medium", "high"],
						"medium",
					),
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
	identityRoles = IDENTITY_ROLES;
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
const stage = () => document.querySelector(".home-stage") as HTMLElement;
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

/** 思考强度里选中的那一档（没有选中项时 undefined）。 */
const checkedLevel = () =>
	screen
		.getAllByRole("radio")
		.find((r) => r.getAttribute("aria-checked") === "true")?.textContent;

/** 胶囊右下角思考级别角标画的级别（没有角标时 undefined；弹层里 role 行的级别图形不算）。 */
const cornerLevel = () =>
	document
		.querySelector(".home-tray [role=img][data-level].absolute")
		?.getAttribute("data-level") ?? undefined;

function prompt(): HTMLTextAreaElement {
	return screen.getByRole("textbox", {
		name: i18n.t("newThread.prompt"),
	}) as HTMLTextAreaElement;
}

describe("首页思考强度按对象的模型能力列出", () => {
	it("default role：选中实际生效的那一档（gpt-x 没有自带默认，起始为配置的 high），不写「默认」", async () => {
		renderHome();
		await waitFor(() => expect(thinkingLevels()).toEqual(["自动", "低", "高"]));
		expect(checkedLevel()).toBe("高");
	});

	it("切模型沿用起始档位（Core 19）：支持 high、自带 medium 时仍是 high，自带默认档位不覆盖", async () => {
		renderHome();
		await pickModel("factory-droid/claude-opus-5-5");
		expect(thinkingLevels()).toEqual(["自动", "低", "中", "高"]);
		expect(checkedLevel()).toBe("高");
		expect(cornerLevel()).toBe("high");
	});

	it("起始档位按目标模型收窄：只支持 low 时选中低", async () => {
		renderHome();
		await pickModel("openai/gpt-lite");
		expect(thinkingLevels()).toEqual(["自动", "低"]);
		expect(checkedLevel()).toBe("低");
		expect(cornerLevel()).toBe("low");
	});

	it("指定目录里的模型：列出它支持的级别（加自动），选中实际生效的那一档", async () => {
		renderHome();
		await pickModel("openai/gpt-x");
		expect(thinkingLevels()).toEqual(["自动", "低", "高"]);
		expect(checkedLevel()).toBe("高");
		expect(cornerLevel()).toBe("high");
	});

	it("模型不能调节思考：没有思考强度一节，也没有角标", async () => {
		renderHome();
		await pickModel("acme/plain");
		expect(thinkingLevels()).toBeNull();
		expect(cornerLevel()).toBeUndefined();
	});

	it("选别的档带上 thinking 并画成角标；换模型后回到新模型上实际生效的级别", async () => {
		renderHome();
		await pickModel("openai/gpt-x");
		thinkingLevels();
		fireEvent.click(screen.getByRole("radio", { name: "低" }));
		expect(cornerLevel()).toBe("low");
		expect(capsule()).not.toHaveTextContent("低");
		// gpt-lite 只支持 low：配置的 high 收窄到 low
		await pickModel("openai/gpt-lite");
		expect(thinkingLevels()).toEqual(["自动", "低"]);
		expect(checkedLevel()).toBe("低");
	});

	it("选回实际生效的那一档：请求不带 thinkingLevel", async () => {
		renderHome();
		await pickModel("openai/gpt-x");
		thinkingLevels();
		fireEvent.click(screen.getByRole("radio", { name: "低" }));
		fireEvent.click(screen.getByRole("radio", { name: "高" }));
		fireEvent.change(prompt(), { target: { value: "跑一下" } });
		fireEvent.keyDown(prompt(), { key: "Enter" });
		await waitFor(() => expect(posts).toHaveLength(1));
		expect(posts[0]?.body).not.toHaveProperty("thinkingLevel");
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
		await waitFor(() => expect(stage().dataset.target).toBe("assistant"));
		expect(pills()).toBeNull();
		expect(screen.queryByRole("button", { name: /^无项目/ })).toBeNull();
		expect(capsule()).toHaveAccessibleName("对象：全局助手");
	});

	it("切到 role 后三胶囊出现，data-target 随选择变化；切回助手后收起", async () => {
		vi.useFakeTimers({ shouldAdvanceTime: true });
		try {
			defaultTarget = "assistant";
			renderHome();
			await waitFor(() => expect(stage().dataset.target).toBe("assistant"));
			fireEvent.click(capsule());
			fireEvent.click(screen.getByRole("button", { name: /^快速/ }));
			expect(stage().dataset.target).toBe("role");
			expect(pills()).not.toBeNull();
			expect(pills()?.className).toContain("home-pills-in");
			expect(
				screen.getByRole("button", { name: "无项目" }),
			).toBeInTheDocument();

			fireEvent.click(capsule());
			fireEvent.click(screen.getByRole("button", { name: /^全局助手/ }));
			expect(stage().dataset.target).toBe("assistant");
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
		await waitFor(() => expect(stage().dataset.target).toBe("assistant"));
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

	it("Ctrl+P 在 cycleOrder 的 role 间轮换（跳过没配置模型的），浮出角色轨，并屏蔽打印", async () => {
		identityRoles = [
			{ role: "smol", inCycle: true, selector: "openai/gpt-lite" },
			{ role: "default", inCycle: true, selector: "openai/gpt-x" },
			{ role: "slow", inCycle: true },
		];
		defaultTarget = "assistant";
		renderHome();
		await waitFor(() => expect(stage().dataset.target).toBe("assistant"));
		const press = (shiftKey = false) => {
			const ev = new KeyboardEvent("keydown", {
				key: "p",
				ctrlKey: true,
				shiftKey,
				bubbles: true,
				cancelable: true,
			});
			act(() => {
				window.dispatchEvent(ev);
			});
			return ev;
		};
		// 全局助手不在轨道里：从第一个可用 role 开始
		await waitFor(() => {
			expect(press().defaultPrevented).toBe(true);
			expect(capsule()).toHaveAccessibleName("对象：快速");
		});
		const track = screen.getByRole("radiogroup", { name: "角色模型" });
		expect(within(track).getByRole("radio", { name: "快速" })).toHaveAttribute(
			"aria-checked",
			"true",
		);
		expect(within(track).getByRole("radio", { name: "思考" })).toHaveAttribute(
			"aria-disabled",
			"true",
		);
		// 没配置模型的「思考」被跳过：快速 → 默认 → 快速
		press();
		expect(stage().dataset.target).toBe("role");
		expect(within(track).getByRole("radio", { name: "默认" })).toHaveAttribute(
			"aria-checked",
			"true",
		);
		press(true);
		expect(capsule()).toHaveAccessibleName("对象：快速");

		fireEvent.change(prompt(), { target: { value: "修个 bug" } });
		fireEvent.keyDown(prompt(), { key: "Enter" });
		await waitFor(() => expect(posts).toHaveLength(1));
		expect(posts[0]).toMatchObject({
			path: "/api/v1/threads",
			body: { prompt: "修个 bug", role: "smol" },
		});
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

describe("首页输入框中文输入法回车确认不误发送", () => {
	it("组字期间及 Safari 补发的 keyCode=229 Enter 不触发新建发送，确认后再次 Enter 才发送", async () => {
		renderHome();
		const el = screen.getByRole("textbox", {
			name: i18n.t("newThread.prompt"),
		}) as HTMLTextAreaElement;

		// 场景 1：compositionstart 与 compositionend 之间（即使 isComposing=false）
		fireEvent.compositionStart(el);
		fireEvent.change(el, { target: { value: "hello" } });
		fireEvent.keyDown(el, { key: "Enter", keyCode: 13, isComposing: false });
		await act(async () => {
			await Promise.resolve();
		});
		expect(posts).toHaveLength(0);

		// 场景 2：Safari/WKWebView 在 compositionend 后补发 keyCode=229
		fireEvent.compositionEnd(el, { data: "hello" });
		fireEvent.keyDown(el, { key: "Enter", keyCode: 229, isComposing: false });
		await act(async () => {
			await Promise.resolve();
		});
		expect(posts).toHaveLength(0);

		// 确认完成后再次按 Enter 应正常发送
		fireEvent.keyDown(el, { key: "Enter", keyCode: 13 });
		await waitFor(() => {
			expect(posts.some((p) => p.path === "/api/v1/threads")).toBe(true);
		});
	});
});
