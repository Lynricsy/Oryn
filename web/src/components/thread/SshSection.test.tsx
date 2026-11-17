/**
 * 信息面板「SSH 主机」卡（M18）：orb 线程列出经网关可用的主机，全自动 orb 上未放开的主机标「全自动不可用」；
 * runner 线程走 omp 的 `get_ssh_hosts`（那台机器自己的配置）；全局与本项目都没有主机时不渲染。
 */
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
import i18n from "../../i18n";
import { MockSsh } from "../../mock/ssh";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import type { ThreadViewState } from "../../state/thread-reducer";
import { uiStore } from "../../state/ui";
import { SshSection } from "./SshSection";

const { cmd } = vi.hoisted(() => ({ cmd: vi.fn() }));
vi.mock("../../realtime/socket", () => ({
	realtime: { cmd: (...args: unknown[]) => cmd(...args) },
}));

const T = "T-ssh";
const PROJECT = "prj_01JCZ0000000000000000ORYN";

let ssh: MockSsh;

function setup(orb: Partial<Orb>, opts: { hosts?: "none" } = {}) {
	const fullOrb = { id: "orb-ssh", state: "ready", ...orb } as Orb;
	const thread = {
		id: T,
		kind: "work",
		orbId: fullOrb.id,
		projectId: PROJECT,
	} as Thread;
	ssh = new MockSsh({
		changed: () => {},
		threads: new Map([[T, thread]]),
		orbs: new Map([[fullOrb.id, fullOrb]]),
	});
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: Request) => {
			const u = new URL(input.url);
			const p = u.pathname.slice("/api/v1/".length);
			if (opts.hosts === "none" && p === "ssh/hosts")
				return Response.json({ hosts: [] });
			return (
				(await ssh.handle(input, p, input.method.toUpperCase(), u)) ??
				new Response(null, { status: 404 })
			);
		}),
	);
	cmd.mockImplementation(async (_t: string, f: Record<string, unknown>) =>
		ssh.runnerCmd(f.type as "get_ssh_hosts" | "ssh_manage" | "ssh_test", f),
	);
	getThreadStore(T)
		.getState()
		.patchLocal({
			agentStatus: "ready",
			thread,
			orb: fullOrb,
		} as unknown as Partial<ThreadViewState>);
	return render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<MemoryRouter>
				<SshSection threadId={T} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
	resetThreadStores();
	uiStore.setState({ collapsedCards: {} });
	cmd.mockReset();
});

afterEach(() => vi.unstubAllGlobals());

const expand = async () =>
	fireEvent.click(await screen.findByRole("button", { name: "SSH 主机" }));

describe("SshSection", () => {
	it("全自动 orb：列出全局与本项目主机，未放开的主机置灰并带「全自动不可用」", async () => {
		setup({ kind: "orb", projectId: PROJECT, automation: true });
		await expand();
		const panel = await screen.findByTestId("ssh-panel");
		const row = (name: string) =>
			within(panel).getByText(name).closest("li") as HTMLElement;
		expect(row("bastion")).not.toHaveTextContent("全自动不可用");
		expect(row("prod-web")).toHaveTextContent("全自动不可用");
		expect(row("staging-db")).toHaveTextContent(
			"postgres@staging-db.internal:2222",
		);
		expect(
			within(row("bastion")).getByRole("button", { name: "复制 ssh bastion" }),
		).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "管理主机" })).toHaveAttribute(
			"href",
			"/settings/agent-ssh",
		);
		expect(cmd).not.toHaveBeenCalled();
	});

	it("runner 线程：展开后经 omp 的 get_ssh_hosts 列出那台机器的主机，不查控制面", async () => {
		setup({ kind: "runner" });
		await expand();
		const list = await screen.findByTestId("ssh-runner");
		expect(within(list).getByText("nas").closest("li")).toHaveTextContent(
			"admin@192.168.1.20:22",
		);
		// native 项不可编辑
		expect(
			screen.queryByRole("button", { name: "编辑 github.com" }),
		).toBeNull();
		expect(cmd).toHaveBeenCalledWith(T, { type: "get_ssh_hosts" });
		const calls = vi.mocked(globalThis.fetch).mock.calls;
		expect(
			calls.some(([r]) => new URL((r as Request).url).pathname.includes("ssh")),
		).toBe(false);

		fireEvent.click(screen.getByRole("button", { name: "测试 nas" }));
		await waitFor(() =>
			expect(cmd).toHaveBeenCalledWith(
				T,
				expect.objectContaining({ type: "ssh_test" }),
			),
		);
	});

	it("全局与本项目都没有主机时不渲染", async () => {
		const { container } = setup(
			{ kind: "orb", projectId: PROJECT },
			{ hosts: "none" },
		);
		await waitFor(() => expect(globalThis.fetch).toHaveBeenCalled());
		expect(container).toBeEmptyDOMElement();
	});
});
