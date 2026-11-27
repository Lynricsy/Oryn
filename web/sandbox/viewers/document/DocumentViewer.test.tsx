import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ViewerContent } from "../shared/content";
import { viewerStrings } from "../shared/strings";
import { DocumentViewer } from "./DocumentViewer";

const strings = viewerStrings("zh-CN");

const MD = `# 设计说明

见 [文档](https://example.com/x) 与图 ![走势](images/a.png)。

<script>alert(1)</script>

\`\`\`ts
const n = 1;
\`\`\`
`;

function content(over: Partial<ViewerContent> = {}): ViewerContent {
	return {
		state: "ready",
		doc: {
			text: MD,
			version: 2,
			head: 2,
			base: "https://a-token.example/_oryn/v/2/",
		},
		editable: false,
		status: { kind: "idle" },
		save: vi.fn().mockResolvedValue("saved"),
		dismiss: vi.fn(),
		...over,
	};
}

describe("DocumentViewer", () => {
	it("渲染 Markdown：跳过原生 HTML、链接留在本页、图片按版本根解析、代码高亮", () => {
		const { container } = render(
			<DocumentViewer content={content()} view={null} strings={strings} />,
		);
		expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent(
			"设计说明",
		);
		expect(container.querySelector("script")).toBeNull();
		expect(screen.getByRole("link", { name: "文档" })).toHaveAttribute(
			"target",
			"_self",
		);
		expect(screen.getByRole("img", { name: "走势" })).toHaveAttribute(
			"src",
			"https://a-token.example/_oryn/v/2/images/a.png",
		);
		expect(
			container.querySelector("pre code.hljs .hljs-keyword"),
		).not.toBeNull();
		expect(screen.queryByRole("button", { name: "编辑" })).toBeNull();
	});

	it("按块编辑后保存全文", async () => {
		const c = content({ editable: true });
		render(<DocumentViewer content={c} view={null} strings={strings} />);
		fireEvent.click(screen.getByRole("button", { name: "编辑" }));
		const blocks = screen.getAllByRole("button", { name: "修改这一块" });
		expect(blocks).toHaveLength(4);
		const save = screen.getByRole("button", { name: "保存为 v3" });
		expect(save).toBeDisabled();
		fireEvent.click(blocks[0] as HTMLElement);
		const box = screen.getByRole("textbox") as HTMLTextAreaElement;
		expect(box.value).toBe("# 设计说明");
		fireEvent.change(box, { target: { value: "# 新标题" } });
		fireEvent.blur(box);
		await act(async () => {
			fireEvent.click(screen.getByRole("button", { name: "保存为 v3" }));
		});
		expect(c.save).toHaveBeenCalledWith(MD.replace("# 设计说明", "# 新标题"));
		expect(screen.queryByRole("button", { name: "保存为 v3" })).toBeNull();
	});

	it("保存没成功时留在编辑态，状态条显示原因", async () => {
		const c = content({
			editable: true,
			save: vi.fn().mockResolvedValue("error"),
			status: { kind: "invalid", message: "document.md:3: 图片不在这个版本里" },
		});
		render(<DocumentViewer content={c} view={null} strings={strings} />);
		expect(screen.getByRole("alert")).toHaveTextContent("图片不在这个版本里");
		fireEvent.click(screen.getByRole("button", { name: "编辑" }));
		fireEvent.click(
			screen.getAllByRole("button", { name: "修改这一块" })[0] as HTMLElement,
		);
		const box = screen.getByRole("textbox");
		fireEvent.change(box, { target: { value: "# x" } });
		fireEvent.blur(box);
		await act(async () => {
			fireEvent.click(screen.getByRole("button", { name: "保存为 v3" }));
		});
		expect(
			screen.getByRole("button", { name: "保存为 v3" }),
		).toBeInTheDocument();
	});

	it("缩略图没有任何按钮", () => {
		render(
			<DocumentViewer
				content={content({ editable: true })}
				view="thumbnail"
				strings={strings}
			/>,
		);
		expect(screen.queryByRole("button")).toBeNull();
	});

	it("评论模式下点块报出块序号与块文字，不进入编辑；整篇按钮报出空锚点", () => {
		const pick = vi.fn();
		render(
			<DocumentViewer
				content={content({ editable: true })}
				view={null}
				strings={strings}
				comments={{ mode: "comment", pins: [], locate: null, pick }}
			/>,
		);
		expect(screen.queryByRole("button", { name: "编辑" })).toBeNull();
		const blocks = screen.getAllByRole("button", { name: "评论这一块" });
		expect(blocks).toHaveLength(4);
		fireEvent.click(blocks[1] as HTMLElement, { clientX: 10, clientY: 20 });
		expect(pick).toHaveBeenLastCalledWith(
			{
				page: null,
				x: null,
				y: null,
				range: null,
				block: 1,
				text: "见 [文档](https://example.com/x) 与图 ![走势](images/a.png)。",
			},
			{ x: 10, y: 20 },
		);
		expect(screen.queryByRole("textbox")).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "评论整篇文档" }), {
			clientX: 5,
			clientY: 6,
		});
		expect(pick).toHaveBeenLastCalledWith(null, { x: 5, y: 6 });
	});

	it("图钉按块文字重新定位：序号对不上时找文字相同的块，找不到不画", () => {
		const anchor = (block: number | null, text: string) => ({
			page: null,
			x: null,
			y: null,
			range: null,
			block,
			text,
		});
		const { container } = render(
			<DocumentViewer
				content={content()}
				view={null}
				strings={strings}
				comments={{
					mode: null,
					pins: [
						{ n: 1, answered: false, anchor: anchor(0, "# 设计说明") },
						{
							n: 2,
							answered: true,
							anchor: anchor(
								3,
								"见 [文档](https://example.com/x) 与图 ![走势](images/a.png)。",
							),
						},
						{ n: 3, answered: false, anchor: anchor(1, "已删掉的段落") },
						{ n: 4, answered: false, anchor: anchor(null, "") },
					],
					locate: null,
					pick: vi.fn(),
				}}
			/>,
		);
		const pinsOf = (sel: string) =>
			[...container.querySelectorAll(`${sel} .oryn-pin`)].map(
				(p) => p.textContent,
			);
		expect(pinsOf('[data-block="0"]')).toEqual(["1"]);
		expect(pinsOf('[data-block="1"]')).toEqual(["2"]);
		expect(pinsOf(".oryn-doc-pins")).toEqual(["4"]);
		expect(container.querySelectorAll(".oryn-pin")).toHaveLength(3);
		expect(
			container.querySelector('[data-block="1"] .oryn-pin-answered'),
		).not.toBeNull();
		// 不在评论模式：块上没有点选按钮
		expect(screen.queryByRole("button", { name: "评论这一块" })).toBeNull();
	});
});
