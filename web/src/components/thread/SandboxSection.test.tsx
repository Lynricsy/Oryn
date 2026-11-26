/**
 * 信息面板「沙箱」分组（M30）：在用的行有倒计时与销毁按钮，已结束的置灰并给出原因；
 * 销毁先确认再调 REST，之后该行变为已结束；`useRealtimeSandboxSync` 收到本线程的 `sandboxes.changed`（或重连）才重拉；
 * 一台都没有时不渲染。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../../api/client";
import { useRealtimeSandboxSync } from "../../api/sandbox-queries";
import i18n from "../../i18n";
import { settleConfirm } from "../../lib/confirm";
import { SandboxSection } from "./SandboxSection";

type Sandbox = Schemas["Sandbox"];

const listeners = new Set<(threadId: string | null) => void>();
const fakeClient = {
	onSandboxesChanged: (cb: (threadId: string | null) => void) => {
		listeners.add(cb);
		return () => {
			listeners.delete(cb);
		};
	},
};

function Sync(): null {
	useRealtimeSandboxSync(fakeClient);
	return null;
}

const T = "T-sbx";
const T0 = Date.UTC(2026, 10, 27, 8, 0, 0);
const MIN = 60_000;

function sandbox(p: Partial<Sandbox> & Pick<Sandbox, "id">): Sandbox {
	return {
		backend: "docker",
		type: "standard",
		provider: "docker-local",
		network: "egress",
		createdNetwork: "egress",
		state: "ready",
		ownerThreadId: T,
		idleSec: 1800,
		lastActiveAt: T0 - 4 * MIN,
		idleDeadline: T0 + 26 * MIN,
		hardDeadline: T0 + 12 * 60 * MIN,
		createdAt: T0 - 6 * MIN,
		...p,
	};
}

const READY = sandbox({
	id: "sbx_01JD9K3M7Q2W8E5R4T6Y1U0P3A",
	ttlDeadline: T0 + 90 * MIN,
});
const IDLE = sandbox({
	id: "sbx_01JD9J0F4H6K8M2N5P7R9T1V3X",
	backend: "kvm",
	network: "none",
	createdNetwork: "none",
	state: "destroyed",
	createdAt: T0 - 52 * MIN,
	destroyedAt: T0 - 44 * MIN,
	destroyReason: "idle",
});

let list: Sandbox[];
let requests: Request[];

function setup(initial: Sandbox[]) {
	list = initial;
	requests = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (req: Request) => {
			requests.push(req);
			const u = new URL(req.url);
			if (u.pathname === "/api/v1/sandboxes" && req.method === "GET")
				return Response.json({ sandboxes: list });
			if (req.method === "DELETE") {
				list = list.map((s) =>
					u.pathname.endsWith(s.id)
						? {
								...s,
								state: "destroyed",
								destroyReason: "owner",
								destroyedAt: Date.now(),
							}
						: s,
				);
				return new Response(null, { status: 204 });
			}
			return new Response(null, { status: 404 });
		}),
	);
	const view = render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<Sync />
			<SandboxSection threadId={T} />
		</QueryClientProvider>,
	);
	return view;
}

const flush = () => act(() => vi.advanceTimersByTimeAsync(0));
const row = (c: HTMLElement, id: string) =>
	c.querySelector(`[data-sandbox="${id}"]`) as HTMLElement;
const gets = () =>
	requests.filter(
		(r) =>
			r.method === "GET" && new URL(r.url).pathname === "/api/v1/sandboxes",
	).length;

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
	vi.useFakeTimers();
	vi.setSystemTime(T0);
});

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
	settleConfirm(false);
	listeners.clear();
});

describe("SandboxSection", () => {
	it("在用的在前：标签、空闲倒计时与到期时刻；已结束的置灰并给原因，没有销毁按钮", async () => {
		const { container } = setup([IDLE, READY]);
		await flush();
		expect(screen.getByText("沙箱")).toBeInTheDocument();
		expect(screen.getByText("1 台在用")).toBeInTheDocument();
		const rows = [...container.querySelectorAll("[data-sandbox]")];
		expect(rows.map((r) => r.getAttribute("data-sandbox"))).toEqual([
			READY.id,
			IDLE.id,
		]);
		const ready = row(container, READY.id);
		expect(ready).toHaveTextContent(READY.id.slice(-6));
		expect(ready).toHaveTextContent("Docker");
		expect(ready).toHaveTextContent("联网");
		expect(ready).toHaveTextContent("运行中");
		expect(ready).toHaveTextContent("空闲 26:00 后回收");
		expect(ready).toHaveTextContent("到期");
		expect(
			ready.querySelector("button[aria-label^='销毁沙箱']"),
		).not.toBeNull();
		const idle = row(container, IDLE.id);
		expect(idle).toHaveClass("opacity-60");
		expect(idle).toHaveTextContent("KVM");
		expect(idle).toHaveTextContent("断网");
		expect(idle).toHaveTextContent("已销毁");
		expect(idle).toHaveTextContent("空闲回收");
		expect(idle.querySelector("button")).toBeNull();
	});

	it("倒计时每秒走动，过点显示「即将回收」", async () => {
		const { container } = setup([
			{ ...READY, idleDeadline: T0 + 2_000, ttlDeadline: undefined },
		]);
		await flush();
		const idle = () =>
			row(container, READY.id).querySelector("[data-testid='sandbox-idle']");
		expect(idle()).toHaveTextContent("空闲 0:02 后回收");
		await act(() => vi.advanceTimersByTimeAsync(1_000));
		expect(idle()).toHaveTextContent("空闲 0:01 后回收");
		await act(() => vi.advanceTimersByTimeAsync(1_500));
		expect(idle()).toHaveTextContent("即将回收");
	});

	it("销毁：确认后调 DELETE，该行变为已结束（主人销毁）", async () => {
		const { container } = setup([READY]);
		await flush();
		act(() => {
			(
				row(container, READY.id).querySelector(
					"button[aria-label^='销毁沙箱']",
				) as HTMLButtonElement
			).click();
		});
		// 取消确认不发请求
		act(() => settleConfirm(false));
		await flush();
		expect(requests.some((r) => r.method === "DELETE")).toBe(false);
		act(() => {
			(
				row(container, READY.id).querySelector(
					"button[aria-label^='销毁沙箱']",
				) as HTMLButtonElement
			).click();
		});
		act(() => settleConfirm(true));
		await flush();
		await flush();
		const del = requests.find((r) => r.method === "DELETE");
		expect(new URL(del?.url ?? "http://x").pathname).toBe(
			`/api/v1/sandboxes/${READY.id}`,
		);
		const ended = row(container, READY.id);
		expect(ended).toHaveClass("opacity-60");
		expect(ended).toHaveTextContent("主人销毁");
		expect(ended.querySelector("button")).toBeNull();
		expect(screen.getByText("共 1 台")).toBeInTheDocument();
	});

	it("只有本线程的 sandboxes.changed（或重连）才重拉", async () => {
		setup([READY]);
		await flush();
		const before = gets();
		await act(async () => {
			for (const cb of listeners) cb("T-other");
			await vi.advanceTimersByTimeAsync(0);
		});
		expect(gets()).toBe(before);
		await act(async () => {
			for (const cb of listeners) cb(T);
			await vi.advanceTimersByTimeAsync(0);
		});
		expect(gets()).toBe(before + 1);
		await act(async () => {
			for (const cb of listeners) cb(null);
			await vi.advanceTimersByTimeAsync(0);
		});
		expect(gets()).toBe(before + 2);
	});

	it("一台都没有时不渲染", async () => {
		const { container } = setup([]);
		await flush();
		expect(container).toBeEmptyDOMElement();
	});
});
