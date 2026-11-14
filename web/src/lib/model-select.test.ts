/** 角色 / 模型切换命令：新 Core 走 oryn_select_model；旧 Core（Unknown command）回退 set_model，且本页面不再重试。 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { OmpCommandError } from "../realtime/socket";
import type { RoleSlot } from "./model-roles";
import {
	isLegacyCore,
	resetLegacyCore,
	selectModel,
	selectRole,
} from "./model-select";

const { cmd } = vi.hoisted(() => ({ cmd: vi.fn() }));
vi.mock("../realtime/socket", () => {
	class OmpCommandError extends Error {
		code: string;
		constructor(command: string, code: string, message: string) {
			super(message || code);
			this.code = code;
			void command;
		}
	}
	return {
		OmpCommandError,
		realtime: { cmd: (...args: unknown[]) => cmd(...args) },
	};
});

const MARE: RoleSlot = {
	id: "Mare",
	name: "Mare",
	status: "ok",
	model: { provider: "openai", id: "gpt-6.1-sol" },
	thinkingLevel: "high",
};

/** 发出的命令帧（cmd 的第二个参数）。 */
const sent = () => cmd.mock.calls.map((c) => c[1] as { type: string });
const types = () => sent().map((f) => f.type);

let legacy = false;

beforeEach(() => {
	cmd.mockReset();
	resetLegacyCore();
	legacy = false;
	cmd.mockImplementation(async (_t: string, f: { type: string }) => {
		if (legacy && f.type.startsWith("oryn_"))
			throw new OmpCommandError(
				f.type,
				"command_failed",
				`Unknown command: ${f.type}`,
			);
		return {};
	});
});

describe("selectRole", () => {
	it("新 Core：只发 oryn_select_model{role}，没有 set_model", async () => {
		expect(await selectRole("T1", MARE)).toEqual({ legacy: false });
		expect(cmd.mock.calls.map((c) => c[1])).toEqual([
			{ type: "oryn_select_model", role: "Mare" },
		]);
		expect(isLegacyCore("T1")).toBe(false);
	});

	it("旧 Core：先收到 Unknown command，再发 set_model + set_thinking_level；第二次不再试新命令", async () => {
		legacy = true;
		expect(await selectRole("T1", MARE)).toEqual({ legacy: true });
		expect(cmd.mock.calls.map((c) => c[1])).toEqual([
			{ type: "oryn_select_model", role: "Mare" },
			{ type: "set_model", provider: "openai", modelId: "gpt-6.1-sol" },
			{ type: "set_thinking_level", level: "high" },
		]);
		expect(isLegacyCore("T1")).toBe(true);
		cmd.mockClear();
		await selectRole("T1", { ...MARE, thinkingLevel: undefined });
		expect(types()).toEqual(["set_model"]);
		// 别的线程照常探测
		await selectRole("T2", MARE);
		expect(types()).toEqual([
			"set_model",
			"oryn_select_model",
			"set_model",
			"set_thinking_level",
		]);
	});

	it("其它错误原样抛出，不当作旧 Core", async () => {
		cmd.mockRejectedValueOnce(
			new OmpCommandError(
				"oryn_select_model",
				"command_failed",
				"Role has no available model: Mare",
			),
		);
		await expect(selectRole("T1", MARE)).rejects.toThrow(
			"Role has no available model",
		);
		expect(types()).toEqual(["oryn_select_model"]);
		expect(isLegacyCore("T1")).toBe(false);
	});
});

describe("selectModel", () => {
	it("新 Core：oryn_select_model{provider, modelId}", async () => {
		await selectModel("T1", { provider: "openai", id: "gpt-6.1-sol" });
		expect(cmd.mock.calls.map((c) => c[1])).toEqual([
			{ type: "oryn_select_model", provider: "openai", modelId: "gpt-6.1-sol" },
		]);
	});

	it("旧 Core：回退 set_model，之后直接发 set_model", async () => {
		legacy = true;
		await selectModel("T1", { provider: "openai", id: "gpt-6.1-sol" });
		expect(types()).toEqual(["oryn_select_model", "set_model"]);
		await selectModel("T1", { provider: "anthropic", id: "claude-opus-5-5" });
		expect(types()).toEqual(["oryn_select_model", "set_model", "set_model"]);
	});
});
