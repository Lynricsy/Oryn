/**
 * 助手的电脑页（M19 步骤 4.4）：三种状态（还没有电脑 / 运行中 / 已冻结）与三个动作（唤醒、暂停、重置），
 * 文件页签走 orb 的文件接口（不带 thread），桌面页签的遮罩唤醒走 `assistant/computer/wake`。走 mock 后端。
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
import { MockServer } from "../mock/server";
import { indexStore } from "../state/stores";
import { AssistantComputerPage } from "./AssistantComputerPage";

let server: MockServer;
let requests: { method: string; url: URL }[];

beforeEach(() => {
	server = new MockServer();
	requests = [];
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			requests.push({ method: req.method, url: new URL(req.url) });
			return (
				(await server.handleFetch(req)) ?? new Response(null, { status: 404 })
			);
		},
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	indexStore.getState().setAll([], 0);
});

/** 直接在 mock 上执行电脑动作（准备初始状态，不经页面）。 */
async function prepare(action: "wake" | "pause") {
	await server.assistant.handle(
		new Request(`http://localhost/api/v1/assistant/computer/${action}`, {
			method: "POST",
		}),
		`assistant/computer/${action}`,
		"POST",
	);
}

function mount() {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={["/assistant/computer"]}>
				<AssistantComputerPage />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const posts = (action: string) =>
	requests.filter(
		(r) =>
			r.method === "POST" &&
			r.url.pathname === `/api/v1/assistant/computer/${action}`,
	);
const stateText = () => screen.getByTestId("computer-state");
const statusCard = () =>
	screen.getByTestId("computer-status").closest("section") as HTMLElement;

describe("助手的电脑页", () => {
	it("还没有电脑：显示说明与「启动」，启动后出现状态卡", async () => {
		mount();
		const empty = await screen.findByTestId("computer-empty");
		expect(empty).toHaveTextContent("助手还没有电脑");
		fireEvent.click(within(empty).getByRole("button", { name: "启动" }));
		await waitFor(() => expect(stateText()).toHaveTextContent("就绪"));
		expect(posts("wake")).toHaveLength(1);
		expect(screen.getByTestId("computer-status")).toHaveTextContent("local");
		expect(screen.getByTestId("computer-status")).toHaveTextContent("medium");
		expect(screen.queryByTestId("computer-empty")).toBeNull();
	});

	it("运行中：文件页签列出电脑的文件（请求不带 thread），暂停后变为已冻结、文件页签给唤醒", async () => {
		await prepare("wake");
		mount();
		await waitFor(() => expect(stateText()).toHaveTextContent("就绪"));
		const card = statusCard();
		expect(within(card).getByRole("button", { name: "唤醒" })).toBeDisabled();
		await screen.findByText("工作区");
		const fsReqs = requests.filter((r) => r.url.pathname.includes("/fs/"));
		expect(fsReqs.length).toBeGreaterThan(0);
		expect(fsReqs.every((r) => !r.url.searchParams.has("thread"))).toBe(true);

		fireEvent.click(within(card).getByRole("button", { name: "暂停" }));
		await waitFor(() => expect(stateText()).toHaveTextContent("已冻结"));
		expect(posts("pause")).toHaveLength(1);
		expect(within(card).getByRole("button", { name: "暂停" })).toBeDisabled();
		expect(
			screen.getByText("电脑没在运行，唤醒后可以浏览文件"),
		).toBeInTheDocument();

		fireEvent.click(within(card).getByRole("button", { name: "唤醒" }));
		await waitFor(() => expect(stateText()).toHaveTextContent("就绪"));
		expect(posts("wake")).toHaveLength(1);
	});

	it("已冻结：桌面页签显示休眠遮罩，遮罩上的唤醒调 assistant/computer/wake", async () => {
		await prepare("wake");
		await prepare("pause");
		mount();
		await waitFor(() => expect(stateText()).toHaveTextContent("已冻结"));
		fireEvent.click(screen.getByRole("tab", { name: "桌面" }));
		const cover = await screen.findByTestId("desktop-asleep");
		fireEvent.click(within(cover).getByRole("button", { name: "唤醒" }));
		await waitFor(() => expect(posts("wake")).toHaveLength(1));
		await waitFor(() =>
			expect(screen.queryByTestId("desktop-asleep")).toBeNull(),
		);
	});

	it("重置：先弹确认，取消不动；确认后删除电脑并回到「还没有电脑」", async () => {
		await prepare("wake");
		mount();
		await waitFor(() => expect(stateText()).toHaveTextContent("就绪"));
		const orbId = [...server.orbs.values()].find((o) => o.computer)?.id;
		expect(orbId).toBeTruthy();

		fireEvent.click(within(statusCard()).getByRole("button", { name: "重置" }));
		const dialog = await screen.findByRole("dialog");
		fireEvent.click(within(dialog).getByRole("button", { name: "取消" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(posts("reset")).toHaveLength(0);

		fireEvent.click(within(statusCard()).getByRole("button", { name: "重置" }));
		fireEvent.click(await screen.findByTestId("computer-reset-confirm"));
		await screen.findByTestId("computer-empty");
		expect(posts("reset")).toHaveLength(1);
		expect(server.orbs.has(orbId as string)).toBe(false);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});
});
