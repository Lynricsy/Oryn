/**
 * 视口：双段视口只认「恰好两段、同一行、不重叠」；安装跟踪后根元素带当前断点与段数；
 * `useViewportSegments` 按值缓存，值不变时引用不变（useSyncExternalStore 要求）。
 */
import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	installNoFocusZoom,
	installViewportTracking,
	readSegments,
	setMinScale,
	useSafeTop,
	useViewportSegments,
} from "./viewport";

function rect(x: number, y: number, width: number, height: number) {
	return { x, y, left: x, top: y, width, height } as DOMRectReadOnly;
}

const fakeWindow = (segments?: DOMRectReadOnly[] | null) =>
	(segments === undefined ? {} : { viewport: { segments } }) as Window;

const initialWidth = window.innerWidth;

afterEach(() => {
	window.innerWidth = initialWidth;
	delete window.viewport;
	delete document.documentElement.dataset.bp;
	delete document.documentElement.dataset.segments;
	document.documentElement.style.removeProperty("--kb-inset");
	document.documentElement.style.removeProperty("--vvh");
});

describe("readSegments", () => {
	it("没有 Viewport Segments API 或 segments 为空时为 null", () => {
		expect(readSegments(fakeWindow())).toBeNull();
		expect(readSegments(fakeWindow(null))).toBeNull();
	});

	it("只有一段时为 null", () => {
		expect(readSegments(fakeWindow([rect(0, 0, 800, 600)]))).toBeNull();
	});

	it("同一行的两段：左段宽、铰链宽、右段宽", () => {
		expect(
			readSegments(fakeWindow([rect(0, 0, 500, 800), rect(530, 0, 470, 800)])),
		).toEqual({ left: 500, gap: 30, right: 470 });
		// 无缝铰链
		expect(
			readSegments(fakeWindow([rect(0, 0, 400, 800), rect(400, 0, 400, 800)])),
		).toEqual({ left: 400, gap: 0, right: 400 });
	});

	it("上下排列、高度不同或重叠时为 null", () => {
		expect(
			readSegments(fakeWindow([rect(0, 0, 800, 400), rect(0, 420, 800, 400)])),
		).toBeNull();
		expect(
			readSegments(fakeWindow([rect(0, 0, 500, 800), rect(530, 0, 470, 700)])),
		).toBeNull();
		expect(
			readSegments(fakeWindow([rect(0, 0, 500, 800), rect(480, 0, 500, 800)])),
		).toBeNull();
	});

	it("三段时为 null", () => {
		expect(
			readSegments(
				fakeWindow([
					rect(0, 0, 300, 800),
					rect(300, 0, 300, 800),
					rect(600, 0, 300, 800),
				]),
			),
		).toBeNull();
	});
});

describe("installViewportTracking", () => {
	it("根元素写入当前断点与段数，resize 后更新，卸载后不再更新", () => {
		window.innerWidth = 390;
		const root = document.documentElement;
		const uninstall = installViewportTracking();
		try {
			expect(root.dataset.bp).toBe("compact");
			expect(root.dataset.segments).toBe("1");

			window.innerWidth = 1030;
			window.viewport = {
				segments: [rect(0, 0, 500, 800), rect(530, 0, 500, 800)],
			};
			window.dispatchEvent(new Event("resize"));
			expect(root.dataset.bp).toBe("expanded");
			expect(root.dataset.segments).toBe("2");
		} finally {
			uninstall();
		}
		window.innerWidth = 700;
		window.dispatchEvent(new Event("resize"));
		expect(root.dataset.bp).toBe("expanded");
	});
});

describe("useViewportSegments", () => {
	it("值不变时返回同一个对象，段变化或消失时跟着变", () => {
		window.viewport = {
			segments: [rect(0, 0, 500, 800), rect(530, 0, 500, 800)],
		};
		const { result, rerender } = renderHook(() => useViewportSegments());
		const first = result.current;
		expect(first).toEqual({ left: 500, gap: 30, right: 500 });
		rerender();
		expect(result.current).toBe(first);

		act(() => {
			window.viewport = {
				segments: [rect(0, 0, 600, 800), rect(620, 0, 400, 800)],
			};
			window.dispatchEvent(new Event("resize"));
		});
		expect(result.current).toEqual({ left: 600, gap: 20, right: 400 });

		act(() => {
			delete window.viewport;
			window.dispatchEvent(new Event("resize"));
		});
		expect(result.current).toBeNull();
	});
});

describe("installNoFocusZoom", () => {
	const BASE = "width=device-width, initial-scale=1.0, viewport-fit=cover";
	function docWith(content: string): Document {
		const doc = document.implementation.createHTMLDocument("");
		const meta = doc.createElement("meta");
		meta.name = "viewport";
		meta.content = content;
		doc.head.appendChild(meta);
		return doc;
	}
	const content = (doc: Document) =>
		doc.querySelector<HTMLMetaElement>('meta[name="viewport"]')?.content;

	it("iPhone 与报 MacIntel 的 iPadOS 加 maximum-scale=1；Android 与桌面 Mac 不动", () => {
		const cases = [
			{
				ua: "Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X)",
				platform: "iPhone",
				touch: 5,
				zoom: true,
			},
			{
				ua: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
				platform: "MacIntel",
				touch: 5,
				zoom: true,
			},
			{
				ua: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
				platform: "MacIntel",
				touch: 0,
				zoom: false,
			},
			{
				ua: "Mozilla/5.0 (Linux; Android 15)",
				platform: "Linux armv8l",
				touch: 5,
				zoom: false,
			},
		];
		for (const c of cases) {
			const doc = docWith(BASE);
			installNoFocusZoom(doc, {
				userAgent: c.ua,
				platform: c.platform,
				maxTouchPoints: c.touch,
			});
			expect(content(doc), c.ua).toBe(
				c.zoom ? `${BASE}, maximum-scale=1` : BASE,
			);
		}
	});

	it("已有 maximum-scale 时不重复追加", () => {
		const doc = docWith(`${BASE}, maximum-scale=2`);
		installNoFocusZoom(doc, {
			userAgent: "iPhone",
			platform: "iPhone",
			maxTouchPoints: 5,
		});
		expect(content(doc)).toBe(`${BASE}, maximum-scale=2`);
	});
});

describe("setMinScale", () => {
	const BASE = "width=device-width, initial-scale=1.0, viewport-fit=cover";
	const docWith = (content: string) => {
		const doc = document.implementation.createHTMLDocument("");
		const meta = doc.createElement("meta");
		meta.name = "viewport";
		meta.content = content;
		doc.head.appendChild(meta);
		return doc;
	};
	const content = (doc: Document) =>
		doc.querySelector<HTMLMetaElement>('meta[name="viewport"]')?.content;

	it("开两次只追加一次 minimum-scale=1，关掉后回到原样（保留 maximum-scale）", () => {
		const doc = docWith(`${BASE}, maximum-scale=1`);
		setMinScale(true, doc);
		setMinScale(true, doc);
		expect(content(doc)).toBe(`${BASE}, maximum-scale=1, minimum-scale=1`);
		setMinScale(false, doc);
		setMinScale(false, doc);
		expect(content(doc)).toBe(`${BASE}, maximum-scale=1`);
	});
});

describe("useSafeTop", () => {
	afterEach(() => vi.restoreAllMocks());

	it("卸载期间转屏、安全区变了，再挂载读到新值（不沿用离开前的高度）", () => {
		let pad = "47px";
		const real = window.getComputedStyle.bind(window);
		vi.spyOn(window, "getComputedStyle").mockImplementation((el, pseudo) => {
			const s = real(el, pseudo);
			return (el as HTMLElement).style?.cssText.includes("safe-area-inset-top")
				? ({ ...s, paddingTop: pad } as CSSStyleDeclaration)
				: s;
		});
		const first = renderHook(() => useSafeTop());
		expect(first.result.current).toBe(47);
		first.unmount();
		pad = "0px"; // 转到横屏：这时没有订阅者在场
		const second = renderHook(() => useSafeTop());
		expect(second.result.current).toBe(0);
	});
});
