/**
 * 版本历史：作者名规则、列表从新到旧与「最新」标签、选旧版本后帧钉住该版本并出现状态条、恢复带当前 head 作
 * `baseVersion`、409 时提示重试。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../../api/client";
import i18n from "../../i18n";
import { indexStore } from "../../state/stores";
import { dismissToast, toastsStore } from "../../state/toasts";
import { ArtifactViewer } from "./ArtifactViewer";
import { authorName, VersionHistory } from "./VersionHistory";

const ID = "art_01JDNAWEB0000000000000000A";
const NOW = Date.now();

const VERSIONS: Schemas["ArtifactVersion"][] = [
	{
		version: 3,
		createdAt: NOW - 60_000,
		authorLabel: "",
		size: 3,
		pages: 1,
	},
	{
		version: 2,
		createdAt: NOW - 120_000,
		authorThreadId: "T-gone",
		authorLabel: "旧线程标题",
		note: "加上了图表",
		size: 2,
		pages: 1,
	},
	{
		version: 1,
		createdAt: NOW - 180_000,
		authorThreadId: "T-live",
		authorLabel: "改名前的标题",
		size: 1,
		pages: 1,
	},
];

let restoreStatus: number;
// 每次申请帧时钉住的版本（null = 跟随最新）
let frameBodies: (number | null)[];
let restoreBodies: { n: string; body: unknown }[];

function wrap(node: ReactNode) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter>{node}</MemoryRouter>
		</QueryClientProvider>,
	);
}

beforeEach(() => {
	restoreStatus = 200;
	frameBodies = [];
	restoreBodies = [];
	indexStore.setState({ threads: {} });
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const path = new URL(req.url).pathname;
			const artifact: Schemas["Artifact"] = {
				id: ID,
				origin: "agent",
				kind: "web",
				title: "本周习惯打卡",
				version: 3,
				size: 3,
				pages: 1,
				createdAt: 1,
				updatedAt: 2,
			};
			if (path === `/api/v1/artifacts/${ID}` && req.method === "GET")
				return Response.json({
					artifact,
					version: 3,
					head: 3,
					files: [],
					capabilities: [],
					threads: [
						{ threadId: "T-live", title: "现在的标题", archived: false },
					],
				});
			if (path === `/api/v1/artifacts/${ID}/versions`)
				return Response.json({ versions: VERSIONS });
			if (path === `/api/v1/artifacts/${ID}/frames`) {
				const body = (await req.json()) as { version?: number };
				frameBodies.push(body.version ?? null);
				return Response.json(
					{ url: "https://a-x.example.test/", token: "t".repeat(26) },
					{ status: 201 },
				);
			}
			const restore = /\/versions\/(\d+)\/restore$/.exec(path);
			if (restore) {
				restoreBodies.push({ n: restore[1] ?? "", body: await req.json() });
				return restoreStatus === 409
					? Response.json(
							{
								code: "conflict",
								message: "现在是版本 4",
								state: { version: 4, deleted: false, changed: ["index.html"] },
							},
							{ status: 409 },
						)
					: Response.json({ ...artifact, version: 4 });
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

describe("authorName", () => {
	const t = i18n.t.bind(i18n);
	it("没有作者线程是「你」；线程还在用当前标题；不在了用快照，再没有是「已删除的线程」", () => {
		const live = (id: string) => (id === "T-live" ? "现在的标题" : undefined);
		expect(authorName({ authorLabel: "" }, live, t)).toBe(
			i18n.t("artifacts.history.you"),
		);
		expect(
			authorName({ authorThreadId: "T-live", authorLabel: "旧" }, live, t),
		).toBe("现在的标题");
		expect(
			authorName({ authorThreadId: "T-gone", authorLabel: "旧" }, live, t),
		).toBe("旧");
		expect(
			authorName({ authorThreadId: "T-gone", authorLabel: "" }, live, t),
		).toBe(i18n.t("artifacts.history.deletedThread"));
	});
});

describe("VersionHistory", () => {
	it("从新到旧列出；最新带标签；有备注用备注作主标题；点一行回调", async () => {
		const onSelect = vi.fn();
		wrap(
			<VersionHistory
				artifactId={ID}
				head={3}
				threads={[{ threadId: "T-live", title: "现在的标题", archived: false }]}
				selected={3}
				onSelect={onSelect}
			/>,
		);
		const list = await screen.findByTestId("version-list");
		const rows = within(list).getAllByRole("button");
		expect(rows.map((r) => r.dataset.version)).toEqual(["3", "2", "1"]);
		expect(rows[0]).toHaveTextContent(i18n.t("artifacts.history.latest"));
		expect(rows[0]).toHaveTextContent(i18n.t("artifacts.history.you"));
		expect(rows[0]).toHaveAttribute("aria-current", "true");
		expect(rows[1]).toHaveTextContent("加上了图表");
		expect(rows[1]).toHaveTextContent("旧线程标题");
		expect(rows[1]).not.toHaveTextContent(i18n.t("artifacts.history.latest"));
		expect(rows[2]).toHaveTextContent("现在的标题");
		fireEvent.click(rows[2] as HTMLElement);
		expect(onSelect).toHaveBeenCalledWith(VERSIONS[2]);
	});
});

describe("查看器里的版本历史", () => {
	async function pinV1() {
		wrap(<ArtifactViewer artifactId={ID} variant="page" initialHistory />);
		const list = await screen.findByTestId("version-list");
		await waitFor(() => expect(frameBodies).toEqual([null]));
		fireEvent.click(
			within(list)
				.getAllByRole("button")
				.find((b) => b.dataset.version === "1") as HTMLElement,
		);
	}

	it("选旧版本：帧以该版本重新申请，出现状态条；回到最新再跟随", async () => {
		await pinV1();
		await waitFor(() => expect(frameBodies).toEqual([null, 1]));
		const bar = screen.getByTestId("artifact-pinned");
		expect(bar).toHaveTextContent(/1.*3/);
		fireEvent.click(
			within(bar).getByRole("button", {
				name: i18n.t("artifacts.history.backToLatest"),
			}),
		);
		expect(screen.queryByTestId("artifact-pinned")).toBeNull();
		await waitFor(() => expect(frameBodies).toEqual([null, 1, null]));
	});

	it("恢复这个版本：baseVersion 是当前 head，成功后回到跟随最新", async () => {
		await pinV1();
		fireEvent.click(await screen.findByTestId("artifact-restore-version"));
		await waitFor(() =>
			expect(restoreBodies).toEqual([{ n: "1", body: { baseVersion: 3 } }]),
		);
		await waitFor(() =>
			expect(screen.queryByTestId("artifact-pinned")).toBeNull(),
		);
	});

	it("409：提示恢复前来了新版本，仍停在旧版本", async () => {
		restoreStatus = 409;
		await pinV1();
		fireEvent.click(await screen.findByTestId("artifact-restore-version"));
		await waitFor(() =>
			expect(toastsStore.getState().toasts.map((t) => t.text)).toContain(
				i18n.t("artifacts.history.conflict"),
			),
		);
		expect(screen.getByTestId("artifact-pinned")).toBeVisible();
	});

	it("版本胶囊与顶栏按钮开关历史侧栏", async () => {
		wrap(<ArtifactViewer artifactId={ID} variant="page" />);
		const pill = await screen.findByTestId("artifact-version");
		expect(screen.queryByTestId("version-history")).toBeNull();
		fireEvent.click(pill);
		expect(await screen.findByTestId("version-history")).toBeVisible();
		fireEvent.click(screen.getByTestId("artifact-history-toggle"));
		expect(screen.queryByTestId("version-history")).toBeNull();
	});
});
