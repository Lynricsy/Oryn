import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { Markdown, type RelativeUrls } from "./Markdown";

describe("Markdown", () => {
	it("单个换行渲染为换行（与 omp 自带 Web 端的 breaks: true 一致）", () => {
		const { container } = render(<Markdown text={"1\n2\n3"} />);
		expect(container.querySelectorAll("br")).toHaveLength(2);
		expect(container.querySelectorAll("p")).toHaveLength(1);
	});

	it("文档模式按 GitHub 渲染：内嵌 HTML 清洗后保留对齐与尺寸、脚本被剥掉，徽章换行不断行", () => {
		const { container } = render(
			<Markdown
				document
				text={
					'<div align="center">\n\n<img src="https://x.test/logo.svg" width="88" onerror="alert(1)">\n\n# T\n\n[![CI](https://x.test/ci.svg)](https://x.test)\n[![Go](https://x.test/go.svg)](https://x.test)\n\n<script>alert(1)</script>\n\n</div>'
				}
			/>,
		);
		const div = container.querySelector(".md-doc > div");
		expect(div?.getAttribute("align")).toBe("center");
		const [logo, ...badges] = container.querySelectorAll("img");
		expect(logo?.getAttribute("width")).toBe("88");
		expect(logo?.hasAttribute("onerror")).toBe(false);
		expect(badges.map((b) => b.alt)).toEqual(["CI", "Go"]);
		expect(container.querySelector("br")).toBeNull();
		expect(container.querySelector("script")).toBeNull();
	});

	it("表格外包圆角容器，有序列表的 start 落到编号计数器上", () => {
		const { container } = render(
			<Markdown text={"| a | b |\n| - | - |\n| 1 | 2 |\n\n3. x\n4. y"} />,
		);
		expect(container.querySelector(".md-table > table")).not.toBeNull();
		const ol = container.querySelector("ol");
		expect(ol?.style.counterReset).toBe("md-ol 2");
		expect(ol?.querySelectorAll("li")).toHaveLength(2);
	});

	it("表格复制：Markdown 切原文（去掉列表缩进、保留行内格式），CSV 按 RFC 4180 加引号、<br> 记作换行", async () => {
		const writeText = vi.fn(async (_: string) => {});
		Object.defineProperty(navigator, "clipboard", {
			configurable: true,
			value: { writeText },
		});
		const table = '| 名 | 说明 |\n   | - | - |\n   | **a** | x, "y"<br>z |';
		render(<Markdown document text={`1. 列表\n\n   ${table}`} />);
		fireEvent.click(
			screen.getByRole("button", {
				name: i18n.t("markdownTable.copyMarkdown"),
			}),
		);
		await waitFor(() =>
			expect(writeText).toHaveBeenLastCalledWith(
				'| 名 | 说明 |\n| - | - |\n| **a** | x, "y"<br>z |',
			),
		);
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("markdownTable.copyCsv") }),
		);
		await waitFor(() =>
			expect(writeText).toHaveBeenLastCalledWith('名,说明\na,"x, ""y""\nz"'),
		);
		Reflect.deleteProperty(navigator, "clipboard");
	});

	it("文档首行的 YAML frontmatter 渲染成字段网格，不变成 setext 标题", () => {
		const { container } = render(
			<Markdown
				text={
					"---\nname: review\ndescription: Review a PR\nallowed-tools: [Read, Grep]\n---\n\n# Body"
				}
			/>,
		);
		const dl = container.querySelector("dl[data-frontmatter]");
		expect(
			[...(dl?.querySelectorAll("dt") ?? [])].map((d) => d.textContent),
		).toEqual(["name", "description", "allowed-tools"]);
		expect(
			[...(dl?.querySelectorAll("dd code") ?? [])].map((c) => c.textContent),
		).toEqual(["Read", "Grep"]);
		expect(container.querySelectorAll("h1, h2")).toHaveLength(1);
		expect(container.querySelector("h1")).toHaveTextContent("Body");
	});

	it("正文里的 --- 与解析不了的开头分隔线照常是 Markdown", () => {
		const { container } = render(
			<Markdown text={"Intro\n\n---\n\nname: x\n\n---\n\nend"} />,
		);
		expect(container.querySelector("dl[data-frontmatter]")).toBeNull();
		expect(container.querySelectorAll("hr")).toHaveLength(2);
		const nested = render(
			<Markdown text={"---\nmeta:\n  author: x\n---\n\nbody"} />,
		);
		expect(nested.container.querySelector("dl[data-frontmatter]")).toBeNull();
	});

	it("frontmatter 里的非法码点转义不打断渲染：整段回落成原文 Markdown", () => {
		const { container } = render(
			<Markdown text={'---\nname: "\\U00110000"\n---\n\nbody text'} />,
		);
		expect(container.querySelector("dl[data-frontmatter]")).toBeNull();
		expect(container).toHaveTextContent("body text");
	});

	const REL: RelativeUrls = {
		link: (h) => `/x/${h}`,
		image: (s) => `/img/${s}`,
	};
	const REL_TEXT =
		"[a](docs/a.md) [e](https://e.com) [h](#top)\n\n![i](p.png) ![w](https://e.com/w.png)";

	it("relativeTo：相对链接走站内 Link（不新开窗口），相对图片换地址，绝对地址与锚点不变", () => {
		const { container } = render(
			<MemoryRouter>
				<Markdown text={REL_TEXT} relativeTo={REL} />
			</MemoryRouter>,
		);
		const [rel, abs, hash] = container.querySelectorAll("a");
		expect(rel?.getAttribute("href")).toBe("/x/docs/a.md");
		expect(rel?.hasAttribute("target")).toBe(false);
		expect(abs?.getAttribute("href")).toBe("https://e.com");
		expect(abs?.getAttribute("target")).toBe("_blank");
		expect(hash?.getAttribute("target")).toBe("_blank");
		const [img, ext] = container.querySelectorAll("img");
		expect(img?.getAttribute("src")).toBe("/img/p.png");
		expect(ext?.getAttribute("src")).toBe("https://e.com/w.png");
	});

	it("relativeTo 返回 null 或不给 relativeTo 时相对地址原样输出", () => {
		const none: RelativeUrls = { link: () => null, image: () => null };
		for (const relativeTo of [none, undefined]) {
			const { container, unmount } = render(
				<Markdown text={REL_TEXT} relativeTo={relativeTo} />,
			);
			const a = container.querySelector("a");
			expect(a?.getAttribute("href")).toBe("docs/a.md");
			expect(a?.getAttribute("target")).toBe("_blank");
			expect(container.querySelector("img")?.getAttribute("src")).toBe("p.png");
			unmount();
		}
	});
});
