import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { isImeKeyEvent, useImeComposition } from "./ime";

function Probe({ onEnter }: { onEnter: () => void }) {
	const ime = useImeComposition();
	return (
		<textarea
			aria-label="probe"
			data-composing={ime.composing ? "1" : "0"}
			onCompositionStart={ime.onCompositionStart}
			onCompositionEnd={ime.onCompositionEnd}
			onBlur={ime.onBlur}
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
	it("compositionstart 到 compositionend 期间同步拦截，即使 isComposing=false", () => {
		const onEnter = vi.fn();
		render(<Probe onEnter={onEnter} />);
		const el = screen.getByLabelText("probe");

		fireEvent.compositionStart(el);
		expect(el).toHaveAttribute("data-composing", "1");
		fireEvent.keyDown(el, { key: "Enter", keyCode: 13, isComposing: false });
		expect(onEnter).not.toHaveBeenCalled();

		fireEvent.compositionEnd(el, { data: "hello" });
		expect(el).toHaveAttribute("data-composing", "0");
	});

	it("Safari / WKWebView：compositionend 后补发 keyCode=229 不触发，紧随其后的正常 Enter 立即触发", () => {
		const onEnter = vi.fn();
		render(<Probe onEnter={onEnter} />);
		const el = screen.getByLabelText("probe");

		fireEvent.compositionStart(el);
		fireEvent.compositionEnd(el, { data: "hello" });
		fireEvent.keyDown(el, { key: "Enter", keyCode: 229, isComposing: false });
		expect(onEnter).not.toHaveBeenCalled();

		fireEvent.keyDown(el, { key: "Enter", keyCode: 13, isComposing: false });
		expect(onEnter).toHaveBeenCalledTimes(1);
	});

	it("数字键或鼠标选词触发 compositionend 后，紧接着按 Enter 不被吞掉", () => {
		const onEnter = vi.fn();
		render(<Probe onEnter={onEnter} />);
		const el = screen.getByLabelText("probe");

		fireEvent.compositionStart(el);
		fireEvent.compositionEnd(el, { data: "你好" });
		// 模拟鼠标选词后无 keyup，或数字键选词后立即按回车发送
		fireEvent.keyDown(el, { key: "Enter", keyCode: 13, isComposing: false });
		expect(onEnter).toHaveBeenCalledTimes(1);
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
