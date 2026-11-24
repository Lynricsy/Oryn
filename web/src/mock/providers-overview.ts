/** Mock 模式的 `/api/v1/providers/overview` 聚合：基于实时 mock orbs/threads/projects/snapshots 计算，并附宿主机指标。 */
import type { Orb, Project, Provider, Schemas, Thread } from "../api/client";
import { NOW, projectSnapshots } from "./fixtures";

export interface MockProvidersOverviewDeps {
	providers: () => Provider[];
	orbs: () => Orb[];
	threads: () => Thread[];
	projects: () => Project[];
}

function hashSeed(id: string): number {
	let h = 2166136261;
	for (let i = 0; i < id.length; i++) {
		h ^= id.charCodeAt(i);
		h = Math.imul(h, 16777619);
	}
	return h >>> 0;
}

function mockOrbSample(o: Orb, now: number): Schemas["OrbMetricSample"] {
	const seed = hashSeed(o.id);
	const running = o.state === "ready";
	const cpuPct = running ? 6 + ((seed % 140) + 10) * 0.45 : 0;
	const memUsedMB = running
		? Math.round(o.memoryMB * (0.22 + ((seed >>> 4) % 48) / 100))
		: o.state === "frozen"
			? Math.round(o.memoryMB * 0.18)
			: 0;
	const diskUsedMB = 1400 + ((seed >>> 8) % 6800);
	const netRxBytes = (120 + ((seed >>> 12) % 900)) * 1024 * 1024;
	const netTxBytes = (45 + ((seed >>> 16) % 340)) * 1024 * 1024;
	return {
		ts: now - 12_000,
		cpuPct: Number(cpuPct.toFixed(1)),
		memUsedMB,
		memLimitMB: o.memoryMB,
		diskUsedMB,
		netRxBytes,
		netTxBytes,
	};
}

function emptyOrbCounts(): Schemas["ProviderOrbCounts"] {
	return {
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
	};
}

function rankOrb(st: Orb["state"]): number {
	if (st === "ready") return 0;
	if (
		st === "requested" ||
		st === "spawning" ||
		st === "connecting" ||
		st === "setting_up" ||
		st === "resuming" ||
		st === "waking"
	)
		return 1;
	if (st === "failed") return 2;
	if (st === "frozen" || st === "freezing") return 3;
	if (st === "stopped" || st === "stopping") return 4;
	return 5;
}

export function buildMockProvidersOverview(
	deps: MockProvidersOverviewDeps,
): Schemas["ProvidersOverview"] {
	const now = Date.now();
	const provList = deps.providers();
	const allOrbs = deps
		.orbs()
		.filter((o) => !o.coordinator && o.kind !== "runner");
	const threads = deps.threads();
	const projectNames = new Map(deps.projects().map((p) => [p.id, p.name]));
	const threadsByOrb = new Map<string, Thread[]>();
	for (const th of threads) {
		if (!th.orbId) continue;
		const list = threadsByOrb.get(th.orbId) ?? [];
		list.push(th);
		threadsByOrb.set(th.orbId, list);
	}

	// 汇总 mock 快照
	const snapsByProv = new Map<string, Schemas["ProviderSnapshotStats"]>();
	for (const list of Object.values(projectSnapshots)) {
		for (const sn of list) {
			const cur = snapsByProv.get(sn.providerId) ?? {
				readyCount: 0,
				buildingCount: 0,
				retiredCount: 0,
				totalBytes: 0,
			};
			if (sn.state === "ready") {
				cur.readyCount++;
				cur.totalBytes += sn.sizeBytes ?? 0;
			} else if (sn.state === "building") {
				cur.buildingCount++;
			} else {
				cur.retiredCount++;
			}
			snapsByProv.set(sn.providerId, cur);
		}
	}

	const summary: Schemas["ProvidersSummary"] = {
		providersTotal: 0,
		providersOnline: 0,
		providersOffline: 0,
		providersDisabled: 0,
		orbs: emptyOrbCounts(),
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
		host: {
			reportedCount: 0,
			cpuCores: 0,
			memTotalMB: 0,
			memUsedMB: 0,
			diskTotalBytes: 0,
			diskUsedBytes: 0,
		},
		snapshots: {
			readyCount: 0,
			buildingCount: 0,
			retiredCount: 0,
			totalBytes: 0,
		},
		jobs: {
			queued: 0,
			claimed: 0,
			succeeded24h: 0,
			failed24h: 0,
		},
	};

	const items: Schemas["ProviderOverviewItem"][] = provList.map((prov, idx) => {
		const provOrbs = allOrbs.filter((o) => o.providerId === prov.id);
		const liveOrbs = provOrbs.filter(
			(o) => !o.deletedAt && o.state !== "deleted",
		);
		const deletedCount =
			provOrbs.length - liveOrbs.length + (idx === 0 ? 14 : 0);
		const counts = emptyOrbCounts();
		counts.deleted = deletedCount;

		const allocated: Schemas["ProviderResourceAllocation"] = {
			runningCpus: 0,
			runningMemoryMB: 0,
			totalCpus: 0,
			totalMemoryMB: 0,
		};
		const live: Schemas["ProviderLiveMetrics"] = {
			sampledOrbs: 0,
			cpuPct: 0,
			memUsedMB: 0,
			memLimitMB: 0,
			netRxBytes: 0,
			netTxBytes: 0,
		};

		const orbItems: Schemas["ProviderOrbSummary"][] = liveOrbs.map((o) => {
			counts.total++;
			allocated.totalCpus += o.cpus;
			allocated.totalMemoryMB += o.memoryMB;
			if (o.state === "ready") {
				counts.running++;
				allocated.runningCpus += o.cpus;
				allocated.runningMemoryMB += o.memoryMB;
			} else if (
				o.state === "requested" ||
				o.state === "spawning" ||
				o.state === "connecting" ||
				o.state === "setting_up" ||
				o.state === "resuming" ||
				o.state === "waking"
			) {
				counts.starting++;
			} else if (o.state === "frozen" || o.state === "freezing") {
				counts.frozen++;
			} else if (o.state === "stopped" || o.state === "stopping") {
				counts.stopped++;
			} else if (o.state === "failed") {
				counts.failed++;
			} else if (o.state === "deleting") {
				counts.deleting++;
			}
			if (o.pool) counts.warmPool++;
			if (o.box) counts.boxes++;
			if (o.computer) counts.computers++;

			const sample = mockOrbSample(o, now);
			live.netRxBytes += sample.netRxBytes;
			live.netTxBytes += sample.netTxBytes;
			if (o.state === "ready") {
				live.sampledOrbs++;
				live.cpuPct += sample.cpuPct;
				live.memUsedMB += sample.memUsedMB;
				live.memLimitMB += sample.memLimitMB;
			}
			const ths = threadsByOrb.get(o.id) ?? [];
			return {
				id: o.id,
				type: o.type,
				state: o.state,
				cpus: o.cpus,
				memoryMB: o.memoryMB,
				projectId: o.projectId,
				projectName: o.projectId ? projectNames.get(o.projectId) : undefined,
				threadIds: ths.map((t) => t.id),
				threadTitles: ths.map((t) => t.title),
				pool: o.pool,
				box: o.box,
				computer: o.computer,
				automation: o.automation,
				nestedDocker: o.nestedDocker,
				snapshotKind: o.snapshotKind,
				failureReason: o.failureReason,
				stateChangedAt: o.stateChangedAt,
				createdAt: o.createdAt,
				lastActivityAt: o.lastAgentActivityAt ?? o.lastUserInteractionAt,
				latestSample: sample,
			};
		});

		orbItems.sort(
			(a, b) =>
				rankOrb(a.state) - rankOrb(b.state) ||
				b.stateChangedAt - a.stateChangedAt,
		);

		const hostMetrics: Schemas["ProviderHostMetrics"] | undefined = prov.online
			? {
					sampledAt: now - 8_000,
					hostname: `${prov.name}-host-01`,
					os: "linux",
					arch: "amd64",
					uptimeSeconds: 12 * 86400 + 4 * 3600 + 20 * 60,
					cpuCores: 16,
					cpuPct: Number(Math.min(92, 14.2 + live.cpuPct * 0.25).toFixed(1)),
					loadAvg1: 1.84,
					loadAvg5: 1.42,
					loadAvg15: 1.15,
					memTotalMB: 65536,
					memUsedMB: Math.min(60000, 12288 + Math.round(live.memUsedMB)),
					memAvailableMB: Math.max(
						4096,
						65536 - 12288 - Math.round(live.memUsedMB),
					),
					diskTotalBytes: 512 * 1024 ** 3,
					diskUsedBytes: 186 * 1024 ** 3,
					diskAvailableBytes: (512 - 186) * 1024 ** 3,
					diskPath:
						prov.driver === "firecracker"
							? "/var/lib/oryn-fc"
							: "/var/lib/docker",
				}
			: undefined;

		const snapshots = snapsByProv.get(prov.id) ?? {
			readyCount: 0,
			buildingCount: 0,
			retiredCount: 0,
			totalBytes: 0,
		};
		const jobs: Schemas["ProviderJobStats"] = prov.online
			? {
					queued: 0,
					claimed: counts.starting > 0 ? 1 : 0,
					succeeded24h: 28,
					failed24h: counts.failed,
				}
			: { queued: 0, claimed: 0, succeeded24h: 0, failed24h: 0 };

		summary.providersTotal++;
		if (prov.disabledAt) summary.providersDisabled++;
		else if (prov.online) summary.providersOnline++;
		else summary.providersOffline++;

		if (prov.online && hostMetrics) {
			summary.host.reportedCount++;
			summary.host.cpuCores += hostMetrics.cpuCores;
			summary.host.memTotalMB += hostMetrics.memTotalMB;
			summary.host.memUsedMB += hostMetrics.memUsedMB;
			summary.host.diskTotalBytes += hostMetrics.diskTotalBytes;
			summary.host.diskUsedBytes += hostMetrics.diskUsedBytes;
		}
		summary.orbs.total += counts.total;
		summary.orbs.running += counts.running;
		summary.orbs.starting += counts.starting;
		summary.orbs.frozen += counts.frozen;
		summary.orbs.stopped += counts.stopped;
		summary.orbs.failed += counts.failed;
		summary.orbs.deleting += counts.deleting;
		summary.orbs.deleted += counts.deleted;
		summary.orbs.warmPool += counts.warmPool;
		summary.orbs.boxes += counts.boxes;
		summary.orbs.computers += counts.computers;

		summary.allocated.runningCpus += allocated.runningCpus;
		summary.allocated.runningMemoryMB += allocated.runningMemoryMB;
		summary.allocated.totalCpus += allocated.totalCpus;
		summary.allocated.totalMemoryMB += allocated.totalMemoryMB;

		summary.live.sampledOrbs += live.sampledOrbs;
		summary.live.cpuPct += live.cpuPct;
		summary.live.memUsedMB += live.memUsedMB;
		summary.live.memLimitMB += live.memLimitMB;
		summary.live.netRxBytes += live.netRxBytes;
		summary.live.netTxBytes += live.netTxBytes;

		summary.snapshots.readyCount += snapshots.readyCount;
		summary.snapshots.buildingCount += snapshots.buildingCount;
		summary.snapshots.retiredCount += snapshots.retiredCount;
		summary.snapshots.totalBytes += snapshots.totalBytes;

		summary.jobs.queued += jobs.queued;
		summary.jobs.claimed += jobs.claimed;
		summary.jobs.succeeded24h += jobs.succeeded24h;
		summary.jobs.failed24h += jobs.failed24h;

		return {
			provider: {
				...prov,
				capabilities: prov.capabilities
					? { ...prov.capabilities, offlineFs: true }
					: undefined,
				leaseUntil: prov.online ? now + 42_000 : prov.leaseUntil,
			},
			version: "v19.0.1-mock",
			baseImageId: "sha256:9f84b2c1d7e0a312",
			allowMultiple: false,
			hostMetrics,
			orbs: counts,
			allocated,
			live,
			snapshots,
			jobs,
			orbItems,
		};
	});

	return {
		generatedAt: now || NOW,
		orbDefaults: {
			provider: provList[0]?.name,
			type: provList[0]?.defaultType,
		},
		summary,
		providers: items,
	};
}
