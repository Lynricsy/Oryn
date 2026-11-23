import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Orb, Thread } from "../../api/client";
import { loadDismissed, type SetupRun } from "../../lib/env-banner";
import { realtime } from "../../realtime/socket";
import {
	getThreadStore,
	indexStore,
	resetThreadStores,
} from "../../state/stores";
import { toastsStore } from "../../state/toasts";
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

/** 新建线程请求的 JSON 正文（「新开线程生成」用例断言） */
const created: unknown[] = [];

const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
	const url = new URL(urlOf(input), "http://x");
	if (
		url.pathname === "/api/v1/threads" &&
		typeof input === "object" &&
		"method" in input &&
		input.method === "POST"
	) {
		created.push(await input.json());
		return Response.json(
			{ thread: thread({ id: "T-env-gen", orbId: "orb-gen" }) },
			{ status: 201 },
		);
	}
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

function setup(o: Orb, t: Thread = thread()) {
	indexStore.getState().upsertOrb(o);
	getThreadStore(TID).getState().patchLocal({ thread: t });
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<MemoryRouter>
			<QueryClientProvider client={client}>
				<EnvBanner key={TID} threadId={TID} />
				<ThreadDialogs threadId={TID} />
			</QueryClientProvider>
		</MemoryRouter>,
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
		created.length = 0;
		toastsStore.setState({ toasts: [] });
	});
	afterEach(() => {
		vi.unstubAllGlobals();
		vi.restoreAllMocks();
	});

	it("setup 失败：「新开线程修复」带日志尾部在新 orb 上建线程，不打扰正在工作的当前线程", async () => {
		const cmd = vi.spyOn(realtime, "cmd").mockResolvedValue(undefined);
		setup(orb({ lastSetup: run() }), thread({ state: "working" }));
		expect(screen.getByText("环境 setup 失败")).toBeInTheDocument();
		expect(screen.getByText("apt-get 退出码 100")).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: /新开线程修复/ }));
		await waitFor(() => expect(screen.queryByTestId("env-banner")).toBeNull());
		expect(cmd).not.toHaveBeenCalled();
		expect(created).toHaveLength(1);
		expect(created[0]).toMatchObject({
			projectId: "prj_1",
			executor: { kind: "new_orb" },
			browserProfileId: "",
		});
		// 标题与普通线程一样自动生成，不写死
		expect(created[0]).not.toHaveProperty("title");
		const { prompt } = created[0] as { prompt: string };
		expect(prompt).toContain("oryn-setup");
		expect(prompt).toContain("apt-get 退出码 100");
		expect(prompt).toContain("log-100");
		expect(prompt).toContain("log-21\n");
		expect(prompt).not.toContain("log-20\n");
		// 新 orb 的 setup 多半同样失败：新线程里不再提示去开修复线程
		expect(loadDismissed("T-env-gen").has("run_failed")).toBe(true);
		expect(toastsStore.getState().toasts[0]?.text).toContain("修复环境配置");
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

	it("日志对话框里「新开线程修复」：同样新建线程，关闭对话框，提示条随之收起", async () => {
		setup(orb({ lastSetup: run({ id: "osr_dlg" }) }));
		fireEvent.click(screen.getByRole("button", { name: /查看日志/ }));
		const dialog = await screen.findByRole("dialog");
		fireEvent.click(
			within(dialog).getByRole("button", { name: /新开线程修复/ }),
		);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(created).toHaveLength(1);
		expect(screen.queryByTestId("env-banner")).toBeNull();
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

	it("agent 没能启动：显示 orbd 给的原因，没有关闭按钮，「重试」调用 wake", async () => {
		const reason =
			'dir_not_served: "/tmp/m11-a/esc" is not inside a directory served by this runner';
		setup(
			orb({ lastSetup: run({ ok: false }) }),
			thread({ state: "error", error: { kind: "agent_failed", reason } }),
		);
		expect(screen.getByText("agent 没能启动")).toBeInTheDocument();
		expect(screen.getByText(reason)).toBeInTheDocument();
		expect(screen.queryByText("orb 启动失败")).toBeNull();
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

	it("缺少配置：需要项目信息加载后才出现，「新开线程生成」在新 orb 上建线程、留在当前线程并收起提示条", async () => {
		const cmd = vi.spyOn(realtime, "cmd").mockResolvedValue(undefined);
		setup(orb({ lastSetup: run({ ok: true, hasRepoScript: false }) }));
		expect(
			await screen.findByText("这个项目还没有环境配置"),
		).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /查看日志/ })).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: /新开线程生成/ }));
		await waitFor(() => expect(screen.queryByTestId("env-banner")).toBeNull());
		// 不往当前线程发消息，而是新建一条同项目、新 orb 的可见线程
		expect(cmd).not.toHaveBeenCalled();
		expect(created).toHaveLength(1);
		expect(created[0]).toMatchObject({
			projectId: "prj_1",
			executor: { kind: "new_orb" },
			browserProfileId: "",
			prompt: expect.stringContaining("oryn-setup"),
		});
		expect(created[0]).not.toHaveProperty("title");
		// 刷新后当前线程仍不再提示；新线程里整类不提示（它正在做这件事）
		expect(loadDismissed(TID).has("missing:osr_1")).toBe(true);
		expect(loadDismissed("T-env-gen").has("missing_config")).toBe(true);
		const [toast] = toastsStore.getState().toasts;
		expect(toast?.text).toContain("编写环境配置");
		expect(toast?.action?.label).toBe("打开线程");
	});
});
