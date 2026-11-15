/**
 * 设置 → 身份与头像（M9.6 / M13）：各卡的保存语义（整体替换、名字与头像互相带上）、全局助手不跟随 Agent 身份与底栏预览、
 * 角色头像链接卡、新对话默认对象、上传入口都先经裁剪对话框。fetch 换成按路径记账的桩，图片解码与编码换成桩。
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
import type { Schemas } from "../../api/client";
import { identityKeys } from "../../api/identity-queries";
import { loadAvatarSource, renderAvatar } from "../../lib/avatar-image";
import { IdentitySettings } from "./IdentitySettings";

vi.mock("../../lib/avatar-image", async (orig) => ({
	...(await orig<object>()),
	loadAvatarSource: vi.fn(),
	renderAvatar: vi.fn(),
}));

type AgentIdentity = Schemas["AgentIdentity"];

interface Write {
	method: string;
	path: string;
	body: unknown;
}

let agent: AgentIdentity;
let assistant: AgentIdentity;
let user: Schemas["AvatarIdentity"];
let composer: Schemas["ComposerPrefs"];
let writes: Write[];
/** 非空时，下一个 PUT 记账后先等它放行（挂住第一次写入）。 */
let holdNextPut: Promise<void> | null;

const CROPPED = "data:image/webp;base64,CROPPED";

async function handle(req: Request): Promise<Response> {
	const path = new URL(req.url, "http://x").pathname;
	const text = req.method === "GET" ? "" : await req.text();
	const body = text ? JSON.parse(text) : undefined;
	if (req.method !== "GET") writes.push({ method: req.method, path, body });
	if (req.method === "PUT" && holdNextPut) {
		const gate = holdNextPut;
		holdNextPut = null;
		await gate;
	}
	const ident = /^\/api\/v1\/settings\/(agent|assistant|user)-identity$/.exec(
		path,
	);
	if (ident) {
		const who = ident[1];
		if (req.method === "PUT") {
			if ((body?.name ?? "").length > 40)
				return Response.json(
					{ code: "invalid_request", message: "名字太长" },
					{ status: 400 },
				);
			if (who === "agent") agent = body;
			else if (who === "assistant") assistant = body;
			else user = body;
		}
		return Response.json(
			who === "agent" ? agent : who === "assistant" ? assistant : user,
		);
	}
	if (path === "/api/v1/settings/composer") {
		if (req.method === "PUT") composer = body;
		return Response.json(composer);
	}
	if (path === "/api/v1/settings/git-identity") return Response.json({});
	return Response.json({ code: "not_found", message: path }, { status: 404 });
}

let client: QueryClient;

function mount() {
	client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<IdentitySettings />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const card = (id: "agent" | "assistant" | "new-thread" | "roles" | "user") =>
	screen.getByTestId(`identity-${id}`);

const puts = () => writes.filter((w) => w.method === "PUT");

beforeEach(() => {
	agent = {};
	assistant = {};
	user = {};
	composer = { defaultTarget: "role" };
	writes = [];
	holdNextPut = null;
	vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) =>
		handle(
			input instanceof Request
				? input
				: new Request(new URL(String(input), "http://x"), init),
		),
	);
	vi.mocked(loadAvatarSource).mockReset();
	vi.mocked(loadAvatarSource).mockImplementation(async () => ({
		image: {} as CanvasImageSource,
		width: 400,
		height: 200,
		url: "blob:avatar",
		release: vi.fn(),
	}));
	vi.mocked(renderAvatar).mockReset().mockReturnValue(CROPPED);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

function pickFile(scope: HTMLElement) {
	const input = scope.querySelector<HTMLInputElement>('input[type="file"]');
	if (!input) throw new Error("没有上传入口");
	fireEvent.change(input, {
		target: { files: [new File(["x"], "a.png", { type: "image/png" })] },
	});
}

describe("Agent 身份", () => {
	it("改名字：PUT 体同时带当前头像；回车只保存一次", async () => {
		agent = { avatarIcon: "🦊" };
		mount();
		const name = await within(card("agent")).findByLabelText("名字");
		await waitFor(() => expect(name).toBeEnabled());
		fireEvent.change(name, { target: { value: "  Ling " } });
		fireEvent.keyDown(name, { key: "Enter" });
		fireEvent.blur(name);
		await waitFor(() => expect(puts()).toHaveLength(1));
		expect(puts()[0]).toEqual({
			method: "PUT",
			path: "/api/v1/settings/agent-identity",
			body: { name: "Ling", avatarIcon: "🦊" },
		});
		await waitFor(() => expect(name).toHaveValue("Ling"));
	});

	it("换头像：PUT 体同时带当前名字", async () => {
		agent = { name: "Ling" };
		mount();
		const scope = card("agent");
		await waitFor(() =>
			expect(within(scope).getByLabelText("名字")).toHaveValue("Ling"),
		);
		fireEvent.click(within(scope).getByRole("button", { name: "🐱" }));
		await waitFor(() => expect(puts()).toHaveLength(1));
		expect(puts()[0]?.body).toEqual({ name: "Ling", avatarIcon: "🐱" });
	});

	it("清空名字：PUT 体没有 name，头像保留", async () => {
		agent = { name: "Ling", avatarIcon: "🦊" };
		mount();
		const name = within(card("agent")).getByLabelText("名字");
		await waitFor(() => expect(name).toHaveValue("Ling"));
		fireEvent.change(name, { target: { value: "   " } });
		fireEvent.blur(name);
		await waitFor(() => expect(puts()).toHaveLength(1));
		expect(puts()[0]?.body).toEqual({ avatarIcon: "🦊" });
	});

	it("名字没变时失焦不保存", async () => {
		agent = { name: "Ling" };
		mount();
		const name = within(card("agent")).getByLabelText("名字");
		await waitFor(() => expect(name).toHaveValue("Ling"));
		fireEvent.change(name, { target: { value: "Ling " } });
		fireEvent.blur(name);
		await act(async () => {});
		expect(puts()).toEqual([]);
	});

	it("服务端回 400：显示错误文案，缓存不变", async () => {
		agent = { name: "Ling", avatarIcon: "🦊" };
		mount();
		const name = within(card("agent")).getByLabelText("名字");
		await waitFor(() => expect(name).toHaveValue("Ling"));
		fireEvent.change(name, { target: { value: "x".repeat(41) } });
		fireEvent.blur(name);
		expect(await within(card("agent")).findByRole("alert")).toHaveTextContent(
			"名字太长",
		);
		expect(client.getQueryData(identityKeys.agent)).toEqual({
			name: "Ling",
			avatarIcon: "🦊",
		});
	});
});

describe("同一张卡连续编辑（写入排队、按最新身份合并）", () => {
	it.each(["agent", "assistant"] as const)(
		"%s：换头像的 PUT 未完成时提交名字——名字等它完成后再发并带上新头像，最终两者都保留",
		async (who) => {
			const initial = { name: "A", avatarIcon: "🦊" };
			if (who === "agent") agent = initial;
			else assistant = initial;
			let release = () => {};
			holdNextPut = new Promise<void>((r) => {
				release = r;
			});
			mount();
			const scope = card(who);
			const name = within(scope).getByLabelText("名字");
			await waitFor(() => expect(name).toHaveValue("A"));
			fireEvent.click(within(scope).getByRole("button", { name: "🐱" }));
			await waitFor(() => expect(puts()).toHaveLength(1));
			expect(puts()[0]?.body).toEqual({ name: "A", avatarIcon: "🐱" });
			// 名字输入不锁：用户的改动不丢
			expect(name).toBeEnabled();
			fireEvent.change(name, { target: { value: "B" } });
			fireEvent.blur(name);
			await act(async () => {});
			expect(puts()).toHaveLength(1);
			await act(async () => release());
			await waitFor(() => expect(puts()).toHaveLength(2));
			expect(puts()[1]?.body).toEqual({ name: "B", avatarIcon: "🐱" });
			const final = { name: "B", avatarIcon: "🐱" };
			await waitFor(() =>
				expect(client.getQueryData(identityKeys[who])).toEqual(final),
			);
			expect(who === "agent" ? agent : assistant).toEqual(final);
		},
	);
});

describe("全局助手", () => {
	it("未设置时是「全局助手」+ sparkle，不跟随 Agent 身份；单独设置后两处预览都换", async () => {
		agent = { name: "Ling", avatarIcon: "🦊" };
		mount();
		const scope = card("assistant");
		const tab = within(scope).getByTestId("assistant-tab-preview");
		// 等助手身份加载完（名字输入可用）、agent 身份也到了，再断言没有跟随 agent
		await waitFor(() =>
			expect(within(scope).getByLabelText("名字")).toBeEnabled(),
		);
		await waitFor(() =>
			expect(within(card("agent")).getByLabelText("名字")).toHaveValue("Ling"),
		);
		expect(tab).toHaveTextContent("全局助手");
		expect(tab).not.toHaveTextContent("Ling");
		expect(tab).not.toHaveTextContent("🦊");
		const small = tab.querySelector('[data-avatar-shape="circle"]');
		expect(small).toHaveAttribute("data-avatar", "default");
		expect(small?.querySelector("svg")).not.toBeNull();
		expect(
			within(scope).getByText("默认 sparkle 标记", { selector: "p" }),
		).toBeInTheDocument();
		const big = scope.querySelector(
			'[data-avatar-of="assistant"][data-avatar-shape="tile"]',
		);
		expect(big).not.toHaveTextContent("🦊");
		// 名字输入框的占位是「全局助手」，不是 agent 的名字
		expect(within(scope).getByLabelText("名字")).toHaveAttribute(
			"placeholder",
			"全局助手",
		);

		fireEvent.click(within(scope).getByRole("button", { name: "🐱" }));
		await waitFor(() => expect(tab).toHaveTextContent("🐱"));
		expect(big).toHaveTextContent("🐱");
		expect(tab).toHaveTextContent("全局助手");
		expect(puts().map((w) => [w.path, w.body])).toEqual([
			["/api/v1/settings/assistant-identity", { avatarIcon: "🐱" }],
		]);
	});

	it("「恢复默认」清除自己的头像但保留名字", async () => {
		assistant = { name: "小助", avatarIcon: "🤖" };
		mount();
		const scope = card("assistant");
		const tab = within(scope).getByTestId("assistant-tab-preview");
		await waitFor(() => expect(tab).toHaveTextContent("小助"));
		fireEvent.click(within(scope).getByRole("button", { name: "恢复默认" }));
		await waitFor(() => expect(puts()).toHaveLength(1));
		expect(puts()[0]?.body).toEqual({ name: "小助" });
	});

	it("页面上没有「跟随 Agent 身份」", async () => {
		const { container } = mount();
		await waitFor(() =>
			expect(within(card("assistant")).getByLabelText("名字")).toBeEnabled(),
		);
		expect(container).not.toHaveTextContent("跟随 Agent 身份");
	});
});

describe("默认 Agent 身份与角色头像入口", () => {
	it("标题是「默认 Agent 身份」；角色头像只剩链接到 Agent 配置的卡片", () => {
		mount();
		expect(screen.getByText("默认 Agent 身份")).toBeInTheDocument();
		const scope = card("roles");
		expect(scope.querySelector("li")).toBeNull();
		expect(
			within(scope).getByRole("link", { name: "前往 Agent 配置" }),
		).toHaveAttribute("href", "/settings/agent-config");
		expect(writes.some((w) => w.path.includes("role-identities"))).toBe(false);
	});
});

describe("新对话默认对象", () => {
	it("按服务端的偏好选中；改选写回 putComposerPrefs，再改回也写回", async () => {
		mount();
		const scope = card("new-thread");
		const role = within(scope).getByRole("radio", {
			name: /Agent（default role）/,
		});
		const helper = within(scope).getByRole("radio", { name: /全局助手/ });
		await waitFor(() => expect(role).toBeChecked());
		expect(helper).not.toBeChecked();
		await waitFor(() => expect(helper).toBeEnabled());

		fireEvent.click(helper);
		await waitFor(() => expect(helper).toBeChecked());
		expect(composer).toEqual({ defaultTarget: "assistant" });
		expect(client.getQueryData(identityKeys.composer)).toEqual({
			defaultTarget: "assistant",
		});

		await waitFor(() => expect(role).toBeEnabled());
		fireEvent.click(role);
		await waitFor(() => expect(role).toBeChecked());
		expect(puts().map((w) => [w.path, w.body])).toEqual([
			["/api/v1/settings/composer", { defaultTarget: "assistant" }],
			["/api/v1/settings/composer", { defaultTarget: "role" }],
		]);
	});

	it("偏好是全局助手时选中全局助手", async () => {
		composer = { defaultTarget: "assistant" };
		mount();
		const scope = card("new-thread");
		await waitFor(() =>
			expect(
				within(scope).getByRole("radio", { name: /全局助手/ }),
			).toBeChecked(),
		);
	});
});

describe("上传入口都先经裁剪对话框", () => {
	const cases: [string, () => HTMLElement, string, Record<string, unknown>][] =
		[
			[
				"Agent",
				() => card("agent"),
				"/api/v1/settings/agent-identity",
				{ avatarImage: CROPPED },
			],
			[
				"全局助手",
				() => card("assistant"),
				"/api/v1/settings/assistant-identity",
				{ avatarImage: CROPPED },
			],
			[
				"我的头像",
				() => card("user"),
				"/api/v1/settings/user-identity",
				{ avatarImage: CROPPED },
			],
		];

	it.each(cases)(
		"%s：选文件后出现裁剪对话框，确认前没有 PUT；确认后才保存裁剪结果",
		async (_, scope, path, body) => {
			mount();
			await waitFor(() =>
				expect(scope().querySelector('input[type="file"]')).not.toBeDisabled(),
			);
			pickFile(scope());
			const dialog = await screen.findByRole("dialog", { name: "裁剪头像" });
			expect(within(dialog).getByTestId("crop-frame")).toBeInTheDocument();
			expect(puts()).toEqual([]);
			fireEvent.click(within(dialog).getByRole("button", { name: "使用" }));
			await waitFor(() => expect(puts()).toHaveLength(1));
			expect(puts()[0]).toEqual({ method: "PUT", path, body });
			expect(screen.queryByRole("dialog")).toBeNull();
		},
	);

	it("取消：没有 PUT", async () => {
		mount();
		await waitFor(() =>
			expect(
				card("agent").querySelector('input[type="file"]'),
			).not.toBeDisabled(),
		);
		pickFile(card("agent"));
		const dialog = await screen.findByRole("dialog", { name: "裁剪头像" });
		fireEvent.click(within(dialog).getByRole("button", { name: "取消" }));
		expect(screen.queryByRole("dialog")).toBeNull();
		await act(async () => {});
		expect(puts()).toEqual([]);
	});

	it("解码失败：显示解码失败，不出现裁剪对话框", async () => {
		vi.mocked(loadAvatarSource).mockRejectedValueOnce(new Error("bad"));
		mount();
		await waitFor(() =>
			expect(
				card("agent").querySelector('input[type="file"]'),
			).not.toBeDisabled(),
		);
		pickFile(card("agent"));
		expect(
			await within(card("agent")).findByText(
				"无法读取这张图片，请使用 PNG、WebP 或 JPEG。",
			),
		).toBeInTheDocument();
		expect(screen.queryByRole("dialog")).toBeNull();
		expect(puts()).toEqual([]);
	});
});
