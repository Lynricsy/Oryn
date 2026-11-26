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
				sandboxes: 0,
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
				netRxBytes: 50 * 1024 * 1024,
				netTxBytes: 12 * 1024 * 1024,
			},
			host: {
				reportedCount: 1,
				cpuCores: 16,
				cpuUsedCores: 3.92,
				memTotalMB: 65536,
				memUsedMB: 20480,
				diskTotalBytes: 500 * 1024 ** 3,
				diskUsedBytes: 200 * 1024 ** 3,
				orbDiskBytes: 2 * 1024 ** 3,
				orbDiskReportedCount: 1,
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
					orbDiskBytes: 2 * 1024 ** 3,
					orbDiskByOrb: {
						"orb-run-0001": 1536 * 1024 ** 2,
						"orb-frz-0002": 512 * 1024 ** 2,
					},
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
					sandboxes: 0,
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
						diskBytes: 1536 * 1024 ** 2,
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
					sandboxes: 0,
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
		expect(screen.getByTestId("where")).toHaveTextContent("/settings/compute");
	});

	it("渲染全局 KPI 与双段占用条（Orb 占比 + 宿主机其他），支持状态筛选与关键字搜索", async () => {
		mount();
		const nodesKpi = await screen.findByTestId("kpi-providers-nodes");
		expect(nodesKpi).toHaveTextContent("1 / 2");
		expect(screen.getByTestId("kpi-providers-orbs")).toHaveTextContent("1 / 3");
		// 内存主读数聚焦 Orb 实时占用（3 GB），条上明确拆分 Orb 段与宿主机其他段
		const memKpi = screen.getByTestId("kpi-providers-memory");
		expect(memKpi).toHaveTextContent("3 GB");
		const memBar = screen.getByTestId("kpi-bar-memory");
		expect(memBar.querySelector('[data-segment="orb"]')).toBeTruthy();
		expect(memBar.querySelector('[data-segment="other"]')).toBeTruthy();
		// 存储主读数独立标注 Orb 可写盘 (2.0 GB)，磁盘条仅反映 statfs 物理卷 (40%)，不从 statfs 做逻辑/CoW 扣减
		const storageKpi = screen.getByTestId("kpi-providers-storage");
		expect(storageKpi).toHaveTextContent("2.0 GB");
		const storageBar = screen.getByTestId("kpi-bar-storage");
		expect(storageBar.querySelector('[data-segment="volume"]')).toBeTruthy();
		expect(storageBar.querySelector('[data-segment="orb"]')).toBeNull();
		expect(storageBar.querySelector('[data-segment="other"]')).toBeNull();
		// CPU 条上明确区分 Orb CPU 与宿主机其他 CPU
		const cpuBar = screen.getByTestId("kpi-bar-cpu");
		expect(cpuBar.querySelector('[data-segment="orb"]')).toBeTruthy();
		expect(cpuBar.querySelector('[data-segment="other"]')).toBeTruthy();
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

	it("无宿主机指标且多台 orb 共享同底盘样本时，存储主读数仅显示快照缓存而不重复累加 orb 磁盘", async () => {
		const base = sampleOverview();
		const prov = base.providers[0];
		const firstOrb = prov?.orbItems[0];
		if (!prov || !firstOrb) throw new Error("missing fixture provider");
		const sharedFsMB = 180 * 1024;
		overviewData = {
			...base,
			summary: {
				...base.summary,
				providersTotal: 1,
				providersOffline: 0,
				host: {
					reportedCount: 0,
					cpuCores: 0,
					cpuUsedCores: 0,
					memTotalMB: 0,
					memUsedMB: 0,
					diskTotalBytes: 0,
					diskUsedBytes: 0,
					orbDiskBytes: 0,
					orbDiskReportedCount: 0,
				},
			},
			providers: [
				{
					...prov,
					hostMetrics: undefined,
					orbItems: [
						{
							...firstOrb,
							id: "orb-shared-1",
							diskBytes: undefined,
							latestSample: {
								ts: NOW - 5_000,
								cpuPct: 30,
								memUsedMB: 2048,
								memLimitMB: 8192,
								diskUsedMB: sharedFsMB,
								netRxBytes: 100,
								netTxBytes: 50,
							},
						},
						{
							...firstOrb,
							id: "orb-shared-2",
							diskBytes: undefined,
							latestSample: {
								ts: NOW - 5_000,
								cpuPct: 20,
								memUsedMB: 1024,
								memLimitMB: 8192,
								diskUsedMB: sharedFsMB,
								netRxBytes: 100,
								netTxBytes: 50,
							},
						},
					],
				},
			],
		};
		mount();
		const storageKpi = await screen.findByTestId("kpi-providers-storage");
		// 仅展示 3.0 GB 快照缓存，而不是 360 GB + 3 GB
		expect(storageKpi).toHaveTextContent("3.0 GB");
		expect(storageKpi).not.toHaveTextContent("360");
		expect(storageKpi).not.toHaveTextContent("363");
		// 逐台 orb 行仍保留各自的 statfs 文件系统样本（180 GB）
		const rows = screen.getAllByTestId("provider-orb-row");
		expect(rows).toHaveLength(2);
		expect(rows[0]).toHaveTextContent("180 GB");
		expect(rows[1]).toHaveTextContent("180 GB");
	});

	it("Docker 稀疏文件导致 orbDiskBytes 大于 statfs 已用空间时，磁盘条按真实 statfs 渲染且不错误扣减", async () => {
		const base = sampleOverview();
		const prov = base.providers[0];
		if (!prov?.hostMetrics) throw new Error("missing fixture provider");
		const tenGiB = 10 * 1024 ** 3;
		const twoGiB = 2 * 1024 ** 3;
		const hundredGiB = 100 * 1024 ** 3;
		overviewData = {
			...base,
			summary: {
				...base.summary,
				host: {
					...base.summary.host,
					diskTotalBytes: hundredGiB,
					diskUsedBytes: twoGiB,
					orbDiskBytes: tenGiB,
					orbDiskReportedCount: 1,
				},
			},
			providers: [
				{
					...prov,
					hostMetrics: {
						...prov.hostMetrics,
						diskTotalBytes: hundredGiB,
						diskUsedBytes: twoGiB,
						orbDiskBytes: tenGiB,
					},
				},
			],
		};
		mount();
		const storageKpi = await screen.findByTestId("kpi-providers-storage");
		// 独立展示 Orb 可写层逻辑大小 10.0 GB，同时右上角与条宽按 statfs 物理卷 2% 渲染
		expect(storageKpi).toHaveTextContent("10.0 GB");
		expect(storageKpi).toHaveTextContent("2%");
		const volSeg = screen
			.getByTestId("kpi-bar-storage")
			.querySelector('[data-segment="volume"]') as HTMLElement | null;
		expect(volSeg).not.toBeNull();
		expect(volSeg?.style.width).toBe("2%");
	});
});
