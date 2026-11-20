/** 原生页头控制器：spec 合成与截断、菜单项转换、oklch 换算、遮挡探测、接管 / 跟随卡片 / 让位 / 回来 / 事件 / 回退，以及常量与 CSS 不漂移。 */
import { renderHook } from "@testing-library/react";
import {
	afterEach,
	beforeAll,
	describe,
	expect,
	it,
	type Mock,
	vi,
} from "vitest";
import type {
	NativeHeaderEvent,
	NativeHeaderMenu,
	NativeHeaderShow,
	NativeHeaderSpec,
} from "./host";
import {
	buildHeaderSpec,
	clip,
	createNativeHeader,
	findMenuItem,
	HEADER_FADE_MS,
	type HeaderEnv,
	type HeaderParts,
	type MenuSource,
	markHex,
	type NativeHeader,
	nativeItems,
	oklchHex,
	REST_MS,
	slotsCovered,
	useSlotFrame,
} from "./native-header";
import {
	NATIVE_LAG_MS,
	SINK_EASE,
	SINK_MS,
	SYNC_BIAS_MS,
} from "./native-tab-bar";

// 前端 tsconfig 不带 Node 类型（静态导入 node:fs 过不了 tsc），只声明用到的函数（同 native-tab-bar.test）；工作目录是 web/
let readFile: (path: string) => string;
beforeAll(async () => {
	const fs = (await import("node:fs" as string)) as {
		readFileSync(path: string, encoding: "utf8"): string;
	};
	readFile = (path) => fs.readFileSync(path, "utf8");
});

const FIXTURES = "src-tauri/plugins/oryn-mobile/fixtures";
const fixture = <T>(name: string): T =>
	JSON.parse(readFile(`${FIXTURES}/${name}`)) as T;

const KEY = "T-0193a1b2-0000-7000-8000-0000000000a1";

/** 菜单源（结构同 `useThreadActions` 生成的 MenuItem）。 */
const QUICK: MenuSource[] = [
	{ key: "archive", label: "归档", icon: "archive" },
	{ key: "share", label: "分享", icon: "share" },
	{ key: "tree", label: "会话树", icon: "tree" },
];
const STATUS: MenuSource[] = [
	{
		key: "env",
		label: "Orb",
		icon: "box",
		native: { sub: { text: "就绪", icon: { orb: "working" } } },
		children: [
			{ key: "info", label: "查看环境信息", icon: "info" },
			{ key: "freeze", label: "冻结", icon: "pause" },
			{ key: "wake", label: "唤醒", icon: "play", disabled: true },
			{ key: "delete", label: "删除 orb", icon: "trash", danger: true },
		],
	},
	{
		key: "public",
		label: "已公开",
		icon: "globe",
		native: { sub: { text: "1 个公开链接有效" } },
	},
	{
		key: "pr-12",
		label: "#12 修复回调",
		icon: "pullRequest",
		native: { sub: { text: "打开 · CI 失败", tone: "err" } },
	},
];
const GROUPS: MenuSource[][] = [
	[
		{ key: "rename", label: "重命名", icon: "edit" },
		{
			key: "color",
			label: "颜色",
			icon: "target",
			children: [
				{
					key: "red",
					// 网页里是带色点的 ReactNode：原生靠 native.label
					label: { node: "红" },
					native: { label: "红", icon: { dot: "#d9534f" } },
				},
				{ key: "none", label: "无" },
			],
		},
		{ key: "pin", label: "置顶", icon: "pin" },
	],
	[
		{ key: "costUsage", label: "成本与用量", icon: "chart", hint: "$1.24" },
		{
			key: "link",
			label: "复制链接",
			icon: "copy",
			native: { copy: `https://oryn.example.com/t/${KEY}` },
		},
	],
	[{ key: "delete", label: "删除线程", icon: "trash", danger: true }],
];
const VIEWS: MenuSource[] = [
	{ key: "agent", label: "对话", icon: "message", checked: true },
	{
		key: "terminal",
		label: { node: "终端" },
		icon: "terminal",
		checked: false,
		native: { label: "终端", dot: "warn", pulse: true },
	},
	{ key: "info", label: "信息", icon: "info", checked: false },
];

/** H1 的输入：buildHeaderSpec 之后与 header-set.json 逐字相等。 */
function parts(
	over: { key?: string; onSelect?: Mock; onTap?: Mock; onTrail?: Mock } = {},
) {
	const theme: HeaderParts["theme"] = {
		dark: false,
		palette: {
			accent: "#2f6fed",
			brand: "#3b82f6",
			err: "#c8372d",
			warn: "#b7791f",
			good: "#2f9e5b",
			frost: "#5b93c9",
		},
		text: { back: "返回", close: "关闭" },
	};
	const lead: HeaderParts["lead"] = {
		frame: { x: 12, y: 68, width: 44, height: 44 },
		label: "侧边栏",
		onTap: over.onTap ?? vi.fn(),
	};
	const title: HeaderParts["title"] = {
		frame: { x: 64, y: 67, width: 265, height: 46 },
		key: over.key ?? KEY,
		text: "修复登录回调",
		icon: { glyph: "🔧", bg: "#f6dcc6", fg: "#7a3b00" },
		meta: [
			{ text: "发布准备", icon: { symbol: "target" } },
			{ text: "oryn", icon: { glyph: "O", bg: "#d5e4fb", fg: "#1f3f7a" } },
			{ text: "就绪", icon: { orb: "working" } },
		],
		marks: [{ symbol: "clock", dim: true }, { symbol: "globe" }],
		label: "修复登录回调, 发布准备, oryn, 就绪",
		menu: {
			label: "「修复登录回调」的操作",
			dim: true,
			quick: nativeItems(QUICK),
			status: nativeItems(STATUS),
			groups: GROUPS.map((g) => nativeItems(g)),
		},
		onSelect: over.onSelect ?? vi.fn(),
	};
	const trail: HeaderParts["trail"] = {
		frame: { x: 337, y: 68, width: 44, height: 44 },
		label: "视图",
		dot: "warn",
		menu: {
			label: "视图",
			dim: false,
			quick: [],
			status: [],
			groups: [nativeItems(VIEWS)],
		},
		onSelect: over.onTrail ?? vi.fn(),
	};
	return { theme, lead, title, trail };
}

afterEach(() => {
	vi.restoreAllMocks();
});

describe("buildHeaderSpec 与 nativeItems", () => {
	it("H1 输入 → 与契约夹具 header-set.json 逐字相等（回调被去掉）", () => {
		const spec = buildHeaderSpec(parts());
		expect(spec).toEqual(fixture("header-set.json"));
		expect(JSON.parse(JSON.stringify(spec))).toEqual(spec);
	});

	it("没有 trail 时 trail 为 null；深色用深色遮罩", () => {
		const p = parts();
		const spec = buildHeaderSpec({
			...p,
			theme: { ...p.theme, dark: true },
			trail: null,
		});
		expect(spec?.trail).toBeNull();
		expect(spec?.tune).toEqual({ scrim: 0.6, tint: 0.55 });
	});

	it("只有侧栏钮（首页）：lead 带页名 key → title / trail 为 null，与契约夹具 header-lead.json 逐字相等", () => {
		const p = parts();
		const spec = buildHeaderSpec({
			theme: {
				dark: true,
				palette: {
					accent: "#7aa2ff",
					brand: "#60a5fa",
					err: "#f2766b",
					warn: "#e0a84a",
					good: "#5cc98a",
					frost: "#8fb8e0",
				},
				text: p.theme.text,
			},
			lead: { ...p.lead, key: "home" },
			title: null,
			trail: null,
		});
		expect(spec).toEqual(fixture("header-lead.json"));
		expect(JSON.parse(JSON.stringify(spec))).toEqual(spec);
	});

	it("key：有标题胶囊取线程 id（侧栏钮的页名不算）；没有胶囊、侧栏钮也不带页名（线程页胶囊还没量到）→ 不成 spec", () => {
		const p = parts();
		expect(
			buildHeaderSpec({ ...p, lead: { ...p.lead, key: "home" } })?.key,
		).toBe(KEY);
		expect(buildHeaderSpec({ ...p, title: null })).toBeNull();
		expect(buildHeaderSpec({ ...p, title: null, trail: null })).toBeNull();
		// 没有胶囊也可以带视图钮
		expect(
			buildHeaderSpec({ ...p, lead: { ...p.lead, key: "home" }, title: null })
				?.trail?.symbol,
		).toBe("square.grid.2x2");
	});

	it("路径 id、label 非字符串且没有 native.label 的项丢弃并告警、未知图标 → circle、没有图标不带 icon", () => {
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const items = nativeItems(
			[
				{ key: "a", label: { node: 1 } },
				{ key: "b", label: "乙", icon: "sparkle" as MenuSource["icon"] },
				{ key: "c", label: "丙" },
			],
			"p/",
		);
		expect(items).toEqual([
			{ id: "p/b", label: "乙", icon: { symbol: "circle" } },
			{ id: "p/c", label: "丙" },
		]);
		expect(warn).toHaveBeenCalledTimes(1);
	});

	it("checked: false 保留、disabled / danger / pulse 为 false 不输出、hint 只认字符串", () => {
		const [item] = nativeItems([
			{
				key: "x",
				label: "项",
				checked: false,
				disabled: false,
				danger: false,
				hint: { node: 3 },
				native: { pulse: false },
			},
		]);
		expect(item).toEqual({ id: "x", label: "项", checked: false });
	});

	it("201 字符的 label 截成 200（按码点）；孙项不展开；子项超过 24 个截掉", () => {
		const long = "字".repeat(201);
		const kids: MenuSource[] = Array.from({ length: 30 }, (_, i) => ({
			key: `k${i}`,
			label: `子${i}`,
			children: [{ key: "g", label: "孙" }],
		}));
		const [item] = nativeItems([{ key: "p", label: long, children: kids }]);
		expect([...(item?.label ?? "")].length).toBe(200);
		expect(item?.children).toHaveLength(24);
		expect(item?.children?.[0]).toEqual({ id: "p/k0", label: "子0" });
		expect(clip("🦊🦊🦊", 2)).toBe("🦊🦊");
	});

	it("超过上限的 quick / 组 / meta / marks 截掉，空组去掉，标题与说明行截断", () => {
		const p = parts();
		const many = Array.from({ length: 30 }, (_, i) => ({
			id: `i${i}`,
			label: `${i}`,
		}));
		const spec = buildHeaderSpec({
			...p,
			title: {
				...p.title,
				text: "题".repeat(301),
				error: "",
				meta: [
					{ text: "" },
					...Array.from({ length: 8 }, () => ({ text: "段".repeat(130) })),
				],
				marks: Array.from({ length: 8 }, () => ({ symbol: "globe" })),
				menu: { ...p.title.menu, quick: many, groups: [[], many] },
			},
		});
		const title = spec?.title;
		if (!title) throw new Error("应有标题胶囊");
		expect([...title.text].length).toBe(300);
		expect("error" in title).toBe(false);
		expect(title.meta).toHaveLength(6);
		expect([...(title.meta[0]?.text ?? "")].length).toBe(120);
		expect(title.marks).toHaveLength(6);
		expect(title.menu.quick).toHaveLength(4);
		expect(title.menu.groups.map((g) => g.length)).toEqual([24]);
	});

	it("findMenuItem：按 a/b 路径找项，找不到为 undefined", () => {
		expect(findMenuItem(STATUS, "env")?.label).toBe("Orb");
		expect(findMenuItem(STATUS, "env/freeze")?.label).toBe("冻结");
		expect(findMenuItem(STATUS, "env/nope")).toBeUndefined();
		expect(findMenuItem(STATUS, "nope/freeze")).toBeUndefined();
	});
});

describe("oklch 换算与 index.css 不漂移", () => {
	it("白、黑与纯红", () => {
		expect(oklchHex(1, 0, 0)).toBe("#ffffff");
		expect(oklchHex(0, 0, 0)).toBe("#000000");
		const red = oklchHex(0.628, 0.2577, 29.23);
		const ch = (s: string, i: number) =>
			Number.parseInt(s.slice(1 + i * 2, 3 + i * 2), 16);
		for (const [i, want] of [255, 0, 0].entries())
			expect(Math.abs(ch(red, i) - want)).toBeLessThanOrEqual(2);
		expect(markHex(30, false)).toMatch(/^#[0-9a-f]{6}$/);
	});

	// 卡片晚 34ms 起步的 CSS 规则（页头与底栏共用选择器）由 native-tab-bar.test 校验
	it("头像、线程色的 oklch 常量与 CSS 一致", () => {
		const css = readFile("src/index.css");
		for (const s of [
			"background: oklch(0.89 0.075 var(--h))",
			"color: oklch(0.4 0.13 var(--h))",
			"background: oklch(0.34 0.08 var(--h))",
			"color: oklch(0.87 0.1 var(--h))",
			"background: oklch(0.64 0.15 var(--h))",
			"background: oklch(0.74 0.14 var(--h))",
		])
			expect(css).toContain(s);
	});
});

describe("slotsCovered", () => {
	function setup(
		vv: Partial<VisualViewport> | null = {
			offsetTop: 0,
			offsetLeft: 0,
			scale: 1,
			height: 852,
		},
	) {
		const lead = document.createElement("button");
		const title = document.createElement("button");
		const inner = document.createElement("span");
		title.append(inner);
		document.body.append(lead, title);
		vi.spyOn(lead, "getBoundingClientRect").mockReturnValue({
			left: 12,
			top: 68,
			width: 44,
			height: 44,
		} as DOMRect);
		vi.spyOn(title, "getBoundingClientRect").mockReturnValue({
			left: 64,
			top: 67,
			width: 265,
			height: 46,
		} as DOMRect);
		const hit = vi.fn((x: number, _y: number): Element | null =>
			x < 60 ? lead : inner,
		);
		const win = {
			innerHeight: 852,
			scrollX: 0,
			scrollY: 0,
			visualViewport: vv,
			document: { elementFromPoint: hit },
		} as unknown as Window;
		return { slots: [lead, title], hit, win };
	}

	afterEach(() => {
		document.body.replaceChildren();
	});

	it("各件都命中自己 → false；圆钮只取中点，胶囊取三点", () => {
		const { slots, hit, win } = setup();
		expect(slotsCovered(slots, win)).toBe(false);
		expect(hit).toHaveBeenCalledTimes(4);
	});

	it("命中别的元素或为空 → true", () => {
		const { slots, hit, win } = setup();
		const other = document.createElement("div");
		hit.mockImplementation(
			(x) => (x > 250 ? other : x < 60 ? slots[0] : slots[1]) ?? null,
		);
		expect(slotsCovered(slots, win)).toBe(true);
		hit.mockReturnValue(null);
		expect(slotsCovered(slots, win)).toBe(true);
	});

	it("页面被推走（visualViewport.offsetTop = 40）→ true；键盘占高但 offsetTop 为 0 → false", () => {
		const pushed = setup({
			offsetTop: 40,
			offsetLeft: 0,
			scale: 1,
			height: 500,
		});
		expect(slotsCovered(pushed.slots, pushed.win)).toBe(true);
		document.body.replaceChildren();
		const kb = setup({ offsetTop: 0, offsetLeft: 0, scale: 1, height: 500 });
		expect(slotsCovered(kb.slots, kb.win)).toBe(false);
	});

	it("没有任何 slot → true", () => {
		const { win } = setup();
		expect(slotsCovered([], win)).toBe(true);
	});
});

/** 排空宏任务前的全部微任务（latest 的 then / finally 链），不等真实时长。 */
const flush = () => new Promise((r) => setTimeout(r, 0));

/** 绝对时钟的起点（Unix 毫秒），假定时器从这里走。 */
const EPOCH = 1_795_000_000_000;

/** 可手动推进的假定时器、手动跑的假帧 + 同步执行的 microtask。 */
function fakeEnv(opts: { reduce?: boolean } = {}) {
	let now = 0;
	let n = 0;
	const timers = new Map<number, { at: number; cb: () => void }>();
	const frames = new Map<number, () => void>();
	const env: HeaderEnv & { blur: Mock<() => void> } = {
		setTimeout(cb, ms) {
			timers.set(++n, { at: now + ms, cb });
			return n;
		},
		clearTimeout(id) {
			timers.delete(id);
		},
		microtask: (cb) => cb(),
		frame(cb) {
			frames.set(++n, cb);
			return n;
		},
		cancelFrame(id) {
			frames.delete(id);
		},
		now: () => EPOCH + now,
		blur: vi.fn(),
		reduce: () => opts.reduce ?? false,
	};
	const advance = (ms: number) => {
		const end = now + ms;
		for (;;) {
			let next: [number, { at: number; cb: () => void }] | null = null;
			for (const e of timers)
				if (e[1].at <= end && (!next || e[1].at < next[1].at)) next = e;
			if (!next) break;
			timers.delete(next[0]);
			now = next[1].at;
			next[1].cb();
		}
		now = end;
	};
	/** 跑一帧。 */
	const frame = () => {
		const cbs = [...frames.values()];
		frames.clear();
		for (const cb of cbs) cb();
	};
	return { env, advance, frame, frames: () => frames.size, now: () => now };
}

interface FakeApi {
	set: Mock<(spec: NativeHeaderSpec | null) => Promise<void>>;
	show: Mock<(s: NativeHeaderShow) => Promise<void>>;
	menu: Mock<(m: NativeHeaderMenu) => Promise<void>>;
}

function fakeApi(opts: { rejectSet?: boolean } = {}): FakeApi {
	return {
		set: vi.fn(async (_spec: NativeHeaderSpec | null) => {
			if (opts.rejectSet) throw "unsupported";
		}),
		show: vi.fn(async (_s: NativeHeaderShow) => {}),
		menu: vi.fn(async (_m: NativeHeaderMenu) => {}),
	};
}

const HIDE: NativeHeaderShow = {
	alpha: 0,
	dx: 0,
	passive: false,
	ms: 0,
	delay: 0,
};

/** 接管 / 遮挡解除时的淡入（卡片静止关闭）。 */
const FADE_IN: NativeHeaderShow = { ...HIDE, alpha: 1, ms: HEADER_FADE_MS };

/** 卡片静止关闭后恢复接触摸的那一条。 */
const REST: NativeHeaderShow = { ...HIDE, alpha: 1 };

/** 拖动中的一帧。 */
const dragShow = (dx: number): NativeHeaderShow => ({
	alpha: 1,
	dx,
	passive: true,
	ms: 0,
	delay: 0,
});

function setup(opts: { rejectSet?: boolean; reduce?: boolean } = {}) {
	const api = fakeApi(opts);
	const { env, advance, frame, frames, now } = fakeEnv(opts);
	let emit: (e: NativeHeaderEvent) => void = () => {};
	const header = createNativeHeader(
		api,
		(cb) => {
			emit = cb;
			return () => {};
		},
		env,
	);
	const p = parts({ onSelect: vi.fn(), onTap: vi.fn(), onTrail: vi.fn() });
	const register = (h: NativeHeader = header, q = p) => {
		h.setPart("theme", q.theme);
		h.setPart("lead", q.lead);
		h.setPart("title", q.title);
		h.setPart("trail", q.trail);
	};
	const shows = () => api.show.mock.calls.map((c) => c[0]);
	return {
		api,
		env,
		advance,
		frame,
		frames,
		now,
		header,
		p,
		register,
		shows,
		emit: (e: NativeHeaderEvent) => emit(e),
	};
}

/** 接管并显示稳了；清掉此前的 show 记录。 */
async function engaged(opts: { reduce?: boolean } = {}) {
	const s = setup(opts);
	s.register();
	await flush();
	s.advance(HEADER_FADE_MS + NATIVE_LAG_MS);
	expect(s.header.active()).toBe(true);
	s.api.show.mockClear();
	s.api.set.mockClear();
	return s;
}

describe("控制器", () => {
	it("theme / lead / title 到齐只 set 一次；应答后接管并淡入，淡入完再晚 34ms 才算显示稳了", async () => {
		const s = setup();
		const sub = vi.fn();
		s.header.subscribe(sub);
		s.header.setPart("theme", s.p.theme);
		s.header.setPart("lead", s.p.lead);
		expect(s.api.set).not.toHaveBeenCalled();
		s.header.setPart("title", s.p.title);
		expect(s.header.engaged()).toBe(false);
		await flush();
		expect(s.api.set).toHaveBeenCalledTimes(1);
		expect(s.header.engaged()).toBe(true);
		expect(sub).toHaveBeenCalled();
		expect(s.shows()).toEqual([FADE_IN]);
		expect(s.header.active()).toBe(false);
		s.advance(HEADER_FADE_MS + NATIVE_LAG_MS - 1);
		expect(s.header.active()).toBe(false);
		s.advance(1);
		expect(s.header.active()).toBe(true);
		// 内容没变（新对象、新回调）：不重发
		s.header.setPart("title", { ...s.p.title, onSelect: vi.fn() });
		await flush();
		expect(s.api.set).toHaveBeenCalledTimes(1);
	});

	it("减少动效：立即显示（ms 0）", async () => {
		const s = setup({ reduce: true });
		s.register();
		await flush();
		expect(s.shows()).toEqual([REST]);
		s.advance(NATIVE_LAG_MS);
		expect(s.header.active()).toBe(true);
	});

	it("set 被拒：不接管，之后的 setPart 不再调 set", async () => {
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const s = setup({ rejectSet: true });
		s.register();
		await flush();
		expect(s.header.engaged()).toBe(false);
		expect(warn).toHaveBeenCalled();
		s.header.setPart("title", { ...s.p.title, text: "别的" });
		await flush();
		expect(s.api.set).toHaveBeenCalledTimes(1);
		expect(s.api.show).not.toHaveBeenCalled();
	});

	it("cardDrag：不让位；同一帧多次只发最后一次；dx = 进度 × 宽 + 往右速度 × 34，钳在 [0, 宽]", async () => {
		const s = await engaged();
		s.header.cardDrag(0.1, 320, 0);
		s.header.cardDrag(0.25, 320, 0.5);
		expect(s.header.active()).toBe(true);
		expect(s.frames()).toBe(1);
		await flush();
		expect(s.api.show).not.toHaveBeenCalled();
		s.frame();
		await flush();
		// 0.25 × 320 + 0.5 × 34 = 97
		expect(s.shows()).toEqual([dragShow(97)]);
		s.api.show.mockClear();
		// 往左不预判
		s.header.cardDrag(0.5, 320, -2);
		s.frame();
		await flush();
		// 超出侧栏宽钳到宽
		s.header.cardDrag(0.95, 320, 3);
		s.frame();
		await flush();
		expect(s.shows()).toEqual([dragShow(160), dragShow(320)]);
		expect(s.shows().some((x) => x.alpha === 0)).toBe(false);
		expect(s.header.active()).toBe(true);
	});

	it("在途期间合并：至多一条 show 在途，其间的多帧只发最新值", async () => {
		const s = await engaged();
		const pending: (() => void)[] = [];
		s.api.show.mockImplementation(
			() =>
				new Promise<void>((r) => {
					pending.push(r);
				}),
		);
		for (const p of [0.1, 0.2, 0.4]) {
			s.header.cardDrag(p, 320, 0);
			s.frame();
		}
		expect(s.shows()).toEqual([dragShow(32)]);
		pending.shift()?.();
		await flush();
		expect(s.shows()).toEqual([dragShow(32), dragShow(128)]);
	});

	it("cardSettle 打开：不让位，发带终点、时长、缓动与绝对起步时刻的 show（打开早 8ms、不延迟）", async () => {
		const s = await engaged();
		s.advance(1000);
		s.header.cardSettle(true, 320, false);
		expect(s.header.active()).toBe(true);
		await flush();
		expect(s.shows()).toEqual([
			{
				alpha: 1,
				dx: 320,
				passive: true,
				ms: SINK_MS,
				delay: 0,
				ease: [...SINK_EASE],
				at: EPOCH + s.now() + NATIVE_LAG_MS - SYNC_BIAS_MS,
			},
		]);
		// 与契约夹具的字段一致（原生按同一份 HeaderShow 解析）
		expect(Object.keys(s.shows()[0] ?? {}).sort()).toEqual(
			Object.keys(fixture<object>("header-show.json")).sort(),
		);
		// 同一目标、同一宽度：不重发
		s.header.cardSettle(true, 320, false);
		await flush();
		expect(s.api.show).toHaveBeenCalledTimes(1);
	});

	it("cardSettle 关闭：晚 8ms、delay 34；REST_MS 后恢复接触摸；instant 推进 0 即恢复", async () => {
		const s = await engaged();
		s.header.cardSettle(true, 320, false);
		await flush();
		s.api.show.mockClear();
		s.header.cardSettle(false, 320, false);
		await flush();
		expect(s.shows()).toEqual([
			{
				alpha: 1,
				dx: 0,
				passive: true,
				ms: SINK_MS,
				delay: NATIVE_LAG_MS,
				ease: [...SINK_EASE],
				at: EPOCH + s.now() + NATIVE_LAG_MS + SYNC_BIAS_MS,
			},
		]);
		s.api.show.mockClear();
		s.advance(REST_MS - 1);
		await flush();
		expect(s.api.show).not.toHaveBeenCalled();
		s.advance(1);
		await flush();
		expect(s.shows()).toEqual([REST]);
		expect(s.header.active()).toBe(true);

		s.api.show.mockClear();
		s.header.cardSettle(true, 320, true);
		s.header.cardSettle(false, 320, true);
		await flush();
		s.advance(0);
		await flush();
		expect(s.shows()).toEqual([
			{ ...REST, dx: 320, passive: true },
			{ ...REST, passive: true },
			REST,
		]);
	});

	it("cardSettle 带起步时刻：等卡片真正起步再发，at 按实际起步算，REST 同步顺延；期间起拖则作废", async () => {
		const s = await engaged();
		s.header.cardSettle(true, 320, false);
		await flush();
		s.api.show.mockClear();
		let begin: (at: number | null) => void = () => {};
		// 主线程被占住，卡片比预计晚 100ms 起步
		s.header.cardSettle(
			false,
			320,
			false,
			new Promise((r) => {
				begin = r;
			}),
		);
		await flush();
		expect(s.api.show).not.toHaveBeenCalled();
		const late = EPOCH + s.now() + NATIVE_LAG_MS + 100;
		// 100ms 后 transitionrun 才到（卡片按 delay 再晚 34ms 起步）
		s.advance(100);
		await flush();
		expect(s.api.show).not.toHaveBeenCalled();
		begin(late);
		await flush();
		expect(s.shows()).toEqual([
			{
				alpha: 1,
				dx: 0,
				passive: true,
				ms: SINK_MS,
				delay: NATIVE_LAG_MS,
				ease: [...SINK_EASE],
				at: late + SYNC_BIAS_MS,
			},
		]);
		s.api.show.mockClear();
		// 静止 = 起步 + REST_MS - NATIVE_LAG_MS：从 settle 算共晚 100ms，等待的 100ms 不重复计
		s.advance(REST_MS - 1);
		await flush();
		expect(s.api.show).not.toHaveBeenCalled();
		s.advance(1);
		await flush();
		expect(s.shows()).toEqual([REST]);

		// 等起步时刻期间又起拖：迟到的起步时刻不再发吸附
		s.header.cardSettle(true, 320, false);
		await flush();
		s.header.cardSettle(false, 320, false, new Promise((r) => (begin = r)));
		s.header.cardDrag(0.5, 320, 0);
		s.api.show.mockClear();
		begin(EPOCH + s.now());
		await flush();
		expect(s.shows().some((x) => x.dx === 0 && x.ms === SINK_MS)).toBe(false);
	});

	it("cardSettle 等过后确认卡片没有过渡（start 给 null）：原生直接就位，不发 260ms 吸附", async () => {
		const s = await engaged();
		s.header.cardDrag(0.5, 320, 0);
		s.advance(16);
		await flush();
		s.api.show.mockClear();
		let begin: (at: number | null) => void = () => {};
		s.header.cardSettle(false, 320, false, new Promise((r) => (begin = r)));
		s.advance(200);
		begin(null);
		await flush();
		s.advance(0);
		await flush();
		expect(s.shows()).toEqual([{ ...REST, passive: true }, REST]);
	});

	it("松手吸附：取消待发的拖动帧，按吸附发；REST_MS 内再拖不恢复接触摸", async () => {
		const s = await engaged();
		s.header.cardDrag(0.3, 320, 0);
		s.header.cardSettle(false, 320, false);
		expect(s.frames()).toBe(0);
		await flush();
		expect(s.shows().map((x) => [x.dx, x.ms, x.delay])).toEqual([
			[0, SINK_MS, NATIVE_LAG_MS],
		]);
		s.advance(100);
		s.header.cardDrag(0.1, 320, 0);
		s.api.show.mockClear();
		s.advance(REST_MS * 2);
		await flush();
		expect(s.api.show).not.toHaveBeenCalled();
	});

	it("挂载时报关闭位：已经静止，只记宽度不发 show；之后遮挡照常生效", async () => {
		const s = await engaged();
		s.header.cardSettle(false, 320, false);
		s.advance(REST_MS);
		await flush();
		expect(s.api.show).not.toHaveBeenCalled();
		s.header.setCovered(true);
		expect(s.header.active()).toBe(false);
		// 关闭时改了侧栏宽也不发（先排空遮挡那两条在途的 show）
		s.header.setCovered(false);
		await flush();
		s.api.show.mockClear();
		s.header.cardSettle(false, 390, false);
		await flush();
		expect(s.api.show).not.toHaveBeenCalled();
	});

	it("setCovered：true 让位、false 回来；菜单开着时盖住不让位，菜单关了才让位", async () => {
		const s = await engaged();
		s.header.setCovered(true);
		expect(s.header.active()).toBe(false);
		s.header.setCovered(false);
		s.advance(HEADER_FADE_MS + NATIVE_LAG_MS);
		expect(s.header.active()).toBe(true);
		await flush();
		expect(s.shows()).toEqual([HIDE, FADE_IN]);

		s.emit({ op: "menu", key: KEY, menu: "title" });
		s.header.setCovered(true);
		expect(s.header.active()).toBe(true);
		expect(s.header.menu()).toBe("title");
		s.emit({ op: "menu", key: KEY, menu: null });
		expect(s.header.active()).toBe(false);
	});

	it("setCovered：卡片动着或开着时直接忽略（不记录），静止关闭后才生效", async () => {
		const s = await engaged();
		s.header.cardDrag(0.2, 320, 0);
		s.header.setCovered(true);
		expect(s.header.active()).toBe(true);
		s.header.cardSettle(true, 320, false);
		s.header.setCovered(true);
		expect(s.header.active()).toBe(true);
		s.header.cardSettle(false, 320, false);
		s.header.setCovered(true);
		expect(s.header.active()).toBe(true);
		s.advance(REST_MS);
		await flush();
		// 动着时的遮挡没被记下：静止后不会自己让位
		expect(s.header.active()).toBe(true);
		expect(s.shows().some((x) => x.alpha === 0)).toBe(false);
		s.header.setCovered(true);
		expect(s.header.active()).toBe(false);
	});

	it("事件：menu 跟随并在打开时 blur；select 调对应部件的 onSelect 并收起；lead 调 onTap；别的 key 一律忽略", async () => {
		const s = await engaged();
		const sub = vi.fn();
		s.header.subscribe(sub);
		s.emit({ op: "menu", key: KEY, menu: "title" });
		expect(s.header.menu()).toBe("title");
		expect(s.env.blur).toHaveBeenCalledTimes(1);
		expect(sub).toHaveBeenCalledTimes(1);
		s.emit({ op: "select", key: KEY, menu: "title", id: "env/freeze" });
		expect(s.p.title.onSelect).toHaveBeenCalledWith("env/freeze");
		expect(s.header.menu()).toBeNull();
		s.emit({ op: "select", key: KEY, menu: "trail", id: "terminal" });
		expect(s.p.trail.onSelect).toHaveBeenCalledWith("terminal");
		s.emit({ op: "lead", key: KEY });
		expect(s.p.lead.onTap).toHaveBeenCalledTimes(1);

		s.emit({ op: "lead", key: "T-old" });
		s.emit({ op: "menu", key: "T-old", menu: "trail" });
		s.emit({ op: "select", key: "T-old", menu: "title", id: "delete" });
		expect(s.p.lead.onTap).toHaveBeenCalledTimes(1);
		expect(s.header.menu()).toBeNull();
		expect(s.p.title.onSelect).toHaveBeenCalledTimes(1);
	});

	it("换 title.key：重新 set、菜单归 null；撤掉 title：set(null) 且退出接管；再登记回来重新接管并 show", async () => {
		const s = await engaged();
		s.emit({ op: "menu", key: KEY, menu: "title" });
		s.header.setPart("title", { ...s.p.title, key: "T-2" });
		expect(s.header.menu()).toBeNull();
		await flush();
		expect(s.api.set.mock.calls.map((c) => c[0]?.key)).toEqual(["T-2"]);
		// 新 key 的事件生效，旧 key 的不生效
		s.emit({ op: "lead", key: KEY });
		expect(s.p.lead.onTap).not.toHaveBeenCalled();
		s.emit({ op: "lead", key: "T-2" });
		expect(s.p.lead.onTap).toHaveBeenCalledTimes(1);

		s.header.setPart("title", null);
		expect(s.header.engaged()).toBe(false);
		expect(s.header.active()).toBe(false);
		await flush();
		expect(s.api.set.mock.calls.at(-1)).toEqual([null]);
		s.api.show.mockClear();
		s.header.setPart("title", s.p.title);
		await flush();
		expect(s.header.engaged()).toBe(true);
		expect(s.shows()).toEqual([FADE_IN]);
	});

	it("只有侧栏钮（首页，lead 带页名）：theme + lead 即 set（title / trail 为 null）并接管；lead 事件按页名回到 onTap", async () => {
		const s = setup();
		const onTap = vi.fn();
		s.header.setPart("theme", s.p.theme);
		s.header.setPart("lead", { ...s.p.lead, key: "home", onTap });
		await flush();
		expect(s.api.set).toHaveBeenCalledTimes(1);
		const spec = s.api.set.mock.calls[0]?.[0];
		expect(spec?.key).toBe("home");
		expect(spec?.title).toBeNull();
		expect(spec?.trail).toBeNull();
		expect(s.header.engaged()).toBe(true);
		expect(s.shows()).toEqual([FADE_IN]);
		s.emit({ op: "lead", key: "home" });
		expect(onTap).toHaveBeenCalledTimes(1);
		s.emit({ op: "lead", key: KEY });
		expect(onTap).toHaveBeenCalledTimes(1);
	});

	it("首页 → 线程页：线程页的侧栏钮不带页名，胶囊登记前撤掉原生页头（不发缺胶囊的 spec），胶囊到了才发线程的 spec", async () => {
		const s = setup();
		s.header.setPart("theme", s.p.theme);
		s.header.setPart("lead", { ...s.p.lead, key: "home" });
		await flush();
		expect(s.header.engaged()).toBe(true);
		s.header.setPart("lead", s.p.lead);
		expect(s.header.engaged()).toBe(false);
		await flush();
		// 视图钮先到也不发（还缺胶囊）；测试环境的 microtask 是同步的，每次登记都合成一次
		s.header.setPart("trail", s.p.trail);
		s.header.setPart("title", s.p.title);
		await flush();
		const sent = s.api.set.mock.calls.map((c) => c[0]);
		expect(sent.map((spec) => spec?.key ?? null)).toEqual(["home", null, KEY]);
		expect(sent.at(-1)?.title?.text).toBe("修复登录回调");
		expect(s.header.engaged()).toBe(true);
	});

	it("available：有宿主时为真，set 被拒后为假并通知订阅者；宿主为 null 时为假", async () => {
		vi.spyOn(console, "warn").mockImplementation(() => {});
		const s = setup({ rejectSet: true });
		expect(s.header.available()).toBe(true);
		const sub = vi.fn();
		s.header.subscribe(sub);
		s.register();
		await flush();
		expect(s.header.available()).toBe(false);
		expect(sub).toHaveBeenCalled();
		const { env } = fakeEnv();
		expect(createNativeHeader(null, () => () => {}, env).available()).toBe(
			false,
		);
	});

	it("closeMenu：菜单开着时调 api.menu({open: null})，没开时不调", async () => {
		const s = await engaged();
		s.header.closeMenu();
		expect(s.api.menu).not.toHaveBeenCalled();
		s.emit({ op: "menu", key: KEY, menu: "trail" });
		s.header.closeMenu();
		expect(s.header.menu()).toBeNull();
		expect(s.api.menu.mock.calls).toEqual([[{ open: null }]]);
	});

	it("宿主为 null 时全部空操作", () => {
		const { env, frame } = fakeEnv();
		const h = createNativeHeader(null, () => () => {}, env);
		const p = parts();
		expect(() => {
			h.setPart("theme", p.theme);
			h.setPart("lead", p.lead);
			h.setPart("title", p.title);
			h.cardDrag(0.5, 320, 0);
			frame();
			h.cardSettle(true, 320, false);
			h.cardSettle(false, 320, false);
			h.setCovered(true);
			h.closeMenu();
		}).not.toThrow();
		expect(h.engaged()).toBe(false);
		expect(h.active()).toBe(false);
		expect(h.menu()).toBeNull();
	});
});

describe("useSlotFrame", () => {
	it("相对所在下沉卡片的左上角、两位小数；宽高为 0 时为 null；on 为假时为 null", () => {
		const card = document.createElement("div");
		card.className = "sink-card";
		const el = document.createElement("button");
		card.append(el);
		document.body.append(card);
		vi.spyOn(card, "getBoundingClientRect").mockReturnValue({
			left: 300,
			top: 0,
		} as DOMRect);
		const rect = vi.spyOn(el, "getBoundingClientRect").mockReturnValue({
			left: 312.004,
			top: 68.336,
			width: 44,
			height: 44,
		} as DOMRect);
		const ref = { current: el };
		const { result, rerender } = renderHook(
			({ on }: { on: boolean }) => useSlotFrame(ref, on),
			{ initialProps: { on: true } },
		);
		expect(result.current).toEqual({ x: 12, y: 68.34, width: 44, height: 44 });
		rerender({ on: false });
		expect(result.current).toBeNull();
		rect.mockReturnValue({ left: 0, top: 0, width: 0, height: 0 } as DOMRect);
		rerender({ on: true });
		expect(result.current).toBeNull();
		card.remove();
	});
});
