/**
 * 设置 › 分享 的 Artifacts 两块（M14 8.4）：自动发布开关发 `PUT /settings/artifacts`；「已发布的 Artifacts」列出标题、
 * 访问级别、版本、访客数，复制链接（有访问码时两行），两步取消发布；空、加载失败两种占位。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../../api/client";
import i18n from "../../i18n";
import { ArtifactPublishing } from "./ArtifactPublishing";

const ID = "art_01JDNADOC0000000000000000B";
const URL_ = "http://localhost:7600/p/abcdefghijklmnopqrstuvwxyz/";

const ITEM: Schemas["ArtifactPublicationListItem"] = {
	artifact: {
		id: ID,
		origin: "agent",
		kind: "document",
		type: "document",
		title: "重连机制设计说明",
		version: 4,
		size: 10,
		pages: 1,
		createdAt: 1,
		updatedAt: 2,
		published: { access: "code" },
	},
	publication: {
		id: "apb_1",
		url: URL_,
		access: "code",
		code: "k7mq",
		codeLocked: false,
		version: 3,
		follow: true,
		visitors: 2,
		createdAt: 1,
		updatedAt: 2,
	},
};

let prefs: Schemas["ArtifactPrefs"];
let list: Schemas["ArtifactPublicationListItem"][] | "fail";
let puts: unknown[];
let deletes: string[];
let copied: string[];

function mount() {
	render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<MemoryRouter>
				<ArtifactPublishing />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

beforeEach(() => {
	prefs = { autoPublish: false };
	list = [ITEM];
	puts = [];
	deletes = [];
	copied = [];
	Object.defineProperty(navigator, "clipboard", {
		configurable: true,
		value: {
			writeText: vi.fn(async (s: string) => {
				copied.push(s);
			}),
		},
	});
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: Request) => {
			const u = new URL(input.url);
			if (u.pathname === "/api/v1/settings/artifacts") {
				if (input.method === "PUT") {
					prefs = (await input.json()) as Schemas["ArtifactPrefs"];
					puts.push(prefs);
				}
				return Response.json(prefs);
			}
			if (u.pathname === "/api/v1/artifact-publications")
				return list === "fail"
					? Response.json(
							{ code: "internal", message: "数据库忙" },
							{ status: 500 },
						)
					: Response.json({ publications: list });
			if (input.method === "DELETE") {
				deletes.push(u.pathname);
				list = [];
				return new Response(null, { status: 204 });
			}
			return new Response(null, { status: 404 });
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("ArtifactPublishing", () => {
	it("自动发布开关发 PUT，并显示服务端返回的状态", async () => {
		mount();
		const toggle = await screen.findByRole("switch", {
			name: i18n.t("artifacts.share.settings.autoPublish"),
		});
		expect(toggle).not.toBeChecked();
		fireEvent.click(toggle);
		await waitFor(() => expect(puts).toEqual([{ autoPublish: true }]));
		await waitFor(() => expect(toggle).toBeChecked());
	});

	it("列出已发布的 artifact：标题、访问级别、版本、访客数；复制链接带访问码", async () => {
		mount();
		const row = await screen.findByTestId("artifact-publication");
		expect(
			screen.getByRole("link", { name: ITEM.artifact.title }),
		).toHaveAttribute("href", `/artifacts/${ID}`);
		expect(row).toHaveTextContent(i18n.t("artifacts.share.access.code"));
		expect(row).toHaveTextContent(i18n.t("artifacts.viewer.version", { n: 3 }));
		expect(row).toHaveTextContent(
			i18n.t("artifacts.share.visitors", { count: 2 }),
		);
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("artifacts.share.copyLink") }),
		);
		await waitFor(() =>
			expect(copied).toEqual([
				`${URL_}\n${i18n.t("artifacts.share.codeLine", { code: "k7mq" })}`,
			]),
		);
	});

	it("取消发布要点两次，之后列表为空", async () => {
		mount();
		await screen.findByTestId("artifact-publication");
		fireEvent.click(
			screen.getByRole("button", {
				name: i18n.t("artifacts.share.unpublishConfirm"),
			}),
		);
		expect(deletes).toEqual([]);
		fireEvent.click(
			screen.getByRole("button", {
				name: i18n.t("artifacts.share.settings.unpublishConfirm"),
			}),
		);
		await waitFor(() =>
			expect(deletes).toEqual([`/api/v1/artifacts/${ID}/publication`]),
		);
		expect(
			await screen.findByText(i18n.t("artifacts.share.settings.empty")),
		).toBeInTheDocument();
	});

	it("没有发布时显示空态", async () => {
		list = [];
		mount();
		expect(
			await screen.findByText(i18n.t("artifacts.share.settings.empty")),
		).toBeInTheDocument();
	});

	it("列表加载失败时显示原因", async () => {
		list = "fail";
		mount();
		expect(await screen.findByText(/数据库忙/)).toBeInTheDocument();
	});
});
