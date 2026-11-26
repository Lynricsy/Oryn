/**
 * SharePopover（M14 8.3）：未发布时选访问级别发布（首次发布提示只出现一次）、三种访问级别切换各发一次 `PUT`、
 * 有访问码时复制的文本是两行、锁定警告、409 `secrets_found` 后勾选确认带 `acknowledgeFindings` 重发、
 * `pendingVersion` 的「查看并发布」；删除已发布的 artifact 弹确认框，默认带 `unpublish=true`。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../../api/client";
import i18n from "../../i18n";
import { dismissToast, toastsStore } from "../../state/toasts";
import { ArtifactMenu } from "./ArtifactMenu";
import { FIRST_PUBLISH_KEY, SharePanel } from "./SharePopover";

const ID = "art_01JDNAWEB0000000000000000A";
const SLUG = "abcdefghijklmnopqrstuvwxyz";
const URL_ = `http://localhost:7600/p/${SLUG}/`;

const ARTIFACT: Schemas["Artifact"] = {
	id: ID,
	origin: "agent",
	kind: "web",
	title: "本周习惯打卡",
	version: 3,
	size: 10,
	pages: 1,
	createdAt: 1,
	updatedAt: 2,
};

const SCAN: Schemas["ShareScan"] = {
	findings: [
		{
			rule: "api_key_sk",
			seq: 0,
			entryId: "index.html",
			preview: "sk-a…xy",
			count: 1,
		},
	],
	scanned: 1,
	truncated: false,
};

let pub: Schemas["ArtifactPublication"] | null;
let findings: boolean;
let puts: Schemas["ArtifactPublishRequest"][];
let requests: { method: string; url: URL }[];
let copied: string[];

function publication(
	over: Partial<Schemas["ArtifactPublication"]> = {},
): Schemas["ArtifactPublication"] {
	return {
		id: "apb_1",
		url: URL_,
		access: "private",
		codeLocked: false,
		version: 3,
		follow: true,
		visitors: 0,
		createdAt: Date.now() - 60_000,
		updatedAt: Date.now() - 60_000,
		...over,
	};
}

function wrap(node: ReactNode) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter>{node}</MemoryRouter>
		</QueryClientProvider>,
	);
}

function mountPanel(over: Partial<Schemas["Artifact"]> = {}) {
	return wrap(<SharePanel artifact={{ ...ARTIFACT, ...over }} />);
}

beforeEach(() => {
	pub = null;
	findings = false;
	puts = [];
	requests = [];
	copied = [];
	localStorage.removeItem(FIRST_PUBLISH_KEY);
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
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const url = new URL(req.url);
			requests.push({ method: req.method, url });
			const state = () =>
				Response.json({
					publication: pub,
					usageBytes: 0,
				} satisfies Schemas["ArtifactPublicationState"]);
			if (url.pathname === `/api/v1/artifacts/${ID}/publication`) {
				if (req.method === "GET") return state();
				if (req.method === "PUT") {
					const body = (await req.json()) as Schemas["ArtifactPublishRequest"];
					puts.push(body);
					if (
						findings &&
						body.access !== "private" &&
						body.acknowledgeFindings !== true
					)
						return Response.json(
							{ code: "secrets_found", message: "x", scan: SCAN },
							{ status: 409 },
						);
					const cur = pub ?? publication();
					pub = {
						...cur,
						access: body.access,
						version: body.version ?? cur.version,
						follow: body.follow ?? cur.follow,
						...(body.access === "code" ? { code: cur.code ?? "k7mq" } : {}),
					};
					if (body.version !== undefined) delete pub.pendingVersion;
					return state();
				}
				if (req.method === "DELETE") {
					pub = null;
					return new Response(null, { status: 204 });
				}
			}
			if (url.pathname === `/api/v1/artifacts/${ID}/publication/code`) {
				if (pub) pub = { ...pub, code: "zz99", codeLocked: false };
				return state();
			}
			return new Response(null, { status: 204 });
		}),
	);
});

afterEach(() => {
	for (const t of toastsStore.getState().toasts) dismissToast(t.id);
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

describe("SharePanel", () => {
	it("未发布：选访问码后发布，首次发布提示只出现一次", async () => {
		mountPanel();
		expect(
			await screen.findByText(i18n.t("artifacts.share.firstPublish")),
		).toBeInTheDocument();
		fireEvent.click(screen.getByTestId("share-access-code"));
		fireEvent.click(screen.getByTestId("share-publish"));
		await waitFor(() =>
			expect(screen.getByTestId("share-code")).toHaveTextContent("k7mq"),
		);
		expect(puts).toEqual([{ access: "code" }]);
		expect(localStorage.getItem(FIRST_PUBLISH_KEY)).toBe("1");
	});

	it("提示看过之后不再出现", async () => {
		localStorage.setItem(FIRST_PUBLISH_KEY, "1");
		mountPanel();
		await screen.findByTestId("share-publish");
		expect(
			screen.queryByText(i18n.t("artifacts.share.firstPublish")),
		).not.toBeInTheDocument();
	});

	it("三种访问级别切换各发一次 PUT，地址不变", async () => {
		pub = publication();
		mountPanel();
		await screen.findByTestId("share-url");
		for (const access of ["code", "public", "private"] as const) {
			fireEvent.click(screen.getByTestId(`share-access-${access}`));
			await waitFor(() => expect(puts.at(-1)).toEqual({ access }));
			await waitFor(() =>
				expect(
					screen.getByTestId(`share-access-${access}`).querySelector("input"),
				).toBeChecked(),
			);
		}
		expect(puts).toHaveLength(3);
		// 再点一次当前级别不发请求
		fireEvent.click(screen.getByTestId("share-access-private"));
		expect(puts).toHaveLength(3);
	});

	it("地址框默认省略协议与结尾斜杠，聚焦时显示全文", async () => {
		pub = publication();
		mountPanel();
		const input = (await screen.findByTestId("share-url")) as HTMLInputElement;
		expect(input.value).toBe(`localhost:7600/p/${SLUG}`);
		fireEvent.focus(input);
		expect(input.value).toBe(URL_);
	});

	it("有访问码时复制的文本是链接加一行访问码", async () => {
		pub = publication({ access: "code", code: "k7mq" });
		mountPanel();
		fireEvent.click(await screen.findByTestId("share-copy"));
		await waitFor(() => expect(copied).toHaveLength(1));
		const lines = (copied[0] ?? "").split("\n");
		expect(lines).toEqual([
			URL_,
			i18n.t("artifacts.share.codeLine", { code: "k7mq" }),
		]);
	});

	it("公开时复制的只是链接", async () => {
		pub = publication({ access: "public" });
		mountPanel();
		fireEvent.click(await screen.findByTestId("share-copy"));
		await waitFor(() => expect(copied).toEqual([URL_]));
	});

	it("锁定时显示警告，换一个访问码后解除", async () => {
		pub = publication({ access: "code", code: "k7mq", codeLocked: true });
		mountPanel();
		expect(await screen.findByTestId("share-locked")).toBeInTheDocument();
		fireEvent.click(screen.getByTestId("share-rotate"));
		await waitFor(() =>
			expect(screen.getByTestId("share-code")).toHaveTextContent("zz99"),
		);
		expect(screen.queryByTestId("share-locked")).not.toBeInTheDocument();
	});

	it("409 secrets_found：列出命中，勾选确认后带 acknowledgeFindings 重发", async () => {
		findings = true;
		mountPanel();
		fireEvent.click(await screen.findByTestId("share-access-public"));
		fireEvent.click(screen.getByTestId("share-publish"));
		expect(await screen.findByTestId("share-findings")).toHaveTextContent(
			"index.html",
		);
		const anyway = screen.getByTestId("share-publish-anyway");
		expect(anyway).toBeDisabled();
		fireEvent.click(
			screen.getByRole("checkbox", { name: i18n.t("share.findings.ack") }),
		);
		fireEvent.click(anyway);
		await screen.findByTestId("share-url");
		expect(puts).toEqual([
			{ access: "public" },
			{ access: "public", acknowledgeFindings: true },
		]);
	});

	it("没开跟随且有新版本：更新到最新版本", async () => {
		pub = publication({ follow: false, version: 1 });
		mountPanel();
		fireEvent.click(await screen.findByTestId("share-update"));
		await waitFor(() =>
			expect(puts).toEqual([{ access: "private", version: 3 }]),
		);
	});

	it("pendingVersion：查看并发布先扫描，确认后带 acknowledgeFindings 发布那个版本", async () => {
		findings = true;
		pub = publication({ access: "public", version: 2, pendingVersion: 3 });
		mountPanel();
		fireEvent.click(await screen.findByTestId("share-review"));
		await screen.findByTestId("share-findings");
		fireEvent.click(
			screen.getByRole("checkbox", { name: i18n.t("share.findings.ack") }),
		);
		fireEvent.click(screen.getByTestId("share-publish-anyway"));
		await waitFor(() =>
			expect(screen.queryByTestId("share-review")).toBeNull(),
		);
		expect(puts).toEqual([
			{ access: "public", version: 3 },
			{ access: "public", version: 3, acknowledgeFindings: true },
		]);
	});

	it("跟随开关发 PUT follow", async () => {
		pub = publication();
		mountPanel();
		fireEvent.click(
			await screen.findByRole("switch", {
				name: i18n.t("artifacts.share.follow"),
			}),
		);
		await waitFor(() =>
			expect(puts).toEqual([{ access: "private", follow: false }]),
		);
	});

	it("取消发布要确认，确认后回到未发布", async () => {
		pub = publication();
		mountPanel();
		fireEvent.click(await screen.findByTestId("share-unpublish"));
		expect(
			screen.getByText(i18n.t("artifacts.share.unpublishBody")),
		).toBeInTheDocument();
		fireEvent.click(screen.getByTestId("share-unpublish-confirm"));
		await screen.findByTestId("share-publish");
		expect(
			requests.some(
				(r) =>
					r.method === "DELETE" &&
					r.url.pathname === `/api/v1/artifacts/${ID}/publication`,
			),
		).toBe(true);
	});
});

describe("删除已发布的 artifact", () => {
	function deletes(): URL[] {
		return requests
			.filter(
				(r) =>
					r.method === "DELETE" && r.url.pathname === `/api/v1/artifacts/${ID}`,
			)
			.map((r) => r.url);
	}

	function openDelete() {
		wrap(
			<ArtifactMenu
				artifact={{ ...ARTIFACT, published: { access: "public" } }}
				place="card"
			/>,
		);
		fireEvent.click(screen.getByTestId("artifact-menu"));
		fireEvent.click(
			screen.getByRole("menuitem", { name: i18n.t("artifacts.menu.delete") }),
		);
	}

	it("弹确认框，默认勾选「同时取消发布」，删除带 unpublish=true", async () => {
		openDelete();
		const box = await screen.findByTestId("artifact-delete-unpublish");
		expect(box).toBeChecked();
		expect(deletes()).toEqual([]);
		fireEvent.click(screen.getByTestId("artifact-delete-confirm"));
		await waitFor(() => expect(deletes()).toHaveLength(1));
		expect(deletes()[0]?.searchParams.get("unpublish")).toBe("true");
		await waitFor(() =>
			expect(toastsStore.getState().toasts.map((t) => t.text)).toEqual([
				i18n.t("artifacts.toast.deleted", { title: ARTIFACT.title }),
			]),
		);
	});

	it("取消勾选后只删除、不取消发布", async () => {
		openDelete();
		fireEvent.click(await screen.findByTestId("artifact-delete-unpublish"));
		fireEvent.click(screen.getByTestId("artifact-delete-confirm"));
		await waitFor(() => expect(deletes()).toHaveLength(1));
		expect(deletes()[0]?.searchParams.has("unpublish")).toBe(false);
	});

	it("卡片菜单的「分享…」打开分享对话框", async () => {
		wrap(<ArtifactMenu artifact={ARTIFACT} place="card" />);
		fireEvent.click(screen.getByTestId("artifact-menu"));
		fireEvent.click(
			screen.getByRole("menuitem", { name: i18n.t("artifacts.share.menu") }),
		);
		expect(
			await screen.findByRole("dialog", {
				name: i18n.t("artifacts.share.title", { title: ARTIFACT.title }),
			}),
		).toBeInTheDocument();
		expect(await screen.findByTestId("share-publish")).toBeInTheDocument();
	});
});
