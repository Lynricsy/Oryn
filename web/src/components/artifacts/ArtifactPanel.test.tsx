/**
 * ArtifactPanel：切换菜单列出线程的全部 artifact、选中项不在列表里时取最近更新的一个、「放大查看」跳整页。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../../api/client";
import i18n from "../../i18n";
import { uiStore } from "../../state/ui";
import ArtifactPanel from "./ArtifactPanel";

const T = "T-artpanel";
const A = "art_01JDNAWEB0000000000000000A";
const B = "art_01JDNADOC0000000000000000B";

function art(
	id: string,
	title: string,
	kind: Schemas["ArtifactKind"],
	updatedAt: number,
): Schemas["Artifact"] {
	return {
		id,
		origin: "agent",
		kind,
		title,
		version: 1,
		size: 1,
		pages: 1,
		createdAt: 1,
		updatedAt,
		threadId: T,
	};
}

let detailRequests: string[];

function Where() {
	return <span data-testid="where">{useLocation().pathname}</span>;
}

function mount() {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={[`/t/${T}`]}>
				<ArtifactPanel threadId={T} />
				<Routes>
					<Route path="*" element={<Where />} />
				</Routes>
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

beforeEach(() => {
	detailRequests = [];
	uiStore.setState({ artifactSel: {} });
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const path = new URL(req.url).pathname;
			if (path === "/api/v1/artifacts")
				// 最近更新的在前
				return Response.json({
					artifacts: [
						art(B, "设计说明", "document", 20),
						art(A, "本周习惯打卡", "web", 10),
					],
				});
			const one = /^\/api\/v1\/artifacts\/(art_[0-9A-Z]{26})$/.exec(path);
			if (one?.[1]) {
				detailRequests.push(one[1]);
				const a =
					one[1] === A
						? art(A, "本周习惯打卡", "web", 10)
						: art(B, "设计说明", "document", 20);
				return Response.json({
					artifact: a,
					version: 1,
					head: 1,
					files: [],
					capabilities: [],
					threads: [],
				});
			}
			if (path.endsWith("/frames"))
				return Response.json(
					{ url: "https://a-x.example.test/", token: "x".repeat(26) },
					{ status: 201 },
				);
			return new Response(null, { status: 204 });
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("ArtifactPanel", () => {
	it("没有选中项时显示最近更新的一个；切换菜单列出全部并能切换", async () => {
		mount();
		const trigger = await screen.findByTestId("artifact-switch");
		expect(trigger).toHaveTextContent("设计说明");
		expect(screen.getByRole("toolbar")).toContainElement(trigger);
		fireEvent.click(trigger);
		const items = screen.getAllByRole("menuitemradio");
		expect(items.map((i) => i.textContent)).toEqual([
			"设计说明",
			"本周习惯打卡",
		]);
		expect(items[0]).toHaveAttribute("aria-checked", "true");
		act(() => {
			fireEvent.click(items[1] as HTMLElement);
		});
		expect(uiStore.getState().artifactSel[T]).toBe(A);
		await waitFor(() =>
			expect(screen.getByTestId("artifact-switch")).toHaveTextContent(
				"本周习惯打卡",
			),
		);
		await waitFor(() => expect(detailRequests).toContain(A));
	});

	it("选中项不在列表里时取最近更新的一个", async () => {
		uiStore.setState({
			artifactSel: { [T]: "art_01JDNAGONE00000000000000000" },
		});
		mount();
		expect(await screen.findByTestId("artifact-switch")).toHaveTextContent(
			"设计说明",
		);
	});

	it("放大查看跳到整页查看器", async () => {
		uiStore.setState({ artifactSel: { [T]: A } });
		mount();
		await screen.findByTestId("artifact-switch");
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("artifacts.panel.expand") }),
		);
		expect(screen.getByTestId("where")).toHaveTextContent(`/artifacts/${A}`);
	});
});
