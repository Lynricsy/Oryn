/**
 * 设置 → 算力：Provider 与 Runner 两组同构——行尾在线状态与 ⋯（删除），「新建」对话框创建后原地一次性显示令牌。
 * 走 mock 后端。
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
import { MockServer } from "../../mock/server";
import { answerConfirm } from "../../test/confirm";
import ComputeSettings from "./ComputeSettings";

let requests: string[];

beforeEach(() => {
	const server = new MockServer();
	requests = [];
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			const u = new URL(req.url);
			requests.push(`${req.method} ${u.pathname}`);
			return (
				(await server.handleFetch(req)) ?? new Response(null, { status: 404 })
			);
		},
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

function mount() {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={["/settings/compute"]}>
				<ComputeSettings />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const group = (name: string) => screen.getByRole("region", { name });

describe("设置 → 算力", () => {
	it("Provider 与 Runner 各一组：行带在线状态，Provider 组可进总览", async () => {
		mount();
		const providers = group("Provider");
		const local = (await within(providers).findByText("local")).closest(
			"li",
		) as HTMLElement;
		expect(local).toHaveTextContent("docker");
		expect(local).toHaveTextContent("在线");
		expect(local).toHaveTextContent("medium");
		expect(
			within(providers).getByRole("link", { name: "查看总览" }),
		).toHaveAttribute("href", "/providers");
		const runners = group("Runner");
		// 名字与主机名同为 nas
		const nas = (await within(runners).findAllByText("nas"))[0]?.closest(
			"li",
		) as HTMLElement;
		expect(nas).toHaveTextContent("离线");
		expect(nas).toHaveTextContent("/srv/projects/blog");
	});

	it("新建 provider：对话框里填名字，创建后原地显示一次性令牌", async () => {
		mount();
		fireEvent.click(
			within(group("Provider")).getByRole("button", { name: "新建" }),
		);
		const dialog = await screen.findByRole("dialog", { name: "新建 provider" });
		fireEvent.change(within(dialog).getByRole("textbox", { name: "名字" }), {
			target: { value: "gpu-box" },
		});
		fireEvent.click(within(dialog).getByRole("button", { name: "创建" }));
		const token = await within(dialog).findByTestId("compute-token");
		expect(token).toHaveTextContent("provider gpu-box 的令牌只显示这一次");
		expect(token).toHaveTextContent("opv_mock_ONLY_SHOWN_ONCE");
		expect(requests).toContain("POST /api/v1/providers");
		fireEvent.click(within(dialog).getByRole("button", { name: "完成" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	it("新建 runner：令牌与完整启动命令用同一个一次性令牌块", async () => {
		mount();
		fireEvent.click(
			within(group("Runner")).getByRole("button", { name: "新建" }),
		);
		const dialog = await screen.findByRole("dialog", { name: "新建 runner" });
		fireEvent.change(within(dialog).getByRole("textbox", { name: "名字" }), {
			target: { value: "studio-2" },
		});
		fireEvent.click(within(dialog).getByRole("button", { name: "创建" }));
		const token = await within(dialog).findByTestId("compute-token");
		expect(token).toHaveTextContent("runner studio-2 的令牌只显示这一次");
		expect(token).toHaveTextContent("启动命令");
		expect(token).toHaveTextContent("orbd run --runner --dir");
		await waitFor(() =>
			expect(within(group("Runner")).getByText("studio-2")).toBeVisible(),
		);
	});

	it("名字冲突时错误留在对话框里", async () => {
		mount();
		fireEvent.click(
			within(group("Runner")).getByRole("button", { name: "新建" }),
		);
		const dialog = await screen.findByRole("dialog", { name: "新建 runner" });
		fireEvent.change(within(dialog).getByRole("textbox", { name: "名字" }), {
			target: { value: "laptop" },
		});
		fireEvent.click(within(dialog).getByRole("button", { name: "创建" }));
		await within(dialog).findByText(/已被占用/);
		expect(within(dialog).queryByTestId("compute-token")).toBeNull();
	});

	it("⋯ → 删除：确认后删掉 runner", async () => {
		mount();
		const runners = group("Runner");
		await within(runners).findByText("studio");
		fireEvent.click(
			within(runners).getByRole("button", { name: "studio 的更多操作" }),
		);
		fireEvent.click(await screen.findByRole("menuitem", { name: "删除" }));
		const req = await answerConfirm(true);
		expect(req.title).toBe("删除 runner studio？");
		await waitFor(() =>
			expect(within(runners).queryByText("studio")).toBeNull(),
		);
		expect(requests.some((r) => r.startsWith("DELETE /api/v1/runners/"))).toBe(
			true,
		);
	});
});
