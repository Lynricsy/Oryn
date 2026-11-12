import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { imageSrc } from "../lib/image-src";
import { Output, ResultImages } from "./parts";

const PNG = "iVBORw0KGgo=";
const SHA = "a".repeat(64);

describe("工具结果图片（H1 / W13）", () => {
	afterEach(() => vi.restoreAllMocks());

	it("只接受栅格图类型；SVG / HTML 等活动文档类型不生成地址", () => {
		expect(imageSrc(PNG, "image/png")).toBe(`data:image/png;base64,${PNG}`);
		expect(imageSrc(PNG, "IMAGE/JPEG; charset=x")).toBe(
			`data:image/jpeg;base64,${PNG}`,
		);
		expect(imageSrc(`blob:sha256:${SHA}`, "image/webp")).toBe(
			`/api/v1/blobs/${SHA}`,
		);
		for (const t of ["image/svg+xml", "text/html", "application/xhtml+xml", ""])
			expect(imageSrc(PNG, t)).toBeNull();
		expect(imageSrc(`blob:sha256:${SHA}`, "image/svg+xml")).toBeNull();
		// data 里夹带 URL 语法时拒绝
		expect(imageSrc('x";alert(1)//', "image/png")).toBeNull();
	});

	it("SVG 结果图不渲染为可点击图片，也不会打开新窗口", () => {
		const open = vi.spyOn(window, "open").mockReturnValue(null);
		render(
			<ResultImages
				result={{
					content: [
						{ type: "image", data: btoa("<svg/>"), mimeType: "image/svg+xml" },
					],
				}}
			/>,
		);
		expect(screen.queryByRole("img")).toBeNull();
		expect(screen.queryByRole("button")).toBeNull();
		expect(screen.getByText(/image\/svg\+xml/)).toBeInTheDocument();
		expect(open).not.toHaveBeenCalled();
	});

	it("点击 blob 引用的缩略图在应用内查看器里用 <img> 打开", () => {
		const open = vi.spyOn(window, "open").mockReturnValue(null);
		const create = vi.spyOn(URL, "createObjectURL");
		render(
			<ResultImages
				result={{
					content: [
						{
							type: "image",
							data: `blob:sha256:${SHA}`,
							mimeType: "image/png",
						},
					],
				}}
			/>,
		);
		fireEvent.click(screen.getByRole("button", { name: "打开图片 1" }));
		const dialog = screen.getByRole("dialog");
		const img = dialog.querySelector("img");
		expect(img?.getAttribute("src")).toBe(`/api/v1/blobs/${SHA}`);
		expect(open).not.toHaveBeenCalled();
		expect(create).not.toHaveBeenCalled();
	});
});

describe("工具渲染器界面文案走 i18n（W15）", () => {
	it("展开 / 收起按钮按当前语言显示并带行数", () => {
		const text = Array.from({ length: 20 }, (_, i) => `line ${i}`).join("\n");
		render(<Output text={text} maxLines={5} />);
		const btn = screen.getByRole("button", { name: "⋯ 还有 15 行" });
		fireEvent.click(btn);
		expect(screen.getByRole("button", { name: "收起" })).toBeInTheDocument();
	});
});
