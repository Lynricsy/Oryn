import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Markdown } from "../markdown/Markdown";
import { AttachmentChips } from "../transcript/MessageView";
import { ImageLightbox } from "./ImageLightbox";
import { ImageViewer } from "./ImageViewer";

const IMG_A = "data:image/png;base64,iVBORw0KGgo=";
const IMG_B = "data:image/gif;base64,R0lGODlhAQABAAAAACw=";
const IMG_C =
	"data:image/webp;base64,UklGRh4AAABXRUJQVlA4TBEAAAAvAAAAAAfQ//73v/+BiOh/AAA=";

describe("ImageLightbox / ImageViewer", () => {
	it("单图模式下渲染图片、标题，支持键盘缩放、旋转、底纹切换与 Esc 关闭", () => {
		let closed = false;
		render(
			<ImageViewer
				src={IMG_A}
				alt="首页截图"
				caption="home.png"
				onClose={() => {
					closed = true;
				}}
			/>,
		);
		const dialog = screen.getByRole("dialog", { name: "home.png" });
		const img = dialog.querySelector("img") as HTMLImageElement;
		expect(img.getAttribute("src")).toBe(IMG_A);

		// 放大与旋转
		fireEvent.click(screen.getByRole("button", { name: "放大（+）" }));
		expect(img.style.transform).toContain("scale(1.5)");

		fireEvent.keyDown(window, { key: "r" });
		expect(img.style.transform).toContain("rotate(90deg)");

		fireEvent.click(screen.getByRole("button", { name: "水平翻转" }));
		expect(img.style.transform).toContain("scale(-1.5, 1.5)");

		// 重置视图
		fireEvent.keyDown(window, { key: "0" });
		expect(img.style.transform).toContain("rotate(0deg)");
		expect(img.style.transform).toContain("scale(1)");

		// 循环切换背景底纹（深色 -> 透明棋盘格 -> 浅色）
		fireEvent.keyDown(window, { key: "b" });
		expect(dialog.className).toContain("lightbox-checker");
		fireEvent.keyDown(window, { key: "b" });
		expect(dialog.className).toContain("bg-[#f2f4f8]");

		// Esc 关闭
		fireEvent.keyDown(window, { key: "Escape" });
		expect(closed).toBe(true);
	});

	it("多图画廊支持箭头、键盘与缩略图条切换，并更新计数器", () => {
		render(
			<ImageLightbox
				images={[
					{ src: IMG_A, alt: "第一张", caption: "a.png" },
					{ src: IMG_B, alt: "第二张", caption: "b.gif" },
					{ src: IMG_C, alt: "第三张", caption: "c.webp" },
				]}
				initialIndex={0}
				onClose={() => {}}
			/>,
		);
		expect(screen.getByText("1 / 3")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "上一张（←）" })).toBeDisabled();

		// 点击下一张
		fireEvent.click(screen.getByRole("button", { name: "下一张（→）" }));
		expect(screen.getByText("2 / 3")).toBeInTheDocument();
		expect(screen.getByRole("dialog", { name: "b.gif" })).toBeInTheDocument();

		// 键盘右箭头到末尾
		fireEvent.keyDown(window, { key: "ArrowRight" });
		expect(screen.getByText("3 / 3")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "下一张（→）" })).toBeDisabled();

		// 点击第一张缩略图跳回
		const thumbs = screen.getAllByRole("option");
		expect(thumbs).toHaveLength(3);
		fireEvent.click(thumbs[0] as HTMLElement);
		expect(screen.getByText("1 / 3")).toBeInTheDocument();
	});

	it("Markdown 正文点击普通图片打开灯箱，点击链接包裹的徽章图不拦截", () => {
		render(
			<Markdown text="![架构图](https://x.test/arch.png)\n\n[![CI](https://x.test/ci.svg)](https://example.com)" />,
		);
		// 点击链接里的徽章图：不打开灯箱
		fireEvent.click(screen.getByRole("img", { name: "CI" }));
		expect(screen.queryByRole("dialog")).toBeNull();

		// 点击普通图片：打开灯箱
		fireEvent.click(screen.getByRole("img", { name: "架构图" }));
		const dialog = screen.getByRole("dialog", { name: "架构图" });
		expect(dialog.querySelector("img")?.getAttribute("src")).toBe(
			"https://x.test/arch.png",
		);
	});

	it("消息附件芯片：图片附件显示小预览并点击打开灯箱，非图片附件维持类型图标与下载链接", () => {
		const { container } = render(
			<AttachmentChips
				attachments={[
					{
						id: "att_01JCZ8M3W4X5Y6Z7A8B9C0D1E1",
						name: "shot.png",
						path: "/tmp/shot.png",
						mime: "image/png",
						size: 4096,
					},
					{
						id: "att_01JCZ8M3W4X5Y6Z7A8B9C0D1E2",
						name: "notes.pdf",
						path: "/tmp/notes.pdf",
						mime: "application/pdf",
						size: 2048,
					},
				]}
			/>,
		);
		const thumb = screen.getByRole("img", { name: "shot.png" });
		expect(thumb).toHaveAttribute(
			"src",
			"/api/v1/attachments/att_01JCZ8M3W4X5Y6Z7A8B9C0D1E1/content?inline=true",
		);
		expect(container.querySelector('svg[data-file-icon="image"]')).toBeNull();
		expect(
			screen.getByText("notes.pdf").closest("a")?.getAttribute("href"),
		).toBe("/api/v1/attachments/att_01JCZ8M3W4X5Y6Z7A8B9C0D1E2/content");

		fireEvent.click(screen.getByText("shot.png"));
		const dialog = screen.getByRole("dialog", { name: "shot.png" });
		expect(dialog.querySelector("img")?.getAttribute("src")).toBe(
			"/api/v1/attachments/att_01JCZ8M3W4X5Y6Z7A8B9C0D1E1/content?inline=true",
		);
	});
});
