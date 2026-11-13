/** 可收起卡片：整行切换、动作按钮不误触、收起后正文 inert、收起状态持久化到 localStorage。 */
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	COLLAPSED_CARDS_KEY,
	loadCollapsedCards,
	uiStore,
	useUi,
} from "../../state/ui";
import { Card } from "./data";

/** 与信息面板同样的接法：收起状态取自 uiStore。 */
function Folding({ onAction }: { onAction: () => void }) {
	const collapsed = useUi((s) => !!s.collapsedCards.demo);
	return (
		<Card
			title="演示"
			collapsible
			collapsed={collapsed}
			onToggle={() => uiStore.getState().toggleCard("demo")}
			summary={<span>摘要</span>}
			action={
				<button type="button" onClick={onAction}>
					动作
				</button>
			}
		>
			<button type="button">正文按钮</button>
		</Card>
	);
}

beforeEach(() => {
	localStorage.clear();
	uiStore.setState({ collapsedCards: {} });
});
afterEach(cleanup);

describe("可收起卡片", () => {
	it("点标题行切换 aria-expanded；收起后正文 inert", () => {
		render(<Folding onAction={() => undefined} />);
		const head = screen.getByRole("button", { name: "演示" });
		expect(head).toHaveAttribute("aria-expanded", "true");
		const body = document.getElementById(
			head.getAttribute("aria-controls") ?? "",
		);
		expect(body).not.toHaveAttribute("inert");
		fireEvent.click(head);
		expect(head).toHaveAttribute("aria-expanded", "false");
		expect(body).toHaveAttribute("inert");
		fireEvent.click(head);
		expect(head).toHaveAttribute("aria-expanded", "true");
		expect(body).not.toHaveAttribute("inert");
	});

	it("可收起卡片不被纵向 flex 容器压缩（overflow-hidden 会让自动最小高度归零）", () => {
		render(<Folding onAction={() => undefined} />);
		const section = screen
			.getByRole("button", { name: "演示" })
			.closest("section");
		expect(section).toHaveClass("overflow-hidden", "shrink-0");
	});

	it("点卡头里的动作按钮不切换", () => {
		const onAction = vi.fn();
		render(<Folding onAction={onAction} />);
		fireEvent.click(screen.getByRole("button", { name: "动作" }));
		expect(onAction).toHaveBeenCalledOnce();
		expect(screen.getByRole("button", { name: "演示" })).toHaveAttribute(
			"aria-expanded",
			"true",
		);
	});

	it("收起状态写进 localStorage，重新加载后首帧即为收起", () => {
		const { unmount } = render(<Folding onAction={() => undefined} />);
		fireEvent.click(screen.getByRole("button", { name: "演示" }));
		unmount();
		expect(JSON.parse(localStorage.getItem(COLLAPSED_CARDS_KEY) ?? "")).toEqual(
			{ demo: true },
		);
		// 模拟刷新：store 以同步读取的存储值初始化
		uiStore.setState({ collapsedCards: loadCollapsedCards() });
		render(<Folding onAction={() => undefined} />);
		expect(screen.getByRole("button", { name: "演示" })).toHaveAttribute(
			"aria-expanded",
			"false",
		);
	});

	it("存储值非法时全部展开", () => {
		localStorage.setItem(COLLAPSED_CARDS_KEY, "[1]");
		expect(loadCollapsedCards()).toEqual({});
		localStorage.setItem(COLLAPSED_CARDS_KEY, "{oops");
		expect(loadCollapsedCards()).toEqual({});
	});

	it("不传 collapsible 时没有切换按钮", () => {
		render(
			<Card title="普通" action={<span>动作</span>}>
				正文
			</Card>,
		);
		expect(screen.queryByRole("button")).toBeNull();
		expect(screen.getByRole("heading", { name: "普通" })).toBeInTheDocument();
	});
});
