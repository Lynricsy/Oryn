import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import {
	afterAll,
	beforeAll,
	beforeEach,
	describe,
	expect,
	it,
	vi,
} from "vitest";
import { DocxView } from "./DocxView";
import { MAX_PREVIEW_BYTES } from "./limits";
import { PdfView } from "./PdfView";
import { PptxView } from "./PptxView";
import type { OfficeAssets, OfficeStrings } from "./types";
import { XlsxView } from "./XlsxView";

// 库与 pdf.js 都换成替身：组件测的是状态流转（过大、失败、渲染完成），不是排版
const lib = vi.hoisted(() => {
	const fail = { error: null as Error | null };
	const docx = {
		pageCount: 2,
		pageSize: () => ({ widthPt: 595, heightPt: 842 }),
		renderPage: async () => {},
		// 第 1 页的文字行片段（CSS px，按 100% 宽度 793.33px 排版）
		collectPageRuns: vi.fn(async () => [
			{ x: 0, y: 0, w: 10, h: 10, text: "页眉", paragraphId: "p0" },
			{ x: 150, y: 550, w: 40, h: 20, text: "季度", paragraphId: "p1" },
			{ x: 190, y: 550, w: 40, h: 20, text: "报告", paragraphId: "p1" },
		]),
		getElementContextAt: vi.fn(async () => null),
		destroy: () => {},
	};
	const pptx = {
		slideCount: 3,
		slideWidth: 12192000,
		slideHeight: 6858000,
		renderSlide: async () => {},
		toMarkdown: vi.fn(
			async () =>
				"# 季度经营回顾 (slide 1)\n\n---\n\n# 本季\\*要点 (slide 2)\n\n• 营收\n",
		),
		getElementContextAt: vi.fn(async () => ({ text: "营收  同比\n增长" })),
		destroy: () => {},
	};
	class XlsxViewer {
		sheetCount = 2;
		load = async () => {};
		destroy() {}
	}
	return {
		docx,
		pptx,
		XlsxViewer,
		docxLoad: vi.fn(async (_: ArrayBuffer) => {
			if (fail.error) throw fail.error;
			return docx;
		}),
		pptxLoad: vi.fn(async (_: ArrayBuffer) => {
			if (fail.error) throw fail.error;
			return pptx;
		}),
		fail,
	};
});

vi.mock("./ooxml", () => ({
	loadDocx: vi.fn(async () => ({ DocxDocument: { load: lib.docxLoad } })),
	loadPptx: vi.fn(async () => ({ PptxPresentation: { load: lib.pptxLoad } })),
	loadXlsx: vi.fn(async () => {
		if (lib.fail.error) throw lib.fail.error;
		return { XlsxViewer: lib.XlsxViewer, XlsxSheetViewer: lib.XlsxViewer };
	}),
}));

const pdf = vi.hoisted(() => {
	const page = {
		userUnit: 1,
		getViewport: ({ scale }: { scale: number }) => ({
			width: 612 * scale,
			height: 792 * scale,
		}),
		render: () => ({ promise: Promise.resolve(), cancel() {} }),
		streamTextContent: () => ({}),
	};
	const doc = { numPages: 2, getPage: async () => page };
	return {
		doc,
		fail: { error: null as Error | null },
	};
});

vi.mock("pdfjs-dist/legacy/build/pdf.mjs", () => ({
	GlobalWorkerOptions: {},
	PixelsPerInch: { PDF_TO_CSS_UNITS: 96 / 72 },
	VerbosityLevel: { ERRORS: 0 },
	getDocument: () => ({
		promise: pdf.fail.error
			? Promise.reject(pdf.fail.error)
			: Promise.resolve(pdf.doc),
		destroy: async () => {},
	}),
	TextLayer: class {
		render() {
			return Promise.resolve();
		}
		cancel() {}
		update() {}
	},
}));

const strings: OfficeStrings = {
	loading: "正在加载…",
	tooLarge: "文件过大，无法预览",
	failed: (r) => `无法显示这个文件：${r}`,
	rendered: (n) => `已渲染 ${n} 页`,
	pageFailed: (n) => `第 ${n} 页无法显示`,
	tooManyPages: "页数过多，无法打印",
	sheetNoPrint: "表格不支持打印视图",
	pageLabel: (n) => `第 ${n} 页`,
	slideLabel: (n) => `第 ${n} 张`,
	pageNumber: "页码",
	zoomIn: "放大",
	zoomOut: "缩小",
	fitWidth: "适应宽度",
	present: "演示",
	exitHint: "按 Esc 退出",
	prev: "上一张",
	next: "下一张",
	counter: (n, t) => `${n} / ${t}`,
};

const assets: OfficeAssets = {
	ooxml: "https://x.test/office/ooxml/",
	pdfjs: "https://x.test/pdfjs/",
};

/** 超过预览上限的「文件」：组件只看 byteLength，不去读。 */
const huge = { byteLength: MAX_PREVIEW_BYTES + 1 } as ArrayBuffer;
const small = new Uint8Array([80, 75, 3, 4]).buffer;

const VIEWS = [
	["DocxView", DocxView, 2],
	["PptxView", PptxView, 3],
	["XlsxView", XlsxView, 2],
	["PdfView", PdfView, 2],
] as const;

// jsdom 不排版：给容器一个宽度，分页视图才会开始画
const clientWidth = Object.getOwnPropertyDescriptor(
	HTMLElement.prototype,
	"clientWidth",
);
beforeAll(() => {
	Object.defineProperty(HTMLElement.prototype, "clientWidth", {
		configurable: true,
		get: () => 800,
	});
});
afterAll(() => {
	if (clientWidth)
		Object.defineProperty(HTMLElement.prototype, "clientWidth", clientWidth);
});

beforeEach(() => {
	lib.docxLoad.mockClear();
	lib.pptxLoad.mockClear();
	lib.fail.error = null;
	pdf.fail.error = null;
});

describe.each(VIEWS)("%s", (_, View, pages) => {
	it("超过预览上限时显示过大提示、不解析", async () => {
		const onReady = vi.fn();
		render(
			<View
				bytes={huge}
				view="full"
				assets={assets}
				strings={strings}
				onReady={onReady}
			/>,
		);
		expect(await screen.findByText("文件过大，无法预览")).toBeInTheDocument();
		expect(lib.docxLoad).not.toHaveBeenCalled();
		expect(lib.pptxLoad).not.toHaveBeenCalled();
		expect(onReady).not.toHaveBeenCalled();
	});

	it("渲染完成后写「已渲染 N 页」并回调 onReady", async () => {
		const onReady = vi.fn();
		render(
			<View
				bytes={small}
				view="full"
				assets={assets}
				strings={strings}
				onReady={onReady}
			/>,
		);
		expect(await screen.findByText(`已渲染 ${pages} 页`)).toBeInTheDocument();
		expect(onReady).toHaveBeenCalledWith(pages);
		expect(onReady).toHaveBeenCalledTimes(1);
	});

	it("解析失败时显示原因", async () => {
		const boom = new Error("坏文件");
		lib.fail.error = boom;
		pdf.fail.error = boom;
		const onReady = vi.fn();
		render(
			<View
				bytes={small}
				view="full"
				assets={assets}
				strings={strings}
				onReady={onReady}
			/>,
		);
		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toMatch(
			/^无法显示这个文件：(坏文件|工作簿损坏)$/,
		);
		expect(onReady).not.toHaveBeenCalled();
	});
});

describe("视图变体", () => {
	it("Word 交给库的是副本，调用方的缓冲区不被转移", async () => {
		render(
			<DocxView
				bytes={small}
				view="thumbnail"
				assets={assets}
				strings={strings}
			/>,
		);
		expect(await screen.findByText("已渲染 2 页")).toBeInTheDocument();
		const passed = lib.docxLoad.mock.calls[0]?.[0];
		expect(passed).not.toBe(small);
		expect(passed?.byteLength).toBe(small.byteLength);
	});

	it("打印视图逐页画完全部页才算完成，并设定纸张尺寸", async () => {
		const onReady = vi.fn();
		const { container } = render(
			<PptxView
				bytes={small}
				view="print"
				assets={assets}
				strings={strings}
				onReady={onReady}
			/>,
		);
		expect(await screen.findByText("已渲染 3 页")).toBeInTheDocument();
		expect(onReady).toHaveBeenCalledWith(3);
		expect(container.querySelectorAll(".oryn-o-print-page")).toHaveLength(3);
		expect(container.querySelector("style")?.textContent).toContain(
			"@page{size:1280px 720px;margin:0}",
		);
	});

	it("表格没有打印视图", () => {
		render(
			<XlsxView bytes={small} view="print" assets={assets} strings={strings} />,
		);
		expect(screen.getByText("表格不支持打印视图")).toBeInTheDocument();
	});

	it("演示：进入时回调 onPresent(true)，Esc 退出时回调 onPresent(false)", async () => {
		const onPresent = vi.fn();
		render(
			<PptxView
				bytes={small}
				view="full"
				assets={assets}
				strings={strings}
				onPresent={onPresent}
			/>,
		);
		const button = await screen.findByRole("button", { name: "演示" });
		act(() => button.click());
		expect(onPresent).toHaveBeenLastCalledWith(true);
		expect(screen.getByRole("dialog", { name: "演示" })).toBeInTheDocument();
		act(() => {
			window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
		});
		expect(onPresent).toHaveBeenLastCalledWith(false);
		expect(screen.queryByRole("dialog")).toBeNull();
	});
});

describe("评论点选与图钉", () => {
	const pin = (n: number, page: number, x: number, y: number) => ({
		n,
		answered: false,
		anchor: {
			page: { number: page, name: "" },
			x,
			y,
			range: null,
			block: null,
			text: "",
		},
	});

	it("Word：点页面给出页号、归一化坐标与点中的整段文字；图钉按坐标画在对应页上", async () => {
		const onPick = vi.fn();
		const rect = vi
			.spyOn(HTMLElement.prototype, "getBoundingClientRect")
			.mockReturnValue(new DOMRect(100, 50, 400, 800));
		try {
			const { container } = render(
				<DocxView
					bytes={small}
					view="full"
					assets={assets}
					strings={strings}
					pickable
					onPick={onPick}
					pins={[pin(3, 2, 0.1, 0.2)]}
					locate={null}
				/>,
			);
			expect(await screen.findByText("已渲染 2 页")).toBeInTheDocument();
			const pick = container.querySelector('[data-page="1"] .oryn-pick');
			fireEvent.click(pick as Element, { clientX: 200, clientY: 450 });
			await waitFor(() => expect(onPick).toHaveBeenCalledTimes(1));
			expect(onPick).toHaveBeenCalledWith(
				{
					page: { number: 1, name: "" },
					x: 0.25,
					y: 0.5,
					range: null,
					block: null,
					text: "季度报告",
				},
				{ x: 200, y: 450 },
			);
			expect(lib.docx.collectPageRuns).toHaveBeenCalledWith(0, {
				width: 595 * (96 / 72),
			});
			const dot = container.querySelector<HTMLElement>(
				'[data-page="2"] .oryn-pin',
			);
			expect(dot?.textContent).toBe("3");
			expect(dot?.style.left).toBe("10%");
			expect(dot?.style.top).toBe("20%");
			expect(container.querySelector('[data-page="1"] .oryn-pin')).toBeNull();
		} finally {
			rect.mockRestore();
		}
	});

	it("PowerPoint：名字取该张标题，文字取点中的形状", async () => {
		const onPick = vi.fn();
		const rect = vi
			.spyOn(HTMLElement.prototype, "getBoundingClientRect")
			.mockReturnValue(new DOMRect(0, 0, 1000, 500));
		try {
			const { container } = render(
				<PptxView
					bytes={small}
					view="full"
					assets={assets}
					strings={strings}
					pickable
					onPick={onPick}
				/>,
			);
			expect(await screen.findByText("已渲染 3 页")).toBeInTheDocument();
			const pick = container.querySelector('[data-page="2"] .oryn-pick');
			fireEvent.click(pick as Element, { clientX: 500, clientY: 100 });
			await waitFor(() => expect(onPick).toHaveBeenCalledTimes(1));
			expect(onPick).toHaveBeenCalledWith(
				{
					page: { number: 2, name: "本季*要点" },
					x: 0.5,
					y: 0.2,
					range: null,
					block: null,
					text: "营收 同比 增长",
				},
				{ x: 500, y: 100 },
			);
			expect(lib.pptx.getElementContextAt).toHaveBeenCalledWith(
				1,
				{ x: 12192000 * 0.5, y: 6858000 * 0.2 },
				{ maxTextCharacters: 200 },
			);
		} finally {
			rect.mockRestore();
		}
	});

	it("不可点选时没有点选层；缩略图与打印视图不画图钉", async () => {
		const { container, unmount } = render(
			<DocxView
				bytes={small}
				view="full"
				assets={assets}
				strings={strings}
				pins={[pin(1, 1, 0.5, 0.5)]}
			/>,
		);
		expect(await screen.findByText("已渲染 2 页")).toBeInTheDocument();
		expect(container.querySelector(".oryn-pick")).toBeNull();
		expect(container.querySelectorAll(".oryn-pin")).toHaveLength(1);
		unmount();
		for (const view of ["thumbnail", "print"] as const) {
			const r = render(
				<PptxView
					bytes={small}
					view={view}
					assets={assets}
					strings={strings}
					pickable
					onPick={vi.fn()}
					pins={[pin(1, 1, 0.5, 0.5)]}
				/>,
			);
			expect(await screen.findByText("已渲染 3 页")).toBeInTheDocument();
			expect(r.container.querySelector(".oryn-pick")).toBeNull();
			expect(r.container.querySelector(".oryn-pin")).toBeNull();
			r.unmount();
		}
	});
});
