/**
 * 「快照与预热」卡的实时更新（M11a 审查第 3 条）：卡片保持挂载时，后台快照 building→ready、池 orb warming→idle→离池
 * 都要自动反映到行与「清除快照」按钮上；刷新由 orb 推送驱动（短窗口合并），在建 / 预热时另有 30s 兜底，
 * 不逐次渲染重复 GET，卸载后停止。全程假时钟，推送经 `acceptOrbPush`（与 WS `orb` 消息同一入口）。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Orb, Project, Schemas } from "../../api/client";
import { SNAPSHOT_POLL_MS } from "../../api/queries";
import i18n from "../../i18n";
import { acceptOrbPush, indexStore } from "../../state/stores";
import { AccelCard, PUSH_REFRESH_WINDOW_MS } from "./ProjectAccel";

const PROJECT: Project = {
	id: "prj_live",
	name: "live",
	repoUrl: "https://github.com/x/live",
	shallowClone: false,
	snapshots: true,
	warmOrbs: 1,
	createdAt: 1,
	updatedAt: 1,
};

const POOL_ORB: Orb = {
	id: "orb-pool0000000000000000000a1",
	providerId: "prov_1",
	type: "medium",
	cpus: 4,
	memoryMB: 8192,
	image: "oryn-orb:dev",
	state: "setting_up",
	stateChangedAt: 100,
	createdAt: 1,
	threadIds: [],
	projectId: PROJECT.id,
	pool: "warming",
};

const snap = (
	state: Schemas["ProjectSnapshot"]["state"],
): Schemas["ProjectSnapshot"] => ({
	id: "snap-1",
	providerId: "prov_1",
	providerName: "local",
	type: "large",
	state,
	configHash: "h",
	depth: 0,
	lineageAt: 0,
	createdAt: 1,
	useCount: 0,
});

let snapshots: Schemas["ProjectSnapshot"][];
let orbs: Orb[];
let gets: string[];

const snapGets = () =>
	gets.filter((p) => p === `/api/v1/projects/${PROJECT.id}/snapshots`).length;
const orbGets = () => gets.filter((p) => p === "/api/v1/orbs").length;

/**
 * 推进假时钟，再多走 20ms 让其间发出的 fetch 落地（响应解析到渲染之间有几毫秒的内部定时器）。
 * 20ms 远小于合并窗口与兜底间隔，不会把下一轮刷新提前触发。
 */
const tick = (ms = 0) =>
	act(async () => {
		await vi.advanceTimersByTimeAsync(ms);
		await vi.advanceTimersByTimeAsync(20);
	});

const push = (over: Partial<Orb>) =>
	act(() => {
		acceptOrbPush({ ...POOL_ORB, ...over });
	});

const card = () => screen.getByTestId("project-accel");
const snapRow = () =>
	card().querySelector('[data-snapshot="snap-1"]') as HTMLElement;
const poolRow = () => card().querySelector(`[data-pool-orb="${POOL_ORB.id}"]`);
const clearButton = () =>
	screen.queryByRole("button", { name: i18n.t("accel.clear") });

function mount() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	const ui = () => (
		<QueryClientProvider client={client}>
			<AccelCard project={PROJECT} />
		</QueryClientProvider>
	);
	const r = render(ui());
	return { ...r, rerenderSame: () => r.rerender(ui()) };
}

beforeEach(() => {
	vi.useFakeTimers();
	snapshots = [snap("building")];
	orbs = [POOL_ORB];
	gets = [];
	indexStore.setState({ orbs: {} });
	vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
		const req = input instanceof Request ? input : new Request(String(input));
		const path = new URL(req.url, "http://x").pathname;
		gets.push(path);
		if (path === `/api/v1/projects/${PROJECT.id}/snapshots`)
			return Response.json({ snapshots });
		if (path === "/api/v1/orbs") return Response.json({ orbs });
		return Response.json({ code: "not_found" }, { status: 404 });
	});
});

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
	indexStore.setState({ orbs: {} });
});

describe("AccelCard 实时更新", () => {
	it("后台快照 building→ready、池 orb warming→idle→离池：行与清除按钮随推送更新，GET 不随渲染重复", async () => {
		const { rerenderSame } = mount();
		await tick();
		expect(snapRow()).toHaveTextContent(i18n.t("accel.state.building"));
		expect(clearButton()).toBeNull();
		expect(poolRow()).toHaveTextContent(i18n.t("accel.pool.warming"));
		expect([snapGets(), orbGets()]).toEqual([1, 1]);

		// 反复渲染不触发请求
		for (let i = 0; i < 5; i++) rerenderSame();
		await tick();
		expect([snapGets(), orbGets()]).toEqual([1, 1]);

		// 快照发布：来源池 orb 的旁注推送（状态、池都没变）→ 窗口后刷新一次
		snapshots = [snap("ready")];
		push({});
		push({});
		await tick(PUSH_REFRESH_WINDOW_MS);
		expect(snapGets()).toBe(2);
		expect(snapRow()).toHaveTextContent(i18n.t("accel.state.ready"));
		expect(clearButton()).not.toBeNull();

		// 预热完成：warming → idle（同时 ready），池行直接取推送
		push({ state: "ready", stateChangedAt: 200, pool: "idle" });
		expect(poolRow()).toHaveTextContent(i18n.t("accel.pool.idle"));
		await tick(PUSH_REFRESH_WINDOW_MS);
		expect(snapGets()).toBe(3);

		// 被认领后离池（状态不变，只有池变化）：行消失，显示「待补位」
		const claimed: Partial<Orb> = {
			state: "ready",
			stateChangedAt: 200,
			pool: undefined,
			threadIds: ["T-1"],
		};
		push(claimed);
		expect(poolRow()).toBeNull();
		expect(screen.getByTestId("pool-empty")).toHaveTextContent(
			i18n.t("accel.pool.pending"),
		);
		await tick(PUSH_REFRESH_WINDOW_MS);
		expect(snapGets()).toBe(4);

		// 就绪 orb 的无关推送（状态 / 池都没变、无在建快照）不刷新；orb 列表始终只拉一次
		push({ ...claimed, lastUserInteractionAt: 500 });
		await tick(PUSH_REFRESH_WINDOW_MS);
		expect([snapGets(), orbGets()]).toEqual([4, 1]);
	});

	it("在建快照 30s 兜底刷新，没有在建 / 预热即停；卸载后不再刷新", async () => {
		orbs = [];
		const { unmount } = mount();
		await tick();
		expect(snapGets()).toBe(1);
		await tick(SNAPSHOT_POLL_MS);
		expect(snapGets()).toBe(2);

		// 构建完成（假设推送漏掉）：兜底拉到 ready 后不再轮询
		snapshots = [snap("ready")];
		await tick(SNAPSHOT_POLL_MS);
		expect(snapGets()).toBe(3);
		expect(snapRow()).toHaveTextContent(i18n.t("accel.state.ready"));
		expect(clearButton()).not.toBeNull();
		await tick(SNAPSHOT_POLL_MS * 3);
		expect(snapGets()).toBe(3);

		// 又有快照在建（经推送得知）→ 恢复兜底；卸载后轮询与推送订阅都停止
		snapshots = [snap("building")];
		push({});
		await tick(PUSH_REFRESH_WINDOW_MS);
		expect(snapGets()).toBe(4);
		unmount();
		push({ state: "ready", stateChangedAt: 300 });
		await tick(SNAPSHOT_POLL_MS * 3);
		expect(snapGets()).toBe(4);
	});
});
