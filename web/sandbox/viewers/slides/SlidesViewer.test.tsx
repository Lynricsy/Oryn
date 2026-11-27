import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { Deck } from "../../../src/lib/artifacts/slides";
import { EIGHT_LAYOUT_DECK } from "../../../src/lib/artifacts/slides-fixture";
import type { ViewerContent } from "../shared/content";
import { viewerStrings } from "../shared/strings";
import { SlidesViewer } from "./SlidesViewer";

const strings = viewerStrings("zh-CN");

function content(deck: Deck, over: Partial<ViewerContent> = {}): ViewerContent {
	return {
		state: "ready",
		doc: {
			text: JSON.stringify(deck),
			version: 3,
			head: 3,
			base: "https://a-token.example/_oryn/v/3/",
		},
		editable: false,
		status: { kind: "idle" },
		save: vi.fn().mockResolvedValue("saved"),
		dismiss: vi.fn(),
		...over,
	};
}

const counter = () => screen.getByTestId("slide-counter").textContent;
const press = (key: string, init: KeyboardEventInit = {}) =>
	act(() => {
		fireEvent.keyDown(window, { key, ...init });
	});

/** 每张幻灯片上必须出现的文字（各版式的必填字段）。 */
const REQUIRED_TEXT = [
	["2026 年第三季度回顾"],
	["本季重点", "发布 2.0", "性能"],
	["第二部分：增长"],
	["方案 A", "贵一些，但快三倍。"],
	[],
	["先做对，再做快。"],
	["42%"],
	["阶段", "时间", "设计", "11 月"],
];

describe("SlidesViewer", () => {
	it("每种版式渲染出它的必填字段", () => {
		const { container } = render(
			<SlidesViewer
				content={content(EIGHT_LAYOUT_DECK)}
				view={null}
				strings={strings}
			/>,
		);
		EIGHT_LAYOUT_DECK.slides.forEach((s, i) => {
			const slide = container.querySelector(".oryn-slide");
			expect(slide?.getAttribute("data-layout")).toBe(s.layout);
			for (const t of REQUIRED_TEXT[i] ?? [])
				expect(slide?.textContent).toContain(t);
			if (s.layout === "image") {
				const img = slide?.querySelector("img");
				expect(img?.getAttribute("src")).toBe(
					"https://a-token.example/_oryn/v/3/images/chart.png",
				);
				expect(img?.getAttribute("alt")).toBe("月活走势图");
			}
			press("ArrowRight");
		});
	});

	it("方向键翻页不越界", () => {
		render(
			<SlidesViewer
				content={content(EIGHT_LAYOUT_DECK)}
				view={null}
				strings={strings}
			/>,
		);
		expect(counter()).toBe("1 / 8");
		press("ArrowLeft");
		press("PageUp");
		expect(counter()).toBe("1 / 8");
		press("ArrowDown");
		press(" ");
		expect(counter()).toBe("3 / 8");
		press(" ", { shiftKey: true });
		expect(counter()).toBe("2 / 8");
		press("End");
		expect(counter()).toBe("8 / 8");
		press("ArrowRight");
		press("PageDown");
		expect(counter()).toBe("8 / 8");
		press("Home");
		expect(counter()).toBe("1 / 8");
	});

	it("演示经运行时请父页面铺满，Esc 退出", () => {
		const present = vi.fn();
		Object.defineProperty(window, "__orynPresent", {
			value: present,
			configurable: true,
		});
		try {
			const { container } = render(
				<SlidesViewer
					content={content(EIGHT_LAYOUT_DECK)}
					view={null}
					strings={strings}
				/>,
			);
			fireEvent.click(screen.getByRole("button", { name: "演示" }));
			expect(present).toHaveBeenLastCalledWith(true);
			expect(container.querySelector(".oryn-slides-presenting")).not.toBeNull();
			expect(screen.getByText("按 Esc 退出")).toBeInTheDocument();
			press("Escape");
			expect(present).toHaveBeenLastCalledWith(false);
			expect(container.querySelector(".oryn-slides-presenting")).toBeNull();
		} finally {
			Reflect.deleteProperty(window, "__orynPresent");
		}
	});

	it("就地改字后保存为新版本，写回两空格缩进的 JSON", async () => {
		const c = content(EIGHT_LAYOUT_DECK, { editable: true });
		const { container } = render(
			<SlidesViewer content={c} view={null} strings={strings} />,
		);
		fireEvent.click(screen.getByRole("button", { name: "编辑" }));
		const title = container.querySelector('[data-path="title"]') as HTMLElement;
		expect(title.getAttribute("contenteditable")).toBe("plaintext-only");
		title.textContent = "新标题";
		fireEvent.blur(title);
		await act(async () => {
			fireEvent.click(screen.getByRole("button", { name: "保存为 v4" }));
		});
		const expected = structuredClone(EIGHT_LAYOUT_DECK);
		if (expected.slides[0]?.layout !== "title") throw new Error("fixture");
		expected.slides[0].title = "新标题";
		expect(c.save).toHaveBeenCalledWith(
			`${JSON.stringify(expected, null, 2)}\n`,
		);
		// 保存后退出编辑
		expect(screen.queryByRole("button", { name: "保存为 v4" })).toBeNull();
	});

	it("没有 content 能力时不显示编辑", () => {
		render(
			<SlidesViewer
				content={content(EIGHT_LAYOUT_DECK)}
				view={null}
				strings={strings}
			/>,
		);
		expect(screen.queryByRole("button", { name: "编辑" })).toBeNull();
	});

	it("打印视图逐页排开并设 @page 尺寸与 data-ready", async () => {
		const deck: Deck = { ...EIGHT_LAYOUT_DECK, aspect: "4:3" };
		const { container } = render(
			<SlidesViewer content={content(deck)} view="print" strings={strings} />,
		);
		expect(container.querySelectorAll(".oryn-s-print-page")).toHaveLength(8);
		expect(container.querySelector("style")?.textContent).toContain(
			"size: 960px 720px",
		);
		await vi.waitFor(() =>
			expect(document.documentElement.dataset.ready).toBe("true"),
		);
		delete document.documentElement.dataset.ready;
	});

	it("缩略图只画第一张、没有按钮", () => {
		const { container } = render(
			<SlidesViewer
				content={content(EIGHT_LAYOUT_DECK)}
				view="thumbnail"
				strings={strings}
			/>,
		);
		expect(container.querySelectorAll(".oryn-slide")).toHaveLength(1);
		expect(container.querySelector("button")).toBeNull();
	});

	it("格式有问题时列出问题而不是白屏", () => {
		render(
			<SlidesViewer
				content={content({ slides: [{ layout: "title" }] } as unknown as Deck)}
				view={null}
				strings={strings}
			/>,
		);
		expect(screen.getByRole("alert")).toHaveTextContent("slides[0].title");
	});

	it("评论模式下点幻灯片报出张号、标题与归一化坐标；定位翻到锚点那一张并画图钉", () => {
		const pick = vi.fn();
		const rect = vi
			.spyOn(HTMLElement.prototype, "getBoundingClientRect")
			.mockReturnValue(new DOMRect(100, 50, 400, 200));
		try {
			const comments = {
				mode: "comment" as const,
				pins: [],
				locate: null,
				pick,
			};
			const { container, rerender } = render(
				<SlidesViewer
					content={content(EIGHT_LAYOUT_DECK)}
					view={null}
					strings={strings}
					comments={comments}
				/>,
			);
			expect(screen.queryByRole("button", { name: "编辑" })).toBeNull();
			const button = screen.getByRole("button", { name: "在第 1 张添加评论" });
			expect(button.closest('[role="img"]')).toBeNull();
			fireEvent.click(button, {
				clientX: 200,
				clientY: 100,
			});
			expect(pick).toHaveBeenLastCalledWith(
				{
					page: { number: 1, name: "2026 年第三季度回顾" },
					x: 0.25,
					y: 0.25,
					range: null,
					block: null,
					text: "",
				},
				{ x: 200, y: 100 },
			);

			const anchor = {
				page: { number: 2, name: "本季重点" },
				x: 0.5,
				y: 0.75,
				range: null,
				block: null,
				text: "",
			};
			rerender(
				<SlidesViewer
					content={content(EIGHT_LAYOUT_DECK)}
					view={null}
					strings={strings}
					comments={{
						...comments,
						mode: null,
						pins: [{ n: 1, answered: false, anchor }],
						locate: anchor,
					}}
				/>,
			);
			expect(counter()).toBe("2 / 8");
			const pin = container.querySelector<HTMLElement>(".oryn-pin");
			expect(pin?.textContent).toBe("1");
			expect(pin?.style.left).toBe("50%");
			expect(pin?.style.top).toBe("75%");
			expect(screen.queryByRole("button", { name: /添加评论/ })).toBeNull();
		} finally {
			rect.mockRestore();
		}
	});
});
