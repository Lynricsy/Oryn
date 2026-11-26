/**
 * 快捷键清单与实际处理一致：应用级组合键渲染真实的 AppShell 派发键盘事件（被处理 = preventDefault），
 * 角色循环挂 RoleTrack 的 useRoleShortcut，输入框按键逐条交给 composerKeyAction（两种发送键各一遍）。
 * mac 用 ⌘（metaKey）、其它平台用 Ctrl（ctrlKey），两者都要认。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { qk } from "../api/queries";
import { useRoleShortcut } from "../components/composer/RoleTrack";
import { AppShell } from "../components/layout/AppShell";
import { uiStore } from "../state/ui";
import { composerKeyAction, type SendKey } from "./composer";
import {
	APP_SHORTCUTS,
	composerShortcuts,
	formatCombo,
	type KeyCombo,
} from "./shortcuts";

vi.mock("../realtime/socket", () => ({
	realtime: {
		connect: vi.fn(),
		close: vi.fn(),
		send: vi.fn(),
		cmd: vi.fn(async () => ({})),
		onSchedulesChanged: vi.fn(() => () => undefined),
		onGitHubChanged: vi.fn(() => () => undefined),
		onSshChanged: vi.fn(() => () => undefined),
		onProjectRepoChanged: vi.fn(() => () => undefined),
		onBoxesChanged: vi.fn(() => () => undefined),
		onSandboxesChanged: vi.fn(() => () => undefined),
		onArtifactsChanged: vi.fn(() => () => undefined),
		onArtifactConsent: vi.fn(() => () => undefined),
		onArtifactWatchesChanged: vi.fn(() => () => undefined),
	},
}));

beforeEach(() => {
	window.innerWidth = 1280;
	localStorage.clear();
	uiStore.setState({ sidebarCollapsed: false, sidebarOpen: false });
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL) => {
			const url = new URL(
				input instanceof Request ? input.url : String(input),
				"http://x",
			);
			if (url.pathname === "/api/v1/projects")
				return Response.json({ projects: [] });
			if (url.pathname === "/api/v1/threads")
				return Response.json({ threads: [] });
			return Response.json({});
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	window.innerWidth = 1024;
});

function client() {
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false, staleTime: Infinity } },
	});
	qc.setQueryData(qk.me, {
		displayName: "Ada",
		createdAt: 1,
		auth: { kind: "web" },
	});
	return qc;
}

const MODS = [
	["Ctrl", { ctrlKey: true }],
	["⌘", { metaKey: true }],
] as const;

/** 在 window 上按下组合键；返回是否被处理（preventDefault）。 */
function press(c: KeyCombo, mod: { ctrlKey?: true; metaKey?: true }): boolean {
	return !fireEvent.keyDown(window, {
		key: c.shift ? c.key.toUpperCase() : c.key,
		code: c.code,
		shiftKey: Boolean(c.shift),
		altKey: Boolean(c.alt),
		...(c.mod ? mod : {}),
	});
}

describe("应用级快捷键（AppShell）", () => {
	const app = APP_SHORTCUTS.filter((s) => s.scope === "app");

	it.each(
		app.flatMap((s) =>
			s.combos.flatMap((c) =>
				MODS.map(([name, mod]) => ({ id: s.id, c, name, mod })),
			),
		),
	)("$id：$name 版 $c.code 被处理", ({ c, mod }) => {
		render(
			<QueryClientProvider client={client()}>
				<MemoryRouter initialEntries={["/"]}>
					<Routes>
						<Route element={<AppShell />}>
							<Route path="*" element={null} />
						</Route>
					</Routes>
				</MemoryRouter>
			</QueryClientProvider>,
		);
		expect(press(c, mod)).toBe(true);
	});

	it("清单之外的组合键不被 AppShell 处理（对照）", () => {
		render(
			<QueryClientProvider client={client()}>
				<MemoryRouter>
					<Routes>
						<Route element={<AppShell />}>
							<Route path="*" element={null} />
						</Route>
					</Routes>
				</MemoryRouter>
			</QueryClientProvider>,
		);
		expect(
			press({ key: "j", code: "KeyJ", mod: true }, { ctrlKey: true }),
		).toBe(false);
		expect(press({ key: "k", code: "KeyK" }, {})).toBe(false);
	});
});

describe("线程页快捷键（RoleTrack.useRoleShortcut）", () => {
	const thread = APP_SHORTCUTS.filter((s) => s.scope === "thread");

	it.each(
		thread.flatMap((s) =>
			s.combos.flatMap((c) =>
				MODS.map(([name, mod]) => ({ id: s.id, c, name, mod })),
			),
		),
	)("$id：$name 版被处理", ({ c, mod }) => {
		const qc = client();
		renderHook(() => useRoleShortcut("T-1"), {
			wrapper: ({ children }: { children: ReactNode }) => (
				<QueryClientProvider client={qc}>{children}</QueryClientProvider>
			),
		});
		expect(press(c, mod)).toBe(true);
	});
});

describe("输入框按键（composerKeyAction）", () => {
	it.each(
		(["enter", "mod"] as const satisfies readonly SendKey[]).flatMap((send) =>
			composerShortcuts(send).flatMap((s) =>
				s.combos.flatMap((c) =>
					MODS.map(([name, mod]) => ({ send, s, c, name, mod })),
				),
			),
		),
	)("发送键 $send：$s.id（$name 版）→ $s.action", ({ send, s, c, mod }) => {
		const e = {
			key: c.key,
			shiftKey: Boolean(c.shift),
			altKey: Boolean(c.alt),
			...(c.mod ? mod : {}),
		};
		const empty = s.empty ?? false;
		let escAt: number | null = null;
		if (s.twice)
			escAt = composerKeyAction(e, s.busy, null, 1000, empty, send).escAt;
		expect(composerKeyAction(e, s.busy, escAt, 1100, empty, send).action).toBe(
			s.action,
		);
	});

	it("一览跟随发送键：⌘Enter 模式下 Enter 是换行、⌘Enter 发送", () => {
		const of = (send: SendKey, id: string) =>
			composerShortcuts(send)
				.find((s) => s.id === id)
				?.combos.map((c) => formatCombo(c, true));
		expect(of("enter", "send")).toEqual(["Enter"]);
		expect(of("enter", "newline")).toEqual(["⇧Enter"]);
		expect(of("mod", "send")).toEqual(["⌘Enter"]);
		expect(of("mod", "newline")).toEqual(["Enter"]);
		expect(of("mod", "followUp")).toEqual(["⇧⌘Enter"]);
	});
});

describe("formatCombo", () => {
	it("mac 用 ⌥⇧⌘ 连写，其它平台 Ctrl+Shift+Alt", () => {
		const c: KeyCombo = { key: "p", code: "KeyP", mod: true, shift: true };
		expect(formatCombo(c, true)).toBe("⇧⌘P");
		expect(formatCombo(c, false)).toBe("Ctrl+Shift+P");
		const e: KeyCombo = { key: "Enter", code: "Enter", mod: true, alt: true };
		expect(formatCombo(e, true)).toBe("⌥⌘Enter");
		expect(formatCombo(e, false)).toBe("Ctrl+Alt+Enter");
		expect(formatCombo({ key: "Escape", code: "Escape" }, false)).toBe("Esc");
	});
});
