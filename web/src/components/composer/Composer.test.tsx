/**
 * 输入区回归：等待应答期间的新草稿不被清空（W4）、同一草稿不重复发送、斜杠模式命令对账（W12）、
 * 切换线程时草稿不串（W5）。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { ThreadPage } from "../../pages/ThreadPage";
import { resetThreadStores } from "../../state/stores";
import { Composer } from "./Composer";

const { cmd } = vi.hoisted(() => ({ cmd: vi.fn() }));
vi.mock("../../realtime/socket", () => ({
	realtime: {
		cmd: (...args: unknown[]) => cmd(...args),
		uiResponse: () => true,
		subscribe: () => () => undefined,
		focus: () => undefined,
	},
	OmpCommandError: class extends Error {},
}));

/** 手动控制的应答（tsconfig 的 lib 不含 Promise.withResolvers）。 */
function deferred<T>() {
	let resolve: (v: T) => void = () => undefined;
	const promise = new Promise<T>((r) => {
		resolve = r;
	});
	return { promise, resolve };
}

const A = "T-0193a1b2-0000-7000-8000-0000000000e1";
const B = "T-0193a1b2-0000-7000-8000-0000000000e2";

function wrap(children: ReactNode, path = "/") {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter initialEntries={[path]}>{children}</MemoryRouter>
		</QueryClientProvider>,
	);
}

function box(): HTMLTextAreaElement {
	return screen.getByRole("textbox", {
		name: i18n.t("composer.label"),
	}) as HTMLTextAreaElement;
}

function type(value: string) {
	fireEvent.change(box(), { target: { value } });
}

function enter() {
	fireEvent.keyDown(box(), { key: "Enter" });
}

function framesOf(t: string): Record<string, unknown>[] {
	return cmd.mock.calls
		.map((c) => c[1] as Record<string, unknown>)
		.filter((f) => f.type === t);
}

let go: (to: string) => void = () => undefined;
function Nav() {
	go = useNavigate();
	return null;
}

describe("Composer", () => {
	beforeEach(() => {
		cmd.mockReset();
		resetThreadStores();
	});
	afterEach(() => {
		vi.restoreAllMocks();
	});

	it("应答回来前编辑的新草稿保留（W4）", async () => {
		const { promise, resolve } = deferred<unknown>();
		cmd.mockImplementation(() => promise);
		wrap(<Composer threadId={A} />);
		type("first");
		enter();
		expect(framesOf("prompt")).toMatchObject([{ message: "first" }]);
		type("new draft while waiting");
		await act(async () => {
			resolve({});
			await promise;
		});
		expect(box().value).toBe("new draft while waiting");
	});

	it("草稿未改动时应答后清空；同一草稿在途时不重复发送", async () => {
		const { promise, resolve } = deferred<unknown>();
		cmd.mockImplementation(() => promise);
		wrap(<Composer threadId={A} />);
		type("once");
		enter();
		enter();
		expect(framesOf("prompt")).toHaveLength(1);
		await act(async () => {
			resolve({});
			await promise;
		});
		expect(box().value).toBe("");
	});

	it("/vibe 斜杠命令按应答更新开关，第二次关闭（W12）", async () => {
		cmd.mockImplementation(async (_tid: string, f: Record<string, unknown>) => {
			if (f.type === "set_vibe_mode") return { enabled: f.enabled };
			if (f.type === "get_state")
				return { isStreaming: false, isSettled: true };
			return {};
		});
		wrap(<Composer threadId={A} />);
		type("/vibe");
		await act(async () => enter());
		await vi.waitFor(() => expect(box().value).toBe(""));
		type("/vibe");
		await act(async () => enter());
		await vi.waitFor(() =>
			expect(framesOf("set_vibe_mode").map((f) => f.enabled)).toEqual([
				true,
				false,
			]),
		);
	});

	it("切换线程后草稿不带到另一个线程（W5）", () => {
		wrap(
			<>
				<Nav />
				<Routes>
					<Route path="/t/:threadId" element={<ThreadPage />} />
				</Routes>
			</>,
			`/t/${A}`,
		);
		type("draft for A");
		act(() => go(`/t/${B}`));
		expect(box().value).toBe("");
		act(() => go(`/t/${A}`));
		expect(box().value).toBe("");
	});
});
