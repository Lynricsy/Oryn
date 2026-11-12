import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Markdown } from "./Markdown";

describe("Markdown", () => {
	it("单个换行渲染为换行（与 omp 自带 Web 端的 breaks: true 一致）", () => {
		const { container } = render(<Markdown text={"1\n2\n3"} />);
		expect(container.querySelectorAll("br")).toHaveLength(2);
		expect(container.querySelectorAll("p")).toHaveLength(1);
	});
});
