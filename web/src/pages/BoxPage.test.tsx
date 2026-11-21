/**
 * box 页（M23）：私有徽标；停止走确认；`box_not_found` 回总览、其它错误原地显示不跳转；没有预览的 app 不挂预览触发；
 * 没有 live 的 app 行不写 commit、不能开线程；两个服务的 app「日志」是子菜单，打开的是 `<app>--<svc>` 的日志。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Box, BoxApp } from "../api/box-queries";
import type { Schemas } from "../api/client";
import i18n from "../i18n";
import { answerConfirm } from "../test/confirm";
import { BoxPage } from "./BoxPage";

function svc(app: string, name: string): Schemas["OrbService"] {
	return {
		name: `${app}--${name}`,
		source: "app",
		app,
		command: "run",
		links: [],
		status: "running",
		restarts: 0,
	};
}

function app(name: string, p: Partial<BoxApp> = {}): BoxApp {
	return {
		name,
		url: `https://${name}.apps.oryn.test`,
		public: true,
		repo: `github.com/acme/${name}`,
		root: "",
		source: true,
		port: 30000,
		deploying: false,
		preview: false,
		services: [svc(name, "web")],
		createdAt: 1,
		updatedAt: 1,
		live: {
			id: `bxr_${name}`,
			commit: "c0ffee1234567",
			branch: "main",
			repo: `github.com/acme/${name}`,
			root: "",
			source: true,
			sizeBytes: 1,
			state: "live",
			createdAt: 1,
			activatedAt: Date.now() - 3_600_000,
		},
		...p,
	};
}

const BOX: Box = {
	id: "box_1",
	name: "workbench",
	orbId: "orb-box-1",
	state: "running",
	desired: "running",
	provider: "local",
	type: "medium",
	cpus: 4,
	memoryMB: 8192,
	createdAt: 1,
	apps: [
		app("bug-tracker", {
			services: [svc("bug-tracker", "web"), svc("bug-tracker", "worker")],
		}),
		app("team-wiki", { public: false }),
		app("tiny-crm", { live: undefined, deploying: true, services: [] }),
	],
};

let reply: () => Response;
let requests: { method: string; path: string }[];

beforeEach(() => {
	reply = () => Response.json(BOX);
	requests = [];
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const method =
				(input instanceof Request ? input.method : init?.method) ?? "GET";
			const url = new URL(
				input instanceof Request ? input.url : String(input),
				"http://localhost",
			);
			const path = decodeURIComponent(url.pathname);
			requests.push({ method, path });
			if (path === "/api/v1/boxes/box_1" && method === "GET") return reply();
			if (path === "/api/v1/boxes/box_1/stop")
				return Response.json(BOX, { status: 202 });
			if (path === "/api/v1/boxes/box_1/activity")
				return Response.json({ items: [] });
			if (path === "/api/v1/boxes/box_1/usage")
				return Response.json({ days: [] });
			if (path.endsWith("/releases")) return Response.json({ releases: [] });
			if (path.includes("/services/")) return new Response("log line\n");
			return Response.json({ code: "not_found", message: "" }, { status: 404 });
		},
	);
});

afterEach(() => vi.unstubAllGlobals());

function Where() {
	return <span data-testid="where">{useLocation().pathname}</span>;
}

function mount() {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={["/boxes/box_1"]}>
				<Routes>
					<Route path="/boxes/:boxId" element={<BoxPage />} />
					<Route path="*" element={null} />
				</Routes>
				<Where />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const row = async (name: string) => {
	const rows = await screen.findAllByTestId("box-app-row");
	const r = rows.find((x) => x.dataset.app === name);
	if (!r) throw new Error(`没有 ${name} 行`);
	return r;
};

describe("box 页", () => {
	it("私有 app 带「私有」徽标，公开的不带", async () => {
		mount();
		expect(await row("team-wiki")).toHaveTextContent(
			i18n.t("boxes.apps.private"),
		);
		expect(await row("bug-tracker")).not.toHaveTextContent(
			i18n.t("boxes.apps.private"),
		);
	});

	it("停止 box 先确认，确认后才发请求", async () => {
		mount();
		fireEvent.click(
			await screen.findByRole("button", { name: i18n.t("boxes.menu.label") }),
		);
		fireEvent.click(
			screen.getByRole("menuitem", { name: i18n.t("boxes.menu.stop") }),
		);
		const req = await answerConfirm(true);
		expect(req.title).toBe(
			i18n.t("boxes.confirm.stop.title", { name: "workbench" }),
		);
		await waitFor(() =>
			expect(requests).toContainEqual({
				method: "POST",
				path: "/api/v1/boxes/box_1/stop",
			}),
		);
	});

	it("取消停止不发请求", async () => {
		mount();
		fireEvent.click(
			await screen.findByRole("button", { name: i18n.t("boxes.menu.label") }),
		);
		fireEvent.click(
			screen.getByRole("menuitem", { name: i18n.t("boxes.menu.stop") }),
		);
		await answerConfirm(false);
		expect(requests.some((r) => r.path.endsWith("/stop"))).toBe(false);
	});

	it("box_not_found 回到总览", async () => {
		reply = () =>
			Response.json(
				{ code: "box_not_found", message: "gone" },
				{ status: 404 },
			);
		mount();
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent(/^\/boxes$/),
		);
	});

	it("500 原地显示错误，不跳转", async () => {
		reply = () =>
			Response.json({ code: "internal", message: "boom" }, { status: 500 });
		mount();
		expect(await screen.findByText("boom")).toBeVisible();
		expect(screen.getByTestId("where")).toHaveTextContent("/boxes/box_1");
	});

	it("preview 为假时不挂预览触发", async () => {
		mount();
		await row("bug-tracker");
		expect(
			screen.queryByRole("button", {
				name: i18n.t("boxes.preview.label", { app: "bug-tracker" }),
			}),
		).toBeNull();
	});

	it("没有 live 的 app：不写 commit，开线程按钮禁用，菜单只有取消部署", async () => {
		mount();
		const r = await row("tiny-crm");
		expect(r).toHaveTextContent(i18n.t("boxes.apps.firstDeploy"));
		expect(r).not.toHaveTextContent("c0ffee1");
		expect(
			within(r).getByRole("button", { name: i18n.t("boxes.apps.newThread") }),
		).toBeDisabled();
		fireEvent.click(
			within(r).getByRole("button", {
				name: i18n.t("boxes.apps.menu", { app: "tiny-crm" }),
			}),
		);
		const items = screen.getAllByRole("menuitem").map((m) => m.textContent);
		expect(items).toEqual([i18n.t("boxes.apps.cancelDeploy")]);
		// 有 live 的行写出短 sha
		expect(await row("bug-tracker")).toHaveTextContent("c0ffee1");
	});

	it("两个服务的 app：日志是子菜单，打开的是 <app>--<svc>", async () => {
		mount();
		const r = await row("bug-tracker");
		fireEvent.click(
			within(r).getByRole("button", {
				name: i18n.t("boxes.apps.menu", { app: "bug-tracker" }),
			}),
		);
		fireEvent.click(
			screen.getByRole("menuitem", { name: i18n.t("boxes.apps.logs") }),
		);
		fireEvent.click(screen.getByRole("menuitem", { name: "worker" }));
		expect(
			await screen.findByRole("dialog", {
				name: i18n.t("services.logTitle", { name: "bug-tracker--worker" }),
			}),
		).toBeInTheDocument();
		await waitFor(() =>
			expect(
				requests.some((q) =>
					q.path.startsWith(
						"/api/v1/orbs/orb-box-1/services/bug-tracker--worker/log",
					),
				),
			).toBe(true),
		);
	});
});
