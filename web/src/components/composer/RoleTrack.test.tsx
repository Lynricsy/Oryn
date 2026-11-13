/**
 * Ctrl+P 角色轮换（Composer 层面）与模型对话框里的角色列表：发出的命令与轨道显示。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import type { SessionState } from "../../omp/types";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import { Composer } from "./Composer";
import { RoleList } from "./RolePicker";
import { resetRoleState } from "./RoleTrack";

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

const T = "T-0193a1b2-0000-7000-8000-0000000000f1";

const CANDIDATES = [
	{ provider: "anthropic", id: "claude-fable-5-2", name: "Claude Fable 5.2" },
	{ provider: "anthropic", id: "claude-opus-5-5", name: "Claude Opus 5.5" },
	{ provider: "google", id: "gemini-4-carbon", name: "Gemini 4 Carbon" },
	{ provider: "openai", id: "gpt-6.1-sol", name: "GPT-6.1 Sol" },
];

/** 主人真实配置的子集（get_model_roles 回读形状）。 */
const ROLES = [
	{ id: "default", name: "Default", section: "chat", model: "@Cyrene" },
	["Hyacinthia", "anthropic", "claude-fable-5-2", "high"],
	["Nahida", "google", "gemini-4-carbon", "high"],
	["Mare", "openai", "gpt-6.1-sol", "high"],
	["Cyrene", "anthropic", "claude-opus-5-5", "high"],
	["commit", "openai", "gpt-6.1-sol", "medium"],
].map((r) =>
	Array.isArray(r)
		? {
				id: r[0],
				name: r[0] === "commit" ? "Commit" : r[0],
				section: "chat",
				model: `${r[1]}/${r[2]}:${r[3]}`,
				selected: { provider: r[1], id: r[2], thinkingLevel: r[3] },
				candidates: CANDIDATES,
			}
		: { ...r, candidates: CANDIDATES },
);

let current = { provider: "google", id: "gemini-4-carbon", level: "high" };

function state(): SessionState {
	const m = CANDIDATES.find(
		(c) => c.provider === current.provider && c.id === current.id,
	);
	return {
		model: m,
		thinkingLevel: current.level,
		thinkingConfigured: current.level,
		isStreaming: false,
		isSettled: true,
	} as unknown as SessionState;
}

function framesOf(t: string): Record<string, unknown>[] {
	return cmd.mock.calls
		.map((c) => c[1] as Record<string, unknown>)
		.filter((f) => f.type === t);
}

function wrap(children: ReactNode) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter>{children}</MemoryRouter>
		</QueryClientProvider>,
	);
}

beforeEach(() => {
	cmd.mockReset();
	resetThreadStores();
	resetRoleState();
	localStorage.clear();
	current = { provider: "google", id: "gemini-4-carbon", level: "high" };
	cmd.mockImplementation(async (_t: string, f: Record<string, unknown>) => {
		switch (f.type) {
			case "get_settings":
				return {
					values: { cycleOrder: ["Hyacinthia", "Nahida", "Mare", "Cyrene"] },
				};
			case "get_model_roles":
				return { roles: ROLES };
			case "get_state":
				return state();
			case "set_model":
				current = {
					...current,
					provider: String(f.provider),
					id: String(f.modelId),
				};
				return {};
			case "set_thinking_level":
				current = { ...current, level: String(f.level) };
				return {};
			default:
				return {};
		}
	});
	getThreadStore(T)
		.getState()
		.apply([{ type: "state", state: state() }]);
});

describe("Ctrl+P 角色轮换", () => {
	it("按 Ctrl+P 发出下一个角色的 set_model（带级别），轨道显示并选中该角色", async () => {
		wrap(
			<div className="glass">
				<Composer threadId={T} />
			</div>,
		);
		const ev = new KeyboardEvent("keydown", {
			key: "p",
			ctrlKey: true,
			bubbles: true,
			cancelable: true,
		});
		await act(async () => {
			window.dispatchEvent(ev);
		});
		expect(ev.defaultPrevented).toBe(true);
		// 当前是 Nahida（gemini-4-carbon:high），下一个是 Mare
		await vi.waitFor(() =>
			expect(framesOf("set_model")).toEqual([
				{ type: "set_model", provider: "openai", modelId: "gpt-6.1-sol" },
			]),
		);
		await vi.waitFor(() =>
			expect(framesOf("set_thinking_level")).toEqual([
				{ type: "set_thinking_level", level: "high" },
			]),
		);
		const track = await screen.findByRole("radiogroup", {
			name: i18n.t("roles.track"),
		});
		expect(
			within(track)
				.getByRole("radio", { name: "Mare" })
				.getAttribute("aria-checked"),
		).toBe("true");
		await vi.waitFor(() =>
			expect(screen.getByText(/Mare.*GPT-6\.1 Sol/)).toBeTruthy(),
		);

		// Shift+Ctrl+P 反向：回到 Nahida
		await act(async () => {
			fireEvent.keyDown(window, { key: "P", ctrlKey: true, shiftKey: true });
		});
		await vi.waitFor(() =>
			expect(framesOf("set_model").at(-1)).toEqual({
				type: "set_model",
				provider: "google",
				modelId: "gemini-4-carbon",
			}),
		);
	});

	it("刷新状态期间再按 Ctrl+P，新目标不会丢：放行后接着切过去，轨道停在新角色", async () => {
		// 第一次 set_model 之后的 get_state 挂起，直到手动放行
		// tsconfig lib 为 ES2023，没有 Promise.withResolvers 的类型
		let release: () => void = () => undefined;
		const gate = new Promise<void>((r) => {
			release = r;
		});
		let held = false;
		const base = cmd.getMockImplementation();
		cmd.mockImplementation(async (t: string, f: Record<string, unknown>) => {
			if (f.type === "get_state" && framesOf("set_model").length === 1) {
				held = true;
				await gate;
			}
			return base?.(t, f);
		});
		wrap(
			<div className="glass">
				<Composer threadId={T} />
			</div>,
		);
		// Nahida → Mare，刷新挂起
		await act(async () => {
			fireEvent.keyDown(window, { key: "p", ctrlKey: true });
		});
		await vi.waitFor(() => expect(held).toBe(true));
		// 刷新期间再按：在途目标 Mare → Cyrene
		await act(async () => {
			fireEvent.keyDown(window, { key: "p", ctrlKey: true });
		});
		await act(async () => {
			release();
		});
		await vi.waitFor(() =>
			expect(framesOf("set_model").at(-1)).toEqual({
				type: "set_model",
				provider: "anthropic",
				modelId: "claude-opus-5-5",
			}),
		);
		const track = await screen.findByRole("radiogroup", {
			name: i18n.t("roles.track"),
		});
		await vi.waitFor(() =>
			expect(
				within(track)
					.getByRole("radio", { name: "Cyrene" })
					.getAttribute("aria-checked"),
			).toBe("true"),
		);
		// 最后一次刷新拿到的是 Cyrene 的状态
		expect(current).toEqual({
			provider: "anthropic",
			id: "claude-opus-5-5",
			level: "high",
		});
	});
});

describe("模型对话框里的角色列表", () => {
	it("按 cycleOrder 列出角色，其余 chat 角色在后，系统角色收进「更多」；点 Mare 切换并回调", async () => {
		const onPicked = vi.fn();
		wrap(<RoleList threadId={T} onPicked={onPicked} />);
		const nahida = await screen.findByRole("button", { name: /^Nahida/ });
		expect(nahida.getAttribute("aria-current")).toBe("true");
		const rows = screen.getAllByRole("button").map((b) => b.textContent ?? "");
		const prefixes = [
			"Hyacinthia",
			"Nahida",
			"Mare",
			"Cyrene",
			i18n.t("roles.names.default"),
		];
		prefixes.forEach((p, i) => {
			expect(rows[i]?.startsWith(p)).toBe(true);
		});
		// commit 是后台角色：收在折叠的「更多」里
		expect(rows.some((r) => r.startsWith(i18n.t("roles.names.commit")))).toBe(
			true,
		);
		expect(screen.getByText(i18n.t("model.moreRoles", { n: 1 }))).toBeTruthy();

		fireEvent.click(screen.getByRole("button", { name: /^Mare/ }));
		expect(onPicked).toHaveBeenCalledOnce();
		await vi.waitFor(() =>
			expect(framesOf("set_model")).toEqual([
				{ type: "set_model", provider: "openai", modelId: "gpt-6.1-sol" },
			]),
		);
		await vi.waitFor(() =>
			expect(framesOf("set_thinking_level")).toEqual([
				{ type: "set_thinking_level", level: "high" },
			]),
		);
	});
});
