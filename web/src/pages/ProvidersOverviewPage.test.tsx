/** Provider 总览页测试：空态引导、KPI 汇总、宿主机与 Orb 指标展示、状态筛选与搜索、打开 OrbDrawer 与快捷运维操作。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../api/client";
import i18n from "../i18n";
import { ProvidersOverviewPage } from "./ProvidersOverviewPage";

const NOW = 1_730_000_000_000;

function sampleOverview(): Schemas["ProvidersOverview"] {
	return {
		generatedAt: NOW,
		orbDefaults: { provider: "docker-main", type: "medium" },
		summary: {
			providersTotal: 2,
			providersOnline: 1,
			providersOffline: 1,
			providersDisabled: 0,
			orbs: {
				total: 3,
				running: 1,
				starting: 0,
				frozen: 1,
				stopped: 0,
				failed: 1,
				deleting: 0,
				deleted: 4,
				warmPool: 1,
				boxes: 0,
				computers: 0,
			},
			allocated: {
				runningCpus: 4,
				runningMemoryMB: 8192,
				totalCpus: 8,
				totalMemoryMB: 16384,
			},
			live: {
				sampledOrbs: 1,
				cpuPct: 48.5,
				memUsedMB: 3072,
				memLimitMB: 8192,
				diskUsedMB: 4096,
				netRxBytes: 50 * 1024 * 1024,
				netTxBytes: 12 * 1024 * 1024,
			},
			host: {
				reportedCount: 1,
				cpuCores: 16,
				memTotalMB: 65536,
				memUsedMB: 20480,
				diskTotalBytes: 500 * 1024 ** 3,
				diskUsedBytes: 200 * 1024 ** 3,
			},
			snapshots: {
				readyCount: 2,
				buildingCount: 0,
				retiredCount: 1,
				totalBytes: 3 * 1024 ** 3,
			},
			jobs: {
				queued: 1,
				claimed: 0,
				succeeded24h: 19,
				failed24h: 1,
			},
		},
		providers: [
			{
				provider: {
					id: "prov_1",
					name: "docker-main",
					driver: "docker",
					capabilities: { freeze: true, snapshot: true, offlineFs: true },
					orbTypes: [
						{ name: "small", cpus: 2, memoryMB: 4096 },
						{ name: "medium", cpus: 4, memoryMB: 8192, features: ["docker"] },
					],
					defaultType: "medium",
					online: true,
					instanceId: "inst-abc123",
					leaseUntil: NOW + 45_000,
					createdAt: NOW - 86_400_000,
				},
				version: "v19.0.1",
				allowMultiple: false,
				hostMetrics: {
					sampledAt: NOW - 5_000,
					hostname: "worker-01",
					os: "linux",
					arch: "amd64",
					uptimeSeconds: 86400 * 3,
					cpuCores: 16,
					cpuPct: 24.5,
					loadAvg1: 1.2,
					loadAvg5: 0.9,
					loadAvg15: 0.7,
					memTotalMB: 65536,
					memUsedMB: 20480,
					memAvailableMB: 45056,
					diskTotalBytes: 500 * 1024 ** 3,
					diskUsedBytes: 200 * 1024 ** 3,
					diskPath: "/var/lib/docker",
				},
				orbs: {
					total: 3,
					running: 1,
					starting: 0,
					frozen: 1,
					stopped: 0,
					failed: 1,
					deleting: 0,
					deleted: 4,
					warmPool: 1,
					boxes: 0,
					computers: 0,
				},
				allocated: {
					runningCpus: 4,
					runningMemoryMB: 8192,
					totalCpus: 8,
					totalMemoryMB: 16384,
				},
				live: {
					sampledOrbs: 1,
					cpuPct: 48.5,
					memUsedMB: 3072,
					memLimitMB: 8192,
					diskUsedMB: 4096,
					netRxBytes: 50 * 1024 * 1024,
					netTxBytes: 12 * 1024 * 1024,
				},
				snapshots: {
					readyCount: 2,
					buildingCount: 0,
					retiredCount: 1,
					totalBytes: 3 * 1024 ** 3,
				},
				jobs: { queued: 1, claimed: 0, succeeded24h: 19, failed24h: 1 },
				orbItems: [
					{
						id: "orb-run-0001",
						type: "medium",
						state: "ready",
						cpus: 4,
						memoryMB: 8192,
						projectId: "prj_1",
						projectName: "oryn-web",
						threadIds: ["thr_1"],
						threadTitles: ["重构算力总览页"],
						nestedDocker: true,
						stateChangedAt: NOW - 60_000,
						createdAt: NOW - 3_600_000,
						latestSample: {
							ts: NOW - 10_000,
							cpuPct: 48.5,
							memUsedMB: 3072,
							memLimitMB: 8192,
							diskUsedMB: 3072,
							netRxBytes: 40 * 1024 * 1024,
							netTxBytes: 10 * 1024 * 1024,
						},
					},
					{
						id: "orb-frz-0002",
						type: "small",
						state: "frozen",
						cpus: 2,
						memoryMB: 4096,
						projectId: "prj_1",
						projectName: "oryn-web",
						threadIds: [],
						threadTitles: [],
						pool: "idle",
						stateChangedAt: NOW - 120_000,
						createdAt: NOW - 7_200_000,
					},
					{
						id: "orb-err-0003",
						type: "small",
						state: "failed",
						cpus: 2,
						memoryMB: 4096,
						threadIds: [],
						threadTitles: [],
						failureReason: "oom_killed",
						stateChangedAt: NOW - 180_000,
						createdAt: NOW - 10_800_000,
					},
				],
			},
			{
				provider: {
					id: "prov_2",
					name: "fc-backup",
					driver: "firecracker",
					capabilities: { freeze: true, snapshot: true },
					orbTypes: [{ name: "small", cpus: 2, memoryMB: 4096 }],
					defaultType: "small",
					online: false,
					createdAt: NOW - 86_400_000,
				},
				allowMultiple: false,
				orbs: {
					total: 0,
					running: 0,
					starting: 0,
					frozen: 0,
					stopped: 0,
					failed: 0,
					deleting: 0,
					deleted: 0,
					warmPool: 0,
					boxes: 0,
					computers: 0,
				},
				allocated: {
					runningCpus: 0,
					runningMemoryMB: 0,
					totalCpus: 0,
					totalMemoryMB: 0,
				},
				live: {
					sampledOrbs: 0,
					cpuPct: 0,
					memUsedMB: 0,
					memLimitMB: 0,
					diskUsedMB: 0,
					netRxBytes: 0,
					netTxBytes: 0,
				},
				snapshots: {
					readyCount: 0,
					buildingCount: 0,
					retiredCount: 0,
					totalBytes: 0,
				},
				jobs: { queued: 0, claimed: 0, succeeded24h: 0, failed24h: 0 },
				orbItems: [],
			},
		],
	};
}

let overviewData: Schemas["ProvidersOverview"];
let calls: { method: string; path: string; body?: unknown }[];

beforeEach(() => {
	calls = [];
	overviewData = sampleOverview();
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			const url = new URL(req.url, "http://localhost");
			let parsed: unknown;
			if (req.method !== "GET" && req.method !== "HEAD") {
				try {
					parsed = await req.clone().json();
				} catch {
					parsed = undefined;
				}
			}
			calls.push({ method: req.method, path: url.pathname, body: parsed });

			if (url.pathname === "/api/v1/providers/overview")
				return Response.json(overviewData);
			if (url.pathname === "/api/v1/stats/meta")
				return Response.json({
					retention: { samplesMs: 86_400_000, eventsMs: 0, recordsMs: 0 },
					earliestTs: NOW - 86_400_000,
				});
			if (url.pathname.endsWith("/timeline"))
				return Response.json({
					orbId: "orb-run-0001",
					from: NOW - 3_600_000,
					to: NOW,
					segments: [],
				});
			if (url.pathname.endsWith("/metrics"))
				return Response.json({
					orbId: "orb-run-0001",
					step: 60,
					samples: [],
				});
			if (url.pathname.endsWith("/setup-runs"))
				return Response.json({ runs: [] });
			if (url.pathname.endsWith("/pause") || url.pathname.endsWith("/resume"))
				return Response.json({ id: "orb-run-0001" }, { status: 202 });
			return Response.json({ code: "not_found", message: "" }, { status: 404 });
		},
	);
});

afterEach(() => vi.unstubAllGlobals());

function Where() {
	return <span data-testid="where">{useLocation().pathname}</span>;
}

function mount() {
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={["/providers"]}>
				<Routes>
					<Route path="/providers" element={<ProvidersOverviewPage />} />
					<Route path="*" element={null} />
				</Routes>
				<Where />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

describe("Provider 总览页", () => {
	it("没有 provider 时展示空态引导，点击按钮跳转到设置页", async () => {
		overviewData = {
			...sampleOverview(),
			summary: {
				...sampleOverview().summary,
				providersTotal: 0,
				providersOnline: 0,
				providersOffline: 0,
			},
			providers: [],
		};
		mount();
		expect(
			await screen.findByText(i18n.t("providers.overview.emptyTitle")),
		).toBeInTheDocument();
		fireEvent.click(
			screen.getByRole("button", {
				name: i18n.t("providers.overview.emptyAction"),
			}),
		);
		expect(screen.getByTestId("where")).toHaveTextContent(
			"/settings/providers",
		);
	});

	it("渲染全局 KPI 与节点卡片，支持状态筛选与关键字搜索", async () => {
		mount();
		const nodesKpi = await screen.findByTestId("kpi-providers-nodes");
		expect(nodesKpi).toHaveTextContent("1 / 2");
		expect(screen.getByTestId("kpi-providers-orbs")).toHaveTextContent("1 / 3");
		expect(screen.getByTestId("kpi-providers-memory")).toHaveTextContent(
			"20 GB / 64 GB",
		);
		expect(screen.getByTestId("kpi-providers-storage")).toHaveTextContent(
			"200.0 GB / 500.0 GB",
		);
		expect(screen.getAllByTestId("provider-node-card")).toHaveLength(2);

		// 筛选仅离线节点
		fireEvent.click(
			screen.getByRole("tab", {
				name: new RegExp(i18n.t("providers.overview.filterOffline")),
			}),
		);
		const cards = screen.getAllByTestId("provider-node-card");
		expect(cards).toHaveLength(1);
		expect(cards[0]).toHaveTextContent("fc-backup");

		// 切回全部，按线程名搜索
		fireEvent.click(
			screen.getByRole("tab", {
				name: new RegExp(i18n.t("providers.overview.filterAll")),
			}),
		);
		fireEvent.change(
			screen.getByPlaceholderText(
				i18n.t("providers.overview.searchPlaceholder"),
			),
			{ target: { value: "重构算力总览页" } },
		);
		expect(screen.getAllByTestId("provider-node-card")).toHaveLength(1);
		expect(screen.getByText("重构算力总览页")).toBeInTheDocument();
	});

	it("点击 orb 打开 OrbDrawer，执行冻结/唤醒触发后端接口", async () => {
		mount();
		const orbRows = await screen.findAllByTestId("provider-orb-row");
		expect(orbRows).toHaveLength(3);

		// 对运行中的 orb 点击冻结
		const runRow = orbRows[0] as HTMLElement;
		fireEvent.click(
			within(runRow).getByRole("button", {
				name: i18n.t("providers.overview.orbs.freeze"),
			}),
		);
		await vi.waitFor(() => {
			expect(
				calls.some(
					(c) =>
						c.method === "POST" &&
						c.path === "/api/v1/orbs/orb-run-0001/pause" &&
						typeof c.body === "object" &&
						c.body !== null &&
						"mode" in c.body &&
						c.body.mode === "freeze",
				),
			).toBe(true);
		});

		// 点击运行中 orb 打开抽屉
		fireEvent.click(
			within(runRow).getByRole("button", {
				name: i18n.t("providers.overview.orbs.inspect"),
			}),
		);
		expect(await screen.findByTestId("orb-drawer")).toBeInTheDocument();
	});
});
