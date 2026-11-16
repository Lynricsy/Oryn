/**
 * mock 后端的在场（M14 6.7，供 `dev:mock` 与截图）：有连接在看某个 artifact 时推 `artifact.activity`。
 * - `viewers` 是观看连接数（同 orynd：开两个窗口就是「在 2 处打开」）；`visitors` 是发布页的访客数（取自发布的
 *   mock，同 orynd——查看器把它写进发布状态的缓存，这里给 0 会盖掉发布夹具的访客数）。
 * - 夹具主线程演一个 agent：每 4 秒换一个目标并依次「改 → 画 → 挪 → 读」；画布的目标是当前文档里的各个画框，
 *   其余类型是入口文件。最后一个观看者离开时停下。
 * 形状与 orynd 的 `WsServerArtifactActivity` 一致；不模拟回合结束（真后端在回合结束时清掉线程的活动）。
 */
import type { Schemas } from "../api/client";
import { MAIN_THREAD } from "./fixtures";
import type { MockSocket } from "./server";

const STEP_MS = 4000;
const DOINGS = ["write", "draw", "move", "read"] as const;

type Focus = NonNullable<Schemas["ArtifactThreadActivity"]["focus"]>;

export interface MockActivityDeps {
	/** 正在看 id 的连接。 */
	watchers(id: string): ReadonlySet<MockSocket>;
	/** agent 轮流「在场」的目标：画布是各画框，其余是入口文件；artifact 不在了时为空。 */
	targets(id: string): Focus[];
	/** 发布页当前的访客数（未发布为 0）。 */
	visitors(id: string): number;
}

export class MockArtifactActivity {
	private readonly deps: MockActivityDeps;
	private readonly running = new Map<
		string,
		{ step: number; timer: ReturnType<typeof setInterval> }
	>();

	constructor(deps: MockActivityDeps) {
		this.deps = deps;
	}

	/** 观看者变了（watch / unwatch / 断开）：重发一次，并按有没有人在看启停演示。 */
	changed(id: string) {
		const viewers = this.deps.watchers(id).size;
		const cur = this.running.get(id);
		if (viewers === 0) {
			if (cur) clearInterval(cur.timer);
			this.running.delete(id);
			return;
		}
		if (!cur) {
			const state = {
				step: 0,
				timer: setInterval(() => {
					state.step += 1;
					this.push(id, state.step);
				}, STEP_MS),
			};
			this.running.set(id, state);
		}
		this.push(id, this.running.get(id)?.step ?? 0);
	}

	private push(id: string, step: number) {
		const watchers = this.deps.watchers(id);
		const targets = this.deps.targets(id);
		const focus = targets[step % Math.max(1, targets.length)];
		const msg: Schemas["WsServerArtifactActivity"] = {
			type: "artifact.activity",
			id,
			viewers: watchers.size,
			threads: focus
				? [
						{
							threadId: MAIN_THREAD,
							doing: DOINGS[step % DOINGS.length] ?? "read",
							focus,
						},
					]
				: [],
			clients: [],
			visitors: this.deps.visitors(id),
		};
		for (const s of watchers) s.deliver(msg);
	}
}
