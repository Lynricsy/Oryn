import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { fitScale, Presenter, type PresenterPage } from "./Presenter";

const PAGES: PresenterPage[] = [
	{ id: "a", name: "首屏", width: 1280, height: 800 },
	{ id: "b", name: "列表", width: 390, height: 844 },
	{ id: "c", name: "详情", width: 1440, height: 900 },
	{ id: "d", name: "设置", width: 1280, height: 800 },
];

function setup(start?: number) {
	i18n.changeLanguage("zh-CN");
	const onClose = vi.fn();
	render(
		<Presenter
			title="原型"
			pages={PAGES}
			start={start}
			renderPage={(p) => <div data-testid="page">{p.id}</div>}
			onClose={onClose}
		/>,
	);
	const position = () => screen.getByTestId("presenter-position").textContent;
	const key = (key: string, init: KeyboardEventInit = {}) =>
		act(() => {
			fireEvent.keyDown(document.body, { key, ...init });
		});
	return { onClose, position, key };
}

describe("Presenter", () => {
	it("铺满窗口的对话框，一次一页", () => {
		setup();
		const dialog = screen.getByRole("dialog", { name: "演示「原型」" });
		expect(dialog.className).toContain("fixed inset-0");
		expect(screen.getAllByTestId("page")).toHaveLength(1);
		expect(screen.getByTestId("page").textContent).toBe("a");
	});

	it("方向键与空格翻页，不越界", () => {
		const { position, key } = setup();
		expect(position()).toBe("首屏 · 1 / 4");
		key("ArrowLeft");
		expect(position()).toBe("首屏 · 1 / 4");
		key("ArrowRight");
		key(" ");
		expect(position()).toBe("详情 · 3 / 4");
		key(" ", { shiftKey: true });
		expect(position()).toBe("列表 · 2 / 4");
		key("ArrowRight");
		key("ArrowRight");
		key("ArrowRight");
		expect(position()).toBe("设置 · 4 / 4");
		expect(screen.getByTestId("page").textContent).toBe("d");
		expect(
			(screen.getByTestId("presenter-next") as HTMLButtonElement).disabled,
		).toBe(true);
	});

	it("从第 3 页开始时初始页码是 3", () => {
		const { position } = setup(2);
		expect(position()).toBe("详情 · 3 / 4");
	});

	it("Esc 与退出按钮调 onClose，按键不再往下传", () => {
		const { onClose } = setup();
		const below = vi.fn();
		window.addEventListener("keydown", below);
		act(() => {
			fireEvent.keyDown(document.body, { key: "Escape" });
		});
		window.removeEventListener("keydown", below);
		expect(onClose).toHaveBeenCalledTimes(1);
		expect(below).not.toHaveBeenCalled();
		fireEvent.click(screen.getByTestId("presenter-close"));
		expect(onClose).toHaveBeenCalledTimes(2);
	});

	it("单页（没有尺寸）直接铺满，没有翻页按钮", () => {
		i18n.changeLanguage("zh-CN");
		render(
			<Presenter
				title="报告"
				pages={[{ id: "x", name: "报告" }]}
				renderPage={() => <div data-testid="page" />}
				onClose={() => {}}
			/>,
		);
		expect(screen.getByTestId("page").parentElement?.className).toContain(
			"absolute inset-0",
		);
		expect(screen.queryByTestId("presenter-next")).toBeNull();
		expect(screen.getByTestId("presenter-position").textContent).toBe("报告");
	});

	it("fitScale 取宽高两个方向里较小的比例", () => {
		expect(fitScale(1280, 800, { width: 640, height: 800 })).toBe(0.5);
		expect(fitScale(1280, 800, { width: 2560, height: 800 })).toBe(1);
		expect(fitScale(1280, 800, { width: -10, height: 800 })).toBe(0);
	});
});
