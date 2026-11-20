/**
 * 首页左上角侧栏钮的 iOS 原生页头接入（lib/native-header.ts）：下沉侧栏布局且宿主有原生页头时，网页钮排成线程页那枚圆钮，
 * 登记只有侧栏钮的 spec（theme + 带页名 `home` 的 lead，没有标题胶囊）；原生显示稳了网页钮透明但仍可命中；
 * 没有原生页头或不是手机布局时与以前完全相同（不登记任何部件）。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../i18n";
import type * as NativeHeaderModule from "../lib/native-header";
import type { HeaderParts } from "../lib/native-header";
import { resetThreadStores } from "../state/stores";
import { uiStore } from "../state/ui";
import { HomePage } from "./HomePage";

const nh = vi.hoisted(() => ({
	available: true,
	active: false,
	setPart: vi.fn(),
	cover: vi.fn(),
	frame: { x: 12, y: 68, width: 44, height: 44 },
	theme: {
		dark: false,
		palette: {
			accent: "#2f6fed",
			brand: "#3b82f6",
			err: "#c8372d",
			warn: "#b7791f",
			good: "#2f9e5b",
			frost: "#5b93c9",
		},
	},
}));
// 换掉控制器的钩子（矩形、调色板固定，遮挡探测只记参数），其余导出用真的
vi.mock("../lib/native-header", async (orig) => {
	// vi.mock 的工厂被提升到所有 import 之前，只能在这里取 React
	const { useEffect } = await import("react");
	return {
		...(await orig<typeof NativeHeaderModule>()),
		useNativeHeaderAvailable: () => nh.available,
		useNativeHeaderActive: () => nh.active,
		useNativeHeaderEngaged: () => nh.active,
		useNativeHeaderMenu: () => null,
		useNativeHeaderPart: (part: string, value: unknown, on = true) => {
			useEffect(() => {
				if (on) nh.setPart(part, value);
			});
			useEffect(
				() => (on ? () => nh.setPart(part, null) : undefined),
				[part, on],
			);
		},
		useSlotFrame: (_ref: unknown, on = true) => (on ? nh.frame : null),
		useHeaderTheme: () => nh.theme,
		useHeaderCover: (_root: unknown, visible: boolean) => nh.cover(visible),
	};
});

/** 某个部件最近一次登记的值（没登记过为 undefined）。 */
function part<K extends keyof HeaderParts>(
	name: K,
): HeaderParts[K] | null | undefined {
	const calls = nh.setPart.mock.calls.filter((c) => c[0] === name);
	return calls.at(-1)?.[1] as HeaderParts[K] | null | undefined;
}

function mount() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<HomePage />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const slot = () => document.querySelector('[data-native-slot="lead"]');

describe("首页侧栏钮的 iOS 原生页头", () => {
	const initialWidth = window.innerWidth;
	beforeEach(() => {
		nh.available = true;
		nh.active = false;
		nh.setPart.mockClear();
		nh.cover.mockClear();
		window.innerWidth = 390;
		vi.stubGlobal(
			"fetch",
			vi.fn(async () =>
				Response.json({ code: "not_found", message: "" }, { status: 404 }),
			),
		);
	});
	afterEach(() => {
		vi.unstubAllGlobals();
		window.innerWidth = initialWidth;
		resetThreadStores();
	});

	it("登记 theme 与带页名 home 的 lead（没有标题胶囊）；网页钮是线程页那枚圆形玻璃钮", () => {
		mount();
		expect(part("theme")).toEqual({
			...nh.theme,
			text: { back: i18n.t("common.back"), close: i18n.t("common.close") },
		});
		expect(part("lead")).toMatchObject({
			frame: nh.frame,
			label: i18n.t("nav.sidebar"),
			key: "home",
		});
		expect(part("title")).toBeUndefined();
		expect(part("trail")).toBeUndefined();
		const el = slot();
		expect(el?.className).toMatch(/(^|\s)glass(\s|$)/);
		expect(el?.className).toMatch(/(^|\s)h-11(\s|$)/);
		expect(el).toHaveAccessibleName(i18n.t("nav.sidebar"));
		expect(el).not.toHaveAttribute("aria-hidden");
		expect(nh.cover).toHaveBeenLastCalledWith(true);
	});

	it("原生显示稳了：网页钮透明（无玻璃）、对读屏隐藏、不进 Tab 序，仍在原位可命中", () => {
		nh.active = true;
		mount();
		const el = slot();
		expect(el?.className).toMatch(/(^|\s)opacity-0(\s|$)/);
		expect(el?.className).not.toMatch(/(^|\s)glass(\s|$)/);
		expect(el).toHaveAttribute("aria-hidden", "true");
		expect(el).toHaveAttribute("tabindex", "-1");
	});

	it("lead.onTap 与网页钮都开关侧栏", () => {
		uiStore.setState({ sidebarOpen: false });
		mount();
		act(() => part("lead")?.onTap());
		expect(uiStore.getState().sidebarOpen).toBe(true);
		act(() => (slot() as HTMLButtonElement).click());
		expect(uiStore.getState().sidebarOpen).toBe(false);
	});

	it("卸载时撤掉 theme 与 lead 的登记", () => {
		const { unmount } = mount();
		nh.setPart.mockClear();
		unmount();
		expect(nh.setPart).toHaveBeenCalledWith("lead", null);
		expect(nh.setPart).toHaveBeenCalledWith("theme", null);
	});

	it("宿主没有原生页头：与以前相同（菜单图标钮、不登记任何部件）", () => {
		nh.available = false;
		mount();
		expect(slot()).toBeNull();
		expect(nh.setPart).not.toHaveBeenCalled();
		expect(
			document.querySelector(`button[aria-label="${i18n.t("nav.sidebar")}"]`),
		).not.toBeNull();
	});

	it("不是手机布局（expanded）：不接管、不登记", () => {
		window.innerWidth = 1200;
		mount();
		expect(slot()).toBeNull();
		expect(nh.setPart).not.toHaveBeenCalled();
	});
});
