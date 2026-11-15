/**
 * ApplyStatusBanner：列出需重启的会话与原因，「重启这些会话」POST 后显示已安排；
 * 写入后稍等再取生效状态并提示已下发；没有可报告的内容时不渲染。
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

const EMPTY: ApplyStatus = { threads: [], pendingReloads: 0 };

let status: ApplyStatus = EMPTY;
let restarts = 0;

const fetchMock = vi.fn(
	async (input: RequestInfo | URL, init?: RequestInit) => {
		const req =
			input instanceof Request
				? input
				: new Request(new URL(String(input), "http://x"), init);
		const { pathname } = new URL(req.url, "http://x");
		if (pathname === "/api/v1/agent-config/apply-status")
			return Response.json(status);
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

it("写入后等服务端防抖再取生效状态，并提示已下发", async () => {
	renderPage(
		<>
			<Writer />
			<ApplyStatusBanner />
		</>,
	);
	await waitFor(() => expect(applyStatusCalls()).toBe(1));
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
	};
	fireEvent.click(screen.getByRole("button", { name: "写入" }));

	expect(
		await screen.findByText(/^已下发/, undefined, { timeout: 3000 }),
	).toBeInTheDocument();
	expect(applyStatusCalls()).toBe(2);
	expect(screen.getByText("1 个会话需重启后生效")).toBeInTheDocument();
	expect(screen.getByText("未命名会话")).toBeInTheDocument();
});
