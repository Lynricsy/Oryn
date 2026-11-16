/**
 * ArtifactMenu：导出原始文件（单文件 `format=html`、多文件 `format=zip`）、下载 PNG 在渲染不可用时置灰并给出原因、
 * 删除后 toast 带撤销、回收站里只有恢复与永久删除。
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
import type { Schemas } from "../../api/client";
import i18n from "../../i18n";
import { dismissToast, toastsStore } from "../../state/toasts";
import { ArtifactMenu, type ArtifactMenuPlace } from "./ArtifactMenu";

const ID = "art_01JDNAWEB0000000000000000A";

const ARTIFACT: Schemas["Artifact"] = {
	id: ID,
	origin: "agent",
	kind: "web",
	title: "本周习惯打卡",
	version: 2,
	size: 10,
	pages: 1,
	createdAt: 1,
	updatedAt: 2,
	threadId: "T-1",
};

let files: string[];
let requests: { method: string; url: URL }[];
let downloads: string[];

function mount(place: ArtifactMenuPlace = "card") {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter>
				<ArtifactMenu artifact={ARTIFACT} place={place} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

function item(name: string): HTMLElement {
	return screen.getByRole("menuitem", { name });
}

beforeEach(() => {
	files = ["index.html"];
	requests = [];
	downloads = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const url = new URL(req.url);
			requests.push({ method: req.method, url });
			if (url.pathname === `/api/v1/artifacts/${ID}` && req.method === "GET")
				return Response.json({
					artifact: ARTIFACT,
					version: 2,
					head: 2,
					files: files.map((path) => ({
						path,
						size: 1,
						mime: "text/html",
						sha256: "0".repeat(64),
					})),
					capabilities: [],
					threads: [],
				});
			if (url.pathname === `/api/v1/artifacts/${ID}/export`) {
				const zip = url.searchParams.get("format") === "zip";
				return new Response(zip ? "PK" : "<h1>hi</h1>", {
					headers: {
						"Content-Type": zip ? "application/zip" : "text/html",
						"Content-Disposition": `attachment; filename*=UTF-8''${encodeURIComponent(`本周习惯打卡.${zip ? "zip" : "html"}`)}`,
					},
				});
			}
			if (url.pathname === "/api/v1/artifacts/capabilities")
				return Response.json({
					frames: "host",
					render: false,
					renderReason: "找不到 Chromium",
					sample: false,
				});
			if (url.pathname === `/api/v1/artifacts/${ID}/restore`)
				return Response.json(ARTIFACT);
			return new Response(null, { status: 204 });
		}),
	);
	// jsdom 没有 blob: 地址；下载只看 `<a download>` 的文件名
	Object.assign(URL, {
		createObjectURL: vi.fn(() => "blob:mock"),
		revokeObjectURL: vi.fn(),
	});
	vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
		this: HTMLAnchorElement,
	) {
		downloads.push(this.download);
	});
});

afterEach(() => {
	for (const t of toastsStore.getState().toasts) dismissToast(t.id);
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

function exportRequests(): string[] {
	return requests
		.filter((r) => r.url.pathname.endsWith("/export"))
		.map((r) => r.url.searchParams.get("format") ?? "");
}

describe("ArtifactMenu", () => {
	it("导出：版本只有一个文件时请求 format=html，下载服务端给的文件名", async () => {
		mount();
		fireEvent.click(screen.getByTestId("artifact-menu"));
		fireEvent.click(item(i18n.t("artifacts.menu.export")));
		await waitFor(() => expect(downloads).toEqual(["本周习惯打卡.html"]));
		expect(exportRequests()).toEqual(["html"]);
		expect(toastsStore.getState().toasts.map((t) => t.text)).toEqual([
			i18n.t("artifacts.export.done", { title: ARTIFACT.title }),
		]);
	});

	it("导出：多个文件时请求 format=zip", async () => {
		files = ["index.html", "style.css"];
		mount();
		fireEvent.click(screen.getByTestId("artifact-menu"));
		fireEvent.click(item(i18n.t("artifacts.menu.export")));
		await waitFor(() => expect(downloads).toEqual(["本周习惯打卡.zip"]));
		expect(exportRequests()).toEqual(["zip"]);
	});

	it("导出失败：toast 报原因并可重试", async () => {
		const base = vi.mocked(fetch).getMockImplementation();
		let fail = true;
		vi.mocked(fetch).mockImplementation(async (input, init) => {
			const req =
				input instanceof Request ? input : new Request(String(input), init);
			if (fail && req.url.includes("/export"))
				return Response.json(
					{ code: "internal", message: "磁盘满了" },
					{ status: 500 },
				);
			return (base as typeof fetch)(input, init);
		});
		mount();
		fireEvent.click(screen.getByTestId("artifact-menu"));
		fireEvent.click(item(i18n.t("artifacts.menu.export")));
		await waitFor(() =>
			expect(toastsStore.getState().toasts[0]?.tone).toBe("err"),
		);
		const toast = toastsStore.getState().toasts[0];
		expect(toast?.text).toContain("磁盘满了");
		fail = false;
		act(() => toast?.action?.run());
		await waitFor(() => expect(downloads).toEqual(["本周习惯打卡.html"]));
	});

	it("下载 PNG：渲染不可用时置灰，悬停提示原因", async () => {
		mount();
		fireEvent.click(screen.getByTestId("artifact-menu"));
		await waitFor(() =>
			expect(item(i18n.t("artifacts.menu.png"))).toHaveAttribute(
				"title",
				"找不到 Chromium",
			),
		);
		expect(item(i18n.t("artifacts.menu.png"))).toBeDisabled();
	});

	it("删除：toast 带撤销，撤销即恢复", async () => {
		mount();
		fireEvent.click(screen.getByTestId("artifact-menu"));
		fireEvent.click(item(i18n.t("artifacts.menu.delete")));
		await waitFor(() =>
			expect(toastsStore.getState().toasts[0]?.text).toBe(
				i18n.t("artifacts.toast.deleted", { title: ARTIFACT.title }),
			),
		);
		expect(
			requests.some(
				(r) =>
					r.method === "DELETE" && r.url.pathname === `/api/v1/artifacts/${ID}`,
			),
		).toBe(true);
		act(() => toastsStore.getState().toasts[0]?.action?.run());
		await waitFor(() =>
			expect(
				requests.some(
					(r) =>
						r.method === "POST" &&
						r.url.pathname === `/api/v1/artifacts/${ID}/restore`,
				),
			).toBe(true),
		);
	});

	it("回收站里只有恢复与永久删除", () => {
		mount("trash");
		fireEvent.click(screen.getByTestId("artifact-menu"));
		expect(screen.getAllByRole("menuitem").map((b) => b.textContent)).toEqual([
			i18n.t("artifacts.menu.restore"),
			i18n.t("artifacts.menu.purge"),
		]);
	});
});
