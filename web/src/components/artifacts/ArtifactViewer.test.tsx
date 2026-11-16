/**
 * ArtifactViewer：顶栏、观看登记、被拦截跳转的提示条、各空态与「在浏览器中打开」。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../../api/client";
import i18n from "../../i18n";
import { realtime } from "../../realtime/socket";
import { ArtifactViewer, safeBlockedUrl } from "./ArtifactViewer";

const ID = "art_01JDNAWEB0000000000000000A";
const FRAME_URL = "https://a-abcdefghijklmnopqrstuvwxyz.example.test/";
const NOW = Date.now();

function artifact(
	patch: Partial<Schemas["Artifact"]> = {},
): Schemas["Artifact"] {
	return {
		id: ID,
		origin: "agent",
		kind: "web",
		title: "本周习惯打卡",
		version: 3,
		size: 100,
		pages: 1,
		createdAt: NOW - 60_000,
		updatedAt: NOW - 1000,
		...patch,
	};
}

let detail: Response | (() => Response);
let opened: string[];

function wrap(node: ReactNode) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(<QueryClientProvider client={qc}>{node}</QueryClientProvider>);
}

beforeEach(() => {
	opened = [];
	detail = () =>
		Response.json({
			artifact: artifact(),
			version: 3,
			head: 3,
			files: [],
			capabilities: [],
			threads: [],
		});
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const path = new URL(req.url).pathname;
			if (path === `/api/v1/artifacts/${ID}` && req.method === "GET")
				return typeof detail === "function" ? detail() : detail;
			if (path === `/api/v1/artifacts/${ID}/frames`)
				return Response.json(
					{ url: FRAME_URL, token: "t".repeat(26) },
					{ status: 201 },
				);
			if (path === `/api/v1/artifacts/${ID}/open-link`)
				return Response.json({ url: "https://a-top.example.test/" });
			if (path === `/api/v1/artifacts/${ID}/restore`)
				return Response.json(artifact());
			return new Response(null, { status: 204 });
		}),
	);
	vi.spyOn(window, "open").mockImplementation((url) => {
		opened.push(String(url));
		return null;
	});
});

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

describe("ArtifactViewer", () => {
	it("顶栏显示标题与版本胶囊；挂载时观看、卸载时取消", async () => {
		const unwatch = vi.fn();
		const watch = vi
			.spyOn(realtime, "watchArtifact")
			.mockImplementation(() => unwatch);
		const view = wrap(
			<ArtifactViewer artifactId={ID} variant="page" onBack={vi.fn()} />,
		);
		await screen.findByText("本周习惯打卡");
		expect(screen.getByTestId("artifact-title")).toHaveTextContent(
			"本周习惯打卡",
		);
		expect(screen.getByTestId("artifact-version")).toHaveTextContent("v3");
		expect(watch).toHaveBeenCalledWith(ID, undefined);
		view.unmount();
		expect(unwatch).toHaveBeenCalled();
	});

	it("页面报告被拦截的跳转时显示提示条，可在浏览器中打开或关闭", async () => {
		wrap(<ArtifactViewer artifactId={ID} variant="panel" />);
		const frame = (await screen.findByTestId(
			"artifact-frame",
		)) as HTMLIFrameElement;
		const post = vi.fn();
		vi.spyOn(frame.contentWindow as Window, "postMessage").mockImplementation(
			post,
		);
		act(() => {
			frame.dispatchEvent(new Event("load"));
		});
		const channel = String(
			(
				post.mock.calls.map((c) => c[0]) as { kind: string; channel: string }[]
			).find((m) => m.kind === "hello")?.channel,
		);
		act(() => {
			window.dispatchEvent(
				new MessageEvent("message", {
					data: {
						v: 1,
						channel,
						kind: "blocked",
						url: "https://example.com/a?b=1",
					},
					source: frame.contentWindow,
				}),
			);
		});
		const bar = await screen.findByTestId("artifact-blocked");
		expect(bar).toHaveTextContent(
			i18n.t("artifacts.viewer.blocked", { host: "example.com" }),
		);
		fireEvent.click(
			screen
				.getAllByRole("button", {
					name: i18n.t("artifacts.viewer.openInBrowser"),
				})
				.find((b) => bar.contains(b)) as HTMLElement,
		);
		expect(opened).toEqual(["https://example.com/a?b=1"]);
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("common.close") }),
		);
		expect(screen.queryByTestId("artifact-blocked")).toBeNull();
	});

	it("非 http(s) 的被拦截地址直接丢弃", () => {
		expect(safeBlockedUrl("javascript:alert(1)")).toBeNull();
		expect(safeBlockedUrl("data:text/html,x")).toBeNull();
		expect(safeBlockedUrl("not a url")).toBeNull();
		expect(safeBlockedUrl("https://example.com")).toBe("https://example.com/");
	});

	it("在浏览器中打开：先申请一次性地址再 window.open（noopener）", async () => {
		wrap(<ArtifactViewer artifactId={ID} variant="page" />);
		fireEvent.click(
			await screen.findByRole("button", {
				name: i18n.t("artifacts.viewer.openInBrowser"),
			}),
		);
		await waitFor(() =>
			expect(opened).toEqual(["https://a-top.example.test/"]),
		);
		expect(window.open).toHaveBeenCalledWith(
			"https://a-top.example.test/",
			"_blank",
			"noopener",
		);
	});

	it("不在了：显示 not_found 空态", async () => {
		detail = () =>
			Response.json(
				{ code: "not_found", message: "artifact 不存在" },
				{ status: 404 },
			);
		wrap(<ArtifactViewer artifactId={ID} variant="page" />);
		expect(
			await screen.findByText(i18n.t("artifacts.viewer.notFound")),
		).toBeVisible();
		expect(screen.queryByTestId("artifact-frame")).toBeNull();
	});

	it("在回收站里：显示剩余天数与恢复，不申请帧", async () => {
		detail = () =>
			Response.json({
				artifact: artifact({ deletedAt: NOW - 3 * 86_400_000 }),
				version: 3,
				head: 3,
				files: [],
				capabilities: [],
				threads: [],
			});
		wrap(<ArtifactViewer artifactId={ID} variant="page" />);
		expect(
			await screen.findByText(i18n.t("artifacts.viewer.deleted")),
		).toBeVisible();
		expect(
			screen.getByText(i18n.t("artifacts.viewer.deletedDays", { count: 27 })),
		).toBeVisible();
		expect(screen.queryByTestId("artifact-frame")).toBeNull();
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("artifacts.viewer.restore") }),
		);
		await waitFor(() =>
			expect(
				vi
					.mocked(fetch)
					.mock.calls.some(([input]) =>
						String(input instanceof Request ? input.url : input).endsWith(
							`/${ID}/restore`,
						),
					),
			).toBe(true),
		);
	});

	it("申请帧失败：显示错误与重试", async () => {
		const base = vi.mocked(fetch).getMockImplementation();
		vi.mocked(fetch).mockImplementation(async (input, init) => {
			const req =
				input instanceof Request ? input : new Request(String(input), init);
			if (req.url.endsWith("/frames"))
				return Response.json(
					{ code: "internal", message: "boom" },
					{ status: 500 },
				);
			return (base as typeof fetch)(input, init);
		});
		wrap(<ArtifactViewer artifactId={ID} variant="page" />);
		expect(
			await screen.findByTestId("artifact-viewer-error"),
		).toHaveTextContent(i18n.t("artifacts.viewer.failed"));
		expect(
			screen.getByRole("button", { name: i18n.t("artifacts.viewer.retry") }),
		).toBeVisible();
	});
});
