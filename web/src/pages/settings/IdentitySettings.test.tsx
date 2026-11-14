/**
 * 设置 → 身份与头像（M9.6）：四张卡的保存语义（整体替换、名字与头像互相带上）、助手的回落与底栏预览、
 * 角色头像列表与遗留项、四个上传入口都先经裁剪对话框。fetch 换成按路径记账的桩，图片解码与编码换成桩。
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
let cycle: string[];
let roleAvatars: Record<string, Schemas["AvatarIdentity"]>;
let selectors: Record<string, string>;
let configError: string | undefined;
let writes: Write[];

function roleOf(role: string): Schemas["RoleIdentity"] {
	return {
		role,
		inCycle: cycle.includes(role),
		...(selectors[role] ? { selector: selectors[role] } : {}),
		...roleAvatars[role],
	};
}

const CROPPED = "data:image/webp;base64,CROPPED";

async function handle(req: Request): Promise<Response> {
	const path = new URL(req.url, "http://x").pathname;
	const text = req.method === "GET" ? "" : await req.text();
	const body = text ? JSON.parse(text) : undefined;
	if (req.method !== "GET") writes.push({ method: req.method, path, body });
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
	if (path === "/api/v1/settings/role-identities") {
		if (req.method === "PUT") {
			const { role, ...avatar } = body as Schemas["RoleIdentityInput"];
			if (avatar.avatarIcon || avatar.avatarImage) roleAvatars[role] = avatar;
			else delete roleAvatars[role];
			return Response.json(roleOf(role));
		}
		const orphans = Object.keys(roleAvatars)
			.filter((r) => !cycle.includes(r))
			.sort();
		return Response.json({
			roles: [...cycle, ...orphans].map(roleOf),
			...(configError ? { configError } : {}),
		});
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

const card = (id: "agent" | "assistant" | "roles" | "user") =>
	screen.getByTestId(`identity-${id}`);

const puts = () => writes.filter((w) => w.method === "PUT");

beforeEach(() => {
	agent = {};
	assistant = {};
	user = {};
	cycle = ["Hyacinthia", "Mare", "Cyrene"];
	roleAvatars = {};
	selectors = { Mare: "openai/gpt-6.1-sol:high", Cyrene: "@Hyacinthia" };
	configError = undefined;
	writes = [];
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

describe("全局助手", () => {
	it("未设置时显示「跟随 Agent 身份」、预览用 agent 的头像与名字；单独设置后两处预览都换", async () => {
		agent = { name: "Ling", avatarIcon: "🦊" };
		mount();
		const scope = card("assistant");
		const tab = within(scope).getByTestId("assistant-tab-preview");
		await waitFor(() => expect(tab).toHaveTextContent("🦊"));
		expect(tab).toHaveTextContent("Ling");
		expect(tab.querySelector('[data-avatar-shape="circle"]')).toHaveTextContent(
			"🦊",
		);
		expect(
			within(scope).getByText("跟随 Agent 身份", { selector: "p" }),
		).toBeInTheDocument();
		const big = scope.querySelector(
			'[data-avatar-of="assistant"][data-avatar-shape="tile"]',
		);
		expect(big).toHaveTextContent("🦊");
		// 名字输入框的占位是 agent 的名字
		expect(within(scope).getByLabelText("名字")).toHaveAttribute(
			"placeholder",
			"Ling",
		);

		fireEvent.click(within(scope).getByRole("button", { name: "🐱" }));
		await waitFor(() => expect(tab).toHaveTextContent("🐱"));
		expect(big).toHaveTextContent("🐱");
		expect(tab).toHaveTextContent("Ling");
		expect(puts().map((w) => [w.path, w.body])).toEqual([
			["/api/v1/settings/assistant-identity", { avatarIcon: "🐱" }],
		]);
		expect(
			within(scope).queryByText("跟随 Agent 身份", { selector: "p" }),
		).toBeNull();
	});

	it("都没设置时底栏预览写「全局助手」；「跟随 Agent 身份」按钮清除自己的头像但保留名字", async () => {
		assistant = { name: "小助", avatarIcon: "🤖" };
		mount();
		const scope = card("assistant");
		const tab = within(scope).getByTestId("assistant-tab-preview");
		await waitFor(() => expect(tab).toHaveTextContent("小助"));
		fireEvent.click(
			within(scope).getByRole("button", { name: "跟随 Agent 身份" }),
		);
		await waitFor(() => expect(puts()).toHaveLength(1));
		expect(puts()[0]?.body).toEqual({ name: "小助" });
	});
});

describe("角色头像", () => {
	it("按 cycleOrder 列出，小字是 selector 或「未配置模型」；设置 / 移除各发一次 PUT", async () => {
		mount();
		const scope = card("roles");
		await waitFor(() =>
			expect(scope.querySelectorAll("li[data-role]")).toHaveLength(3),
		);
		expect(
			[...scope.querySelectorAll("li[data-role]")].map((li) =>
				li.getAttribute("data-role"),
			),
		).toEqual(["Hyacinthia", "Mare", "Cyrene"]);
		const mare = scope.querySelector('li[data-role="Mare"]') as HTMLElement;
		expect(mare).toHaveTextContent("openai/gpt-6.1-sol:high");
		expect(mare).toHaveTextContent("未设置头像");
		expect(scope.querySelector('li[data-role="Hyacinthia"]')).toHaveTextContent(
			"未配置模型",
		);

		const toggle = within(mare).getByRole("button", { name: "设置头像" });
		expect(toggle).toHaveAttribute("aria-expanded", "false");
		fireEvent.click(toggle);
		expect(toggle).toHaveAttribute("aria-expanded", "true");
		fireEvent.click(within(mare).getByRole("button", { name: "🌙" }));
		await waitFor(() => expect(mare).not.toHaveTextContent("未设置头像"));
		expect(
			mare.querySelector('[data-avatar-of="role"][data-avatar="icon"]'),
		).toHaveTextContent("🌙");
		fireEvent.click(within(mare).getByRole("button", { name: "移除头像" }));
		await waitFor(() => expect(mare).toHaveTextContent("未设置头像"));
		expect(puts().map((w) => w.body)).toEqual([
			{ role: "Mare", avatarIcon: "🌙" },
			{ role: "Mare" },
		]);
	});

	it("遗留项另起一组，只有「移除头像」；移除后从列表消失", async () => {
		roleAvatars = { Old: { avatarIcon: "🐙" } };
		mount();
		const scope = card("roles");
		const old = (await waitFor(() => {
			const el = scope.querySelector('li[data-role="Old"]');
			if (!el) throw new Error("没有遗留项");
			return el;
		})) as HTMLElement;
		expect(within(scope).getByText("不在 cycleOrder 里")).toBeInTheDocument();
		expect(within(old).queryByRole("button", { name: "设置头像" })).toBeNull();
		fireEvent.click(within(old).getByRole("button", { name: "移除头像" }));
		await waitFor(() =>
			expect(scope.querySelector('li[data-role="Old"]')).toBeNull(),
		);
		expect(puts().map((w) => w.body)).toEqual([{ role: "Old" }]);
	});

	it("config.yml 解析失败时有提示；底部链接到 Agent 配置", async () => {
		configError = "yaml: line 3";
		mount();
		const scope = card("roles");
		expect(await within(scope).findByRole("status")).toHaveTextContent(
			"config.yml 解析失败，暂按默认 cycleOrder 列出：yaml: line 3",
		);
		expect(
			within(scope).getByRole("link", { name: "编辑 config.yml" }),
		).toHaveAttribute("href", "/settings/agent-config");
	});
});

describe("上传入口都先经裁剪对话框", () => {
	const cases: [
		string,
		() => HTMLElement,
		string,
		Record<string, unknown>,
		(() => Promise<void>)?,
	][] = [
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
			"role",
			() => card("roles").querySelector('li[data-role="Mare"]') as HTMLElement,
			"/api/v1/settings/role-identities",
			{ role: "Mare", avatarImage: CROPPED },
			async () => {
				const mare = await waitFor(() => {
					const el = card("roles").querySelector('li[data-role="Mare"]');
					if (!el) throw new Error("role 列表还没到");
					return el as HTMLElement;
				});
				fireEvent.click(within(mare).getByRole("button", { name: "设置头像" }));
			},
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
		async (_, scope, path, body, prepare) => {
			mount();
			await prepare?.();
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
