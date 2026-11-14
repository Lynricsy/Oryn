/**
 * 软键盘：insertText 逐字符、删除 → BackSpace、换行 → Return、组合输入只在 compositionend 发最终文本、
 * 不可取消的输入按哨兵差异兜底、附加键与粘滞修饰键（下一个键之后自动松开）。
 */
import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { KEYSYM } from "./keysym";
import { SoftKeyboard } from "./SoftKeyboard";

function setup() {
	const onKey = vi.fn();
	render(<SoftKeyboard onKey={onKey} />);
	const input = screen.getByTestId("desktop-kb-input") as HTMLTextAreaElement;
	return { onKey, input };
}

function beforeInput(
	el: HTMLElement,
	inputType: string,
	data: string | null = null,
	opts: { isComposing?: boolean; cancelable?: boolean } = {},
) {
	const ev = new InputEvent("beforeinput", {
		inputType,
		data,
		isComposing: opts.isComposing ?? false,
		cancelable: opts.cancelable ?? true,
		bubbles: true,
	});
	act(() => {
		el.dispatchEvent(ev);
	});
	return ev;
}

describe("SoftKeyboard", () => {
	it("挂载即聚焦隐藏输入框，内容是一个空格哨兵", () => {
		const { input } = setup();
		expect(document.activeElement).toBe(input);
		expect(input.value).toBe(" ");
	});

	it("insertText 逐字符发 keysym（中文走 Unicode keysym），并阻止默认、复位哨兵", () => {
		const { onKey, input } = setup();
		const ev = beforeInput(input, "insertText", "a中");
		expect(ev.defaultPrevented).toBe(true);
		expect(onKey.mock.calls).toEqual([[0x61], [0x01000000 + 0x4e2d]]);
		expect(input.value).toBe(" ");
	});

	it("删除 → BackSpace；换行 / 段落 → Return", () => {
		const { onKey, input } = setup();
		beforeInput(input, "deleteContentBackward");
		beforeInput(input, "insertLineBreak");
		beforeInput(input, "insertParagraph");
		expect(onKey.mock.calls).toEqual([
			[KEYSYM.BackSpace],
			[KEYSYM.Return],
			[KEYSYM.Return],
		]);
	});

	it("组合输入期间不发，compositionend 发最终文本", () => {
		const { onKey, input } = setup();
		beforeInput(input, "insertCompositionText", "n", { isComposing: true });
		beforeInput(input, "insertCompositionText", "ni", { isComposing: true });
		expect(onKey).not.toHaveBeenCalled();
		fireEvent.compositionEnd(input, { data: "你" });
		expect(onKey.mock.calls).toEqual([[0x01000000 + 0x4f60]]);
		expect(input.value).toBe(" ");
	});

	it("不可取消的输入：input 事件按与哨兵的差异补发（新增文字 / 删掉哨兵 = 退格）", () => {
		const { onKey, input } = setup();
		beforeInput(input, "insertText", "ok", { cancelable: false });
		expect(onKey).not.toHaveBeenCalled();
		input.value = " ok";
		fireEvent.input(input);
		expect(onKey.mock.calls).toEqual([[0x6f], [0x6b]]);
		expect(input.value).toBe(" ");
		onKey.mockClear();
		input.value = "";
		fireEvent.input(input);
		expect(onKey.mock.calls).toEqual([[KEYSYM.BackSpace]]);
	});

	it("附加键：Esc / Tab / 方向键；Ctrl 粘滞：下一个键前按下、之后抬起并自动松开", () => {
		const { onKey, input } = setup();
		fireEvent.click(screen.getByRole("button", { name: "Esc" }));
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("deskPanel.kb.left") }),
		);
		expect(onKey.mock.calls).toEqual([[KEYSYM.Escape], [KEYSYM.Left]]);
		onKey.mockClear();

		const ctrl = screen.getByRole("button", {
			name: i18n.t("deskPanel.kb.ctrl"),
		});
		fireEvent.click(ctrl);
		expect(ctrl).toHaveAttribute("aria-pressed", "true");
		beforeInput(input, "insertText", "c");
		expect(onKey.mock.calls).toEqual([
			[KEYSYM.Control_L, true],
			[0x63],
			[KEYSYM.Control_L, false],
		]);
		expect(ctrl).toHaveAttribute("aria-pressed", "false");
		onKey.mockClear();
		beforeInput(input, "insertText", "c");
		expect(onKey.mock.calls).toEqual([[0x63]]);
	});

	it("Ctrl + Alt 同时粘滞：按下顺序 Ctrl、Alt，抬起逆序；附加键同样带修饰", () => {
		const { onKey } = setup();
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("deskPanel.kb.ctrl") }),
		);
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("deskPanel.kb.alt") }),
		);
		fireEvent.click(screen.getByRole("button", { name: "Tab" }));
		expect(onKey.mock.calls).toEqual([
			[KEYSYM.Control_L, true],
			[KEYSYM.Alt_L, true],
			[KEYSYM.Tab],
			[KEYSYM.Alt_L, false],
			[KEYSYM.Control_L, false],
		]);
	});

	it("硬件方向键与 Esc 在隐藏输入框里也转发", () => {
		const { onKey, input } = setup();
		fireEvent.keyDown(input, { key: "ArrowUp" });
		fireEvent.keyDown(input, { key: "Escape" });
		expect(onKey.mock.calls).toEqual([[KEYSYM.Up], [KEYSYM.Escape]]);
	});
});
