import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Markdown } from "./Markdown";

describe("Markdown", () => {
	it("单个换行渲染为换行（与 omp 自带 Web 端的 breaks: true 一致）", () => {
		const { container } = render(<Markdown text={"1\n2\n3"} />);
		expect(container.querySelectorAll("br")).toHaveLength(2);
		expect(container.querySelectorAll("p")).toHaveLength(1);
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
});
