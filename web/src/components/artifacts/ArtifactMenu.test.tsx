/**
 * ArtifactMenu：导出原始文件（单文件 `format=html`、多文件 `format=zip`）、文档与幻灯片的导出子菜单、下载 PNG 与 PDF
 * 在渲染不可用时置灰并给出原因、删除后 toast 带撤销、回收站里只有恢复与永久删除；Office 类型的导出子菜单（原件、PDF、
 * Excel 的 PDF 需要引擎，配置了引擎时 Office 的 PDF 都放开）与查看器里的「上传新版本」（409 时提示刷新重试）。
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
import { EIGHT_LAYOUT_DECK } from "../../lib/artifacts/slides-fixture";
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
// 能力里的 `office.convert`（配置了 Office 引擎）
let officeConvert: boolean;

function mount(
	place: ArtifactMenuPlace = "card",
	over: Partial<Schemas["Artifact"]> = {},
) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter>
				<ArtifactMenu artifact={{ ...ARTIFACT, ...over }} place={place} />
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
	officeConvert = false;
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
				const format = url.searchParams.get("format") ?? "";
				return new Response(format === "zip" ? "PK" : "<h1>hi</h1>", {
					headers: {
						"Content-Disposition": `attachment; filename*=UTF-8''${encodeURIComponent(`本周习惯打卡.${format === "original" ? "docx" : format}`)}`,
					},
				});
			}
			if (url.pathname === `/api/v1/artifacts/${ID}/file`) {
				if (url.searchParams.get("path") === "slides.json")
					return new Response(JSON.stringify(EIGHT_LAYOUT_DECK));
				return Response.json(
					{ code: "not_found", message: "没有这个文件" },
					{ status: 404 },
				);
			}
			if (url.pathname === "/api/v1/artifacts/capabilities")
				return Response.json({
					frames: "host",
					render: false,
					renderReason: "找不到 Chromium",
					sample: false,
					office: { edit: officeConvert, convert: officeConvert },
				});
			if (url.pathname === `/api/v1/artifacts/${ID}/upload`)
				return Response.json(
					{ code: "conflict", message: "有更新的版本" },
					{ status: 409 },
				);
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

	it("文档：导出子菜单是 Word 与 Markdown，Markdown 由服务端按卡片上的版本生成", async () => {
		mount("card", { type: "document", kind: "document" });
		fireEvent.click(screen.getByTestId("artifact-menu"));
		fireEvent.click(item(i18n.t("artifacts.menu.export")));
		expect(screen.getAllByRole("menuitem").map((b) => b.textContent)).toEqual([
			i18n.t("artifacts.export.docx"),
			i18n.t("artifacts.export.md"),
		]);
		fireEvent.click(item(i18n.t("artifacts.export.md")));
		await waitFor(() => expect(downloads).toEqual(["本周习惯打卡.md"]));
		const req = requests.find((r) => r.url.pathname.endsWith("/export"));
		expect(req?.url.searchParams.get("format")).toBe("md");
		expect(req?.url.searchParams.get("version")).toBe("2");
	});

	it("幻灯片：渲染不可用时 PDF 置灰并给出原因；PowerPoint 在浏览器里生成，取不到的图片在 toast 里说明", async () => {
		mount("card", { type: "slides", kind: "slides" });
		fireEvent.click(screen.getByTestId("artifact-menu"));
		fireEvent.click(item(i18n.t("artifacts.menu.export")));
		await waitFor(() =>
			expect(item(i18n.t("artifacts.export.pdf"))).toHaveAttribute(
				"title",
				"找不到 Chromium",
			),
		);
		expect(item(i18n.t("artifacts.export.pdf"))).toBeDisabled();
		fireEvent.click(item(i18n.t("artifacts.export.pptx")));
		await waitFor(() => expect(downloads).toEqual(["本周习惯打卡.pptx"]));
		expect(exportRequests()).toEqual([]);
		expect(
			requests
				.filter((r) => r.url.pathname.endsWith("/file"))
				.map((r) => r.url.searchParams.get("path"))
				.toSorted(),
		).toEqual(["images/chart.png", "images/launch.png", "slides.json"]);
		expect(toastsStore.getState().toasts.map((t) => t.text)).toEqual([
			i18n.t("artifacts.export.doneMissing", {
				title: ARTIFACT.title,
				count: 2,
			}),
		]);
	});

	it("Word：导出子菜单是原件与 PDF，原件由服务端按卡片上的版本给出", async () => {
		mount("card", { type: "docx", kind: "document" });
		fireEvent.click(screen.getByTestId("artifact-menu"));
		expect(
			screen.queryByRole("menuitem", { name: i18n.t("artifacts.menu.upload") }),
		).toBeNull();
		fireEvent.click(item(i18n.t("artifacts.menu.export")));
		const original = i18n.t("artifacts.export.original", { ext: "docx" });
		expect(screen.getAllByRole("menuitem").map((b) => b.textContent)).toEqual([
			original,
			i18n.t("artifacts.export.pdf"),
		]);
		await waitFor(() =>
			expect(item(i18n.t("artifacts.export.pdf"))).toHaveAttribute(
				"title",
				"找不到 Chromium",
			),
		);
		fireEvent.click(item(original));
		await waitFor(() => expect(downloads).toEqual(["本周习惯打卡.docx"]));
		const req = requests.find((r) => r.url.pathname.endsWith("/export"));
		expect(req?.url.searchParams.get("format")).toBe("original");
		expect(req?.url.searchParams.get("version")).toBe("2");
	});

	it("Excel：PDF 需要 Office 引擎而置灰", () => {
		mount("card", { type: "xlsx", kind: "sheet" });
		fireEvent.click(screen.getByTestId("artifact-menu"));
		fireEvent.click(item(i18n.t("artifacts.menu.export")));
		const pdf = item(i18n.t("artifacts.export.pdf"));
		expect(pdf).toBeDisabled();
		expect(pdf).toHaveAttribute(
			"title",
			i18n.t("artifacts.export.needsEngine"),
		);
	});

	it("配置了 Office 引擎：Excel 与 Word 的 PDF 都放开，不再依赖渲染器", async () => {
		officeConvert = true;
		for (const over of [
			{ type: "xlsx", kind: "sheet" },
			{ type: "docx", kind: "document" },
		] as const) {
			const view = mount("card", over);
			fireEvent.click(screen.getByTestId("artifact-menu"));
			fireEvent.click(item(i18n.t("artifacts.menu.export")));
			await waitFor(() =>
				expect(item(i18n.t("artifacts.export.pdf"))).toBeEnabled(),
			);
			expect(item(i18n.t("artifacts.export.pdf"))).not.toHaveAttribute("title");
			fireEvent.click(item(i18n.t("artifacts.export.pdf")));
			await waitFor(() => expect(exportRequests()).toContain("pdf"));
			requests = [];
			view.unmount();
		}
	});

	it("PDF：导出子菜单只有原件", () => {
		mount("card", { type: "pdf", kind: "document" });
		fireEvent.click(screen.getByTestId("artifact-menu"));
		fireEvent.click(item(i18n.t("artifacts.menu.export")));
		expect(screen.getAllByRole("menuitem").map((b) => b.textContent)).toEqual([
			i18n.t("artifacts.export.original", { ext: "pdf" }),
		]);
	});

	it("查看器里的 Office 类型可以上传新版本：以卡片上的版本为 baseVersion，409 时提示刷新重试", async () => {
		mount("viewer", { type: "pptx", kind: "slides" });
		fireEvent.click(screen.getByTestId("artifact-menu"));
		fireEvent.click(item(i18n.t("artifacts.menu.upload")));
		const input = screen.getByTestId("artifact-upload-input");
		expect(input).toHaveAttribute("accept", ".pptx");
		fireEvent.change(input, {
			target: { files: [new File(["PK"], "新版.pptx")] },
		});
		await waitFor(() =>
			expect(toastsStore.getState().toasts.map((t) => t.text)).toEqual([
				i18n.t("artifacts.upload.conflict"),
			]),
		);
		const req = requests.find((r) => r.url.pathname.endsWith("/upload"));
		expect(req?.method).toBe("POST");
		expect(req?.url.searchParams.get("baseVersion")).toBe("2");
	});

	it("非 Office 类型没有「上传新版本」", () => {
		mount("viewer", { type: "document", kind: "document" });
		fireEvent.click(screen.getByTestId("artifact-menu"));
		expect(
			screen.queryByRole("menuitem", { name: i18n.t("artifacts.menu.upload") }),
		).toBeNull();
		expect(screen.queryByTestId("artifact-upload-input")).toBeNull();
	});
});
