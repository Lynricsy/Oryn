/**
 * Ctrl+P 角色轮换（Composer 层面）与模型对话框里的角色列表：发出的命令与轨道显示。
 * M9.6：新 Core 发 oryn_select_model{role}（不发 set_model、不写本机记忆）；旧 Core 收到 Unknown command 后回退
 * set_model + set_thinking_level 并写本机记忆，之后不再试新命令。线程里有来源记录时当前格只认记录。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	renderHook,
	screen,
	within,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Entry } from "../../api/client";
import i18n from "../../i18n";
import { resetLegacyCore } from "../../lib/model-select";
import type { SessionState } from "../../omp/types";
import { OmpCommandError } from "../../realtime/socket";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import { Composer } from "./Composer";
import { RoleList } from "./RolePicker";
import { resetRoleState, useRoleView } from "./RoleTrack";

const { cmd } = vi.hoisted(() => ({ cmd: vi.fn() }));
vi.mock("../../realtime/socket", () => ({
	realtime: {
		cmd: (...args: unknown[]) => cmd(...args),
		uiResponse: () => true,
		subscribe: () => () => undefined,
		focus: () => undefined,
	},
	OmpCommandError: class extends Error {
		constructor(_command: string, _code: string, message: string) {
			super(message);
		}
	},
}));

const T = "T-0193a1b2-0000-7000-8000-0000000000f1";
/** 旧 Core 的线程：oryn_* 一律 Unknown command。 */
const LEGACY = "T-0193a1b2-0000-7000-8000-0000000000f2";

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

let seq = 0;
/** 模拟控制面把 Core 写下的条目推到线程 store（同一文件、依次接上 parentId）。 */
function pushEntries(
	threadId: string,
	items: { type: string; payload: Record<string, unknown> }[],
) {
	const store = getThreadStore(threadId);
	const entries: Entry[] = [];
	let parent = store.getState().leafEntryId;
	for (const it of items) {
		seq++;
		const id = `e${seq}`;
		entries.push({
			seq,
			file: "s.jsonl",
			entryId: id,
			parentId: parent,
			type: it.type,
			payload: { type: it.type, id, ...it.payload },
		});
		parent = id;
	}
	store
		.getState()
		.apply([{ type: "entries", msg: { entries, leafEntryId: parent } }]);
}

const record = (data: Record<string, unknown>) => ({
	type: "custom",
	payload: { customType: "oryn-model-source", data: { v: 1, ...data } },
});

function framesOf(t: string): Record<string, unknown>[] {
	return cmd.mock.calls
		.map((c) => c[1] as Record<string, unknown>)
		.filter((f) => f.type === t);
}

function wrap(children: ReactNode) {
	return render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<MemoryRouter>{children}</MemoryRouter>
		</QueryClientProvider>,
	);
}

beforeEach(() => {
	cmd.mockReset();
	resetThreadStores();
	resetRoleState();
	resetLegacyCore();
	localStorage.clear();
	seq = 0;
	current = { provider: "google", id: "gemini-4-carbon", level: "high" };
	cmd.mockImplementation(
		async (threadId: string, f: Record<string, unknown>) => {
			switch (f.type) {
				case "get_settings":
					return {
						values: { cycleOrder: ["Hyacinthia", "Nahida", "Mare", "Cyrene"] },
					};
				case "get_model_roles":
					return { roles: ROLES };
				case "get_state":
					return state();
				case "oryn_select_model": {
					if (threadId === LEGACY)
						throw new OmpCommandError(
							"oryn_select_model",
							"command_failed",
							"Unknown command: oryn_select_model",
						);
					const role = ROLES.find((r) => r.id === f.role);
					const sel = role && "selected" in role ? role.selected : undefined;
					if (!sel)
						throw new Error(`Role has no available model: ${String(f.role)}`);
					current = {
						provider: String(sel.provider),
						id: String(sel.id),
						level: String(sel.thinkingLevel),
					};
					const model = `${sel.provider}/${sel.id}`;
					pushEntries(threadId, [
						{ type: "model_change", payload: { model, role: f.role } },
						record({ source: "role", role: f.role, model }),
					]);
					return { model: sel, source: "role", role: f.role };
				}
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
		},
	);
	for (const id of [T, LEGACY])
		getThreadStore(id)
			.getState()
			.apply([{ type: "state", state: state() }]);
});

describe("Ctrl+P 角色轮换", () => {
	it("按 Ctrl+P 发出下一个角色的 oryn_select_model{role}，没有 set_model、不写本机记忆；轨道选中该角色", async () => {
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
			expect(framesOf("oryn_select_model")).toEqual([
				{ type: "oryn_select_model", role: "Mare" },
			]),
		);
		const track = await screen.findByRole("radiogroup", {
			name: i18n.t("roles.track"),
		});
		await vi.waitFor(() =>
			expect(screen.getByText(/Mare.*GPT-6\.1 Sol/)).toBeTruthy(),
		);
		expect(
			within(track)
				.getByRole("radio", { name: "Mare" })
				.getAttribute("aria-checked"),
		).toBe("true");
		expect(framesOf("set_model")).toEqual([]);
		expect(framesOf("set_thinking_level")).toEqual([]);
		expect(localStorage.getItem(`oryn.role.${T}`)).toBeNull();

		// Shift+Ctrl+P 反向：回到 Nahida（轨道只认记录：当前格 = 记录里的 Mare）
		await act(async () => {
			fireEvent.keyDown(window, { key: "P", ctrlKey: true, shiftKey: true });
		});
		await vi.waitFor(() =>
			expect(framesOf("oryn_select_model").at(-1)).toEqual({
				type: "oryn_select_model",
				role: "Nahida",
			}),
		);
	});

	it("刷新状态期间再按 Ctrl+P，新目标不会丢：放行后接着切过去，轨道停在新角色", async () => {
		// 第一次切换之后的 get_state 挂起，直到手动放行
		// tsconfig lib 为 ES2023，没有 Promise.withResolvers 的类型
		let release: () => void = () => undefined;
		const gate = new Promise<void>((r) => {
			release = r;
		});
		let held = false;
		const base = cmd.getMockImplementation();
		cmd.mockImplementation(async (t: string, f: Record<string, unknown>) => {
			if (
				f.type === "get_state" &&
				framesOf("oryn_select_model").length === 1
			) {
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
			expect(framesOf("oryn_select_model").at(-1)).toEqual({
				type: "oryn_select_model",
				role: "Cyrene",
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
		expect(current).toEqual({
			provider: "anthropic",
			id: "claude-opus-5-5",
			level: "high",
		});
	});
});

describe("模型对话框里的角色列表", () => {
	it("按 cycleOrder 列出角色，其余 chat 角色在后，系统角色收进「更多」；点 Mare 发 oryn_select_model 并回调", async () => {
		const onPicked = vi.fn();
		wrap(<RoleList threadId={T} onPicked={onPicked} />);
		const nahida = await screen.findByRole("button", { name: /^Nahida/ });
		expect(nahida.getAttribute("aria-current")).toBe("true");
		expect(nahida.getAttribute("data-role")).toBe("Nahida");
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
			expect(framesOf("oryn_select_model")).toEqual([
				{ type: "oryn_select_model", role: "Mare" },
			]),
		);
		expect(framesOf("set_model")).toEqual([]);
	});

	it("旧 Core：第一次点击先收到 Unknown command 再发 set_model + set_thinking_level，写本机记忆、没有错误文字；第二次不再试新命令", async () => {
		wrap(
			<div className="glass">
				<Composer threadId={LEGACY} />
				<RoleList threadId={LEGACY} onPicked={() => undefined} />
			</div>,
		);
		fireEvent.click(await screen.findByRole("button", { name: /^Mare/ }));
		await vi.waitFor(() =>
			expect(framesOf("set_thinking_level")).toEqual([
				{ type: "set_thinking_level", level: "high" },
			]),
		);
		expect(
			cmd.mock.calls
				.map((c) => (c[1] as { type: string }).type)
				.filter((t) => /model|thinking/.test(t) && !t.startsWith("get_")),
		).toEqual(["oryn_select_model", "set_model", "set_thinking_level"]);
		await vi.waitFor(() =>
			expect(
				JSON.parse(localStorage.getItem(`oryn.role.${LEGACY}`) ?? "null"),
			).toMatchObject({
				role: "Mare",
				provider: "openai",
				id: "gpt-6.1-sol",
			}),
		);
		expect(document.querySelector(".text-err")).toBeNull();

		fireEvent.click(screen.getByRole("button", { name: /^Cyrene/ }));
		await vi.waitFor(() =>
			expect(framesOf("set_model").at(-1)).toEqual({
				type: "set_model",
				provider: "anthropic",
				modelId: "claude-opus-5-5",
			}),
		);
		expect(framesOf("oryn_select_model")).toHaveLength(1);
	});
});

describe("有来源记录的线程：当前格只认记录", () => {
	function view(threadId = T) {
		return renderHook(() => useRoleView(threadId, true, true), {
			wrapper: ({ children }) => (
				<QueryClientProvider
					client={
						new QueryClient({ defaultOptions: { queries: { retry: false } } })
					}
				>
					{children}
				</QueryClientProvider>
			),
		});
	}

	it("manual 记录（与 Mare 同模型）→ 轨道与列表都不高亮任何格；本机记忆不读", async () => {
		current = { provider: "openai", id: "gpt-6.1-sol", level: "high" };
		getThreadStore(T)
			.getState()
			.apply([{ type: "state", state: state() }]);
		// 本机记忆说是 Mare：有记录的线程里它不起作用
		localStorage.setItem(
			`oryn.role.${T}`,
			JSON.stringify({
				role: "Mare",
				provider: "openai",
				id: "gpt-6.1-sol",
				thinkingLevel: "high",
			}),
		);
		pushEntries(T, [
			{
				type: "model_change",
				payload: { model: "openai/gpt-6.1-sol", role: "default" },
			},
			record({ source: "manual", model: "openai/gpt-6.1-sol" }),
		]);
		const { result } = view();
		await vi.waitFor(() => expect(result.current.cycle.length).toBe(4));
		expect(result.current.trackId).toBeUndefined();
		expect(result.current.currentId).toBeUndefined();
	});

	it("没有记录的线程：同一状态下照旧按本机记忆 / 模型认出 Mare（对照）", async () => {
		current = { provider: "openai", id: "gpt-6.1-sol", level: "high" };
		getThreadStore(T)
			.getState()
			.apply([{ type: "state", state: state() }]);
		const { result } = view();
		await vi.waitFor(() => expect(result.current.cycle.length).toBe(4));
		expect(result.current.trackId).toBe("Mare");
	});

	it("startup + alias: Cyrene → 轨道里 Cyrene 那格选中", async () => {
		current = { provider: "anthropic", id: "claude-opus-5-5", level: "high" };
		getThreadStore(T)
			.getState()
			.apply([{ type: "state", state: state() }]);
		pushEntries(T, [
			{ type: "model_change", payload: { model: "anthropic/claude-opus-5-5" } },
			record({
				source: "startup",
				role: "default",
				alias: "Cyrene",
				model: "anthropic/claude-opus-5-5",
			}),
		]);
		const { result } = view();
		await vi.waitFor(() => expect(result.current.cycle.length).toBe(4));
		expect(result.current.trackId).toBe("Cyrene");
		// 列表里 default 自己在列：当前角色是记录里被选中的 default
		expect(result.current.currentId).toBe("default");
	});
});
