/**
 * orb 抽屉的轮询窗口（M10 审查 W2）：查询键用所选窗口，实际请求的终点每轮取 `min(to, 现在)`。
 * 抽屉开着时，下一轮轮询会把新采样与新的状态段带进图里；历史窗口的终点不越过所选 `to`。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { OrbDrawer } from "./OrbDrawer";

const NOW = Date.UTC(2026, 10, 15, 10, 0);
const DAY = 86_400_000;
const DAY0 = Date.UTC(2026, 10, 15);
const ORB = "orb-aaa";

let urls: URL[];
let samples: { ts: number; cpuPct: number }[];
let events: { ts: number; state: string }[];

beforeEach(() => {
	vi.useFakeTimers({ now: NOW });
	urls = [];
	samples = [{ ts: NOW - 30_000, cpuPct: 40 }];
	events = [{ ts: DAY0, state: "ready" }];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (req: Request) => {
			const url = new URL(req.url);
			urls.push(url);
			const q = url.searchParams;
			const from = Number(q.get("from"));
			const to = Number(q.get("to"));
			const p = url.pathname.replace(/^\/api\/v1\//, "");
			if (p === `orbs/${ORB}/timeline`) {
				const inside = events.filter((e) => e.ts < to);
				return Response.json({
					orbId: ORB,
					from,
					to,
					truncated: false,
					segments: inside.map((e, i) => ({
						from: Math.max(from, e.ts),
						to: inside[i + 1]?.ts ?? to,
						state: e.state,
					})),
				});
			}
			if (p === `orbs/${ORB}/metrics`)
				return Response.json({
					orbId: ORB,
					step: Number(q.get("step")),
					samples: samples
						.filter((s) => s.ts >= from && s.ts <= to)
						.map((s) => ({
							...s,
							memUsedMB: 512,
							memLimitMB: 4096,
							diskUsedMB: 0,
							netRxBytes: 0,
							netTxBytes: 0,
						})),
				});
			if (p === `orbs/${ORB}/setup-runs`) return Response.json({ runs: [] });
			return Response.json({ code: "not_found", message: p }, { status: 404 });
		}),
	);
});

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

function mount(from: number, to: number) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	render(
		<QueryClientProvider client={client}>
			<OrbDrawer
				orbId={ORB}
				title="orb"
				from={from}
				to={to}
				samplesRetentionMs={0}
				onClose={() => {}}
			/>
		</QueryClientProvider>,
	);
}

function requested(kind: "timeline" | "metrics"): number[] {
	return urls
		.filter((u) => u.pathname === `/api/v1/orbs/${ORB}/${kind}`)
		.map((u) => Number(u.searchParams.get("to")));
}

async function tick(ms: number) {
	await act(async () => {
		await vi.advanceTimersByTimeAsync(ms);
	});
}

/** CPU 折线的点数。 */
function cpuPoints(): number {
	const chart = within(screen.getByTestId("orb-drawer")).getAllByTestId(
		"line-chart",
	)[0];
	const pts = chart?.querySelector("polyline")?.getAttribute("points") ?? "";
	return pts ? pts.split(" ").length : 0;
}

describe("OrbDrawer 轮询窗口", () => {
	it("今天：首轮终点 = 打开时刻；61 秒后下一轮终点推进，新采样与新状态段进入图中", async () => {
		mount(DAY0, DAY0 + DAY);
		await tick(0);
		expect(requested("timeline")).toEqual([NOW]);
		expect(requested("metrics")).toEqual([NOW]);
		expect(cpuPoints()).toBe(1);
		expect(
			screen.getByTestId("timeline-strip").querySelector("[data-state=frozen]"),
		).toBeNull();

		// 打开期间又来了一个采样和一次冻结
		samples.push({ ts: NOW + 45_000, cpuPct: 5 });
		events.push({ ts: NOW + 50_000, state: "frozen" });
		await tick(61_000);
		expect(requested("timeline").at(-1)).toBeGreaterThanOrEqual(NOW + 60_000);
		expect(requested("metrics").at(-1)).toBeGreaterThanOrEqual(NOW + 60_000);
		expect(cpuPoints()).toBe(2);
		expect(
			screen.getByTestId("timeline-strip").querySelector("[data-state=frozen]"),
		).not.toBeNull();
	});

	it("历史窗口：轮询后终点仍是所选的 to，不越过它", async () => {
		const to = DAY0 - DAY;
		mount(to - 7 * DAY, to);
		await tick(0);
		await tick(61_000);
		const ends = [...requested("timeline"), ...requested("metrics")];
		expect(ends.length).toBeGreaterThanOrEqual(4);
		for (const end of ends) expect(end).toBe(to);
	});
});
