/**
 * 私有发布页的登录中转：换到授权后整页跳转（location.replace）；发布记录不存在时显示「已不在线上」；
 * 其它错误可以重试。
 */
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../i18n";
import { ArtifactOpenPage, publishedPage } from "./ArtifactOpenPage";

const SLUG = "abcdefghijklmnopqrstuvwxyz";
const GRANTED = `https://p-${SLUG}.example.test/~g.s/`;

let reply: () => Response;
let calls: string[];

beforeEach(() => {
	calls = [];
	reply = () => Response.json({ url: GRANTED });
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			calls.push(`${req.method} ${new URL(req.url).pathname}`);
			return reply();
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

function renderAt(slug = SLUG) {
	return render(
		<MemoryRouter initialEntries={[`/artifacts/open/${slug}`]}>
			<Routes>
				<Route path="/artifacts/open/:slug" element={<ArtifactOpenPage />} />
			</Routes>
		</MemoryRouter>,
	);
}

describe("ArtifactOpenPage", () => {
	it("换到授权后用 location.replace 打开发布页", async () => {
		const replace = vi
			.spyOn(publishedPage, "replace")
			.mockImplementation(() => {});
		renderAt();
		expect(
			screen.getByRole("status", { name: i18n.t("artifacts.open.loading") }),
		).toBeInTheDocument();
		await waitFor(() => expect(replace).toHaveBeenCalledWith(GRANTED));
		expect(calls).toEqual([`POST /api/v1/artifact-publications/${SLUG}/grant`]);
	});

	it("发布记录不存在（404）时显示「已不在线上」，不跳转", async () => {
		const replace = vi
			.spyOn(publishedPage, "replace")
			.mockImplementation(() => {});
		reply = () =>
			Response.json(
				{ code: "not_found", message: "这个页面已不在线上。" },
				{ status: 404 },
			);
		renderAt();
		expect(
			await screen.findByText(i18n.t("artifacts.open.gone")),
		).toBeInTheDocument();
		expect(replace).not.toHaveBeenCalled();
	});

	it("其它错误可以重试", async () => {
		const replace = vi
			.spyOn(publishedPage, "replace")
			.mockImplementation(() => {});
		reply = () =>
			Response.json({ code: "internal", message: "boom" }, { status: 500 });
		renderAt();
		const retry = await screen.findByRole("button", {
			name: i18n.t("artifacts.open.retry"),
		});
		reply = () => Response.json({ url: GRANTED });
		fireEvent.click(retry);
		await waitFor(() => expect(replace).toHaveBeenCalledWith(GRANTED));
		expect(calls).toHaveLength(2);
	});
});
