import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Orb, Thread } from "../../api/client";
import type { SetupRun } from "../../lib/env-banner";
import { realtime } from "../../realtime/socket";
import {
	getThreadStore,
	indexStore,
	resetThreadStores,
} from "../../state/stores";
import { uiStore } from "../../state/ui";
import { ThreadDialogs } from "./Dialogs";
import { EnvBanner } from "./EnvBanner";

const TID = "T-env-test";
const ORB = "orb-envtest";
const LOG = `${Array.from({ length: 100 }, (_, i) => `log-${i + 1}`).join("\n")}\n`;

function run(p: Partial<SetupRun> = {}): SetupRun {
	return {
		id: "osr_1",
		orbId: ORB,
		phase: "setup",
		trigger: "auto",
		startedAt: 0,
		finishedAt: Date.now() - 60_000,
		ok: false,
		setupRan: true,
		durationMs: 30_000,
		hasRepoScript: true,
		hasProjectScript: false,
		error: "apt-get 退出码 100",
		...p,
	};
}

function orb(p: Partial<Orb> = {}): Orb {
	return {
		id: ORB,
		providerId: "prov_1",
		type: "medium",
		cpus: 4,
		memoryMB: 8192,
		image: "oryn-orb:dev",
		projectId: "prj_1",
		state: "ready",
		stateChangedAt: Date.now(),
		createdAt: 0,
		threadIds: [TID],
		...p,
	};
}

function thread(p: Partial<Thread> = {}): Thread {
	return {
		id: TID,
		title: "t",
		titleSource: "auto",
		state: "idle",
		stateChangedAt: 0,
		pinned: false,
		labels: [],
		unread: false,
		createdAt: 0,
		updatedAt: 0,
		projectId: "prj_1",
		orbId: ORB,
		...p,
	};
}

/** openapi-fetch 传入的 Request 可能来自另一个 realm，按字段取 url 而不是 instanceof。 */
function urlOf(input: unknown): string {
	return typeof input === "object" && input !== null && "url" in input
		? String(input.url)
		: String(input);
}

/** 日志接口返回的正文；个别用例换成带特定报错的日志 */
let logBody = LOG;

const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
	const url = new URL(urlOf(input), "http://x");
	if (url.pathname === "/api/v1/projects")
		return Response.json({
			projects: [
				{
					id: "prj_1",
					name: "p",
					repoUrl: "https://github.com/x/y",
					baseBranch: "main",
					shallowClone: false,
					createdAt: 0,
					updatedAt: 0,
				},
			],
		});
	if (url.pathname.endsWith("/log"))
		return new Response(logBody, { headers: { "Content-Type": "text/plain" } });
	if (url.pathname.endsWith("/wake"))
		return new Response(null, { status: 202 });
	return Response.json({ code: "not_found", message: "" }, { status: 404 });
});

function setup(o: Orb, t: Thread = thread(), running = false) {
	indexStore.getState().upsertOrb(o);
	getThreadStore(TID).getState().patchLocal({ thread: t, running });
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<EnvBanner key={TID} threadId={TID} />
			<ThreadDialogs threadId={TID} />
		</QueryClientProvider>,
	);
}

describe("环境提示条", () => {
	beforeEach(() => {
		localStorage.clear();
		resetThreadStores();
		uiStore.getState().close();
		vi.stubGlobal("fetch", fetchMock);
		fetchMock.mockClear();
		logBody = LOG;
	});
	afterEach(() => {
		vi.unstubAllGlobals();
		vi.restoreAllMocks();
	});

	it("setup 失败：显示错误，「让 agent 修复」以 prompt 发送带日志尾部的提示词", async () => {
		const cmd = vi.spyOn(realtime, "cmd").mockResolvedValue(undefined);
		setup(orb({ lastSetup: run() }));
		expect(screen.getByText("环境 setup 失败")).toBeInTheDocument();
		expect(screen.getByText("apt-get 退出码 100")).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: /让 agent 修复/ }));
		await waitFor(() => expect(cmd).toHaveBeenCalledTimes(1));
		const [tid, frame] = cmd.mock.calls[0] ?? [];
		expect(tid).toBe(TID);
		if (frame?.type !== "prompt")
			throw new Error(`应为 prompt：${frame?.type}`);
		const message = frame.message;
		expect(message).toContain("oryn-setup");
		expect(message).toContain("apt-get 退出码 100");
		expect(message).toContain("log-100");
		expect(message).toContain("log-21\n");
		expect(message).not.toContain("log-20\n");
		expect(
			await screen.findByRole("button", { name: /已交给 agent/ }),
		).toBeDisabled();
	});

	it("agent 忙时改为排队（follow_up），不打断当前工作", async () => {
		const cmd = vi.spyOn(realtime, "cmd").mockResolvedValue(undefined);
		setup(orb({ lastSetup: run() }), thread({ state: "working" }), true);
		fireEvent.click(screen.getByRole("button", { name: /让 agent 修复/ }));
		await waitFor(() => expect(cmd).toHaveBeenCalledTimes(1));
		expect(cmd.mock.calls[0]?.[1]).toMatchObject({ type: "follow_up" });
	});

	it("关闭后按运行记录记住；同一线程出现新的失败记录时再次显示", () => {
		const view = setup(orb({ lastSetup: run({ id: "osr_old" }) }));
		fireEvent.click(screen.getByRole("button", { name: "忽略" }));
		expect(screen.queryByTestId("env-banner")).toBeNull();
		view.unmount();
		// 刷新后仍保持关闭
		setup(orb({ lastSetup: run({ id: "osr_old" }) })).unmount();
		expect(screen.queryByTestId("env-banner")).toBeNull();
		setup(orb({ lastSetup: run({ id: "osr_new", error: "又失败了" }) }));
		expect(screen.getByText("又失败了")).toBeInTheDocument();
	});

	it("查看日志：对话框加载该次运行的日志", async () => {
		setup(orb({ lastSetup: run({ id: "osr_log" }) }));
		fireEvent.click(screen.getByRole("button", { name: /查看日志/ }));
		const pre = await screen.findByTestId("setup-log");
		expect(pre.textContent).toContain("log-100");
		expect(
			fetchMock.mock.calls.some(([u]) =>
				urlOf(u).endsWith(`/orbs/${ORB}/setup-runs/osr_log/log`),
			),
		).toBe(true);
	});

	it("失败原因：按退出码与日志尾部写出「哪一步、为什么」和下一步建议，原文留在日志对话框里", async () => {
		logBody =
			"running .oryn/setup\n+ pnpm install\n.oryn/setup: line 4: pnpm: command not found\n";
		setup(
			orb({
				lastSetup: run({
					id: "osr_127",
					error: ".oryn/setup: exit status 127",
				}),
			}),
		);
		// 日志到达前就能按退出码判断，到达后补上具体命令名
		expect(
			await screen.findByText("「.oryn/setup」调用的命令 pnpm 不存在"),
		).toBeInTheDocument();
		expect(
			screen.getByText("在脚本里先安装它，或确认它在 PATH 里。"),
		).toBeInTheDocument();
		expect(screen.queryByText(".oryn/setup: exit status 127")).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: /查看日志/ }));
		const dialog = await screen.findByRole("dialog");
		expect(dialog).toHaveTextContent("「.oryn/setup」调用的命令 pnpm 不存在");
		expect(dialog).toHaveTextContent(".oryn/setup: exit status 127");
	});

	it("orb 失败：显示原因，没有关闭按钮，「重试」调用 wake", async () => {
		setup(
			orb({ state: "failed", failureReason: "orb 原因" }),
			thread({
				state: "error",
				error: { kind: "orb_failed", reason: "pull access denied" },
			}),
		);
		expect(screen.getByText("orb 启动失败")).toBeInTheDocument();
		expect(screen.getByText("pull access denied")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "忽略" })).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "重试" }));
		await waitFor(() =>
			expect(
				fetchMock.mock.calls.some(([u]) =>
					urlOf(u).endsWith(`/threads/${TID}/wake`),
				),
			).toBe(true),
		);
	});

	it("缺少配置：需要项目信息加载后才出现，并提供「让 agent 生成」", async () => {
		setup(orb({ lastSetup: run({ ok: true, hasRepoScript: false }) }));
		expect(
			await screen.findByText("这个项目还没有环境配置"),
		).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /查看日志/ })).toBeNull();
		expect(
			screen.getByRole("button", { name: /让 agent 生成/ }),
		).toBeInTheDocument();
	});
});
