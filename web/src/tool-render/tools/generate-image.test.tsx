/**
 * generate_image 卡片：输入段的提示词卡（主体在前，描述字段、修改项逐行），运行中按请求比例的占位框，
 * 结束后 details.images 并进画廊、读数给张数、provider · model 收在次要行；出错时错误块。
 */
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ToolView } from "../ToolView";

const PNG_1PX =
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";

const ARGS = {
	subject: "一根被剪断又接上的网线",
	style: "等距插画",
	aspect_ratio: "16:9",
	changes: ["线缆改成青碧色", 3],
};

describe("generate_image 卡片", () => {
	it("提示词卡：主体全文、描述字段与修改项逐行，形状不对的修改项标出", () => {
		const { container } = render(
			<ToolView name="generate_image" args={ARGS} defaultOpen />,
		);
		const input = container.querySelector(".tv-sec--in");
		expect(input?.querySelector(".tv-gi-subject")).toHaveTextContent(
			ARGS.subject,
		);
		expect(input).toHaveTextContent("风格等距插画");
		expect(input?.querySelectorAll(".tv-gi-changes li")).toHaveLength(2);
		expect(input).toHaveTextContent("[invalid change]");
	});

	it("运行中：按请求比例给占位框", () => {
		const { container } = render(
			<ToolView name="generate_image" args={ARGS} running defaultOpen />,
		);
		const pending = container.querySelector<HTMLElement>(".tv-gi-pending");
		expect(pending).not.toBeNull();
		expect(pending?.style.getPropertyValue("--tv-gi-aspect")).toBe("16 / 9");
	});

	it("结束：details.images 进画廊，读数给张数，provider · model 与保存路径在次要行", () => {
		const { container } = render(
			<ToolView
				name="generate_image"
				args={ARGS}
				result={{
					content: [{ type: "text", text: "Provider: openai" }],
					details: {
						provider: "openai",
						model: "gpt-image-2",
						imageCount: 1,
						imagePaths: ["/tmp/img-1.png"],
						images: [{ data: PNG_1PX, mimeType: "image/png" }],
					},
				}}
				defaultOpen
			/>,
		);
		expect(container.querySelector(".tv-meta")).toHaveTextContent("1 张");
		expect(container.querySelector(".tv-gi-gallery img.tv-img")).not.toBeNull();
		expect(container.querySelector(".tv-gi-foot")).toHaveTextContent(
			"openai · gpt-image-2",
		);
		expect(container.querySelector(".tv-gi-foot")).toHaveTextContent(
			"/tmp/img-1.png",
		);
		// 有图时不重复 Core 的文字回执
		expect(screen.queryByText("Provider: openai")).toBeNull();
	});

	it("出错：错误块，没有读数", () => {
		const { container } = render(
			<ToolView
				name="generate_image"
				args={ARGS}
				result={{
					content: [
						{
							type: "text",
							text: "No image model could run this request.\nTried: a, b",
						},
					],
					isError: true,
				}}
				defaultOpen
			/>,
		);
		expect(container.querySelector(".tv-errblock-head")).toHaveTextContent(
			"No image model could run this request.",
		);
		expect(container.querySelector(".tv-meta")).toBeEmptyDOMElement();
	});
});
