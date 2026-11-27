/**
 * 设置 → 控制面：三组运维参数的显示、单字段提交、恢复为配置文件的值、缩短保留期的二次确认、服务端拒绝后还原。
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
import { confirmStore } from "../../lib/confirm";
import { MockServer } from "../../mock/server";
import { answerConfirm } from "../../test/confirm";
import ServerSettings from "./ServerSettings";

let server: MockServer;
let patches: unknown[];

beforeEach(() => {
	server = new MockServer();
	patches = [];
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			if (req.method === "PATCH") patches.push(await req.clone().json());
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
			<MemoryRouter initialEntries={["/settings/server"]}>
				<ServerSettings />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const input = (name: string) =>
	screen.findByRole("spinbutton", { name }) as Promise<HTMLInputElement>;

async function commit(name: string, value: string) {
	const el = await input(name);
	fireEvent.change(el, { target: { value } });
	fireEvent.blur(el);
	return el;
}

describe("设置 → 控制面", () => {
	it("三个分组与生效值", async () => {
		mount();
		expect(await input("Agent 空闲时长")).toHaveValue(5);
		for (const name of ["空闲回收", "数据保留", "磁盘上限"])
			expect(screen.getByRole("region", { name })).toBeVisible();
		expect(await input("离开时长")).toHaveValue(20);
		expect(await input("冻结多久后停机")).toHaveValue(24);
		expect(await input("资源采样")).toHaveValue(30);
		expect(await input("快照总量上限")).toHaveValue(40);
		expect(
			screen.getByRole("switch", { name: "打开线程时自动唤醒" }),
		).toBeChecked();
	});

	it("改一个字段：失焦后只发这一个字段，单位换算成毫秒", async () => {
		mount();
		await commit("Agent 空闲时长", "10");
		await waitFor(() =>
			expect(patches).toEqual([{ idle: { agentIdleMs: 600000 } }]),
		);
		const row = (await input("Agent 空闲时长")).closest("li") as HTMLElement;
		await within(row).findByText(/配置文件里是 5 分钟/);
	});

	it("值没变或不合法不发请求", async () => {
		mount();
		await commit("Agent 空闲时长", "5");
		await commit("快照总量上限", "1.5");
		expect(await screen.findByRole("alert")).toHaveTextContent(
			"请输入不小于 0 的整数",
		);
		expect(patches).toEqual([]);
	});

	it("改过的键：显示配置文件里的值，「恢复」发 null", async () => {
		server.opsOverrides.set("idle.agentIdleMs", { value: 600000, at: 1 });
		mount();
		const el = await input("Agent 空闲时长");
		await waitFor(() => expect(el).toHaveValue(10));
		const row = el.closest("li") as HTMLElement;
		expect(row).toHaveTextContent("已在这里改过 · 配置文件里是 5 分钟");
		fireEvent.click(
			within(row).getByRole("button", {
				name: "恢复「Agent 空闲时长」为配置文件里的值",
			}),
		);
		await waitFor(() =>
			expect(patches).toEqual([{ idle: { agentIdleMs: null } }]),
		);
		await waitFor(() => expect(el).toHaveValue(5));
		expect(
			within(row).queryByRole("button", { name: /恢复/ }),
		).not.toBeInTheDocument();
	});

	it("缩短保留期先确认：取消不发请求并还原，确认才发", async () => {
		mount();
		// 回车提交后确认框抢走焦点、输入框失焦：只弹一次确认
		let asked = 0;
		const unsubscribe = confirmStore.subscribe((s, prev) => {
			if (s.pending && s.pending !== prev.pending) asked++;
		});
		const el = await input("资源采样");
		fireEvent.change(el, { target: { value: "7" } });
		fireEvent.keyDown(el, { key: "Enter" });
		fireEvent.blur(el);
		const req = await answerConfirm(false);
		expect(req.title).toBe("缩短「资源采样」的保留期？");
		expect(req.danger).toBe(true);
		await waitFor(() => expect(el).toHaveValue(30));
		expect(patches).toEqual([]);
		expect(asked).toBe(1);
		unsubscribe();

		await commit("资源采样", "7");
		await answerConfirm(true);
		await waitFor(() =>
			expect(patches).toEqual([{ statsRetention: { samplesMs: 604800000 } }]),
		);
		await waitFor(() => expect(el).toHaveValue(7));
	});

	it("服务端拒绝：错误显示在分组下，输入框还原成生效值", async () => {
		mount();
		const el = await commit("资源采样", "0.5");
		await answerConfirm(true);
		const group = screen.getByRole("region", { name: "数据保留" });
		expect(await within(group).findByRole("alert")).toHaveTextContent(
			"不短于 24h",
		);
		await waitFor(() => expect(el).toHaveValue(30));
		expect(patches).toEqual([{ statsRetention: { samplesMs: 43200000 } }]);
	});
});
