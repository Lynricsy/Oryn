import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { statsWindow } from "../components/stats/statsParams";
import {
	MAIN_THREAD,
	makeOrb,
	projects,
	spaces,
	threads,
} from "../mock/fixtures";
import { handleStatsFetch, resetStatsMock } from "../mock/stats";
import {
	type AppNotification,
	notificationsStore,
} from "../state/notifications";
import { StatsPage } from "./StatsPage";

const ORBS = [makeOrb("orb-aaa", [MAIN_THREAD]), makeOrb("orb-bbb", [])];
const deps = {
	threads: () => threads,
	projects: () => projects,
	spaces: () => spaces,
	orbs: () => ORBS,
};

let urls: URL[];
let posts: { path: string; body: unknown }[];
/** 每个测试可以替换某个路径的应答（返回 undefined = 走 mock）。 */
let override: (url: URL) => Response | undefined;

beforeEach(() => {
	urls = [];
	posts = [];
	override = () => undefined;
	resetStatsMock();
	vi.stubGlobal(
		"fetch",
		vi.fn(async (req: Request) => {
			const url = new URL(req.url);
			const p = url.pathname.replace(/^\/api\/v1\//, "");
			if (req.method === "POST") {
				posts.push({ path: url.pathname, body: await req.json() });
				return new Response(null, { status: 204 });
			}
			if (p === "notifications")
				return Response.json({ notifications: [], unread: 0 });
			urls.push(url);
			const o = override(url);
			if (o) return o;
			if (p === "projects") return Response.json({ projects });
			if (p === "spaces") return Response.json({ spaces });
			if (/^orbs\/[^/]+\/metrics$/.test(p))
				return Response.json({ orbId: "x", step: 15, samples: [] });
			if (/^orbs\/[^/]+\/setup-runs$/.test(p))
				return Response.json({ runs: [] });
			return (
				(await handleStatsFetch(req, p, req.method, url, deps)) ??
				Response.json({ code: "not_found", message: p }, { status: 404 })
			);
		}),
	);
	notificationsStore.setState({ items: [], unread: 0, toasts: [] });
});

afterEach(() => {
	vi.unstubAllGlobals();
});

function Where() {
	const l = useLocation();
	return <span data-testid="where">{`${l.pathname}${l.search}`}</span>;
}

function mount(path: string) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	render(
		<MemoryRouter initialEntries={[path]}>
			<QueryClientProvider client={client}>
				<Routes>
					<Route
						path="/stats/:tab"
						element={
							<>
								<StatsPage />
								<Where />
							</>
						}
					/>
					<Route path="*" element={<Where />} />
				</Routes>
			</QueryClientProvider>
		</MemoryRouter>,
	);
}

/** 某个接口发出的查询参数（去掉了值为空的）。 */
function queries(path: string): Record<string, string>[] {
	return urls
		.filter((u) => u.pathname === `/api/v1/${path}`)
		.map((u) => Object.fromEntries(u.searchParams));
}

const w30 = () => statsWindow({ range: "30d" }, Date.now());

describe("StatsPage 用量", () => {
	it("发出的请求：主图（by + series + limit 8）、本期与上一周期总计、五张分组表与工具表", async () => {
		mount("/stats/usage");
		expect(await screen.findByTestId("kpi-cost")).toBeInTheDocument();
		const w = w30();
		const base = { from: String(w.from), to: String(w.to), tz: w.tz };
		await waitFor(() => expect(queries("stats/usage")).toHaveLength(8));
		const got = queries("stats/usage");
		// 指标行取本期 groupBy=total（按模型分组时工具与回合为 0）
		expect(got).toContainEqual({ ...base, groupBy: "total" });
		expect(got).toContainEqual({
			...base,
			groupBy: "model",
			series: "day",
			limit: "8",
		});
		expect(got).toContainEqual({
			from: String(w.from - (w.to - w.from)),
			to: String(w.from),
			tz: w.tz,
			groupBy: "total",
		});
		for (const g of ["model", "project", "space", "thread", "purpose"])
			expect(got).toContainEqual({ ...base, groupBy: g, limit: "20" });
		expect(queries("stats/tools")).toContainEqual({ ...base, limit: "20" });
	});

	it("指标的成本等于主图应答的 total.costUsd；柱子数 = 窗口天数", async () => {
		mount("/stats/usage?range=7d");
		const kpi = await screen.findByTestId("kpi-cost");
		const res = await (
			await handleStatsFetch(
				new Request("http://x"),
				"stats/usage",
				"GET",
				urls.find((u) => u.searchParams.get("series") === "day") as URL,
				deps,
			)
		)?.json();
		const cost = res.total.costUsd as number;
		expect(kpi).toHaveTextContent(`$${cost.toFixed(2)}`);
		expect(
			screen.getByTestId("stats-chart").querySelectorAll("g[data-bucket]"),
		).toHaveLength(7);
		// 指标格的直接父元素是 grid
		expect(kpi.parentElement?.className).toMatch(/\bgrid\b/);
	});

	it("点项目表的一行设为筛选：URL 带 project，之后的请求都带它", async () => {
		mount("/stats/usage");
		const table = await screen.findByTestId("table-project");
		const pid = projects[0]?.id as string;
		const row = await within(table).findByRole("button", {
			name: new RegExp(projects[0]?.name ?? ""),
		});
		expect(row).toHaveAttribute("data-row-key", pid);
		fireEvent.click(row);
		expect(screen.getByTestId("where")).toHaveTextContent(
			`/stats/usage?project=${pid}`,
		);
		await waitFor(() =>
			expect(
				queries("stats/usage").some(
					(q) => q.project === pid && q.groupBy === "model" && q.series,
				),
			).toBe(true),
		);
		// 筛选胶囊带清除
		fireEvent.click(
			screen.getByRole("button", {
				name: `清除筛选「${projects[0]?.name}」`,
			}),
		);
		expect(screen.getByTestId("where")).toHaveTextContent(/^\/stats\/usage$/);
	});

	it("导出菜单：当前视图 CSV / JSON 与用量明细 CSV 都是带 download 的同源链接", async () => {
		mount("/stats/usage?range=7d&by=project");
		fireEvent.click(await screen.findByTestId("stats-export"));
		const w = statsWindow({ range: "7d" }, Date.now());
		const href = (k: string) =>
			new URL(
				document.querySelector(`[data-export=${k}]`)?.getAttribute("href") ??
					"",
				"http://x",
			);
		for (const k of ["view-csv", "view-json", "records-csv"])
			expect(document.querySelector(`[data-export=${k}]`)).toHaveAttribute(
				"download",
			);
		const csv = href("view-csv");
		expect(csv.pathname).toBe("/api/v1/stats/usage");
		expect(Object.fromEntries(csv.searchParams)).toEqual({
			from: String(w.from),
			to: String(w.to),
			tz: w.tz,
			groupBy: "project",
			series: "day",
			format: "csv",
		});
		expect(href("view-json").searchParams.get("download")).toBe("true");
		expect(href("view-json").searchParams.get("format")).toBeNull();
		const rec = href("records-csv");
		expect(rec.pathname).toBe("/api/v1/stats/usage/records");
		expect(Object.fromEntries(rec.searchParams)).toEqual({
			from: String(w.from),
			to: String(w.to),
		});
	});

	it("回填未完成时页头下方提示 n / m", async () => {
		override = (u) =>
			u.pathname === "/api/v1/stats/meta"
				? Response.json({
						now: Date.now(),
						factsVersion: 1,
						backfill: { done: 3, total: 10 },
						retention: { samplesMs: 0, eventsMs: 0, usageMs: 0 },
					})
				: undefined;
		mount("/stats/usage");
		expect(
			await screen.findByText(/历史数据回填中 3 \/ 10/),
		).toBeInTheDocument();
	});

	it("空数据：指标为 0、图下提示「这段时间没有数据」", async () => {
		override = (u) => {
			if (u.pathname !== "/api/v1/stats/usage") return undefined;
			const zero = {
				key: "total",
				requests: 0,
				requestErrors: 0,
				inputTokens: 0,
				outputTokens: 0,
				cacheReadTokens: 0,
				cacheWriteTokens: 0,
				costUsd: 0,
				toolCalls: 0,
				toolErrors: 0,
				turns: 0,
			};
			return Response.json({
				groupBy: u.searchParams.get("groupBy"),
				from: 0,
				to: 1,
				tz: "UTC",
				total: zero,
				groups: [],
				series: [],
			});
		};
		mount("/stats/usage");
		expect(await screen.findByTestId("kpi-cost")).toHaveTextContent("$0");
		expect(
			(await screen.findAllByText("这段时间没有数据")).length,
		).toBeGreaterThan(0);
	});

	it("进入用量标签：store 里每条未读预算通知各按 id 标一次已读（已读的与线程通知不动）", async () => {
		const n = (p: Partial<AppNotification>): AppNotification => ({
			id: "x",
			kind: "budget",
			title: "Daily budget warning",
			body: "",
			url: "/stats/usage",
			createdAt: 1,
			deliveries: [],
			...p,
		});
		notificationsStore.setState({
			items: [
				n({ id: "ntf_b1" }),
				n({ id: "ntf_b2", title: "Monthly budget exceeded" }),
				n({ id: "ntf_b0", readAt: 5 }),
				n({
					id: "ntf_t",
					kind: "done",
					threadId: MAIN_THREAD,
					url: `/t/${MAIN_THREAD}`,
				}),
			],
			unread: 3,
		});
		mount("/stats/usage");
		await waitFor(() =>
			expect(posts.map((p) => p.body)).toEqual([
				{ id: "ntf_b1" },
				{ id: "ntf_b2" },
			]),
		);
		expect(posts.every((p) => p.path === "/api/v1/notifications/read")).toBe(
			true,
		);
	});
});

describe("StatsPage Orb 与产出", () => {
	it("Orb 标签：按粒度 / 类型 / 单台三次请求；不标预算通知；点一行打开抽屉，Esc 关闭", async () => {
		notificationsStore.setState({
			items: [
				{
					id: "ntf_b1",
					kind: "budget",
					title: "Daily budget warning",
					body: "",
					url: "/stats/usage",
					createdAt: 1,
					deliveries: [],
				},
			],
			unread: 1,
		});
		mount("/stats/orbs");
		const w = w30();
		const row = await screen.findByRole("button", {
			name: new RegExp(threads.find((t) => t.id === MAIN_THREAD)?.title ?? ""),
		});
		const got = queries("stats/orbs");
		const base = { from: String(w.from), to: String(w.to), tz: w.tz };
		for (const g of ["day", "type", "orb"])
			expect(got).toContainEqual({ ...base, groupBy: g });
		expect(row).toHaveAttribute("data-orb-row", "orb-aaa");
		expect(screen.getByTestId("kpi-running")).toBeInTheDocument();
		fireEvent.click(row);
		const drawer = await screen.findByTestId("orb-drawer");
		expect(
			await within(drawer).findByTestId("timeline-strip"),
		).toBeInTheDocument();
		expect(within(drawer).getAllByTestId("line-chart")).toHaveLength(2);
		expect(queries("orbs/orb-aaa/timeline")).toHaveLength(1);
		fireEvent.keyDown(window, { key: "Escape" });
		expect(screen.queryByTestId("orb-drawer")).toBeNull();
		expect(posts).toEqual([]);
	});

	it("产出标签：按粒度 / 项目 / 空间三次请求，指标含已交付", async () => {
		mount("/stats/output?range=90d&space=none");
		expect(await screen.findByTestId("kpi-shipped")).toBeInTheDocument();
		const w = statsWindow({ range: "90d" }, Date.now());
		const base = {
			from: String(w.from),
			to: String(w.to),
			tz: w.tz,
			space: "none",
		};
		const got = queries("stats/output");
		for (const g of ["day", "project", "space"])
			expect(got).toContainEqual({ ...base, groupBy: g });
		// 已选的「无空间」筛选显示为胶囊
		expect(document.querySelector("[data-filter=space]")).toHaveTextContent(
			"（无空间）",
		);
	});

	it("未知标签回到用量", async () => {
		mount("/stats/bogus");
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent("/stats/usage"),
		);
	});
});
