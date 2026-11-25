/**
 * MockupDialog 与画布导出菜单（M29 D1 / D3）：
 * - 画框多选（默认全选、取消单个后只传选中帧、全不选禁止提交）
 * - 格式（mockup-png / mockup-pdf）、缩放（1x/2x/3x）、圆角、边框、阴影、设备外壳、长图、背景（浅/深/透明/自定义色校验）
 * - 下载失败 toast 报原因并支持重试
 * - ArtifactMenu 画布导出子菜单：导出样机打开 MockupDialog、导出工程（format=project）、部署到 box（sendGeneratedPrompt）
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
import { sendGeneratedPrompt } from "../../state/actions";
import { dismissToast, toastsStore } from "../../state/toasts";
import { ArtifactMenu } from "./ArtifactMenu";
import { MockupDialog } from "./MockupDialog";

vi.mock("../../state/actions", () => ({
	sendGeneratedPrompt: vi.fn(async () => undefined),
}));

const ID = "art_01JDNACANVAS000000000001";

const CANVAS_ARTIFACT: Schemas["Artifact"] = {
	id: ID,
	origin: "agent",
	kind: "canvas",
	title: "产品原型",
	version: 3,
	size: 2048,
	pages: 2,
	createdAt: 1,
	updatedAt: 3,
	threadId: "T-design",
};

const CANVAS_DOC = {
	format: 2,
	design: { engine: 1, start: "home" },
	canvases: [{ id: "c1", name: "首页" }],
	pages: [
		{
			id: "home",
			canvas: "c1",
			name: "首页",
			file: "dist/home.html",
			source: "src/frames/home.tsx",
			device: "browser",
			x: 0,
			y: 0,
			width: 1280,
			height: 800,
			locked: false,
		},
		{
			id: "mobile",
			canvas: "c1",
			name: "移动端",
			file: "dist/mobile.html",
			source: "src/frames/mobile.tsx",
			device: "phone",
			x: 1328,
			y: 0,
			width: 390,
			height: 844,
			locked: false,
		},
	],
	notes: [],
};

let requests: { method: string; url: URL }[];
let downloads: string[];
let renderOk: boolean;
let exportFailOnce: boolean;

function setupFetch() {
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const url = new URL(req.url);
			requests.push({ method: req.method, url });
			if (url.pathname === "/api/v1/artifacts/capabilities") {
				return Response.json({
					frames: "host",
					render: renderOk,
					renderReason: renderOk ? undefined : "找不到 Chromium",
					sample: false,
				});
			}
			if (
				url.pathname === `/api/v1/artifacts/${ID}/file` &&
				url.searchParams.get("path") === "canvas.json"
			) {
				return new Response(JSON.stringify(CANVAS_DOC), {
					headers: { "Content-Type": "application/json" },
				});
			}
			if (url.pathname === `/api/v1/artifacts/${ID}/export`) {
				if (exportFailOnce) {
					exportFailOnce = false;
					return Response.json(
						{ code: "render_unavailable", message: "渲染器繁忙" },
						{ status: 503 },
					);
				}
				const fmt = url.searchParams.get("format") ?? "";
				const ext =
					fmt === "mockup-pdf" ? "pdf" : fmt === "project" ? "zip" : "png";
				const name =
					fmt === "project"
						? `产品原型-v3-project.zip`
						: `产品原型-v3-mockup.${ext}`;
				return new Response(ext === "pdf" ? "%PDF-1.4" : "PNG", {
					headers: {
						"Content-Disposition": `attachment; filename*=UTF-8''${encodeURIComponent(name)}`,
					},
				});
			}
			return new Response(null, { status: 204 });
		}),
	);
}

beforeEach(() => {
	requests = [];
	downloads = [];
	renderOk = true;
	exportFailOnce = false;
	vi.mocked(sendGeneratedPrompt).mockClear();
	setupFetch();
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

function mountDialog(onClose = vi.fn()) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(
		<QueryClientProvider client={qc}>
			<MockupDialog artifact={CANVAS_ARTIFACT} onClose={onClose} />
		</QueryClientProvider>,
	);
	return { onClose };
}

describe("MockupDialog", () => {
	it("加载画框列表、调整参数并请求 mockup-png 导出", async () => {
		const { onClose } = mountDialog();
		await screen.findByText("移动端");

		// 取消勾选「首页」，只导「移动端」
		fireEvent.click(screen.getByLabelText(/首页/));
		// 缩放改为 3x
		fireEvent.click(screen.getByRole("button", { name: "3x" }));
		// 背景选自定义并输入合法色值
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("artifacts.mockup.bgCustom") }),
		);
		fireEvent.change(screen.getByPlaceholderText("#f4f4f5"), {
			target: { value: "#102030" },
		});
		// 圆角改为 24
		fireEvent.change(
			screen.getByRole("slider", { name: i18n.t("artifacts.mockup.radius") }),
			{ target: { value: "24" } },
		);
		// 关闭边框、开启长图
		fireEvent.click(screen.getByLabelText(i18n.t("artifacts.mockup.border")));
		fireEvent.click(screen.getByLabelText(i18n.t("artifacts.mockup.long")));

		fireEvent.click(screen.getByTestId("mockup-download"));

		await waitFor(() => expect(downloads).toEqual(["产品原型-v3-mockup.png"]));
		expect(onClose).toHaveBeenCalledTimes(1);

		const req = requests.find((r) => r.url.pathname.endsWith("/export"));
		expect(req?.url.searchParams.get("format")).toBe("mockup-png");
		expect(req?.url.searchParams.get("version")).toBe("3");
		expect(req?.url.searchParams.get("frames")).toBe("mobile");
		expect(req?.url.searchParams.get("scale")).toBe("3");
		expect(req?.url.searchParams.get("radius")).toBe("24");
		expect(req?.url.searchParams.get("border")).toBe("false");
		expect(req?.url.searchParams.get("bg")).toBe("#102030");
		expect(req?.url.searchParams.get("shadow")).toBe("true");
		expect(req?.url.searchParams.get("shell")).toBe("true");
		expect(req?.url.searchParams.get("long")).toBe("true");
	});

	it("支持 PDF 格式、非法自定义色与零画框校验，失败时 toast 支持重试", async () => {
		exportFailOnce = true;
		const { onClose } = mountDialog();
		await screen.findByText("首页");

		// 全不选禁用导出按钮
		fireEvent.click(screen.getByLabelText(/全选/));
		expect(screen.getByTestId("mockup-download")).toBeDisabled();
		fireEvent.click(screen.getByLabelText(/全选/));
		expect(screen.getByTestId("mockup-download")).not.toBeDisabled();

		// 非法十六进制色禁用导出按钮
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("artifacts.mockup.bgCustom") }),
		);
		fireEvent.change(screen.getByPlaceholderText("#f4f4f5"), {
			target: { value: "not-a-color" },
		});
		expect(screen.getByTestId("mockup-download")).toBeDisabled();
		fireEvent.click(
			screen.getByRole("button", {
				name: i18n.t("artifacts.mockup.bgTransparent"),
			}),
		);

		// 切到 PDF
		fireEvent.click(
			screen.getByLabelText(i18n.t("artifacts.mockup.formatPdf")),
		);
		expect(screen.getByRole("button", { name: "2x" })).toBeDisabled();

		fireEvent.click(screen.getByTestId("mockup-download"));
		await waitFor(() =>
			expect(toastsStore.getState().toasts[0]?.tone).toBe("err"),
		);
		expect(toastsStore.getState().toasts[0]?.text).toContain("渲染器繁忙");
		expect(onClose).not.toHaveBeenCalled();

		// 点重试成功
		act(() => toastsStore.getState().toasts[0]?.action?.run());
		await waitFor(() => expect(downloads).toEqual(["产品原型-v3-mockup.pdf"]));
		const last = requests
			.filter((r) => r.url.pathname.endsWith("/export"))
			.at(-1);
		expect(last?.url.searchParams.get("format")).toBe("mockup-pdf");
		expect(last?.url.searchParams.get("bg")).toBe("transparent");
		// 全选时不传 frames 参数
		expect(last?.url.searchParams.has("frames")).toBe(false);
	});
});

describe("ArtifactMenu 画布交付菜单", () => {
	function mountMenu(over: Partial<Schemas["Artifact"]> = {}) {
		const qc = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		return render(
			<QueryClientProvider client={qc}>
				<MemoryRouter>
					<ArtifactMenu
						artifact={{ ...CANVAS_ARTIFACT, ...over }}
						place="viewer"
					/>
				</MemoryRouter>
			</QueryClientProvider>,
		);
	}

	it("导出子菜单含样机、工程 zip 与部署到 box，三项动作分别生效", async () => {
		mountMenu();
		fireEvent.click(screen.getByTestId("artifact-menu"));
		fireEvent.click(
			screen.getByRole("menuitem", { name: i18n.t("artifacts.menu.export") }),
		);

		// 1. 导出工程 (.zip)
		fireEvent.click(
			screen.getByRole("menuitem", {
				name: i18n.t("artifacts.export.project"),
			}),
		);
		await waitFor(() => expect(downloads).toEqual(["产品原型-v3-project.zip"]));
		expect(
			requests.some(
				(r) =>
					r.url.pathname.endsWith("/export") &&
					r.url.searchParams.get("format") === "project",
			),
		).toBe(true);

		// 2. 部署到 box -> sendGeneratedPrompt 给作者线程
		fireEvent.click(screen.getByTestId("artifact-menu"));
		fireEvent.click(
			screen.getByRole("menuitem", { name: i18n.t("artifacts.menu.export") }),
		);
		fireEvent.click(
			screen.getByRole("menuitem", { name: i18n.t("artifacts.export.deploy") }),
		);
		await waitFor(() => expect(sendGeneratedPrompt).toHaveBeenCalledTimes(1));
		const [tid, prompt] = vi.mocked(sendGeneratedPrompt).mock.calls[0] as [
			string,
			string,
		];
		expect(tid).toBe("T-design");
		expect(prompt).toContain(`oryn design eject ${ID}`);
		expect(prompt).toContain("oryn boxes deploy");

		// 3. 导出样机… -> 打开 MockupDialog
		fireEvent.click(screen.getByTestId("artifact-menu"));
		fireEvent.click(
			screen.getByRole("menuitem", { name: i18n.t("artifacts.menu.export") }),
		);
		await waitFor(() =>
			expect(
				screen.getByRole("menuitem", {
					name: i18n.t("artifacts.export.mockup"),
				}),
			).not.toBeDisabled(),
		);
		fireEvent.click(
			screen.getByRole("menuitem", { name: i18n.t("artifacts.export.mockup") }),
		);
		expect(
			await screen.findByRole("dialog", {
				name: i18n.t("artifacts.mockup.title"),
			}),
		).toBeInTheDocument();
	});

	it("没有作者线程时不显示部署到 box；渲染不可用时导出样机置灰", async () => {
		renderOk = false;
		mountMenu({ threadId: undefined });
		fireEvent.click(screen.getByTestId("artifact-menu"));
		fireEvent.click(
			screen.getByRole("menuitem", { name: i18n.t("artifacts.menu.export") }),
		);
		await waitFor(() =>
			expect(
				screen.getByRole("menuitem", {
					name: i18n.t("artifacts.export.mockup"),
				}),
			).toBeDisabled(),
		);
		expect(
			screen.queryByRole("menuitem", {
				name: i18n.t("artifacts.export.deploy"),
			}),
		).toBeNull();
	});
});
