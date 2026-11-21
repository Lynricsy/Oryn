/**
 * 首页执行位置选择器：新线程总是新 orb，运行中的 orb（含预热池）不供挑选、编号不出现；带 Docker 的类型显示「Docker」徽标。
 * Boxes 分组（M23）：选中 app 后请求带 `executor.kind = box`；选了 box 后「新 orb」不再是选中态；停着的 box 点不了；
 * box 页交来的 seed 立即预选（box 列表晚到也一样、首屏偏好是全局助手也发到工作线程）；box 不在了不发请求；服务端错误码走人话文案。
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
import type { Box } from "../api/box-queries";
import type { Orb, Provider } from "../api/client";
import i18n from "../i18n";
import { resetThreadStores } from "../state/stores";
import { uiStore } from "../state/ui";
import { HomePage } from "./HomePage";

const PROVIDER: Provider = {
	id: "prov_1",
	name: "local",
	driver: "docker",
	capabilities: { freeze: true, snapshot: true },
	orbTypes: [
		{ name: "medium", cpus: 4, memoryMB: 8192 },
		{ name: "medium-docker", cpus: 4, memoryMB: 8192, features: ["docker"] },
	],
	defaultType: "medium",
	online: true,
	createdAt: 1,
} as Provider;

function orb(over: Partial<Orb> & Pick<Orb, "id">): Orb {
	return {
		providerId: "prov_1",
		type: "medium",
		cpus: 4,
		memoryMB: 8192,
		image: "oryn-orb:dev",
		state: "ready",
		stateChangedAt: 1,
		createdAt: 1,
		threadIds: [],
		...over,
	};
}

const ORBS: Orb[] = [
	orb({ id: "orb-work0000000000000001", threadIds: ["T-1"] }),
	orb({
		id: "orb-dock0000000000000002",
		threadIds: ["T-2"],
		type: "medium-docker",
		nestedDocker: true,
	}),
	orb({ id: "orb-pool0000000000000003", pool: "idle" }),
	orb({ id: "orb-warm0000000000000004", pool: "warming", state: "spawning" }),
];

function box(p: Partial<Box> & Pick<Box, "id" | "name">): Box {
	return {
		orbId: `orb-${p.id}`,
		state: "running",
		desired: "running",
		provider: "local",
		type: "medium",
		cpus: 4,
		memoryMB: 8192,
		createdAt: 1,
		apps: [],
		...p,
	};
}

const LIVE = {
	id: "bxr_1",
	commit: "c0ffee1",
	branch: "main",
	repo: "github.com/acme/x",
	root: "",
	source: true,
	sizeBytes: 1,
	state: "live",
	createdAt: 1,
	activatedAt: 1,
} as const;

const BOXES: Box[] = [
	box({
		id: "box_1",
		name: "workbench",
		apps: [
			{
				name: "bug-tracker",
				public: true,
				repo: "github.com/acme/x",
				root: "",
				source: true,
				port: 30000,
				live: LIVE,
				deploying: false,
				preview: false,
				services: [],
				createdAt: 1,
				updatedAt: 1,
			},
		],
	}),
	box({ id: "box_2", name: "lab", desired: "stopped", state: "stopped" }),
];

let posts: { path: string; body: unknown }[];
let boxesReply: () => Promise<Response>;
let threadReply: () => Response;
let defaultTarget: "assistant" | null;

beforeEach(() => {
	posts = [];
	defaultTarget = null;
	boxesReply = async () => Response.json({ boxes: [] });
	threadReply = () =>
		Response.json(
			{
				thread: {
					id: "T-new",
					title: "",
					titleSource: "auto",
					state: "provisioning",
					stateChangedAt: 1,
					pinned: false,
					labels: [],
					unread: false,
					createdAt: 1,
					updatedAt: 1,
					kind: "work",
				},
			},
			{ status: 201 },
		);
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), "http://x"), init);
			const url = new URL(req.url, "http://x");
			if (req.method === "POST") {
				const text = await req.text();
				posts.push({
					path: url.pathname,
					body: text ? JSON.parse(text) : undefined,
				});
				if (url.pathname === "/api/v1/threads") return threadReply();
			}
			if (url.pathname === "/api/v1/providers")
				return Response.json({ providers: [PROVIDER] });
			if (url.pathname === "/api/v1/projects")
				return Response.json({ projects: [] });
			if (url.pathname === "/api/v1/orbs") return Response.json({ orbs: ORBS });
			if (url.pathname === "/api/v1/boxes") return boxesReply();
			if (url.pathname === "/api/v1/settings/composer" && defaultTarget)
				return Response.json({ defaultTarget });
			return Response.json({ code: "not_found", message: "" }, { status: 404 });
		},
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	resetThreadStores();
	uiStore.getState().takeHomeSeed();
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
/** 弹层里主文字恰为 `text` 的选项按钮（前面的徽标、后面的说明不算）。 */
const option = (text: string) => {
	const b = screen
		.getAllByRole("button")
		.find(
			(x) =>
				x.closest("[data-floating]") &&
				[...x.querySelectorAll("span")].some((s) => s.textContent === text),
		);
	if (!b) throw new Error(`没有选项 ${text}`);
	return b;
};
const send = async (text: string) => {
	fireEvent.change(prompt(), { target: { value: text } });
	fireEvent.keyDown(prompt(), { key: "Enter" });
};

it("运行中的 orb 不可选、编号不出现；只有声明 docker 的类型带徽标", async () => {
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
	// 唯一的 provider：胶囊直接显示它和它的 defaultType
	fireEvent.click(
		await screen.findByRole("button", { name: "local · medium" }),
	);
	// 已有 orb（普通、Docker、预热池）都不出现，编号也不出现
	await screen.findAllByRole("button", { name: /^local/ });
	for (const o of ORBS)
		expect(screen.queryByText(new RegExp(o.id.slice(0, 12)))).toBeNull();
	expect(screen.queryByText(i18n.t("newThread.newOrb"))).not.toBeNull();
	// 新 orb 的类型：只有声明了 docker 特性的类型带徽标
	const types = screen
		.getAllByRole("button", { name: /^local/ })
		.map((b) => b.textContent ?? "");
	expect(types.filter((s) => s.includes("Docker"))).toHaveLength(1);
	expect(types.find((s) => s.includes("medium-docker"))).toContain("Docker");
});

describe("Boxes 执行位置（M23）", () => {
	const pill = (name: string | RegExp) => screen.findByRole("button", { name });

	it("选中 app 后请求带 box 执行位置；新 orb 不再是选中态", async () => {
		boxesReply = async () => Response.json({ boxes: BOXES });
		renderHome();
		fireEvent.click(await pill("local · medium"));
		await screen.findByText(i18n.t("boxes.picker.label"));
		fireEvent.click(option("bug-tracker"));
		fireEvent.click(await pill("workbench · bug-tracker"));
		const selected = screen
			.getAllByRole("button")
			.filter(
				(b) =>
					b.textContent?.startsWith("local") &&
					b.className.split(" ").includes("bg-hover"),
			);
		expect(selected).toHaveLength(0);
		fireEvent.click(await pill("workbench · bug-tracker"));
		await send("看看日志");
		await waitFor(() => expect(posts).toHaveLength(1));
		expect(posts[0]).toMatchObject({
			path: "/api/v1/threads",
			body: {
				prompt: "看看日志",
				executor: { kind: "box", boxId: "box_1", app: "bug-tracker" },
			},
		});
		expect(posts[0]?.body).not.toHaveProperty("browserProfileId");
	});

	it("停着的 box 的行看得见、点不了", async () => {
		boxesReply = async () => Response.json({ boxes: BOXES });
		renderHome();
		fireEvent.click(await pill("local · medium"));
		await screen.findByText(i18n.t("boxes.picker.label"));
		const lab = option("lab");
		expect(lab).toBeDisabled();
		fireEvent.click(lab);
		expect(
			screen.getByRole("button", { name: "local · medium" }),
		).toBeInTheDocument();
		expect(screen.getByTestId("executor-options-body")).toBeInTheDocument();
	});

	it("seed 带执行位置、box 列表晚到：仍预选，发送时执行位置正确", async () => {
		let release: () => void = () => undefined;
		const gate = new Promise<void>((r) => {
			release = r;
		});
		boxesReply = async () => {
			await gate;
			return Response.json({ boxes: BOXES });
		};
		uiStore.getState().seedHome({
			text: "",
			executor: { kind: "box", boxId: "box_1", app: "bug-tracker" },
		});
		renderHome();
		// 列表没到：胶囊先显示 …
		expect(await pill("…")).toBeInTheDocument();
		await act(async () => release());
		expect(await pill("workbench · bug-tracker")).toBeInTheDocument();
		await send("修一下");
		await waitFor(() => expect(posts).toHaveLength(1));
		expect(posts[0]).toMatchObject({
			path: "/api/v1/threads",
			body: { executor: { kind: "box", boxId: "box_1", app: "bug-tracker" } },
		});
	});

	it("首屏偏好是全局助手、seed 带执行位置：发到工作线程而不是助手", async () => {
		defaultTarget = "assistant";
		boxesReply = async () => Response.json({ boxes: BOXES });
		uiStore.getState().seedHome({
			text: "",
			executor: { kind: "box", boxId: "box_1" },
		});
		renderHome();
		expect(await pill(/^workbench · /)).toBeInTheDocument();
		await send("在机器上看看");
		await waitFor(() => expect(posts).toHaveLength(1));
		expect(posts[0]).toMatchObject({
			path: "/api/v1/threads",
			body: { executor: { kind: "box", boxId: "box_1" } },
		});
	});

	it("box 列表到了却没有这台 box：不发请求，打开执行位置弹层并说明", async () => {
		boxesReply = async () => Response.json({ boxes: BOXES });
		uiStore.getState().seedHome({
			text: "",
			executor: { kind: "box", boxId: "box_gone" },
		});
		renderHome();
		expect(await pill(i18n.t("boxes.picker.gone"))).toBeInTheDocument();
		await send("hello");
		expect(await screen.findByRole("alert")).toHaveTextContent(
			i18n.t("boxes.picker.gone"),
		);
		expect(screen.getByTestId("executor-options-body")).toBeInTheDocument();
		expect(posts).toHaveLength(0);
	});

	it("服务端回 box_stopped：显示人话文案", async () => {
		boxesReply = async () => Response.json({ boxes: BOXES });
		threadReply = () =>
			Response.json(
				{ code: "box_stopped", message: "this box is stopped" },
				{ status: 409 },
			);
		uiStore.getState().seedHome({
			text: "",
			executor: { kind: "box", boxId: "box_1" },
		});
		renderHome();
		await pill(/^workbench · /);
		await send("hello");
		expect(await screen.findByRole("alert")).toHaveTextContent(
			i18n.t("errors.box_stopped"),
		);
	});
});
