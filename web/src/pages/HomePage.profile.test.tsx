/**
 * 新建线程框的浏览器 Profile 胶囊（M8 4.6）：只在新 orb 且有 Profile 时出现；三种请求体
 * （跟随项目默认 = 不带字段、不使用 = 空串、指定 = id）；409 `profile_in_use` 弹对话框，选「接管」后带 profileMode 重发，
 * 重发钉住对话框展示的 Profile（期间项目默认被改也不变）；Ctrl+B 在输入框里打开弹层。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../api/client";
import i18n from "../i18n";
import { resetThreadStores } from "../state/stores";
import { HomePage } from "./HomePage";

const WORK = "bpf_01JCZ8M3W4X5Y6Z7A8B9C0DWRK";
const GH = "bpf_01JCZ8M3W4X5Y6Z7A8B9C0DGHB";
const HOLDER: Schemas["BrowserProfileHolder"] = {
	orbId: "orb-holder000000000000000001",
	threadIds: ["T-holder"],
	threadId: "T-holder",
	threadTitle: "比价下单",
	since: 1_700_000_000_000,
};

let profiles: Schemas["BrowserProfile"][];
let posts: Schemas["CreateThreadRequest"][];
let conflictOnce: boolean;
/** 服务端此刻的项目默认 Profile（可被「另一台设备」改掉） */
let projectDefault: string;

beforeEach(() => {
	posts = [];
	conflictOnce = false;
	projectDefault = WORK;
	profiles = [
		{
			id: WORK,
			name: "工作账号",
			revision: 3,
			sizeBytes: 1,
			createdAt: 1,
			updatedAt: 1,
		},
		{
			id: GH,
			name: "GitHub 测试号",
			revision: 1,
			sizeBytes: 1,
			holder: HOLDER,
			createdAt: 1,
			updatedAt: 1,
		},
	];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL) => {
			const req = input as Request;
			const path = new URL(req.url).pathname;
			if (path === "/api/v1/providers") return Response.json({ providers: [] });
			if (path === "/api/v1/projects")
				return Response.json({
					projects: [
						{
							id: "prj_1",
							name: "oryn",
							shallowClone: false,
							defaultBrowserProfileId: projectDefault,
							createdAt: 1,
							updatedAt: 1,
						},
					],
				});
			if (path === "/api/v1/orbs")
				return Response.json({
					orbs: [
						{
							id: "orb-live00000000000000000001",
							state: "ready",
							type: "medium",
							threadIds: [],
						},
					],
				});
			if (path === "/api/v1/browser-profiles")
				return Response.json({ profiles });
			if (path === "/api/v1/threads" && req.method === "POST") {
				const body = (await req.json()) as Schemas["CreateThreadRequest"];
				posts.push(body);
				// 与服务端一致：缺省 browserProfileId 时按当时的项目默认解析
				const resolved =
					body.browserProfileId ?? (body.projectId ? projectDefault : GH);
				if (conflictOnce && !body.profileMode)
					return Response.json(
						{
							code: "profile_in_use",
							message: "held",
							profileId: resolved,
							profileName: resolved === WORK ? "工作账号" : "GitHub 测试号",
							holder: HOLDER,
						},
						{ status: 409 },
					);
				return Response.json(
					{
						thread: {
							id: `T-new-${posts.length}`,
							title: "x",
							state: "provisioning",
							labels: [],
						},
					},
					{ status: 201 },
				);
			}
			return Response.json({ code: "not_found", message: "" }, { status: 404 });
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	resetThreadStores();
});

function renderHome() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<HomePage />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const prompt = () =>
	screen.getByRole("textbox", { name: i18n.t("newThread.prompt") });

/** 等 Profile 胶囊出现（列表到达后）。 */
async function profilePill() {
	return screen.findByRole("button", { name: /^浏览器 Profile：/ });
}

async function send(text = "打开 GitHub") {
	fireEvent.change(prompt(), { target: { value: text } });
	await act(async () => {
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("composer.send") }),
		);
	});
}

async function pick(option: RegExp) {
	fireEvent.click(await profilePill());
	fireEvent.click(screen.getByRole("button", { name: option }));
}

describe("首页 Profile 胶囊", () => {
	it("默认跟随项目默认：不带 browserProfileId", async () => {
		renderHome();
		expect(await profilePill()).toHaveAccessibleName(
			i18n.t("newThread.profileDefaultLabel", { name: i18n.t("common.none") }),
		);
		await send();
		expect(posts).toHaveLength(1);
		expect(posts[0]).not.toHaveProperty("browserProfileId");
		expect(posts[0]).not.toHaveProperty("profileMode");
	});

	it("选了项目后「跟随项目默认」显示解析出的名字", async () => {
		renderHome();
		await profilePill();
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("newThread.noProject") }),
		);
		fireEvent.click(await screen.findByRole("button", { name: /^oryn/ }));
		expect(await profilePill()).toHaveAccessibleName(
			i18n.t("newThread.profileDefaultLabel", { name: "工作账号" }),
		);
	});

	it("「不用 Profile」：browserProfileId 为空串", async () => {
		renderHome();
		await pick(new RegExp(`^${i18n.t("newThread.profileNone")}`));
		await send();
		expect(posts[0]?.browserProfileId).toBe("");
	});

	it("指定 Profile：带它的 id；被持有的选项标「使用中」", async () => {
		renderHome();
		fireEvent.click(await profilePill());
		expect(
			screen.getByRole("button", { name: /GitHub 测试号/ }),
		).toHaveTextContent(i18n.t("newThread.profileInUse"));
		fireEvent.click(screen.getByRole("button", { name: /^工作账号/ }));
		await send();
		expect(posts[0]?.browserProfileId).toBe(WORK);
	});

	it("409 profile_in_use：弹对话框（持有者线程），选「接管」后以 profileMode=takeover 重发", async () => {
		conflictOnce = true;
		renderHome();
		await pick(/^GitHub 测试号/);
		await send();
		const dialog = await screen.findByTestId("profile-conflict");
		expect(dialog).toHaveTextContent("比价下单");
		expect(screen.queryByRole("alert")).toBeNull();
		await act(async () => {
			fireEvent.click(screen.getByTestId("profile-conflict-takeover"));
		});
		expect(posts.map((p) => [p.browserProfileId, p.profileMode])).toEqual([
			[GH, undefined],
			[GH, "takeover"],
		]);
		await waitFor(() =>
			expect(screen.queryByTestId("profile-conflict")).toBeNull(),
		);
	});

	it("跟随项目默认撞上 409 后，项目默认被另一台设备改掉：接管仍钉住对话框里的 Profile，请求其余字段不变", async () => {
		conflictOnce = true;
		renderHome();
		await profilePill();
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("newThread.noProject") }),
		);
		fireEvent.click(await screen.findByRole("button", { name: /^oryn/ }));
		await send("登录后台");
		const dialog = await screen.findByTestId("profile-conflict");
		expect(dialog).toHaveTextContent("比价下单");
		projectDefault = GH;
		await act(async () => {
			fireEvent.click(screen.getByTestId("profile-conflict-takeover"));
		});
		expect(posts).toHaveLength(2);
		expect(posts[0]).not.toHaveProperty("browserProfileId");
		const { browserProfileId, profileMode, ...rest } = posts[1] ?? {};
		expect([browserProfileId, profileMode]).toEqual([WORK, "takeover"]);
		expect(rest).toEqual(posts[0]);
	});

	it("挂到已有 orb 时不出现，也不带 Profile 字段", async () => {
		renderHome();
		await pick(/^工作账号/);
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("newThread.newOrbDefault") }),
		);
		fireEvent.click(await screen.findByRole("button", { name: /orb-live/ }));
		expect(
			screen.queryByRole("button", { name: /^浏览器 Profile：/ }),
		).toBeNull();
		await send();
		expect(posts[0]).not.toHaveProperty("browserProfileId");
	});

	it("没有任何 Profile 时不出现", async () => {
		profiles = [];
		renderHome();
		await screen.findByRole("button", { name: i18n.t("newThread.noProject") });
		await act(async () => {});
		expect(
			screen.queryByRole("button", { name: /^浏览器 Profile：/ }),
		).toBeNull();
	});

	it("Ctrl+B 在输入框里打开 Profile 弹层", async () => {
		renderHome();
		await profilePill();
		fireEvent.keyDown(prompt(), { key: "b", ctrlKey: true });
		expect(
			screen.getByRole("button", {
				name: new RegExp(`^${i18n.t("newThread.profileFollow")}`),
			}),
		).toBeInTheDocument();
	});
});
