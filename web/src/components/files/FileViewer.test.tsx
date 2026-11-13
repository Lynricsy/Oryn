/** 图片预览：SVG 以 `data:` URL 显示（不透明源），位图用 object URL；超过 32MiB 的图片不读取内容。 */
import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import {
	type FileEntry,
	type FileSource,
	MAX_IMAGE_PREVIEW_BYTES,
} from "../../lib/file-source";
import { FileViewer } from "./FileViewer";

function imageSource(entry: Partial<FileEntry>, bytes: string) {
	const full: FileEntry = {
		path: "/r/img",
		name: "img",
		type: "file",
		size: bytes.length,
		mtime: 0,
		mode: 420,
		...entry,
	};
	const readBlob = vi.fn(async () => new Blob([bytes]));
	const src = {
		id: "mem",
		caps: { mkdir: false, rename: false, upload: false, download: true },
		stat: async () => full,
		readBlob,
	} as unknown as FileSource;
	return { src, readBlob, entry: full };
}

beforeEach(() => {
	vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: () => "blob:x" }));
});
afterEach(() => vi.unstubAllGlobals());

describe("FileViewer 图片", () => {
	it("SVG 用 data: URL", async () => {
		const svg =
			'<svg xmlns="http://www.w3.org/2000/svg"><script>1</script></svg>';
		const { src } = imageSource({ name: "a.svg", mime: "image/svg+xml" }, svg);
		render(<FileViewer source={src} path="/r/a.svg" />);
		const img = await screen.findByRole("img", { name: "a.svg" });
		expect(img.getAttribute("src")).toBe(
			`data:image/svg+xml;base64,${btoa(svg)}`,
		);
	});

	it("位图用 object URL", async () => {
		const { src } = imageSource({ name: "a.png", mime: "image/png" }, "png");
		render(<FileViewer source={src} path="/r/a.png" />);
		const img = await screen.findByRole("img", { name: "a.png" });
		expect(img.getAttribute("src")).toBe("blob:x");
	});

	it("超过 32MiB 的图片只显示信息，不读取内容", async () => {
		const { src, readBlob } = imageSource(
			{
				name: "huge.png",
				mime: "image/png",
				size: MAX_IMAGE_PREVIEW_BYTES + 1,
			},
			"",
		);
		render(
			<FileViewer source={src} path="/r/huge.png" onDownload={() => {}} />,
		);
		expect(
			await screen.findAllByRole("button", {
				name: i18n.t("files.actions.download"),
			}),
		).not.toHaveLength(0);
		expect(screen.queryByRole("img", { name: "huge.png" })).toBeNull();
		expect(readBlob).not.toHaveBeenCalled();
	});
});
