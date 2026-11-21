/** Boxes 总览页（M23）：没有 box 时是介绍页（新建 Box + 阅读文档）；有 box 时列出并可进入，页头有新建按钮。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Box } from "../api/box-queries";
import i18n from "../i18n";
import { BoxesPage } from "./BoxesPage";

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
	apps: [],
};

let boxes: Box[];

beforeEach(() => {
	boxes = [];
	vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
		const url = new URL(input instanceof Request ? input.url : String(input));
		if (url.pathname === "/api/v1/boxes") return Response.json({ boxes });
		if (url.pathname === "/api/v1/box-activity")
			return Response.json({
				items: boxes.length
					? [
							{
								id: "a:1",
								boxId: "box_1",
								boxName: "workbench",
								ts: Date.now() - 60_000,
								kind: "created",
								actor: "owner",
							},
						]
					: [],
			});
		if (url.pathname === "/api/v1/providers")
			return Response.json({ providers: [] });
		return Response.json({ code: "not_found", message: "" }, { status: 404 });
	});
});

afterEach(() => vi.unstubAllGlobals());

function Where() {
	return <span data-testid="where">{useLocation().pathname}</span>;
}

function mount() {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={["/boxes"]}>
				<Routes>
					<Route path="/boxes" element={<BoxesPage />} />
					<Route path="*" element={null} />
				</Routes>
				<Where />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

describe("Boxes 总览", () => {
	it("没有 box：介绍页带「新建 Box」与文档链接，页头不另放新建按钮", async () => {
		mount();
		const hero = await screen.findByTestId("boxes-hero");
		expect(
			within(hero).getByRole("button", { name: i18n.t("boxes.new.open") }),
		).toBeInTheDocument();
		const docs = within(hero).getByRole("link", {
			name: i18n.t("boxes.hero.docs"),
		});
		expect(docs).toHaveAttribute(
			"href",
			expect.stringContaining("docs/features.md#boxes"),
		);
		expect(
			screen.getAllByRole("button", { name: i18n.t("boxes.new.open") }),
		).toHaveLength(1);
		// 点主按钮打开新建对话框
		fireEvent.click(
			within(hero).getByRole("button", { name: i18n.t("boxes.new.open") }),
		);
		expect(
			await screen.findByRole("dialog", { name: i18n.t("boxes.new.title") }),
		).toBeInTheDocument();
	});

	it("有 box：列出并可进入，最近活动带 box 名", async () => {
		boxes = [BOX];
		mount();
		const list = await screen.findByTestId("boxes-list");
		expect(screen.queryByTestId("boxes-hero")).toBeNull();
		expect(await screen.findByTestId("box-activity")).toHaveTextContent(
			"workbench",
		);
		fireEvent.click(within(list).getByRole("button", { name: /workbench/ }));
		expect(screen.getByTestId("where")).toHaveTextContent("/boxes/box_1");
	});
});
