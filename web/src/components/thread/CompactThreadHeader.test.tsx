/**
 * 手机标题菜单：子菜单只存 key、每次从最新菜单项取；切线程（页头被复用）时菜单收起，
 * 之后的操作只能落到当前线程，不执行上一个线程缓存的回调。
 * iOS 原生页头的接入：三件登记成声明式 spec（路径 id、状态珠档位、copy），原生显示稳了网页三件换成透明，
 * 原生回传的 id 在最新的菜单项里找回调。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Orb, Thread } from "../../api/client";
import i18n from "../../i18n";
import type * as NativeHeaderModule from "../../lib/native-header";
import type { HeaderParts } from "../../lib/native-header";
import { makeOrb } from "../../mock/fixtures";
import type * as Actions from "../../state/actions";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { answerConfirm } from "../../test/confirm";
import { ConfirmHost } from "../ui/ConfirmHost";
import { CompactThreadHeader } from "./CompactThreadHeader";

const { patchThread } = vi.hoisted(() => ({
	patchThread: vi.fn(async (_threadId: string, _patch: unknown) => undefined),
}));
vi.mock("../../state/actions", async (orig) => ({
	...(await orig<typeof Actions>()),
	patchThread: (threadId: string, patch: unknown) =>
		patchThread(threadId, patch),
}));

const nh = vi.hoisted(() => ({
	active: false,
	setPart: vi.fn(),
	cover: vi.fn(),
	frame: { x: 64, y: 67, width: 265, height: 46 },
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
// 换掉控制器实例与钩子（矩形、调色板固定，遮挡探测只记参数），其余导出（nativeItems、findMenuItem、颜色换算）用真的
vi.mock("../../lib/native-header", async (orig) => {
	// vi.mock 的工厂被提升到所有 import 之前，只能在这里取 React
	const { useEffect } = await import("react");
	return {
		...(await orig<typeof NativeHeaderModule>()),
		nativeHeader: { setPart: nh.setPart },
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

const A = "T-0193a1b2-0000-7000-8000-0000000000a1";
const B = "T-0193a1b2-0000-7000-8000-0000000000b2";
const ORB_A = "orb-01jcz8m3w4x5y6z7a8b9c0aaaa";
const ORB_B = "orb-01jcz8m3w4x5y6z7a8b9c0bbbb";

function seed(threadId: string, orbId: string, state: Orb["state"] = "ready") {
	const thread = {
		id: threadId,
		title: threadId === A ? "线程 A" : "线程 B",
		titleSource: "auto",
		state: "idle",
		stateChangedAt: 0,
		pinned: false,
		labels: [],
		unread: false,
		createdAt: 0,
		updatedAt: 0,
		orbId,
	} as unknown as Thread;
	getThreadStore(threadId)
		.getState()
		.apply([
			{ type: "thread", thread },
			{ type: "orb", orb: makeOrb(orbId, [threadId], state) },
		]);
}

function mount() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	const tree = (tid: string) => (
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<CompactThreadHeader threadId={tid} veil={56} />
				<ConfirmHost />
			</MemoryRouter>
		</QueryClientProvider>
	);
	const r = render(tree(A));
	return (tid: string) => r.rerender(tree(tid));
}

const capsule = () => screen.getByTestId("thread-title");
const item = (key: string) =>
	document.querySelector<HTMLButtonElement>(
		`[data-testid="title-menu"] [data-menu-key="${key}"]`,
	);

function openColorSubmenu() {
	fireEvent.click(capsule());
	const color = item("color");
	if (!color) throw new Error("没有颜色项");
	fireEvent.click(color);
}

describe("CompactThreadHeader 标题菜单", () => {
	beforeEach(() => {
		resetThreadStores();
		patchThread.mockClear();
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => Response.json({ code: "not_found" }, { status: 404 })),
		);
		seed(A, ORB_A);
		seed(B, ORB_B);
	});
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("A 的颜色子菜单开着时切到 B：菜单收起；再打开后改的是 B 的颜色", () => {
		const show = mount();
		openColorSubmenu();
		expect(item("red")).not.toBeNull();
		show(B);
		expect(capsule()).toHaveAttribute("aria-expanded", "false");
		// 退出动画期间也不能再出现 A 的子菜单
		expect(item("red")).toBeNull();
		openColorSubmenu();
		const red = item("red");
		if (!red) throw new Error("没有红色项");
		fireEvent.click(red);
		expect(patchThread.mock.calls).toEqual([[B, { color: "red" }]]);
	});

	it("运行环境行（无 box）直接打开信息页，不进子菜单", () => {
		uiStore.setState({ threadTab: "agent" });
		mount();
		fireEvent.click(capsule());
		const env = item("env");
		if (!env) throw new Error("没有运行环境行");
		fireEvent.click(env);
		expect(uiStore.getState().threadTab).toBe("info");
		expect(item("info")).toBeNull();
	});

	it("菜单选「删除」后取消确认：焦点回到标题胶囊，不掉到 body", async () => {
		mount();
		fireEvent.click(capsule());
		const del = item("delete");
		if (!del) throw new Error("没有删除项");
		fireEvent.click(del);
		await answerConfirm(false);
		expect(screen.queryByRole("dialog")).toBeNull();
		expect(capsule()).toHaveFocus();
	});
});

/** 最近一次登记的某个部件。 */
function part<K extends keyof HeaderParts>(k: K): HeaderParts[K] {
	const call = nh.setPart.mock.calls.filter((c) => c[0] === k).at(-1);
	if (!call?.[1]) throw new Error(`没有登记 ${k}`);
	return call[1] as HeaderParts[K];
}

function mountNative(visible = true) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	const tree = (tid: string) => (
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<CompactThreadHeader threadId={tid} veil={56} visible={visible} />
			</MemoryRouter>
		</QueryClientProvider>
	);
	const r = render(tree(A));
	return { show: (tid: string) => r.rerender(tree(tid)), unmount: r.unmount };
}

describe("CompactThreadHeader iOS 原生页头", () => {
	const initialWidth = window.innerWidth;
	beforeEach(() => {
		resetThreadStores();
		patchThread.mockClear();
		nh.setPart.mockClear();
		nh.cover.mockClear();
		nh.active = false;
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => Response.json({ code: "not_found" }, { status: 404 })),
		);
		seed(A, ORB_A);
		seed(B, ORB_B);
	});
	afterEach(() => {
		vi.unstubAllGlobals();
		window.innerWidth = initialWidth;
		nh.active = false;
		Reflect.deleteProperty(navigator, "clipboard");
	});

	it("登记 theme / lead / title：key 是线程 id，菜单是路径 id，env 行带状态珠档位，复制链接带 copy", () => {
		mountNative();
		expect(part("theme")).toEqual({
			...nh.theme,
			text: { back: i18n.t("common.back"), close: i18n.t("common.close") },
		});
		expect(part("lead")).toMatchObject({
			frame: nh.frame,
			label: i18n.t("nav.sidebar"),
		});
		const title = part("title");
		expect(title.key).toBe(A);
		expect(title.text).toBe("线程 A");
		expect(title.menu.dim).toBe(true);
		expect(title.menu.quick.map((i) => i.id)).toEqual([
			"archive",
			"share",
			"tree",
		]);
		const env = title.menu.status.find((i) => i.id === "env");
		expect(env?.sub?.icon?.orb).toBe("ready");
		expect(env?.children).toBeUndefined();
		// 胶囊第二行的运行环境段与菜单的说明行同一档位
		expect(title.meta.at(-1)?.icon?.orb).toBe("ready");
		const groups = title.menu.groups.flat();
		expect(groups.find((i) => i.id === "link")?.copy).toBe(
			`${location.origin}/t/${A}`,
		);
		// 颜色子项的 label 是 ReactNode：走 native.label 与色点，一项不丢
		const color = groups.find((i) => i.id === "color");
		expect(color?.children?.map((i) => i.id)).toContain("color/red");
		expect(
			color?.children?.find((i) => i.id === "color/red")?.icon?.dot,
		).toMatch(/^#[0-9a-f]{6}$/);
		// 没有项落到兜底图标
		expect(
			[...title.menu.quick, ...title.menu.status, ...groups]
				.flatMap((i) => [i, ...(i.children ?? [])])
				.filter((i) => i.icon?.symbol === "circle"),
		).toEqual([]);
	});

	it("线程开始工作：运行环境的状态珠变 working", () => {
		mountNative();
		act(() => {
			const s = getThreadStore(A).getState();
			s.patchLocal({ thread: { ...s.thread, state: "working" } as Thread });
		});
		const title = part("title");
		expect(title.menu.status.find((i) => i.id === "env")?.sub?.icon?.orb).toBe(
			"working",
		);
		expect(title.meta.at(-1)?.icon?.orb).toBe("working");
	});

	it("原生显示稳了：侧栏钮与胶囊透明（无玻璃）、对读屏隐藏、不进 Tab 序，仍在原位可命中", () => {
		nh.active = true;
		mountNative();
		for (const slot of ["lead", "title"]) {
			const el = document.querySelector(`[data-native-slot="${slot}"]`);
			expect(el?.className).toMatch(/(^|\s)opacity-0(\s|$)/);
			expect(el?.className).not.toMatch(/(^|\s)glass(\s|$)/);
			expect(el).toHaveAttribute("aria-hidden", "true");
			expect(el).toHaveAttribute("tabindex", "-1");
		}
	});

	it("没接管时网页三件照常是玻璃", () => {
		mountNative();
		for (const slot of ["lead", "title"]) {
			const el = document.querySelector(`[data-native-slot="${slot}"]`);
			expect(el?.className).toMatch(/(^|\s)glass(\s|$)/);
			expect(el).not.toHaveAttribute("aria-hidden");
		}
	});

	it("原生选中按路径在最新菜单项里找回调：切到 B 后（含上一份登记的 onSelect）只改 B 的颜色", () => {
		const { show } = mountNative();
		act(() => part("title").onSelect("color/red"));
		expect(patchThread.mock.calls).toEqual([[A, { color: "red" }]]);
		const stale = part("title").onSelect;
		patchThread.mockClear();
		show(B);
		expect(part("title").key).toBe(B);
		act(() => part("title").onSelect("color/red"));
		act(() => stale("color/red"));
		expect(patchThread.mock.calls).toEqual([
			[B, { color: "red" }],
			[B, { color: "red" }],
		]);
	});

	it("带 copy 的项（复制链接）不再执行网页回调；禁用项、有子页的项、未知 id 都不执行", () => {
		const writeText = vi.fn(async () => undefined);
		Object.defineProperty(navigator, "clipboard", {
			value: { writeText },
			configurable: true,
		});
		mountNative();
		act(() => part("title").onSelect("link"));
		expect(writeText).not.toHaveBeenCalled();
		// 已归档的线程不能加定时：「定时」禁用
		act(() => {
			const s = getThreadStore(A).getState();
			s.patchLocal({ thread: { ...s.thread, archivedAt: 1 } as Thread });
		});
		const schedule = part("title")
			.menu.groups.flat()
			.find((i) => i.id === "schedule");
		expect(schedule?.disabled).toBe(true);
		act(() => part("title").onSelect("schedule"));
		expect(screen.queryByRole("dialog")).toBeNull();
		act(() => part("title").onSelect("color"));
		act(() => part("title").onSelect("nope/x"));
		expect(patchThread).not.toHaveBeenCalled();
	});

	it("lead.onTap 开关侧栏", () => {
		window.innerWidth = 390;
		uiStore.setState({ sidebarOpen: false });
		mountNative();
		act(() => part("lead").onTap());
		expect(uiStore.getState().sidebarOpen).toBe(true);
		act(() => part("lead").onTap());
		expect(uiStore.getState().sidebarOpen).toBe(false);
	});

	it("visible 交给遮挡探测：切到面板标签（visible=false）时原生当帧让位", () => {
		mountNative(false);
		expect(nh.cover).toHaveBeenLastCalledWith(false);
		nh.cover.mockClear();
		mountNative(true);
		expect(nh.cover).toHaveBeenLastCalledWith(true);
	});

	it("卸载时撤掉三件的登记", () => {
		const { unmount } = mountNative();
		nh.setPart.mockClear();
		unmount();
		expect(nh.setPart).toHaveBeenCalledWith("title", null);
		expect(nh.setPart).toHaveBeenCalledWith("lead", null);
		expect(nh.setPart).toHaveBeenCalledWith("theme", null);
	});
});
