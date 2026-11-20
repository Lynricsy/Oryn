/** 原生底栏控制器：spec 生成、遮挡探测、头像栅格化、接管 / 回退 / 去重 / 在途合并，以及常量与 CSS 不漂移。 */
import {
	afterEach,
	beforeAll,
	describe,
	expect,
	it,
	type Mock,
	vi,
} from "vitest";
import type { NativeTabBarReveal, NativeTabBarSpec } from "./host";
import {
	buildTabBarSpec,
	createNativeTabBar,
	type NativeTabBar,
	rasterizeAvatar,
	SINK_EASE,
	SINK_MS,
	slotCovered,
	type TabBarSpecInput,
} from "./native-tab-bar";

// 前端 tsconfig 不带 Node 类型（静态导入 node:fs 过不了 tsc），只声明用到的函数（同 SectionPanes.test）；工作目录是 web/
let readFile: (path: string) => string;
beforeAll(async () => {
	const fs = (await import("node:fs" as string)) as {
		readFileSync(path: string, encoding: "utf8"): string;
	};
	readFile = (path) => fs.readFileSync(path, "utf8");
});

const FIXTURES = "src-tauri/plugins/oryn-mobile/fixtures";

/** N1 夹具对应的输入。 */
const INPUT: TabBarSpecInput = {
	labels: {
		threads: "线程",
		assistant: "小助手",
		projects: "项目",
		more: "更多",
	},
	selected: "threads",
	frame: { x: 12, y: 758, width: 296, height: 52 },
	dark: false,
	accent: "#2f6fed",
	bg: "#f7f6f3",
	assistant: { image: null, glyph: "🦊", unread: true },
	conn: "warn",
};

const SPEC = buildTabBarSpec(INPUT);

/** 排空宏任务前的全部微任务（latest 的 then / finally 链），不等真实时长。 */
const flush = () => new Promise((r) => setTimeout(r, 0));

interface FakeHost {
	set: Mock<(spec: NativeTabBarSpec | null) => Promise<void>>;
	reveal: Mock<(r: NativeTabBarReveal) => Promise<void>>;
}

function fakeHost(opts: { rejectSet?: boolean } = {}): FakeHost {
	return {
		set: vi.fn(async (_spec: NativeTabBarSpec | null) => {
			if (opts.rejectSet) throw "unsupported";
		}),
		reveal: vi.fn(async (_r: NativeTabBarReveal) => {}),
	};
}

/** 建控制器并接管（侧栏关着）；清掉接管时补发的那条 reveal。 */
async function engagedBar() {
	const h = fakeHost();
	const bar = createNativeTabBar(h);
	bar.setSpec(SPEC);
	await flush();
	expect(bar.engaged()).toBe(true);
	h.reveal.mockClear();
	return { h, bar };
}

const reveals = (h: FakeHost) => h.reveal.mock.calls.map((c) => c[0]);

afterEach(() => {
	vi.restoreAllMocks();
	vi.unstubAllGlobals();
});

describe("buildTabBarSpec", () => {
	it("N1 输入 → 与契约夹具 tab-bar-set.json 逐字相等", () => {
		expect(SPEC).toEqual(JSON.parse(readFile(`${FIXTURES}/tab-bar-set.json`)));
	});

	it("没有角标的项不带 badge 键；助手图标按 image → glyph → sparkles 取", () => {
		const plain = buildTabBarSpec({
			...INPUT,
			assistant: { image: null, glyph: null, unread: false },
			conn: null,
		});
		for (const item of plain.items) expect("badge" in item).toBe(false);
		expect(plain.items[1]?.icon).toEqual({ symbol: "sparkles" });
		const img = buildTabBarSpec({
			...INPUT,
			assistant: { image: "QUJD", glyph: "🦊", unread: false },
			conn: "err",
		});
		expect(img.items[1]?.icon).toEqual({ image: "QUJD" });
		expect(img.items[3]?.badge).toBe("err");
	});
});

describe("控制器", () => {
	it("接管：set 首次成功才算接管，立刻补发当前卡片左缘与遮挡；接管前的 reveal 不发；同一 spec 不重发", async () => {
		const h = fakeHost();
		const bar = createNativeTabBar(h);
		const sub = vi.fn();
		bar.subscribe(sub);
		bar.reveal(320);
		bar.setCovered(true);
		expect(h.reveal).not.toHaveBeenCalled();
		bar.setSpec(SPEC);
		expect(bar.engaged()).toBe(false);
		await flush();
		expect(h.set.mock.calls).toEqual([[SPEC]]);
		expect(bar.engaged()).toBe(true);
		expect(sub).toHaveBeenCalledTimes(1);
		expect(reveals(h)).toEqual([{ x: 320, covered: true }]);
		bar.setSpec(structuredClone(SPEC));
		await flush();
		expect(h.set).toHaveBeenCalledTimes(1);
	});

	it("接管时侧栏关着、无遮挡：补发 {x: 0, covered: false}", async () => {
		const h = fakeHost();
		const bar = createNativeTabBar(h);
		bar.setSpec(SPEC);
		await flush();
		expect(reveals(h)).toEqual([{ x: 0, covered: false }]);
	});

	it("拒绝回退：set 被拒后本页永久退回网页底栏，不再调用宿主", async () => {
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const h = fakeHost({ rejectSet: true });
		const bar = createNativeTabBar(h);
		const sub = vi.fn();
		bar.subscribe(sub);
		bar.setSpec(SPEC);
		await flush();
		expect(bar.engaged()).toBe(false);
		expect(sub).toHaveBeenCalled();
		expect(warn).toHaveBeenCalled();
		bar.setSpec({ ...SPEC, selected: "more" });
		bar.setSpec(null);
		bar.reveal(320);
		bar.setCovered(true);
		await flush();
		expect(h.set).toHaveBeenCalledTimes(1);
		expect(h.reveal).not.toHaveBeenCalled();
	});

	it("reveal 被拒只 warn，不影响接管", async () => {
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const { h, bar } = await engagedBar();
		h.reveal.mockRejectedValueOnce("invalid_reveal");
		bar.reveal(320);
		await flush();
		expect(warn).toHaveBeenCalled();
		expect(bar.engaged()).toBe(true);
	});

	it("打开吸附终点 = reveal 夹具；关闭回到 0", async () => {
		const { h, bar } = await engagedBar();
		bar.reveal(320);
		await flush();
		expect(reveals(h)).toEqual([
			JSON.parse(readFile(`${FIXTURES}/tab-bar-reveal.json`)),
		]);
		h.reveal.mockClear();
		bar.reveal(0);
		await flush();
		expect(reveals(h)).toEqual([{ x: 0, covered: false }]);
	});

	it("去重：同一 {x, covered} 不重发；任一字段变了照发", async () => {
		const { h, bar } = await engagedBar();
		bar.reveal(0);
		bar.setCovered(false);
		await flush();
		expect(h.reveal).not.toHaveBeenCalled();
		bar.reveal(320);
		await flush();
		bar.reveal(320);
		bar.setCovered(true);
		await flush();
		bar.setCovered(true);
		bar.reveal(0);
		await flush();
		expect(reveals(h)).toEqual([
			{ x: 320, covered: false },
			{ x: 320, covered: true },
			{ x: 0, covered: true },
		]);
	});

	it("setCovered 不影响接管（网页胶囊不换回来）", async () => {
		const { bar } = await engagedBar();
		const sub = vi.fn();
		bar.subscribe(sub);
		bar.setCovered(true);
		expect(bar.engaged()).toBe(true);
		expect(sub).not.toHaveBeenCalled();
	});

	it("在途期间合并：至多一条在途，其间的多次只发最新值", async () => {
		const { h, bar } = await engagedBar();
		const pending: (() => void)[] = [];
		h.reveal.mockImplementation(
			() =>
				new Promise<void>((r) => {
					pending.push(r);
				}),
		);
		bar.reveal(32);
		bar.reveal(64);
		bar.reveal(128);
		expect(reveals(h)).toEqual([{ x: 32, covered: false }]);
		pending.shift()?.();
		await flush();
		expect(reveals(h)).toEqual([
			{ x: 32, covered: false },
			{ x: 128, covered: false },
		]);
		pending.shift()?.();
		await flush();
		expect(h.reveal).toHaveBeenCalledTimes(2);
	});

	it("setSpec(null)：立即退出接管并撤栏；撤栏期间不发 reveal；再 set 重新接管并补发当前值", async () => {
		const { h, bar } = await engagedBar();
		const sub = vi.fn();
		bar.subscribe(sub);
		bar.setSpec(null);
		expect(bar.engaged()).toBe(false);
		expect(sub).toHaveBeenCalledTimes(1);
		await flush();
		expect(h.set.mock.calls.at(-1)).toEqual([null]);
		bar.reveal(320);
		await flush();
		expect(h.reveal).not.toHaveBeenCalled();
		bar.setSpec(SPEC);
		await flush();
		expect(bar.engaged()).toBe(true);
		expect(reveals(h)).toEqual([{ x: 320, covered: false }]);
	});

	it("撤栏清空去重：再接管时与撤栏前相同的值也照发", async () => {
		const { h, bar } = await engagedBar();
		bar.reveal(320);
		await flush();
		h.reveal.mockClear();
		bar.setSpec(null);
		await flush();
		bar.setSpec(SPEC);
		await flush();
		expect(reveals(h)).toEqual([{ x: 320, covered: false }]);
	});

	it("宿主为 null 时全部空操作", () => {
		const bar: NativeTabBar = createNativeTabBar(null);
		expect(() => {
			bar.setSpec(SPEC);
			bar.reveal(320);
			bar.setCovered(true);
			bar.setCovered(false);
			bar.setSpec(null);
		}).not.toThrow();
		expect(bar.engaged()).toBe(false);
	});
});

describe("slotCovered", () => {
	function setup(vv: { height: number; offsetTop: number } | null = null) {
		const slot = document.createElement("div");
		const inner = document.createElement("button");
		slot.append(inner);
		document.body.append(slot);
		const rect = { left: 12, top: 758, width: 296, height: 52 };
		vi.spyOn(slot, "getBoundingClientRect").mockImplementation(
			() => rect as DOMRect,
		);
		const hit = vi.fn((_x: number, _y: number): Element | null => inner);
		const win = {
			innerHeight: 852,
			visualViewport: vv,
			document: { elementFromPoint: hit },
		} as unknown as Window;
		return { slot, inner, rect, hit, win };
	}

	afterEach(() => {
		document.body.replaceChildren();
	});

	it("三点都落在胶囊内 → 未遮挡；取点在竖直中线的 15% / 50% / 85%", () => {
		const { slot, hit, win } = setup({ height: 852, offsetTop: 0 });
		expect(slotCovered(slot, win)).toBe(false);
		const pts = hit.mock.calls.map(([x, y]) => [Math.round(x * 10) / 10, y]);
		expect(pts).toEqual([
			[56.4, 784],
			[160, 784],
			[263.6, 784],
		]);
	});

	it("任一点是别的元素或为空 → 遮挡", () => {
		const { slot, hit, win } = setup();
		const other = document.createElement("div");
		document.body.append(other);
		hit.mockImplementation((x) => (x > 200 ? other : slot));
		expect(slotCovered(slot, win)).toBe(true);
		hit.mockImplementation((x) => (x < 100 ? null : slot));
		expect(slotCovered(slot, win)).toBe(true);
	});

	it("软键盘弹出或矩形为空 → 遮挡", () => {
		const kb = setup({ height: 500, offsetTop: 0 });
		expect(slotCovered(kb.slot, kb.win)).toBe(true);
		expect(kb.hit).not.toHaveBeenCalled();
		const empty = setup();
		empty.rect.width = 0;
		expect(slotCovered(empty.slot, empty.win)).toBe(true);
	});

	it("判定为遮挡时（调试构建）记下命中的元素与取点", () => {
		const debug = vi.spyOn(console, "debug").mockImplementation(() => {});
		const { slot, hit, win } = setup();
		const other = document.createElement("div");
		hit.mockReturnValue(other);
		expect(slotCovered(slot, win)).toBe(true);
		expect(debug).toHaveBeenCalledWith("[host] native tab bar covered", {
			x: expect.closeTo(56.4, 1),
			y: 784,
			hit: other,
		});
		debug.mockClear();
		hit.mockImplementation(() => slot);
		expect(slotCovered(slot, win)).toBe(false);
		expect(debug).not.toHaveBeenCalled();
	});
});

describe("rasterizeAvatar", () => {
	class FakeImage {
		onload: (() => void) | null = null;
		onerror: (() => void) | null = null;
		naturalWidth = 120;
		naturalHeight = 80;
		set src(v: string) {
			queueMicrotask(() => (v === "bad" ? this.onerror?.() : this.onload?.()));
		}
	}

	it("居中裁成方形并缩到 60×60，返回不带前缀的 base64", async () => {
		vi.stubGlobal("Image", FakeImage);
		const ctx = { drawImage: vi.fn() };
		vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(
			ctx as unknown as CanvasRenderingContext2D,
		);
		vi.spyOn(HTMLCanvasElement.prototype, "toDataURL").mockReturnValue(
			"data:image/png;base64,QUJD",
		);
		expect(await rasterizeAvatar("data:image/png;base64,eA==")).toBe("QUJD");
		expect(ctx.drawImage.mock.calls[0]?.slice(1)).toEqual([
			20, 0, 80, 80, 0, 0, 60, 60,
		]);
	});

	it("加载失败或拿不到画布 → null", async () => {
		vi.stubGlobal("Image", FakeImage);
		expect(await rasterizeAvatar("bad")).toBeNull();
		vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(null);
		expect(await rasterizeAvatar("data:image/png;base64,eA==")).toBeNull();
	});
});

describe("常量与 index.css 不漂移", () => {
	it("--ease、260ms 开合；原生底栏接管时页面透明（底色交给原生垫底）", () => {
		const css = readFile("src/index.css");
		expect(css).toContain(`--ease: cubic-bezier(${SINK_EASE.join(", ")});`);
		expect(css).toContain(`translate ${SINK_MS}ms var(--ease)`);
		const at = css.indexOf(":root[data-native-under] body");
		expect(at).toBeGreaterThan(-1);
		expect(css.slice(at, css.indexOf("}", at))).toContain(
			"background: transparent;",
		);
	});
});
