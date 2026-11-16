import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import i18n from "../../../i18n";
import type { CanvasPage } from "../../../lib/artifacts/canvas-doc";
import { FocusedPage } from "./FocusedPage";
import { keyAction } from "./keys";

const page = (id: string, x: number): CanvasPage => ({
	id,
	canvas: "c1",
	name: id.toUpperCase(),
	file: `${id}.html`,
	x,
	y: 0,
	width: 1280,
	height: 800,
	locked: false,
});

const PAGES = [page("a", 0), page("b", 1400), page("c", 2800)];

function Harness({
	start,
	onClose,
	below,
}: {
	start: number;
	onClose(): void;
	below(): void;
}) {
	const [index, setIndex] = useState(start);
	return (
		// biome-ignore lint/a11y/noStaticElementInteractions: 测试里观察按键是否往外传
		<div onKeyDown={below}>
			<FocusedPage
				pages={PAGES}
				index={index}
				contentOf={(p) =>
					p.id === "b"
						? { kind: "empty" }
						: { kind: "frame", src: `https://a.example/_oryn/v/3/${p.file}` }
				}
				onIndex={setIndex}
				onClose={onClose}
			/>
		</div>
	);
}

function setup(start = 0) {
	i18n.changeLanguage("zh-CN");
	const onClose = vi.fn();
	const below = vi.fn();
	render(<Harness start={start} onClose={onClose} below={below} />);
	const root = screen.getByTestId("canvas-focused");
	const position = () =>
		screen.getByTestId("canvas-focus-position").textContent;
	return { root, onClose, below, position };
}

describe("FocusedPage", () => {
	it("显示一个画框的帧，获得焦点", () => {
		const { root, position } = setup();
		expect(document.activeElement).toBe(root);
		expect(position()).toBe("1 / 3");
		const frame = root.querySelector("iframe");
		expect(frame?.getAttribute("src")).toBe(
			"https://a.example/_oryn/v/3/a.html",
		);
		expect(frame?.getAttribute("sandbox")).toBe(
			"allow-scripts allow-forms allow-downloads",
		);
	});

	it("← / → 按阅读顺序翻页，不越界；按键不往外传", () => {
		const { root, below, position } = setup();
		fireEvent.keyDown(root, { key: "ArrowLeft" });
		expect(position()).toBe("1 / 3");
		fireEvent.keyDown(root, { key: "ArrowRight" });
		expect(position()).toBe("2 / 3");
		// 空白画框显示占位
		expect(screen.getByText("新画框 · Agent 画好之前是空的")).toBeTruthy();
		fireEvent.keyDown(root, { key: "ArrowRight" });
		fireEvent.keyDown(root, { key: "ArrowRight" });
		expect(position()).toBe("3 / 3");
		expect(
			(screen.getByTestId("canvas-focus-next") as HTMLButtonElement).disabled,
		).toBe(true);
		expect(below).not.toHaveBeenCalled();
	});

	it("Esc 与「返回画布」退出聚焦", () => {
		const { root, onClose } = setup(1);
		fireEvent.keyDown(root, { key: "Escape" });
		expect(onClose).toHaveBeenCalledTimes(1);
		fireEvent.click(screen.getByTestId("canvas-focus-back"));
		expect(onClose).toHaveBeenCalledTimes(2);
	});

	it("画布上按 Enter 是聚焦", () => {
		expect(
			keyAction({
				key: "Enter",
				code: "Enter",
				shiftKey: false,
				altKey: false,
				ctrlKey: false,
				metaKey: false,
			}),
		).toEqual({ type: "focus" });
	});
});
