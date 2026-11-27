/**
 * 画布编辑器在手机（`compact` 断点，计划 M14 9.2）上只读：不显示工具条、图层与属性面板，换成视图条（评论、适合全部）；
 * 宽屏照旧是完整的工具条。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../../i18n";
import { CanvasEditor } from "./CanvasEditor";

const ID = "art_01JDNACANVAS00000000000000";
const NOW = Date.now();
const DOC = {
	format: 1,
	canvases: [{ id: "c1", name: "首页" }],
	pages: [
		{
			id: "hero",
			canvas: "c1",
			name: "首屏",
			file: "hero.html",
			x: 0,
			y: 0,
			width: 1280,
			height: 800,
		},
	],
	notes: [],
};

function mount() {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter>
				<CanvasEditor
					artifactId={ID}
					variant="page"
					title="首页改版草图"
					onComment={() => {}}
				/>
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

let width: number;

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
	width = window.innerWidth;
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const path = new URL(req.url).pathname;
			if (path === `/api/v1/artifacts/${ID}`)
				return Response.json({
					artifact: {
						id: ID,
						origin: "agent",
						kind: "canvas",
						title: "首页改版草图",
						version: 1,
						size: 1,
						pages: 1,
						createdAt: NOW,
						updatedAt: NOW,
					},
					version: 1,
					head: 1,
					files: [
						{
							path: "canvas.json",
							size: 1,
							mime: "application/json",
							sha256: "a",
						},
						{ path: "hero.html", size: 1, mime: "text/html", sha256: "b" },
					],
					capabilities: [],
					threads: [],
				});
			if (path === `/api/v1/artifacts/${ID}/canvas`)
				return Response.json({ version: 1, seq: 0, doc: DOC });
			if (path === `/api/v1/artifacts/${ID}/frames`)
				return Response.json(
					{ url: "https://a-x.example.test/", token: "t".repeat(26) },
					{ status: 201 },
				);
			if (path === "/api/v1/artifacts/capabilities")
				return Response.json({
					frames: "host",
					render: false,
					sample: false,
					office: { edit: false, convert: false },
				});
			return new Response(null, { status: 204 });
		}),
	);
});

afterEach(() => {
	window.innerWidth = width;
	vi.unstubAllGlobals();
});

describe("CanvasEditor 的断点", () => {
	it("手机：只读，没有工具条与图层开关，视图条只有评论与适合全部", async () => {
		window.innerWidth = 390;
		mount();
		// 视图条同样叫「画布工具」，但完整工具条的移动工具与缩放菜单都不在
		const bar = await screen.findByTestId("canvas-compact-bar");
		expect(bar).toBe(
			screen.getByRole("toolbar", { name: i18n.t("artifacts.canvas.toolbar") }),
		);
		expect(screen.queryByTestId("canvas-tool-move")).toBeNull();
		expect(screen.queryByTestId("canvas-zoom-menu")).toBeNull();
		expect(screen.queryByTestId("canvas-layers-toggle")).toBeNull();
		expect(screen.getByTestId("canvas-editor")).toHaveAttribute(
			"data-readonly",
		);
		expect(screen.getByTestId("canvas-tool-comment")).toBeInTheDocument();
		expect(screen.getByTestId("canvas-zoom-fit")).toBeInTheDocument();
		// 只有一个画布页时不放页面下拉
		expect(screen.queryByTestId("canvas-sheet-menu")).toBeNull();
	});

	it("宽屏：完整的工具条与图层开关，没有视图条", async () => {
		window.innerWidth = 1280;
		mount();
		await waitFor(() =>
			expect(
				screen.getByRole("toolbar", {
					name: i18n.t("artifacts.canvas.toolbar"),
				}),
			).toBeInTheDocument(),
		);
		expect(screen.getByTestId("canvas-layers-toggle")).toBeInTheDocument();
		expect(screen.queryByTestId("canvas-compact-bar")).toBeNull();
		expect(screen.getByTestId("canvas-editor")).not.toHaveAttribute(
			"data-readonly",
		);
	});
});
