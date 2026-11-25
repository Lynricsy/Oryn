import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	isImeKeyEvent,
	POST_COMPOSITION_ENTER_MS,
	useImeComposition,
} from "./ime";

function Probe({ onEnter }: { onEnter: () => void }) {
	const ime = useImeComposition();
	return (
		<textarea
			aria-label="probe"
			data-composing={ime.composing ? "1" : "0"}
			onCompositionStart={ime.onCompositionStart}
			onCompositionEnd={ime.onCompositionEnd}
			onBlur={ime.onBlur}
			onKeyUp={ime.onKeyUp}
			onKeyDown={(e) => {
				if (ime.shouldIgnoreKeyDown(e)) return;
				if (e.key === "Enter" && !e.shiftKey) {
					e.preventDefault();
					onEnter();
				}
			}}
		/>
	);
}

describe("isImeKeyEvent", () => {
	it("识别 isComposing、Process、keyCode=229、which=229", () => {
		expect(isImeKeyEvent({ key: "Enter", isComposing: true })).toBe(true);
		expect(isImeKeyEvent({ key: "Process" })).toBe(true);
		expect(
			isImeKeyEvent({ key: "Enter", isComposing: false, keyCode: 229 }),
		).toBe(true);
		expect(
			isImeKeyEvent({ key: "Enter", isComposing: false, which: 229 }),
		).toBe(true);
		expect(
			isImeKeyEvent({ key: "Enter", isComposing: false, keyCode: 13 }),
		).toBe(false);
	});
});

describe("useImeComposition", () => {
	afterEach(() => {
		vi.useRealTimers();
	});

	it("高速打字末字母 rollover 的 keyup 不会提前清除 compositionend 保护窗口", () => {
		const onEnter = vi.fn();
		render(<Probe onEnter={onEnter} />);
		const el = screen.getByLabelText("probe");

		fireEvent.compositionStart(el);
		fireEvent.compositionEnd(el, { data: "hello" });
		// 末字母 o 在 compositionend 后才松手
		fireEvent.keyUp(el, { key: "o" });
		fireEvent.keyDown(el, { key: "Enter", keyCode: 13 });
		expect(onEnter).not.toHaveBeenCalled();

		// 紧接着用户真正再按回车应触发发送
		fireEvent.keyDown(el, { key: "Enter", keyCode: 13 });
		expect(onEnter).toHaveBeenCalledTimes(1);
	});

	it("超过保护窗口后按 Enter 正常发送", () => {
		vi.useFakeTimers();
		const onEnter = vi.fn();
		render(<Probe onEnter={onEnter} />);
		const el = screen.getByLabelText("probe");

		fireEvent.compositionStart(el);
		fireEvent.compositionEnd(el, { data: "hello" });
		act(() => {
			vi.advanceTimersByTime(POST_COMPOSITION_ENTER_MS + 1);
		});
		fireEvent.keyDown(el, { key: "Enter", keyCode: 13 });
		expect(onEnter).toHaveBeenCalledTimes(1);
	});

	it("compositionend 后紧跟 Shift+Enter 换行不被拦截", () => {
		const onEnter = vi.fn();
		render(<Probe onEnter={onEnter} />);
		const el = screen.getByLabelText("probe");

		fireEvent.compositionStart(el);
		fireEvent.compositionEnd(el, { data: "hello" });
		const ev = new KeyboardEvent("keydown", {
			key: "Enter",
			shiftKey: true,
			keyCode: 13,
			bubbles: true,
			cancelable: true,
		});
		act(() => {
			el.dispatchEvent(ev);
		});
		expect(ev.defaultPrevented).toBe(false);
		expect(onEnter).not.toHaveBeenCalled();
	});

	it("组字期间失焦会重置状态，重新聚焦后 Enter 正常发送", () => {
		const onEnter = vi.fn();
		render(<Probe onEnter={onEnter} />);
		const el = screen.getByLabelText("probe");

		fireEvent.compositionStart(el);
		expect(el).toHaveAttribute("data-composing", "1");
		fireEvent.blur(el);
		expect(el).toHaveAttribute("data-composing", "0");
		fireEvent.keyDown(el, { key: "Enter", keyCode: 13 });
		expect(onEnter).toHaveBeenCalledTimes(1);
	});
});
