import { describe, expect, it } from "vitest";
import {
	parseSlash,
	resolveSlash,
	type SlashContext,
	UNAVAILABLE_COMMANDS,
} from "./slash";

const ctx: SlashContext = {
	planModeEnabled: false,
	vibeEnabled: true,
	loopEnabled: false,
	agentsPaused: false,
	fastModeEnabled: false,
	slowModeEnabled: false,
	compactModes: ["snap", "remote"],
};

function resolve(text: string, c: SlashContext = ctx) {
	const parsed = parseSlash(text);
	if (!parsed) throw new Error(`not a slash command: ${text}`);
	return resolveSlash(parsed, c);
}

describe("斜杠命令拦截映射", () => {
	it("/plan 切换计划模式（不发给模型）", () => {
		expect(resolve("/plan")).toEqual({
			kind: "rpc",
			frame: { type: "set_plan_mode", enabled: true },
		});
		expect(resolve("/plan", { ...ctx, planModeEnabled: true })).toEqual({
			kind: "rpc",
			frame: { type: "set_plan_mode", enabled: false },
		});
	});

	it.each(["/tree", "/branch", "/rewind"])("%s 打开会话树", (cmd) => {
		expect(resolve(cmd)).toEqual({ kind: "open_tree" });
	});

	it("会话类命令映射到客户端动作", () => {
		expect(resolve("/fork")).toEqual({ kind: "open_fork" });
		expect(resolve("/new")).toEqual({ kind: "new_thread" });
		expect(resolve("/resume")).toEqual({ kind: "thread_search" });
		expect(resolve("/clear")).toEqual({
			kind: "rpc",
			frame: { type: "clear_context" },
		});
		expect(resolve("/queue 之后再看测试")).toEqual({
			kind: "rpc",
			frame: { type: "follow_up", message: "之后再看测试" },
		});
		expect(resolve("/queue")).toEqual({ kind: "needs_args", name: "queue" });
	});

	it("/thinking 带级别直接设置，不带时要求参数（模型对话框里已没有思考级别）", () => {
		expect(resolve("/thinking High")).toEqual({
			kind: "rpc",
			frame: { type: "set_thinking_level", level: "high" },
		});
		expect(resolve("/thinking")).toEqual({
			kind: "needs_args",
			name: "thinking",
		});
	});

	it("/fast ultra 选极速档，其余参数照旧开关快速档", () => {
		expect(resolve("/fast ultra")).toEqual({
			kind: "rpc",
			frame: { type: "set_ultrafast_mode", enabled: true },
		});
		expect(resolve("/fast off", { ...ctx, fastModeEnabled: true })).toEqual({
			kind: "rpc",
			frame: { type: "set_fast_mode", enabled: false },
		});
	});

	it("btw / tan / omfg / vibe / loop / goal / guided-goal / pause 走对应 RPC", () => {
		expect(resolve("/btw 这是什么")).toEqual({
			kind: "rpc",
			frame: { type: "btw", question: "这是什么" },
		});
		expect(resolve("/btw")).toEqual({ kind: "open_btw" });
		expect(resolve("/tan 顺便修 lint")).toEqual({
			kind: "rpc",
			frame: { type: "tan", work: "顺便修 lint" },
		});
		expect(resolve("/omfg 别再改测试了")).toEqual({
			kind: "rpc",
			frame: { type: "omfg", complaint: "别再改测试了" },
		});
		expect(resolve("/vibe")).toEqual({
			kind: "rpc",
			frame: { type: "set_vibe_mode", enabled: false },
		});
		expect(resolve("/vibe on")).toEqual({
			kind: "rpc",
			frame: { type: "set_vibe_mode", enabled: true },
		});
		expect(resolve("/loop 每小时检查 CI")).toEqual({
			kind: "rpc",
			frame: { type: "set_loop_mode", enabled: true, args: "每小时检查 CI" },
		});
		expect(resolve("/loop off")).toEqual({
			kind: "rpc",
			frame: { type: "set_loop_mode", enabled: false },
		});
		expect(resolve("/goal")).toEqual({ kind: "open_goal" });
		expect(resolve("/goal 发布 M2")).toEqual({
			kind: "rpc",
			frame: { type: "set_goal", objective: "发布 M2" },
		});
		expect(resolve("/goal pause")).toEqual({
			kind: "rpc",
			frame: { type: "set_goal", action: "pause" },
		});
		expect(resolve("/guided-goal")).toEqual({
			kind: "rpc",
			frame: { type: "guided_goal" },
		});
		expect(resolve("/pause")).toEqual({
			kind: "rpc",
			frame: { type: "set_agents_paused", enabled: true },
		});
	});

	it.each([...UNAVAILABLE_COMMANDS])("/%s 提示「Oryn 中不可用」", (name) => {
		expect(resolve(`/${name}`)).toEqual({ kind: "unavailable", name });
	});

	it("命令菜单里的会话操作", () => {
		expect(resolve("/handoff 交给下一位")).toEqual({
			kind: "rpc",
			frame: { type: "handoff", customInstructions: "交给下一位" },
		});
		expect(resolve("/compact snap 只保留结论")).toEqual({
			kind: "prompt",
			text: "/compact snap 只保留结论",
		});
		expect(resolve("/compact 只保留结论")).toEqual({
			kind: "rpc",
			frame: { type: "compact", customInstructions: "只保留结论" },
		});
		expect(resolve("/shake images")).toEqual({
			kind: "rpc",
			frame: { type: "shake_context", mode: "images" },
		});
		expect(resolve("/fresh")).toEqual({
			kind: "rpc",
			frame: { type: "fresh" },
		});
		expect(resolve("/retry")).toEqual({
			kind: "rpc",
			frame: { type: "retry" },
		});
		expect(resolve("/context")).toEqual({
			kind: "report",
			report: "get_context_report",
		});
		expect(resolve("/usage")).toEqual({ kind: "report", report: "get_usage" });
	});

	it("其余命令经 prompt 交给 omp", () => {
		expect(resolve("/review HEAD~1")).toEqual({
			kind: "prompt",
			text: "/review HEAD~1",
		});
		expect(resolve("/Skill:foo")).toEqual({
			kind: "prompt",
			text: "/skill:foo",
		});
	});

	it("路径不是命令", () => {
		expect(parseSlash("/usr/bin/env")).toBeNull();
		expect(parseSlash("普通文本")).toBeNull();
	});
});
