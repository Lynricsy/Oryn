import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Orb } from "../../api/client";
import i18n from "../../i18n";
import { handleOrbFetch, resetOrbFs } from "../../mock/orbfs";
import { getThreadStore } from "../../state/stores";
import FilesPanel from "./FilesPanel";
import { ACTIVITY_DEBOUNCE_MS } from "./hooks";

vi.mock("../../state/terminals", () => ({
	terminalHub: { onExit: vi.fn(() => () => {}) },
}));

const t = (k: string, o: Record<string, unknown> = {}) =>
	i18n.t(k as "common.save", o);
const ORB = "orb-files00000000000000000001";
/** 停机预览对这台 orb 回 409 `offline_unsupported`（provider 不支持） */
const ORB_NO_OFFLINE = "orb-nooff00000000000000000001";
let listCalls: string[];
/** 带 `offline` 的 orb 文件请求 */
let offlineCalls: string[];
let wakeCalls: string[];

beforeEach(() => {
	resetOrbFs();
	localStorage.clear();
	listCalls = [];
	offlineCalls = [];
	wakeCalls = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const url = new URL(req.url);
			if (url.pathname.endsWith("/fs/list"))
				listCalls.push(url.searchParams.get("path") ?? "");
			if (url.searchParams.has("offline")) offlineCalls.push(url.pathname);
			const wake = /^\/api\/v1\/threads\/([^/]+)\/wake$/.exec(url.pathname);
			if (wake && req.method === "POST") {
				wakeCalls.push(wake[1] ?? "");
				return new Response(null, { status: 202 });
			}
			if (
				url.pathname.startsWith(`/api/v1/orbs/${ORB_NO_OFFLINE}/`) &&
				url.searchParams.has("offline")
			)
				return Response.json(
					{ code: "offline_unsupported", message: "provider 不支持停机预览" },
					{ status: 409 },
				);
			if (url.pathname === "/api/v1/projects/prj_a/agent-config/files")
				return Response.json({
					files: [
						{
							path: "rules/p.md",
							size: 3,
							mode: 420,
							sha256: "0".repeat(64),
							updatedAt: 1,
						},
					],
				});
			if (url.pathname === "/api/v1/agent-config/files")
				return Response.json({
					files: [
						{
							path: "AGENTS.md",
							size: 3,
							mode: 420,
							sha256: "0".repeat(64),
							updatedAt: 1,
						},
					],
				});
			return (await handleOrbFetch(req)) ?? new Response(null, { status: 404 });
		}),
	);
});
afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

/** 面板里的「发布为公开链接」用到 TanStack Query 的 mutation，需要 QueryClient。 */
function renderPanel(threadId: string, orbId?: string, projectId?: string) {
	const client = new QueryClient();
	return render(
		<QueryClientProvider client={client}>
			<FilesPanel threadId={threadId} orbId={orbId} projectId={projectId} />
		</QueryClientProvider>,
	);
}

const tab = (key: string) => screen.findByRole("tab", { name: t(key) });

describe("FilesPanel", () => {
	it("有 orb：根切换器为 Workspace | Home | Agent 配置，默认打开工作区", async () => {
		renderPanel("T-f1", ORB);
		expect(await tab("files.roots.workspace")).toHaveAttribute(
			"aria-selected",
			"true",
		);
		expect(await tab("files.roots.home")).toBeInTheDocument();
		expect(await tab("files.roots.agentConfig")).toBeInTheDocument();
		expect(await screen.findByText("README.md")).toBeInTheDocument();
		fireEvent.click(await tab("files.roots.agentConfig"));
		expect(await screen.findByText("AGENTS.md")).toBeInTheDocument();
	});

	it("orb 已删除：只剩 Agent 配置并提示", async () => {
		renderPanel("T-f2");
		expect(await screen.findByText(t("files.orbDeleted"))).toBeInTheDocument();
		expect(await tab("files.roots.agentConfig")).toBeInTheDocument();
		expect(
			screen.queryByRole("tab", { name: t("files.roots.workspace") }),
		).toBeNull();
		expect(await screen.findByText("AGENTS.md")).toBeInTheDocument();
	});

	it("旧版 orbd：提示新建线程，Agent 配置仍可用", async () => {
		renderPanel("T-f3", "orb-old00000000000000000000e4");
		expect(await screen.findByText(t("files.outdated"))).toBeInTheDocument();
		expect(await tab("files.roots.agentConfig")).toBeInTheDocument();
	});

	it("线程属于项目：Agent 配置之前多一个项目 Agent 配置根，列的是该项目的配置", async () => {
		renderPanel("T-f5", ORB, "prj_a");
		await tab("files.roots.agentConfig");
		expect(screen.getAllByRole("tab").map((el) => el.textContent)).toEqual([
			t("files.roots.workspace"),
			t("files.roots.home"),
			t("files.roots.projectAgentConfig"),
			t("files.roots.agentConfig"),
		]);
		fireEvent.click(await tab("files.roots.projectAgentConfig"));
		expect(await screen.findByText("rules")).toBeInTheDocument();
		expect(screen.queryByText("AGENTS.md")).toBeNull();
	});

	it("线程属于项目但 orb 已删除：两份 Agent 配置都在", async () => {
		renderPanel("T-f6", undefined, "prj_a");
		expect(await tab("files.roots.projectAgentConfig")).toBeInTheDocument();
		expect(await tab("files.roots.agentConfig")).toBeInTheDocument();
	});

	it("agent 回合结束后 1s 防抖刷新已展开的目录", async () => {
		renderPanel("T-f4", ORB);
		await screen.findByText("README.md");
		await waitFor(() =>
			expect(listCalls).toEqual(["/home/user/workspace/repo"]),
		);
		vi.useFakeTimers();
		act(() => getThreadStore("T-f4").getState().patchLocal({ running: true }));
		act(() => getThreadStore("T-f4").getState().patchLocal({ running: false }));
		await act(async () => {
			await vi.advanceTimersByTimeAsync(ACTIVITY_DEBOUNCE_MS - 10);
		});
		expect(listCalls).toHaveLength(1);
		await act(async () => {
			await vi.advanceTimersByTimeAsync(20);
		});
		expect(listCalls).toHaveLength(2);
	});

	describe("orb 停机：只读预览", () => {
		const setOrbState = (threadId: string, orbId: string, state: string) =>
			getThreadStore(threadId)
				.getState()
				.patchLocal({ orb: { id: orbId, state } as Orb });

		it("带 offline 读取、显示提示条，隐藏一切写入口；离开停机后换回在线来源", async () => {
			setOrbState("T-off1", ORB, "stopped");
			renderPanel("T-off1", ORB);
			expect(
				await screen.findByText(t("files.offline.banner")),
			).toBeInTheDocument();
			expect(await screen.findByText("README.md")).toBeInTheDocument();
			expect(offlineCalls).toContain(`/api/v1/orbs/${ORB}/fs/roots`);
			expect(offlineCalls).toContain(`/api/v1/orbs/${ORB}/fs/list`);
			for (const key of [
				"files.actions.newFile",
				"files.actions.newFolder",
				"files.actions.upload",
			])
				expect(screen.queryByRole("button", { name: t(key) })).toBeNull();

			act(() => setOrbState("T-off1", ORB, "starting"));
			await waitFor(() =>
				expect(screen.queryByText(t("files.offline.banner"))).toBeNull(),
			);
			expect(
				await screen.findByRole("button", { name: t("files.actions.newFile") }),
			).toBeInTheDocument();
			const before = offlineCalls.length;
			expect(await screen.findByText("README.md")).toBeInTheDocument();
			expect(offlineCalls).toHaveLength(before);
		});

		it("「唤醒以编辑」唤醒该线程的 orb", async () => {
			setOrbState("T-off2", ORB, "stopped");
			renderPanel("T-off2", ORB);
			fireEvent.click(
				await screen.findByRole("button", { name: t("files.offline.wake") }),
			);
			await waitFor(() => expect(wakeCalls).toEqual(["T-off2"]));
		});

		it("provider 不支持停机预览：就地提示并给出唤醒按钮，Agent 配置仍可用", async () => {
			setOrbState("T-off3", ORB_NO_OFFLINE, "stopped");
			renderPanel("T-off3", ORB_NO_OFFLINE);
			expect(
				await screen.findByText(t("files.offline.unsupported")),
			).toBeInTheDocument();
			expect(
				screen.getAllByRole("button", { name: t("files.offline.wake") }),
			).toHaveLength(2);
			expect(await tab("files.roots.agentConfig")).toBeInTheDocument();
		});
	});
});
