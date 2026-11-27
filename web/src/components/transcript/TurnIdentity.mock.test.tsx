/**
 * 线程页集成（mock 后端，M9.6 §10）：在 roleThread 上点 Mare 发一条、再在模型列表里点 Mare 的同一模型发一条，
 * 两个回合头不同（role / agent）；清掉本机切换状态与 localStorage、销毁并重建线程 store（只靠 mock 重放的条目）后不变。
 * 旧 Core 的线程：点 role 回退 set_model，回合头仍回落。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { type ReactNode, useMemo } from "react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Entry, WsThreadSnapshot } from "../../api/client";
import { resetLegacyCore } from "../../lib/model-select";
import { M96_IDS } from "../../mock/fixtures";
import { MockServer, type MockSocket } from "../../mock/server";
import { OmpCommandError } from "../../realtime/socket";
import {
	getThreadStore,
	indexStore,
	resetThreadStores,
	type ThreadMessage,
	useThread,
} from "../../state/stores";
import type { OmpFrame } from "../../state/thread-reducer";
import { buildTranscript } from "../../state/transcript";
import { uiStore } from "../../state/ui";
import { ModelChip } from "../composer/ComposerMeta";
import { resetRoleState } from "../composer/RoleTrack";
import { TranscriptContext, type TranscriptContextValue } from "./context";
import { Transcript } from "./Transcript";

/** realtime 的替身：命令经 mock 后端的 WS 处理器应答，推送进线程 store。 */
const bridge = vi.hoisted(() => ({
	server: undefined as
		| undefined
		| { onClient(s: unknown, m: Record<string, unknown>): void },
	socket: undefined as unknown,
	pending: new Map<
		string,
		{ resolve(v: unknown): void; reject(e: unknown): void; type: string }
	>(),
	seq: 0,
	sent: [] as Record<string, unknown>[],
}));

vi.mock("../../realtime/socket", () => {
	class OmpCommandError extends Error {
		constructor(_command: string, _code: string, message: string) {
			super(message);
		}
	}
	return {
		OmpCommandError,
		realtime: {
			cmd: (threadId: string, frame: Record<string, unknown>) =>
				new Promise((resolve, reject) => {
					const id = `c${++bridge.seq}`;
					bridge.sent.push(frame);
					bridge.pending.set(id, { resolve, reject, type: String(frame.type) });
					bridge.server?.onClient(bridge.socket, {
						type: "cmd",
						threadId,
						id,
						frame,
					});
				}),
			subscribe: () => () => undefined,
			focus: () => undefined,
		},
	};
});

class Socket implements MockSocket {
	private queued?: Map<string, ThreadMessage[]>;

	// 模拟生产 WebSocket 的批量写入；这里验证回合完成后的身份，不测流式动画。
	async batch(run: () => Promise<void>) {
		const queued = new Map<string, ThreadMessage[]>();
		this.queued = queued;
		try {
			await run();
		} finally {
			this.queued = undefined;
			for (const [threadId, messages] of queued)
				getThreadStore(threadId).getState().apply(messages);
		}
	}

	private apply(threadId: string, message: ThreadMessage) {
		if (!this.queued) {
			getThreadStore(threadId).getState().apply([message]);
			return;
		}
		const messages = this.queued.get(threadId);
		if (messages) messages.push(message);
		else this.queued.set(threadId, [message]);
	}

	deliver(msg: Record<string, unknown>) {
		const threadId = String(msg.threadId ?? "");
		switch (msg.type) {
			case "res": {
				const p = bridge.pending.get(String(msg.id));
				if (!p) return;
				bridge.pending.delete(String(msg.id));
				const r = msg.response as {
					success: boolean;
					data?: unknown;
					error?: string;
				};
				if (r.success) p.resolve(r.data);
				else
					p.reject(
						new OmpCommandError(p.type, "command_failed", r.error ?? ""),
					);
				return;
			}
			case "thread.snapshot":
				this.apply(threadId, {
					type: "thread.snapshot",
					snapshot: msg as unknown as WsThreadSnapshot,
				});
				return;
			case "entries":
				this.apply(threadId, {
					type: "entries",
					msg: {
						entries: msg.entries as Entry[],
						leafEntryId: msg.leafEntryId as string,
					},
				});
				return;
			case "frame":
				this.apply(threadId, { type: "frame", frame: msg.frame as OmpFrame });
				return;
		}
	}
}

let server: MockServer;

function subscribe(threadId: string) {
	act(() => {
		server.onClient(bridge.socket as Socket, {
			type: "sub",
			threadId,
			afterSeq: 0,
		});
	});
}

function View({ threadId }: { threadId: string }): ReactNode {
	const entries = useThread(threadId, (s) => s.entries);
	const leaf = useThread(threadId, (s) => s.leafEntryId);
	const liveMessage = useThread(threadId, (s) => s.liveMessage);
	const model = useMemo(
		() =>
			buildTranscript({
				entries,
				leafEntryId: leaf,
				liveMessage,
				liveTools: {},
				liveToolOrder: [],
				notices: [],
			}),
		[entries, leaf, liveMessage],
	);
	const ctx: TranscriptContextValue = useMemo(
		() => ({
			threadId,
			toolResults: model.toolResults,
			liveTools: model.liveTools,
			subagents: {},
			openSubagent: () => undefined,
			forkFrom: () => undefined,
			rewindTo: () => undefined,
			labelEntry: () => undefined,
		}),
		[threadId, model],
	);
	return (
		<TranscriptContext.Provider value={ctx}>
			<Transcript items={model.items} />
		</TranscriptContext.Provider>
	);
}

function mount(threadId: string) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<ModelChip threadId={threadId} />
				<View threadId={threadId} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const heads = () =>
	[...document.querySelectorAll<HTMLElement>("[data-turn-identity]")].map(
		(h) => ({
			identity: h.dataset.turnIdentity,
			role: h.dataset.role,
			source: h.dataset.turnSource,
		}),
	);

beforeEach(() => {
	server = new MockServer();
	server.delay = 0;
	bridge.server = server as unknown as typeof bridge.server;
	bridge.socket = new Socket();
	bridge.pending.clear();
	bridge.sent = [];
	server.connect(bridge.socket as Socket);
	resetThreadStores();
	resetRoleState();
	resetLegacyCore();
	localStorage.clear();
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			return (
				(await server.handleFetch(req)) ?? new Response(null, { status: 404 })
			);
		},
	);
	// jsdom 没有布局：视口给足、每行一个非零高度，所有行都挂载
	vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(
		50_000,
	);
	vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(40);
	vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(800);
	indexStore.setState({ threads: {} });
});

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
	uiStore.getState().close();
});

async function send(threadId: string, text: string) {
	await act(async () => {
		await (bridge.socket as Socket).batch(() =>
			server.runPrompt(threadId, text),
		);
	});
}

describe("线程页集成：点 role 与手动选同一模型（mock 后端）", () => {
	it("两个回合头不同；清掉本机状态、重建线程 store 后不变", async () => {
		const T = M96_IDS.roleThread;
		subscribe(T);
		const view = mount(T);
		const before = heads().length;
		expect(before).toBe(7);

		// 1. 模型对话框里点 Mare（角色行），发一条
		act(() => uiStore.getState().open({ kind: "model" }));
		fireEvent.click(await screen.findByRole("button", { name: /^Mare/ }));
		await vi.waitFor(() =>
			expect(bridge.sent).toContainEqual({
				type: "oryn_select_model",
				role: "Mare",
			}),
		);
		await send(T, "点了 Mare 之后的一条");

		// 2. 模型列表里点 Mare 的同一模型，发一条
		act(() => uiStore.getState().open({ kind: "model" }));
		const row = await vi.waitFor(() => {
			const el = document.querySelector<HTMLElement>(
				'[data-model="openai/gpt-6.1-sol"]',
			);
			if (!el) throw new Error("模型列表未就绪");
			return el;
		});
		fireEvent.click(row);
		await vi.waitFor(() =>
			expect(bridge.sent).toContainEqual({
				type: "oryn_select_model",
				provider: "openai",
				modelId: "gpt-6.1-sol",
			}),
		);
		await vi.waitFor(() => expect(uiStore.getState().dialog).toBeNull());
		await send(T, "手动选同一模型之后的一条");

		const expected = [
			{ identity: "role", role: "Mare", source: "role" },
			{ identity: "agent", role: undefined, source: "manual" },
		];
		// role 头像列表是异步取的：等它到了再比
		await vi.waitFor(() => expect(heads().slice(-2)).toEqual(expected));
		expect(heads()).toHaveLength(before + 2);
		// 整个过程没有 set_model，也没写本机记忆
		expect(bridge.sent.some((f) => f.type === "set_model")).toBe(false);
		expect(localStorage.getItem(`oryn.role.${T}`)).toBeNull();

		// 3. 清掉本机状态、销毁并重建线程 store：只靠 mock 重放的条目
		view.unmount();
		resetRoleState();
		localStorage.clear();
		resetThreadStores();
		subscribe(T);
		mount(T);
		await vi.waitFor(() => expect(heads().slice(-2)).toEqual(expected));
		expect(heads()).toHaveLength(before + 2);
	});

	it("旧 Core 的线程：点 role 先收到 Unknown command 再 set_model，回合头仍回落", async () => {
		const T = M96_IDS.legacyThread;
		subscribe(T);
		mount(T);
		act(() => uiStore.getState().open({ kind: "model" }));
		fireEvent.click(await screen.findByRole("button", { name: /^Mare/ }));
		await vi.waitFor(() =>
			expect(
				bridge.sent
					.map((f) => f.type)
					.filter((t) => t === "oryn_select_model" || t === "set_model"),
			).toEqual(["oryn_select_model", "set_model"]),
		);
		await send(T, "旧 Core 上点了 Mare");
		await vi.waitFor(() => expect(heads().length).toBe(2));
		expect(heads().map((h) => h.identity)).toEqual(["agent", "agent"]);
		expect(server.entries.get(T)?.some((e) => e.type === "custom")).toBe(false);
	});
});
