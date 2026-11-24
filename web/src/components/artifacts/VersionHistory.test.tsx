/**
 * 版本历史：作者名规则、列表从新到旧与「最新」标签、按回合折叠（相邻同回合成组、不相邻不合并、无回合单列、标题回退、
 * 展开选中组内版本、选中时自动展开）、检查徽标四态、选旧版本后帧钉住该版本并出现状态条、恢复带当前 head 作
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
import { authorName, groupByTurn, VersionHistory } from "./VersionHistory";

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
// 版本接口返回的列表（缺省 VERSIONS）
let versionList: Schemas["ArtifactVersion"][];
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
	versionList = VERSIONS;
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
				return Response.json({ versions: versionList });
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

describe("按回合折叠", () => {
	const at = (n: number) => NOW - (10 - n) * 60_000;
	const v = (
		version: number,
		extra: Partial<Schemas["ArtifactVersion"]> = {},
	): Schemas["ArtifactVersion"] => ({
		version,
		createdAt: at(version),
		authorThreadId: "T-live",
		authorLabel: "改名前的标题",
		size: 1,
		pages: 1,
		...extra,
	});
	// v8–v6 同一回合 A；v5 无回合；v4 回合 B 只有一个版本；v3–v2 又是回合 A（与上面不相邻）；v1 无回合、用户写的
	const TURNED = [
		v(8, {
			turnId: "A",
			title: "加暗色模式",
			note: "收尾",
			check: { status: "clean", issues: [] },
		}),
		v(7, { turnId: "A", note: "调整按钮" }),
		v(6, { turnId: "A" }),
		v(5, { note: "手改", check: { status: "stale", issues: [] } }),
		v(4, {
			turnId: "B",
			title: "做登录页",
			note: "登录页初稿",
			check: {
				status: "issues",
				issues: [{ rule: "text-clipped" }, { rule: "contrast-low" }],
			},
		}),
		v(3, { turnId: "A", title: "再改暗色" }),
		v(2, { turnId: "A" }),
		v(1, {
			authorThreadId: undefined,
			authorLabel: "",
			check: { status: "unverified", issues: [] },
		}),
	];
	const THREADS = [
		{ threadId: "T-live", title: "现在的标题", archived: false },
	];

	function renderHistory(selected: number, onSelect = vi.fn()) {
		versionList = TURNED;
		const qc = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		const wrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={qc}>
				<MemoryRouter>{children}</MemoryRouter>
			</QueryClientProvider>
		);
		const ui = (sel: number) => (
			<VersionHistory
				artifactId={ID}
				head={8}
				threads={THREADS}
				selected={sel}
				onSelect={onSelect}
			/>
		);
		const r = render(ui(selected), { wrapper });
		return { onSelect, rerender: (sel: number) => r.rerender(ui(sel)) };
	}

	/** 顶层各项：组 = `g:<turn>`，单行 = `v<n>`。 */
	function topLevel(list: HTMLElement): string[] {
		return Array.from(list.children).map((li) => {
			const el = li as HTMLElement;
			if (el.dataset.testid === "version-group") return `g:${el.dataset.turn}`;
			return `v${el.querySelector<HTMLElement>("[data-version]")?.dataset.version}`;
		});
	}
	const groups = () => screen.getAllByTestId("version-group");
	const header = (g: HTMLElement) =>
		within(g).getAllByRole("button")[0] as HTMLElement;
	const versionRow = (n: number) =>
		document.querySelector<HTMLElement>(`[data-version="${n}"]`);

	it("相邻同回合折叠成组；不相邻的同回合不合并；无回合与单版本回合各自一行", async () => {
		renderHistory(8);
		const list = await screen.findByTestId("version-list");
		expect(topLevel(list)).toEqual(["g:A", "v5", "v4", "g:A", "v1"]);
		const [first, second] = groups() as [HTMLElement, HTMLElement];
		expect(header(first)).toHaveTextContent("v8–v6");
		expect(header(second)).toHaveTextContent("v3–v2");
		// 收起时组内各版本不渲染
		expect(header(first)).toHaveAttribute("aria-expanded", "false");
		expect(versionRow(7)).toBeNull();
		expect(versionRow(2)).toBeNull();
	});

	it("主标题：title → note → 作者名；组头取组内最新版本，组头带作者与时间", async () => {
		renderHistory(8);
		await screen.findByTestId("version-list");
		const [first, second] = groups() as [HTMLElement, HTMLElement];
		expect(header(first)).toHaveTextContent("加暗色模式");
		expect(header(first)).not.toHaveTextContent("收尾");
		expect(header(first)).toHaveTextContent("现在的标题");
		expect(header(first)).toHaveTextContent(i18n.t("artifacts.history.latest"));
		expect(header(second)).toHaveTextContent("再改暗色");
		expect(versionRow(4)).toHaveTextContent("做登录页");
		expect(versionRow(4)).not.toHaveTextContent("登录页初稿");
		expect(versionRow(5)).toHaveTextContent("手改");
		expect(versionRow(1)).toHaveTextContent(i18n.t("artifacts.history.you"));
	});

	it("点组头展开，可选中组内版本；再点收起", async () => {
		const { onSelect } = renderHistory(8);
		await screen.findByTestId("version-list");
		const first = groups()[0] as HTMLElement;
		fireEvent.click(header(first));
		expect(header(first)).toHaveAttribute("aria-expanded", "true");
		const inner = within(first)
			.getAllByRole("button")
			.slice(1)
			.map((b) => b.dataset.version);
		expect(inner).toEqual(["8", "7", "6"]);
		// 展开后「最新」标签落在 v8 那一行，组头不再重复
		expect(header(first)).not.toHaveTextContent(
			i18n.t("artifacts.history.latest"),
		);
		expect(versionRow(8)).toHaveTextContent(i18n.t("artifacts.history.latest"));
		expect(versionRow(8)).toHaveAttribute("aria-current", "true");
		// 组内行主标题不重复回合标题：note → 作者名
		expect(versionRow(8)).toHaveTextContent("收尾");
		expect(versionRow(7)).toHaveTextContent("调整按钮");
		expect(versionRow(6)).toHaveTextContent("现在的标题");
		fireEvent.click(versionRow(7) as HTMLElement);
		expect(onSelect).toHaveBeenCalledWith(TURNED[1]);
		fireEvent.click(header(first));
		expect(versionRow(7)).toBeNull();
	});

	it("选中组内的旧版本：组自动展开并高亮该行", async () => {
		const { rerender } = renderHistory(3);
		await screen.findByTestId("version-list");
		const [first, second] = groups() as [HTMLElement, HTMLElement];
		expect(header(second)).toHaveAttribute("aria-expanded", "true");
		expect(header(first)).toHaveAttribute("aria-expanded", "false");
		expect(versionRow(3)).toHaveAttribute("aria-current", "true");
		// 收起后选中落在组内：组头代为高亮；换选中到另一组的旧版本 → 那一组也展开
		fireEvent.click(header(second));
		expect(header(second)).toHaveClass("bg-active");
		rerender(6);
		expect(header(first)).toHaveAttribute("aria-expanded", "true");
		expect(versionRow(6)).toHaveAttribute("aria-current", "true");
		expect(versionRow(3)).toBeNull();
		// 手动收起过的组，选中再次落进来时重新展开
		rerender(2);
		expect(header(second)).toHaveAttribute("aria-expanded", "true");
		expect(versionRow(2)).toHaveAttribute("aria-current", "true");
	});

	it("检查徽标四态；issues 带条数；没有 check 不显示", async () => {
		renderHistory(8);
		await screen.findByTestId("version-list");
		const badge = (el: HTMLElement | null) =>
			el && within(el).queryByTestId("version-check");
		const firstHeader = header(groups()[0] as HTMLElement);
		expect(badge(firstHeader)).toHaveAttribute("data-status", "clean");
		expect(badge(firstHeader)).toHaveTextContent(
			i18n.t("artifacts.history.check.clean"),
		);
		expect(badge(versionRow(5))).toHaveAttribute("data-status", "stale");
		expect(badge(versionRow(5))).toHaveTextContent(
			i18n.t("artifacts.history.check.stale"),
		);
		expect(badge(versionRow(4))).toHaveAttribute("data-status", "issues");
		expect(badge(versionRow(4))).toHaveTextContent(
			i18n.t("artifacts.history.check.issues", { count: 2 }),
		);
		expect(badge(versionRow(4))).toHaveTextContent("2");
		expect(badge(versionRow(1))).toHaveAttribute("data-status", "unverified");
		expect(badge(versionRow(1))).toHaveTextContent(
			i18n.t("artifacts.history.check.unverified"),
		);
		expect(badge(header(groups()[1] as HTMLElement))).toBeNull();
	});
});

describe("groupByTurn", () => {
	it("新 → 旧逐个看：同回合相邻成组，换回合或无回合就断开", () => {
		const mk = (version: number, turnId?: string) => ({
			version,
			createdAt: 0,
			authorLabel: "",
			size: 0,
			pages: 0,
			...(turnId ? { turnId } : {}),
		});
		const shape = groupByTurn([
			mk(6, "A"),
			mk(5, "A"),
			mk(4),
			mk(3),
			mk(2, "A"),
			mk(1, "B"),
		]).map((e) =>
			e.kind === "turn"
				? `${e.turnId}[${e.versions.map((x) => x.version).join(",")}]`
				: `v${e.version.version}`,
		);
		expect(shape).toEqual(["A[6,5]", "v4", "v3", "v2", "v1"]);
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
