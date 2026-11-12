import { describe, expect, it } from "vitest";
import {
	composerKeyAction,
	DOUBLE_ESC_MS,
	parseComposerInput,
} from "./composer";

const enter = { key: "Enter" };

describe("输入区按键", () => {
	it("空闲时 Enter 发送，Shift+Enter 换行", () => {
		expect(composerKeyAction(enter, false, null, 0).action).toBe("prompt");
		expect(
			composerKeyAction({ ...enter, ctrlKey: true }, false, null, 0).action,
		).toBe("prompt");
		expect(
			composerKeyAction({ ...enter, shiftKey: true }, false, null, 0).action,
		).toBe("newline");
	});

	it("忙时 Enter = steer，Ctrl/Cmd+Enter = 排队，Alt+Enter = 中断并发送", () => {
		expect(composerKeyAction(enter, true, null, 0).action).toBe("steer");
		expect(
			composerKeyAction({ ...enter, ctrlKey: true }, true, null, 0).action,
		).toBe("follow_up");
		expect(
			composerKeyAction({ ...enter, metaKey: true }, true, null, 0).action,
		).toBe("follow_up");
		expect(
			composerKeyAction({ ...enter, altKey: true }, true, null, 0).action,
		).toBe("abort_and_prompt");
	});

	it("Esc Esc（间隔内）= 中断；单次或超时不中断", () => {
		const first = composerKeyAction({ key: "Escape" }, true, null, 1000);
		expect(first).toEqual({ action: "none", escAt: 1000 });
		expect(
			composerKeyAction(
				{ key: "Escape" },
				true,
				first.escAt,
				1000 + DOUBLE_ESC_MS,
			).action,
		).toBe("abort");
		expect(
			composerKeyAction(
				{ key: "Escape" },
				true,
				first.escAt,
				1001 + DOUBLE_ESC_MS,
			).action,
		).toBe("none");
		// 空闲时不中断
		expect(composerKeyAction({ key: "Escape" }, false, 1000, 1100).action).toBe(
			"none",
		);
	});

	it("输入法组字中不响应", () => {
		expect(
			composerKeyAction({ ...enter, isComposing: true }, true, null, 0).action,
		).toBe("none");
	});
});

describe("输入解析", () => {
	it("! 与 !! 前缀为 bash", () => {
		expect(parseComposerInput("!ls -la")).toEqual({
			kind: "bash",
			command: "ls -la",
			excludeFromContext: false,
		});
		expect(parseComposerInput("!! git status")).toEqual({
			kind: "bash",
			command: "git status",
			excludeFromContext: true,
		});
		expect(parseComposerInput("!")).toEqual({ kind: "empty" });
	});

	it("斜杠命令与普通文本", () => {
		expect(parseComposerInput("/plan")).toEqual({
			kind: "slash",
			text: "/plan",
		});
		expect(parseComposerInput("/etc/hosts 是什么")).toEqual({
			kind: "slash",
			text: "/etc/hosts 是什么",
		});
		expect(parseComposerInput("  你好 ")).toEqual({
			kind: "text",
			text: "你好",
		});
		expect(parseComposerInput("   ")).toEqual({ kind: "empty" });
	});
});
