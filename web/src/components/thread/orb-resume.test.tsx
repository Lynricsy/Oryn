/**
 * 回归：orb 挂起期间控制面以 `agent_unavailable` 拒绝只读查询（不唤醒、不重放）。信息面板与模式条里以
 * 「agent 可应答」为条件的 `get_*` 查询必须在 orb 回到 `ready`（WS `orb` 消息）后自行重发一次。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render } from "@testing-library/react";
import {
	afterEach,
	beforeEach,
	describe,
	expect,
	it,
	type MockInstance,
	vi,
} from "vitest";
import type { Orb } from "../../api/client";
import { makeOrb } from "../../mock/fixtures";
import { realtime } from "../../realtime/socket";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import { InfoPanel } from "./InfoPanel";
import { ModeStrip } from "./ModeBar";

const TID = "T-0193a1b2-0000-7000-8000-0000000000e1";
const ORB = "orb-01jcz8m3w4x5y6z7a8b9c0resume";

function orb(state: Orb["state"]): Orb {
	return makeOrb(ORB, [TID], state);
}

/** 走 socket 收到 `orb` 消息后的同一条路径：写进线程 store。 */
function deliverOrb(state: Orb["state"]) {
	getThreadStore(TID)
		.getState()
		.apply([{ type: "orb", orb: orb(state) }]);
}

const QUERIES = [
	"get_subagents",
	"get_context_report",
	"get_vibe_mode",
	"get_loop_mode",
] as const;

describe("orb 挂起恢复后重发只读查询", () => {
	let cmd: MockInstance<typeof realtime.cmd>;
	const count = (type: string) =>
		cmd.mock.calls.filter(([, f]) => f.type === type).length;

	beforeEach(() => {
		resetThreadStores();
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => Response.json({ code: "not_found" }, { status: 404 })),
		);
		cmd = vi.spyOn(realtime, "cmd").mockImplementation(async (_id, frame) => {
			switch (frame.type) {
				case "get_subagents":
					return { subagents: [] } as never;
				case "get_vibe_mode":
					return { enabled: false } as never;
				case "get_loop_mode":
					return { enabled: false } as never;
				default:
					return {} as never;
			}
		});
	});
	afterEach(() => {
		vi.unstubAllGlobals();
		vi.restoreAllMocks();
	});

	function mount() {
		const client = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		render(
			<QueryClientProvider client={client}>
				<ModeStrip threadId={TID} />
				<InfoPanel threadId={TID} />
			</QueryClientProvider>,
		);
	}

	it("打开线程时 orb 已冻结：不发查询；orb 回到 ready 后各发一次", async () => {
		getThreadStore(TID)
			.getState()
			.patchLocal({ agentStatus: "ready", orb: orb("frozen") });
		mount();
		await act(async () => {});
		for (const q of QUERIES) expect(count(q), q).toBe(0);

		await act(async () => deliverOrb("waking"));
		await act(async () => deliverOrb("ready"));
		for (const q of QUERIES) expect(count(q), q).toBe(1);
	});

	it("就绪 → 冻结 → 解冻：解冻后模式与子 agent 查询重发（agent 状态全程是 ready）", async () => {
		getThreadStore(TID)
			.getState()
			.patchLocal({ agentStatus: "ready", orb: orb("ready") });
		mount();
		await act(async () => {});
		expect(count("get_vibe_mode")).toBe(1);
		expect(count("get_subagents")).toBe(1);

		await act(async () => deliverOrb("freezing"));
		await act(async () => deliverOrb("frozen"));
		expect(count("get_vibe_mode")).toBe(1);

		await act(async () => deliverOrb("ready"));
		expect(count("get_vibe_mode")).toBe(2);
		expect(count("get_loop_mode")).toBe(2);
		expect(count("get_subagents")).toBe(2);
	});
});
