import { act, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { OfficeViewProps } from "../../../src/lib/artifacts/office/types";
import { frameAssets, mountOffice } from "./office";

function page(config: Record<string, unknown>): void {
	document.body.innerHTML = `<script type="application/json" id="oryn-viewer">${JSON.stringify(config)}</script><div id="root"></div>`;
}

const base = {
	type: "pdf",
	viewer: "pdf",
	format: 1,
	version: 2,
	head: 2,
	entry: "document.pdf",
	size: 9,
	title: "季度报告",
	locale: "zh-CN",
	root: "/",
};

afterEach(() => {
	vi.unstubAllGlobals();
	document.body.innerHTML = "";
});

function Probe({ bytes, view, assets }: OfficeViewProps) {
	return (
		<p>{`view=${view} bytes=${bytes.byteLength} ooxml=${assets.ooxml}`}</p>
	);
}

describe("mountOffice", () => {
	it("取入口文件的字节交给视图；独占窗口时下载链接指向 attachment 变体", async () => {
		const fetch = vi.fn(
			async (_url: URL | string) => new Response(new Uint8Array(9)),
		);
		vi.stubGlobal("fetch", fetch);
		page({ ...base, standalone: true });
		await act(async () => mountOffice(Probe));
		expect(
			await screen.findByText(
				`view=full bytes=9 ooxml=${new URL("/_oryn/viewer/ooxml/", location.href).href}`,
			),
		).toBeInTheDocument();
		expect(String(fetch.mock.calls[0]?.[0])).toBe(
			new URL("document.pdf", location.href).href,
		);
		const link = screen.getByRole("link", { name: "下载原件" });
		expect(link.getAttribute("href")).toBe("document.pdf?download=1");
		expect(link.getAttribute("download")).toBe("季度报告.pdf");
	});

	it("内嵌帧没有下载链接；超过预览上限时不取文件", async () => {
		const fetch = vi.fn(
			async (_url: URL | string) => new Response(new Uint8Array(1)),
		);
		vi.stubGlobal("fetch", fetch);
		page({ ...base, size: 65 * 1024 * 1024 });
		await act(async () => mountOffice(Probe));
		expect(await screen.findByText("文件过大，无法预览")).toBeInTheDocument();
		expect(fetch).not.toHaveBeenCalled();
		expect(screen.queryByRole("link")).toBeNull();
	});
});

describe("frameAssets", () => {
	it("资源目录是帧根下的绝对地址", () => {
		expect(
			frameAssets("/_af/tok/", "http://h.test/_af/tok/_oryn/v/1/"),
		).toEqual({
			ooxml: "http://h.test/_af/tok/_oryn/viewer/ooxml/",
			pdfjs: "http://h.test/_af/tok/_oryn/viewer/pdfjs/",
		});
	});
});
