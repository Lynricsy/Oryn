import { describe, expect, it } from "vitest";
import { parseDelimited } from "./csv";
import { extOf, previewKind } from "./preview-kind";

describe("previewKind", () => {
	it.each([
		["report.PDF", { kind: "blob", viewer: "pdf" }],
		["a.docx", { kind: "blob", viewer: "docx" }],
		["a.xls", { kind: "blob", viewer: "sheet" }],
		["deck.pptx", { kind: "blob", viewer: "slides" }],
		["IMG_0001.HEIC", { kind: "blob", viewer: "heic" }],
		["scan.tif", { kind: "blob", viewer: "tiff" }],
		["art.psd", { kind: "blob", viewer: "psd" }],
		["logo.png", { kind: "blob", viewer: "image" }],
		["src.tar.gz", { kind: "blob", viewer: "archive" }],
		["model.glb", { kind: "blob", viewer: "model" }],
		["font.woff2", { kind: "blob", viewer: "font" }],
		["clip.mov", { kind: "media", media: "video" }],
		["song.flac", { kind: "media", media: "audio" }],
	])("%s 按扩展名分流（与 text 嗅探无关）", (name, want) => {
		expect(previewKind(name, "application/octet-stream", false)).toEqual(want);
		expect(previewKind(name, undefined, true)).toEqual(want);
	});

	it("可渲染的文本格式要求内容是 UTF-8 文本", () => {
		expect(previewKind("a.csv", "text/csv", true)).toEqual({
			kind: "text",
			render: "table",
		});
		expect(previewKind("a.ipynb", undefined, true)).toEqual({
			kind: "text",
			render: "notebook",
		});
		expect(previewKind("utf16.csv", "text/csv", false)).toEqual({
			kind: "binary",
		});
	});

	it("无扩展名时按 MIME 与 text 嗅探", () => {
		expect(previewKind("Makefile", "text/plain", true)).toEqual({
			kind: "text",
		});
		expect(previewKind("blob", "image/png", false)).toEqual({
			kind: "blob",
			viewer: "image",
		});
		expect(previewKind("blob", "video/mp4", false)).toEqual({
			kind: "media",
			media: "video",
		});
		expect(previewKind("data.bin", "application/octet-stream", false)).toEqual({
			kind: "binary",
		});
	});

	it("原型上的属性名不会被当成扩展名表项", () => {
		expect(previewKind("x.constructor", undefined, false)).toEqual({
			kind: "binary",
		});
		expect(previewKind("x.__proto__", undefined, true)).toEqual({
			kind: "text",
		});
	});

	it("隐藏文件没有扩展名", () => {
		expect(extOf("/home/user/.bashrc")).toBe("");
		expect(extOf("/a.b/c")).toBe("");
	});
});

describe("parseDelimited", () => {
	it("引号内的分隔符、换行与转义引号原样保留", () => {
		const csv = '\ufeffname,note\r\n"a,b","line1\nline2"\r\nc,"say ""hi"""\n';
		expect(parseDelimited(csv, ",", 100)).toEqual({
			rows: [
				["name", "note"],
				["a,b", "line1\nline2"],
				["c", 'say "hi"'],
			],
			truncated: false,
		});
	});

	it("TSV、空字段与末尾无换行", () => {
		expect(parseDelimited("a\t\tc\n1\t2", "\t", 100).rows).toEqual([
			["a", "", "c"],
			["1", "2"],
		]);
	});

	it("超过行数上限时截断", () => {
		const r = parseDelimited("1\n2\n3\n", ",", 2);
		expect(r).toEqual({ rows: [["1"], ["2"]], truncated: true });
		expect(parseDelimited("1\n2\n", ",", 2).truncated).toBe(false);
	});
});
