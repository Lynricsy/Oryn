import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import {
	composerKeyAction,
	DOUBLE_ESC_MS,
	getSendKey,
	parseComposerInput,
	SEND_KEY_STORAGE,
	setSendKey,
	useSendKey,
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
		// 空闲、输入框有字：不中断，也不计入连按
		expect(composerKeyAction({ key: "Escape" }, false, 1000, 1100)).toEqual({
			action: "none",
			escAt: null,
		});
	});

	it("空闲且输入框为空：Esc Esc（间隔内）= 回退", () => {
		const first = composerKeyAction({ key: "Escape" }, false, null, 1000, true);
		expect(first).toEqual({ action: "none", escAt: 1000 });
		expect(
			composerKeyAction({ key: "Escape" }, false, first.escAt, 1200, true),
		).toEqual({ action: "rewind", escAt: null });
		expect(
			composerKeyAction(
				{ key: "Escape" },
				false,
				first.escAt,
				1001 + DOUBLE_ESC_MS,
				true,
			).action,
		).toBe("none");
	});

	it("输入法组字中与 Safari/WebKit 补发的 229 / Process 不响应", () => {
		expect(
			composerKeyAction({ ...enter, isComposing: true }, true, null, 0).action,
		).toBe("none");
		expect(
			composerKeyAction(
				{ ...enter, isComposing: false, keyCode: 229 },
				false,
				null,
				0,
			).action,
		).toBe("none");
		expect(
			composerKeyAction(
				{ ...enter, isComposing: false, which: 229 },
				false,
				null,
				0,
			).action,
		).toBe("none");
		expect(composerKeyAction({ key: "Process" }, false, null, 0).action).toBe(
			"none",
		);
	});
});

describe("发送键 ⌘/Ctrl+Enter（mod）", () => {
	const mod = (e: object, busy: boolean) =>
		composerKeyAction({ ...enter, ...e }, busy, null, 0, false, "mod").action;

	it("空闲：Enter / Shift+Enter / Alt+Enter 换行，⌘ 或 Ctrl+Enter 发送（带 Shift 也发送）", () => {
		expect(mod({}, false)).toBe("newline");
		expect(mod({ shiftKey: true }, false)).toBe("newline");
		expect(mod({ altKey: true }, false)).toBe("newline");
		expect(mod({ metaKey: true }, false)).toBe("prompt");
		expect(mod({ ctrlKey: true }, false)).toBe("prompt");
		expect(mod({ ctrlKey: true, shiftKey: true }, false)).toBe("prompt");
	});

	it("忙时：⌘Enter = steer，⇧⌘Enter = 排队，⌥⌘Enter = 中断并发送；不带 ⌘ 仍是换行", () => {
		expect(mod({}, true)).toBe("newline");
		expect(mod({ altKey: true }, true)).toBe("newline");
		expect(mod({ metaKey: true }, true)).toBe("steer");
		expect(mod({ ctrlKey: true, shiftKey: true }, true)).toBe("follow_up");
		expect(mod({ metaKey: true, altKey: true }, true)).toBe("abort_and_prompt");
	});

	it("Esc Esc 不受发送键影响", () => {
		const first = composerKeyAction(
			{ key: "Escape" },
			true,
			null,
			1000,
			false,
			"mod",
		);
		expect(
			composerKeyAction(
				{ key: "Escape" },
				true,
				first.escAt,
				1200,
				false,
				"mod",
			).action,
		).toBe("abort");
	});
});

describe("发送键偏好", () => {
	afterEach(() => setSendKey("enter"));

	it("缺省 Enter 发送；setSendKey 存进 localStorage，composerKeyAction 不传发送键时读它", () => {
		expect(getSendKey()).toBe("enter");
		setSendKey("mod");
		expect(localStorage.getItem(SEND_KEY_STORAGE)).toBe("mod");
		expect(composerKeyAction(enter, false, null, 0).action).toBe("newline");
		expect(
			composerKeyAction({ ...enter, ctrlKey: true }, false, null, 0).action,
		).toBe("prompt");
		setSendKey("enter");
		expect(composerKeyAction(enter, false, null, 0).action).toBe("prompt");
	});

	it("别的标签页改了偏好（storage 事件）：useSendKey 跟上", () => {
		const { result } = renderHook(() => useSendKey());
		expect(result.current).toBe("enter");
		act(() => {
			localStorage.setItem(SEND_KEY_STORAGE, "mod");
			window.dispatchEvent(
				new StorageEvent("storage", { key: SEND_KEY_STORAGE }),
			);
		});
		expect(result.current).toBe("mod");
		expect(getSendKey()).toBe("mod");
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

	it("单独的 '.' 解析为 resume，其他含点文本与 'c' 仍为普通文本", () => {
		expect(parseComposerInput(".")).toEqual({ kind: "resume" });
		expect(parseComposerInput("  . \n")).toEqual({ kind: "resume" });
		expect(parseComposerInput("..")).toEqual({ kind: "text", text: ".." });
		expect(parseComposerInput(". 继续")).toEqual({
			kind: "text",
			text: ". 继续",
		});
		expect(parseComposerInput("c")).toEqual({ kind: "text", text: "c" });
	});

	it("$ / $$ 紧跟空白解析为 Python eval；$HOME 等自然文本保持 text", () => {
		expect(parseComposerInput("$ print(1)")).toEqual({
			kind: "eval",
			code: "print(1)",
			excludeFromContext: false,
		});
		expect(parseComposerInput("$$ 1 + 1")).toEqual({
			kind: "eval",
			code: "1 + 1",
			excludeFromContext: true,
		});
		expect(parseComposerInput("$HOME")).toEqual({
			kind: "text",
			text: "$HOME",
		});
		expect(parseComposerInput("$$var")).toEqual({
			kind: "text",
			text: "$$var",
		});
	});

	it("? 开头解析为 btw，?? 保持普通文本", () => {
		expect(parseComposerInput("? 这是什么")).toEqual({
			kind: "btw",
			question: "这是什么",
		});
		expect(parseComposerInput("?")).toEqual({ kind: "btw", question: "" });
		expect(parseComposerInput("??")).toEqual({ kind: "text", text: "??" });
	});

	it("-> / => 解析为 follow_up 队列简写，空正文忽略", () => {
		expect(parseComposerInput("-> 下一步再跑单测")).toEqual({
			kind: "queue",
			text: "下一步再跑单测",
		});
		expect(parseComposerInput("=> 顺手看日志")).toEqual({
			kind: "queue",
			text: "顺手看日志",
		});
		expect(parseComposerInput("->")).toEqual({ kind: "empty" });
		expect(parseComposerInput("=>   ")).toEqual({ kind: "empty" });
	});
});
