/**
 * 手机标题菜单：子菜单只存 key、每次从最新菜单项取；切线程（页头被复用）时菜单收起，
 * 之后的 orb 操作只能落到当前线程，不执行上一个线程缓存的回调。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Orb, Thread } from "../../api/client";
import { makeOrb } from "../../mock/fixtures";
import type * as Actions from "../../state/actions";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import { CompactThreadHeader } from "./CompactThreadHeader";

const { pauseOrb } = vi.hoisted(() => ({
	pauseOrb: vi.fn(async (_orbId: string, _mode: string) => undefined),
}));
vi.mock("../../state/actions", async (orig) => ({
	...(await orig<typeof Actions>()),
	pauseOrb: (orbId: string, mode: string) => pauseOrb(orbId, mode),
}));

const A = "T-0193a1b2-0000-7000-8000-0000000000a1";
const B = "T-0193a1b2-0000-7000-8000-0000000000b2";
const ORB_A = "orb-01jcz8m3w4x5y6z7a8b9c0aaaa";
const ORB_B = "orb-01jcz8m3w4x5y6z7a8b9c0bbbb";

function seed(threadId: string, orbId: string, state: Orb["state"] = "ready") {
	const thread = {
		id: threadId,
		title: threadId === A ? "线程 A" : "线程 B",
		titleSource: "auto",
		state: "idle",
		stateChangedAt: 0,
		pinned: false,
		labels: [],
		unread: false,
		createdAt: 0,
		updatedAt: 0,
		orbId,
	} as unknown as Thread;
	getThreadStore(threadId)
		.getState()
		.apply([
			{ type: "thread", thread },
			{ type: "orb", orb: makeOrb(orbId, [threadId], state) },
		]);
}

function mount() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	const tree = (tid: string) => (
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<CompactThreadHeader threadId={tid} veil={56} />
			</MemoryRouter>
		</QueryClientProvider>
	);
	const r = render(tree(A));
	return (tid: string) => r.rerender(tree(tid));
}

const capsule = () => screen.getByTestId("thread-title");
const item = (key: string) =>
	document.querySelector<HTMLButtonElement>(
		`[data-testid="title-menu"] [data-menu-key="${key}"]`,
	);

function openEnvSubmenu() {
	fireEvent.click(capsule());
	const env = item("env");
	if (!env) throw new Error("没有运行环境行");
	fireEvent.click(env);
}

describe("CompactThreadHeader 标题菜单", () => {
	beforeEach(() => {
		resetThreadStores();
		pauseOrb.mockClear();
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => Response.json({ code: "not_found" }, { status: 404 })),
		);
		seed(A, ORB_A);
		seed(B, ORB_B);
	});
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("A 的运行环境子菜单开着时切到 B：菜单收起；再打开后冻结的是 B 的 orb", () => {
		const show = mount();
		openEnvSubmenu();
		expect(item("freeze")).not.toBeNull();
		show(B);
		expect(capsule()).toHaveAttribute("aria-expanded", "false");
		// 退出动画期间也不能再出现 A 的子菜单
		expect(item("freeze")).toBeNull();
		openEnvSubmenu();
		const freeze = item("freeze");
		if (!freeze) throw new Error("没有冻结项");
		fireEvent.click(freeze);
		expect(pauseOrb.mock.calls).toEqual([[ORB_B, "freeze"]]);
	});

	it("子菜单开着时线程状态变化：子菜单项跟着最新状态（orb 冻结后「冻结」不可点）", () => {
		mount();
		openEnvSubmenu();
		expect(item("freeze")).toBeEnabled();
		act(() => {
			getThreadStore(A)
				.getState()
				.apply([{ type: "orb", orb: makeOrb(ORB_A, [A], "frozen") }]);
		});
		expect(item("freeze")).toBeDisabled();
	});
});
