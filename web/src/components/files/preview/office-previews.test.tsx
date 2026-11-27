/**
 * 文件面板的 Office 预览是 lib/artifacts/office 视图的薄包装：读出字节、普通视图、不点选、主站资源目录、应用语言文案；
 * 表格只有 xlsx / xlsm 交给 XlsxView，其余格式仍走 SheetJS。
 */
import { render, screen } from "@testing-library/react";
import { Suspense } from "react";
import { describe, expect, it, vi } from "vitest";
import type { OfficeViewProps } from "../../../lib/artifacts/office/types";
import DocxPreview from "./DocxPreview";
import PdfPreview from "./PdfPreview";
import SheetPreview from "./SheetPreview";
import SlidesPreview from "./SlidesPreview";

const { probe } = vi.hoisted(() => ({
	probe:
		(label: string) =>
		({ bytes, view, assets, strings, pickable }: OfficeViewProps) => (
			<p>
				{[
					label,
					`bytes=${bytes.byteLength}`,
					`view=${view}`,
					`pickable=${pickable}`,
					`ooxml=${assets.ooxml}`,
					`pdfjs=${assets.pdfjs}`,
					strings.rendered(2),
					strings.counter(1, 3),
				].join(" ")}
			</p>
		),
}));

vi.mock("../../../lib/artifacts/office/DocxView", () => ({
	DocxView: probe("docx"),
}));
vi.mock("../../../lib/artifacts/office/PptxView", () => ({
	PptxView: probe("pptx"),
}));
vi.mock("../../../lib/artifacts/office/PdfView", () => ({
	PdfView: probe("pdf"),
}));
vi.mock("../../../lib/artifacts/office/XlsxView", () => ({
	XlsxView: probe("xlsx"),
}));
vi.mock("./SheetJsPreview", () => ({
	default: ({ name }: { name: string }) => <p>{`sheetjs ${name}`}</p>,
}));

const root = new URL(import.meta.env.BASE_URL, window.location.origin).href;
const blob = new Blob([new Uint8Array(5)]);

describe("Office 文件预览", () => {
	it.each([
		["docx", DocxPreview, "a.docx"],
		["pptx", SlidesPreview, "a.pptx"],
		["pdf", PdfPreview, "a.pdf"],
		["xlsx", SheetPreview, "a.xlsx"],
		["xlsx", SheetPreview, "B.XLSM"],
	] as const)(
		"%s 视图拿到 %s 的字节、主站资源与中文文案",
		async (label, Preview, name) => {
			render(<Preview blob={blob} name={name} />);
			expect(
				await screen.findByText(
					`${label} bytes=5 view=full pickable=false ooxml=${root}office/ooxml/ pdfjs=${root}pdfjs/ 已渲染 2 页 1 / 3`,
				),
			).toBeInTheDocument();
		},
	);

	it("非 OOXML 表格走 SheetJS", async () => {
		render(
			<Suspense fallback={null}>
				<SheetPreview blob={blob} name="old.ods" />
			</Suspense>,
		);
		expect(await screen.findByText("sheetjs old.ods")).toBeInTheDocument();
	});
});
