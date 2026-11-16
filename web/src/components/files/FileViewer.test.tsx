/**
 * 预览分流与不可信内容边界：SVG 以 `data:` URL 显示（不透明源），位图用 object URL；超过上限的文件不读取内容；
 * HTML 只进不执行脚本的惰性 iframe，并在文档最前注入禁止网络请求的 CSP。
 */
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import {
	type FileEntry,
	type FileSource,
	MAX_PREVIEW_BLOB_BYTES,
} from "../../lib/file-source";
import { FileViewer } from "./FileViewer";
import { lockdownHtml } from "./preview/InertFrame";

function memSource(entry: Partial<FileEntry>, content: string) {
	const full: FileEntry = {
		path: "/r/f",
		name: "f",
		type: "file",
		size: content.length,
		mtime: 0,
		mode: 420,
		...entry,
	};
	const readBlob = vi.fn(async () => new Blob([content]));
	const readText = vi.fn(async () => ({ text: content, truncated: false }));
	const src = {
		id: "mem",
		caps: { mkdir: false, rename: false, upload: false, download: true },
		stat: async () => full,
		readBlob,
		readText,
		downloadUrl: (p: string) => `/dl?path=${p}`,
	} as unknown as FileSource;
	return { src, readBlob, readText };
}

beforeEach(() => {
	vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: () => "blob:x" }));
});
afterEach(() => vi.unstubAllGlobals());

describe("FileViewer 图片", () => {
	it("SVG 的预览用 data: URL，可切到源码", async () => {
		const svg =
			'<svg xmlns="http://www.w3.org/2000/svg"><script>1</script></svg>';
		const { src } = memSource(
			{ name: "a.svg", mime: "image/svg+xml", text: true },
			svg,
		);
		render(<FileViewer source={src} path="/r/a.svg" />);
		const img = await screen.findByRole("img", { name: "a.svg" });
		expect(img.getAttribute("src")).toBe(
			`data:image/svg+xml;base64,${btoa(svg)}`,
		);
		expect(
			screen.getByRole("button", { name: i18n.t("files.viewer.source") }),
		).toBeTruthy();
	});

	it("位图用 object URL", async () => {
		const { src } = memSource({ name: "a.png", mime: "image/png" }, "png");
		render(<FileViewer source={src} path="/r/a.png" />);
		const img = await screen.findByRole("img", { name: "a.png" });
		expect(img.getAttribute("src")).toBe("blob:x");
	});

	it("超过整份预览上限的文件只显示信息，不读取内容", async () => {
		const { src, readBlob } = memSource(
			{ name: "huge.pdf", size: MAX_PREVIEW_BLOB_BYTES + 1 },
			"",
		);
		render(
			<FileViewer source={src} path="/r/huge.pdf" onDownload={() => {}} />,
		);
		expect(
			await screen.findAllByRole("button", {
				name: i18n.t("files.actions.download"),
			}),
		).not.toHaveLength(0);
		expect(readBlob).not.toHaveBeenCalled();
	});
});

describe("FileViewer HTML 与音视频", () => {
	it("HTML 预览进不执行脚本的 iframe，源码视图可编辑", async () => {
		const html = "<p onclick='x()'>hi</p><script>alert(1)</script>";
		const { src } = memSource(
			{ name: "a.html", mime: "text/html", text: true },
			html,
		);
		render(<FileViewer source={src} path="/r/a.html" />);
		const frame = await screen.findByTitle("a.html");
		expect(frame.getAttribute("sandbox")).toBe("allow-same-origin");
		await vi.waitFor(() =>
			expect((frame as HTMLIFrameElement).srcdoc).toBe(lockdownHtml(html)),
		);
		await userEvent.click(
			screen.getByRole("button", { name: i18n.t("files.viewer.source") }),
		);
		// 切到源码后预览帧卸载（编辑器懒加载期间 Suspense 会先把它隐藏）
		await vi.waitFor(() => expect(screen.queryByTitle("a.html")).toBeNull(), {
			timeout: 5000,
		});
	});

	it("视频直接用下载地址流式播放，不整份读入", async () => {
		const { src, readBlob } = memSource({ name: "clip.mp4" }, "");
		const { container } = render(
			<FileViewer source={src} path="/r/clip.mp4" />,
		);
		await vi.waitFor(() =>
			expect(container.querySelector("video")?.getAttribute("src")).toBe(
				"/dl?path=/r/clip.mp4",
			),
		);
		expect(readBlob).not.toHaveBeenCalled();
	});
});

describe("FileViewer 渲染视图", () => {
	it("超过编辑上限的 Markdown：整份读取较慢时仍等到结果并渲染", async () => {
		const { src, readBlob } = memSource(
			{ name: "big.md", size: 3 * 1024 * 1024, text: true },
			"# 截断的开头",
		);
		readBlob.mockImplementation(
			() =>
				new Promise((resolve) =>
					setTimeout(() => resolve(new Blob(["# 完整标题\n\n正文"])), 50),
				),
		);
		render(<FileViewer source={src} path="/r/big.md" />);
		expect(
			await screen.findByRole("heading", { name: "完整标题" }),
		).toBeTruthy();
		expect(readBlob).toHaveBeenCalledTimes(1);
	});

	it("结构损坏的 Notebook 显示无法解析，返回按钮仍可用", async () => {
		const { src } = memSource(
			{ name: "a.ipynb", text: true },
			'{"cells":[null]}',
		);
		const onBack = vi.fn();
		render(<FileViewer source={src} path="/r/a.ipynb" onBack={onBack} />);
		expect(await screen.findByText(/cell 1 is not an object/)).toBeTruthy();
		await userEvent.click(
			screen.getByRole("button", { name: i18n.t("files.viewer.back") }),
		);
		expect(onBack).toHaveBeenCalled();
	});

	it("字段形状不对的 Notebook 单元按缺省渲染，不抛错", async () => {
		const nb = {
			cells: [
				{
					cell_type: "code",
					source: 5,
					outputs: [
						{ output_type: "error", traceback: "x", ename: "E", evalue: "v" },
						{ output_type: "stream", text: { a: 1 } },
						null,
					],
				},
				{ cell_type: "markdown", source: ["## 小节"] },
			],
			metadata: { kernelspec: "python" },
		};
		const { src } = memSource(
			{ name: "b.ipynb", text: true },
			JSON.stringify(nb),
		);
		render(<FileViewer source={src} path="/r/b.ipynb" />);
		expect(await screen.findByRole("heading", { name: "小节" })).toBeTruthy();
		expect(screen.getByText("E: v")).toBeTruthy();
	});
});

describe("lockdownHtml", () => {
	it("CSP meta 放在 doctype 之后、任何内容之前", () => {
		const out = lockdownHtml("<!DOCTYPE html><html><img src=/x>");
		expect(
			out.startsWith(
				'<!DOCTYPE html><meta http-equiv="Content-Security-Policy"',
			),
		).toBe(true);
		expect(out).toContain("default-src 'none'");
		expect(lockdownHtml("<b>x</b>").startsWith("<meta")).toBe(true);
	});
});
