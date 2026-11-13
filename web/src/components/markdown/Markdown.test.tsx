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
});
