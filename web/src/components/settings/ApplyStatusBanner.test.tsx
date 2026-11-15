/**
 * ApplyStatusBanner：列出需重启的会话与原因，「重启这些会话」POST 后显示已安排；
 * 写入后按服务端 push.pending 显示正在下发 / 已下发；有 orb 没下发成功时列出；没有可报告的内容时不渲染。
 */
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
	type ApplyStatus,
	useAgentFileActions,
} from "../../api/agent-config-queries";
import {
	agentFetch,
	renderPage,
	resetHome,
} from "../../pages/settings/agent/agent-config-mock";
import { ApplyStatusBanner } from "./ApplyStatusBanner";

const IDLE_PUSH: ApplyStatus["push"] = { pending: false, failed: [] };
const EMPTY: ApplyStatus = { threads: [], pendingReloads: 0, push: IDLE_PUSH };

let status: ApplyStatus = EMPTY;
let restarts = 0;
/** 设了它时，下一个 apply-status 请求按发出时的状态作答，但等它兑现后才返回（模拟写入前发出、写入后才到的应答）。 */
let holdNextStatus: Promise<void> | undefined;

const fetchMock = vi.fn(
	async (input: RequestInfo | URL, init?: RequestInit) => {
		const req =
			input instanceof Request
				? input
				: new Request(new URL(String(input), "http://x"), init);
		const { pathname } = new URL(req.url, "http://x");
		if (pathname === "/api/v1/agent-config/apply-status") {
			const snapshot = status;
			const hold = holdNextStatus;
			holdNextStatus = undefined;
			if (hold) await hold;
			return Response.json(snapshot);
		}
		// 服务端在应答写入之前就登记了待下发
		if (
			pathname.startsWith("/api/v1/agent-config/files/") &&
			req.method === "PUT"
		)
			status = { ...status, push: { ...status.push, pending: true } };
		if (pathname === "/api/v1/agent-config/restart-sessions") {
			expect(req.method).toBe("POST");
			restarts++;
			status = {
				...status,
				threads: status.threads.map((th) => ({ ...th, scheduled: true })),
			};
			return Response.json({
				threadIds: status.threads.map((th) => th.threadId),
			});
		}
		return agentFetch(req);
	},
);

beforeEach(() => {
	status = EMPTY;
	restarts = 0;
	holdNextStatus = undefined;
	resetHome({});
	vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
	vi.unstubAllGlobals();
	fetchMock.mockClear();
});

const applyStatusCalls = () =>
	fetchMock.mock.calls.filter(([input]) =>
		String(input instanceof Request ? input.url : input).endsWith(
			"/agent-config/apply-status",
		),
	).length;

it("列出需重启的会话与原因；点「重启这些会话」后 POST 并显示已安排", async () => {
	status = {
		threads: [
			{
				threadId: "t1",
				kind: "work",
				title: "修登录页",
				reasons: ["hook:pre:bash:bash.ts"],
				scheduled: false,
			},
		],
		pendingReloads: 2,
		push: IDLE_PUSH,
	};
	renderPage(<ApplyStatusBanner />);

	expect(await screen.findByText("1 个会话需重启后生效")).toBeInTheDocument();
	expect(screen.getByText("修登录页")).toBeInTheDocument();
	expect(screen.getByText("hook:pre:bash:bash.ts")).toBeInTheDocument();
	expect(
		screen.getByText("2 个会话正在跑回合，空闲时自动重新加载。"),
	).toBeInTheDocument();
	expect(screen.queryByText("已安排")).not.toBeInTheDocument();

	fireEvent.click(screen.getByRole("button", { name: "重启这些会话" }));

	expect(await screen.findByText("已安排")).toBeInTheDocument();
	expect(restarts).toBe(1);
	expect(screen.getByText(/已安排重启/)).toBeInTheDocument();
	expect(
		screen.queryByRole("button", { name: "重启这些会话" }),
	).not.toBeInTheDocument();
});

it("没有需重启或待重新加载的会话、也没写入过时不渲染", async () => {
	const { container } = renderPage(<ApplyStatusBanner />);
	await waitFor(() => expect(applyStatusCalls()).toBe(1));
	await Promise.resolve();
	expect(container).toBeEmptyDOMElement();
});

function Writer() {
	const actions = useAgentFileActions();
	return (
		<button type="button" onClick={() => void actions.write("hooks/a.ts", "x")}>
			写入
		</button>
	);
}

it("写入后显示正在下发，服务端下发完毕后提示已下发并带出需重启的会话", async () => {
	renderPage(
		<>
			<Writer />
			<ApplyStatusBanner />
		</>,
	);
	await waitFor(() => expect(applyStatusCalls()).toBe(1));
	fireEvent.click(screen.getByRole("button", { name: "写入" }));

	expect(await screen.findByText(/正在下发/)).toBeInTheDocument();
	expect(screen.queryByText(/^已下发/)).not.toBeInTheDocument();

	// 下发完成：会话级分类随之落定
	status = {
		threads: [
			{
				threadId: "t2",
				kind: "work",
				title: "",
				reasons: ["hooks/a.ts"],
				scheduled: false,
			},
		],
		pendingReloads: 0,
		push: { pending: false, appliedAt: Date.now(), failed: [] },
	};
	expect(
		await screen.findByText(/^已下发/, undefined, { timeout: 3000 }),
	).toBeInTheDocument();
	expect(screen.queryByText(/正在下发/)).not.toBeInTheDocument();
	expect(screen.getByText("1 个会话需重启后生效")).toBeInTheDocument();
	expect(screen.getByText("未命名会话")).toBeInTheDocument();
});

it("写入之前发出、写入之后才到的旧应答不算下发完毕", async () => {
	// 项目 lib 没有 Promise.withResolvers
	let release = () => {};
	holdNextStatus = new Promise<void>((r) => {
		release = r;
	});
	renderPage(
		<>
			<Writer />
			<ApplyStatusBanner />
		</>,
	);
	await waitFor(() => expect(applyStatusCalls()).toBe(1));
	fireEvent.click(screen.getByRole("button", { name: "写入" }));
	expect(await screen.findByText(/正在下发/)).toBeInTheDocument();
	// 写入前发出的请求现在才带着 pending=false 返回
	release();
	// 之后的轮询（写入之后发出）读到 pending=true
	await waitFor(() => expect(applyStatusCalls()).toBeGreaterThanOrEqual(2), {
		timeout: 3000,
	});
	expect(screen.getByText(/正在下发/)).toBeInTheDocument();
	expect(screen.queryByText(/^已下发/)).not.toBeInTheDocument();

	status = { ...status, push: { ...status.push, pending: false } };
	expect(
		await screen.findByText(/^已下发/, undefined, { timeout: 3000 }),
	).toBeInTheDocument();
});

it("最近一次下发有 orb 失败时列出 orb 与原因，不显示已下发", async () => {
	status = {
		...EMPTY,
		push: {
			pending: false,
			appliedAt: Date.now(),
			failed: [{ orbId: "orb-7", error: "orb 未就绪" }],
		},
	};
	renderPage(<ApplyStatusBanner />);
	expect(await screen.findByText("1 个 orb 没能下发")).toBeInTheDocument();
	expect(screen.getByText("orb-7")).toBeInTheDocument();
	expect(screen.getByText("orb 未就绪")).toBeInTheDocument();
	expect(screen.queryByText(/^已下发/)).not.toBeInTheDocument();
});
