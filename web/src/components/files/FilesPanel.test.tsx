import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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
let listCalls: string[];

beforeEach(() => {
	resetOrbFs();
	localStorage.clear();
	listCalls = [];
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

const tab = (key: string) => screen.findByRole("tab", { name: t(key) });

describe("FilesPanel", () => {
	it("有 orb：根切换器为 Workspace | Home | Agent 配置，默认打开工作区", async () => {
		render(<FilesPanel threadId="T-f1" orbId={ORB} />);
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
		render(<FilesPanel threadId="T-f2" />);
		expect(await screen.findByText(t("files.orbDeleted"))).toBeInTheDocument();
		expect(await tab("files.roots.agentConfig")).toBeInTheDocument();
		expect(
			screen.queryByRole("tab", { name: t("files.roots.workspace") }),
		).toBeNull();
		expect(await screen.findByText("AGENTS.md")).toBeInTheDocument();
	});

	it("旧版 orbd：提示新建线程，Agent 配置仍可用", async () => {
		render(
			<FilesPanel threadId="T-f3" orbId="orb-old00000000000000000000e4" />,
		);
		expect(await screen.findByText(t("files.outdated"))).toBeInTheDocument();
		expect(await tab("files.roots.agentConfig")).toBeInTheDocument();
	});

	it("agent 回合结束后 1s 防抖刷新已展开的目录", async () => {
		render(<FilesPanel threadId="T-f4" orbId={ORB} />);
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
});
