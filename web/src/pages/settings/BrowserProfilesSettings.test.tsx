/**
 * 设置 → 浏览器 Profile：空态说明、新建（名字校验）、列表（大小、保存时间、空闲 / 使用中 · 线程链接 / 只读副本 / 保存失败原因）、
 * 重命名、删除（确认；被持有时禁用）。
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
import i18n from "../../i18n";
import BrowserProfilesSettings, {
	validProfileName,
} from "./BrowserProfilesSettings";

type Profile = Schemas["BrowserProfile"];

let profiles: Profile[];
let requests: { method: string; path: string; body: unknown }[];

const WORK: Profile = {
	id: "bpf_01JCZ8M3W4X5Y6Z7A8B9C0DWRK",
	name: "工作账号",
	revision: 3,
	sizeBytes: 18_874_368,
	savedAt: Date.now() - 12 * 60_000,
	holder: {
		orbId: "orb-a",
		threadIds: ["T-main"],
		threadId: "T-main",
		threadTitle: "🔧 WS: 重构重连",
		since: 1,
	},
	readers: [{ orbId: "orb-b", threadIds: ["T-b"], since: 1 }],
	createdAt: 1,
	updatedAt: 1,
};

const SHOP: Profile = {
	id: "bpf_01JCZ8M3W4X5Y6Z7A8B9C0DSHP",
	name: "淘宝采购",
	revision: 0,
	sizeBytes: 0,
	saveError: "too_large",
	createdAt: 1,
	updatedAt: 1,
};

beforeEach(() => {
	requests = [];
	profiles = [WORK, SHOP];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL) => {
			const req = input as Request;
			const path = new URL(req.url).pathname;
			const text = req.method === "GET" ? "" : await req.text();
			const body = text ? JSON.parse(text) : undefined;
			requests.push({ method: req.method, path, body });
			if (path === "/api/v1/browser-profiles" && req.method === "GET")
				return Response.json({ profiles });
			if (path === "/api/v1/browser-profiles" && req.method === "POST") {
				if (body.name === "工作账号")
					return Response.json(
						{ code: "profile_name_taken", message: "taken" },
						{ status: 409 },
					);
				const p: Profile = {
					id: "bpf_NEW",
					name: body.name,
					revision: 0,
					sizeBytes: 0,
					createdAt: 2,
					updatedAt: 2,
				};
				profiles = [...profiles, p];
				return Response.json(p, { status: 201 });
			}
			const one = /^\/api\/v1\/browser-profiles\/(.+)$/.exec(path);
			if (one && req.method === "PATCH") {
				profiles = profiles.map((p) =>
					p.id === one[1] ? { ...p, name: body.name } : p,
				);
				return Response.json(profiles.find((p) => p.id === one[1]));
			}
			if (one && req.method === "DELETE") {
				profiles = profiles.filter((p) => p.id !== one[1]);
				return new Response(null, { status: 204 });
			}
			return new Response(null, { status: 404 });
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

function mount() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<BrowserProfilesSettings />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const rowOf = async (name: string) =>
	(await screen.findAllByTestId("browser-profile-row")).find((r) =>
		r.textContent?.includes(name),
	) as HTMLElement;

describe("validProfileName", () => {
	it("trim 后 1–40 个字符、不含控制字符", () => {
		expect(validProfileName("  工作  ")).toBe(true);
		expect(validProfileName("   ")).toBe(false);
		expect(validProfileName("a".repeat(40))).toBe(true);
		expect(validProfileName("a".repeat(41))).toBe(false);
		expect(validProfileName("a\tb")).toBe(false);
		expect(validProfileName("😀".repeat(40))).toBe(true);
	});
});

describe("BrowserProfilesSettings", () => {
	it("空列表：说明 Profile 是什么、同一时间只在一台 orb 上写回", async () => {
		profiles = [];
		mount();
		expect(
			await screen.findByText(i18n.t("browserProfiles.emptyTitle")),
		).toBeInTheDocument();
		expect(
			screen.getByText(i18n.t("browserProfiles.emptyHint")),
		).toBeInTheDocument();
	});

	it("行：使用中 · 线程（链接到线程）、只读副本数、大小与保存时间；保存失败给原因", async () => {
		mount();
		const work = await rowOf("工作账号");
		const link = within(work).getByRole("link", {
			name: i18n.t("browserProfiles.inUse", { thread: "重构重连" }),
		});
		expect(link).toHaveAttribute("href", "/t/T-main");
		expect(work).toHaveTextContent(
			i18n.t("browserProfiles.readers", { count: 1 }),
		);
		expect(work).toHaveTextContent("18");
		const shop = await rowOf("淘宝采购");
		expect(shop).toHaveTextContent(i18n.t("browserProfiles.idle"));
		expect(shop).toHaveTextContent(
			i18n.t("browserProfiles.saveFailed", {
				reason: i18n.t("browserProfiles.saveError.too_large"),
			}),
		);
		expect(shop).toHaveTextContent(i18n.t("browserProfiles.neverSaved"));
	});

	it("新建：名字非法时禁用；重名显示错误；成功后清空输入并出现在列表里", async () => {
		mount();
		const input = screen.getByRole("textbox", {
			name: i18n.t("browserProfiles.name"),
		});
		const create = screen.getByRole("button", {
			name: i18n.t("common.create"),
		});
		fireEvent.change(input, { target: { value: "   " } });
		expect(create).toBeDisabled();
		fireEvent.change(input, { target: { value: "工作账号" } });
		await act(async () => {
			fireEvent.click(create);
		});
		expect(
			await screen.findByText(i18n.t("errors.profile_name_taken")),
		).toBeInTheDocument();
		fireEvent.change(input, { target: { value: " 新账号 " } });
		await act(async () => {
			fireEvent.click(create);
		});
		expect(requests).toContainEqual({
			method: "POST",
			path: "/api/v1/browser-profiles",
			body: { name: "新账号" },
		});
		await waitFor(() => expect(input).toHaveValue(""));
		expect(await rowOf("新账号")).toBeInTheDocument();
	});

	it("重命名：行内编辑后 PATCH", async () => {
		mount();
		const shop = await rowOf("淘宝采购");
		fireEvent.click(
			within(shop).getByRole("button", {
				name: i18n.t("browserProfiles.rename"),
			}),
		);
		const input = within(shop).getByRole("textbox", {
			name: i18n.t("browserProfiles.name"),
		});
		fireEvent.change(input, { target: { value: "采购" } });
		await act(async () => {
			fireEvent.click(
				within(shop).getByRole("button", { name: i18n.t("common.save") }),
			);
		});
		expect(requests).toContainEqual({
			method: "PATCH",
			path: `/api/v1/browser-profiles/${SHOP.id}`,
			body: { name: "采购" },
		});
		expect(await rowOf("采购")).toBeInTheDocument();
	});

	it("删除：被持有时禁用并提示先解除；空闲的确认后 DELETE", async () => {
		const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
		mount();
		const work = await rowOf("工作账号");
		expect(
			within(work).getByRole("button", {
				name: i18n.t("browserProfiles.deleteHeld"),
			}),
		).toBeDisabled();
		const shop = await rowOf("淘宝采购");
		await act(async () => {
			fireEvent.click(
				within(shop).getByRole("button", { name: i18n.t("common.delete") }),
			);
		});
		expect(confirm).toHaveBeenCalledWith(
			i18n.t("browserProfiles.deleteConfirm", { name: "淘宝采购" }),
		);
		expect(requests).toContainEqual({
			method: "DELETE",
			path: `/api/v1/browser-profiles/${SHOP.id}`,
			body: undefined,
		});
		await waitFor(() =>
			expect(screen.getAllByTestId("browser-profile-row")).toHaveLength(1),
		);
	});
});
