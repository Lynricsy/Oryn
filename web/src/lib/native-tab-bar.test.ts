/** 原生底栏控制器：spec 生成、遮挡探测与网页胶囊的交接、头像栅格化、接管 / 回退 / 去重 / 在途合并。 */
import {
	afterEach,
	beforeAll,
	describe,
	expect,
	it,
	type Mock,
	vi,
} from "vitest";
import type { NativeTabBarSpec } from "./host";
import type { SinkGate } from "./native-sink";
import {
	buildTabBarSpec,
	createNativeTabBar,
	type NativeTabBar,
	rasterizeAvatar,
	slotCovered,
	type TabBarSpecInput,
	UNCOVER_MS,
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
	assistant: { image: null, glyph: "🦊", unread: true },
	conn: "warn",
};

const SPEC = buildTabBarSpec(INPUT);

/** 排空宏任务前的全部微任务（latest 的 then / finally 链），不等真实时长。 */
const flush = () => new Promise((r) => setTimeout(r, 0));

interface FakeHost {
	set: Mock<(spec: NativeTabBarSpec | null) => Promise<void>>;
	cover: Mock<(covered: boolean) => Promise<void>>;
}

function fakeHost(opts: { rejectSet?: boolean } = {}): FakeHost {
	return {
		set: vi.fn(async (_spec: NativeTabBarSpec | null) => {
			if (opts.rejectSet) throw "unsupported";
		}),
		cover: vi.fn(async (_covered: boolean) => {}),
	};
}

/** 可控的根滚动接管：`flip` 切换并通知订阅者。 */
interface FakeSink extends SinkGate {
	flip(on: boolean): void;
}

function fakeSink(on = true): FakeSink {
	let engaged = on;
	const subs = new Set<() => void>();
	return {
		engaged: () => engaged,
		subscribe(cb) {
			subs.add(cb);
			return () => {
				subs.delete(cb);
			};
		},
		flip(next) {
			engaged = next;
			for (const cb of [...subs]) cb();
		},
	};
}

/** 假计时：`advance` 推进到点的回调（UNCOVER_MS）。 */
function fakeTimers() {
	let now = 0;
	let next = 1;
	const pending = new Map<number, { at: number; cb: () => void }>();
	return {
		setTimeout(cb: () => void, ms: number) {
			const id = next++;
			pending.set(id, { at: now + ms, cb });
			return id;
		},
		clearTimeout(id: number) {
			pending.delete(id);
		},
		advance(ms: number) {
			now += ms;
			for (const [id, t] of [...pending]) {
				if (t.at > now) continue;
				pending.delete(id);
				t.cb();
			}
		},
	};
}

/** 建控制器并接管（根滚动已接管、无遮挡）；清掉接管时补发的那条 cover。 */
async function engagedBar() {
	const h = fakeHost();
	const sink = fakeSink();
	const timers = fakeTimers();
	const bar = createNativeTabBar(h, sink, timers);
	bar.setSpec(SPEC);
	await flush();
	expect(bar.engaged()).toBe(true);
	h.cover.mockClear();
	return { h, sink, bar, timers };
}

const covers = (h: FakeHost) => h.cover.mock.calls.map((c) => c[0]);

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
	it("接管：set 首次成功才算接管，立刻补发当前遮挡；接管前的遮挡不发；同一 spec 不重发", async () => {
		const h = fakeHost();
		const bar = createNativeTabBar(h, fakeSink());
		const sub = vi.fn();
		bar.subscribe(sub);
		bar.setCovered(true);
		expect(h.cover).not.toHaveBeenCalled();
		bar.setSpec(SPEC);
		expect(bar.engaged()).toBe(false);
		await flush();
		expect(h.set.mock.calls).toEqual([[SPEC]]);
		expect(bar.engaged()).toBe(true);
		expect(sub).toHaveBeenCalledTimes(1);
		expect(covers(h)).toEqual([true]);
		bar.setSpec(structuredClone(SPEC));
		await flush();
		expect(h.set).toHaveBeenCalledTimes(1);
	});

	it("接管时无遮挡：补发 cover(false)", async () => {
		const h = fakeHost();
		const bar = createNativeTabBar(h, fakeSink());
		bar.setSpec(SPEC);
		await flush();
		expect(covers(h)).toEqual([false]);
	});

	it("根滚动未接管：setSpec 不调 set；接管后补发想要的 spec 并接管", async () => {
		const h = fakeHost();
		const sink = fakeSink(false);
		const bar = createNativeTabBar(h, sink);
		bar.setSpec(SPEC);
		bar.setCovered(true);
		await flush();
		expect(h.set).not.toHaveBeenCalled();
		expect(h.cover).not.toHaveBeenCalled();
		expect(bar.engaged()).toBe(false);
		sink.flip(true);
		await flush();
		expect(h.set.mock.calls).toEqual([[SPEC]]);
		expect(bar.engaged()).toBe(true);
		expect(covers(h)).toEqual([true]);
	});

	it("根滚动撤掉：立即退出接管并发 set(null)；再接管时重发 spec", async () => {
		const { h, sink, bar } = await engagedBar();
		const sub = vi.fn();
		bar.subscribe(sub);
		sink.flip(false);
		expect(bar.engaged()).toBe(false);
		expect(sub).toHaveBeenCalledTimes(1);
		await flush();
		expect(h.set.mock.calls.at(-1)).toEqual([null]);
		bar.setSpec({ ...SPEC, selected: "more" });
		await flush();
		expect(h.set.mock.calls.at(-1)).toEqual([null]);
		sink.flip(true);
		await flush();
		expect(h.set.mock.calls.at(-1)).toEqual([{ ...SPEC, selected: "more" }]);
		expect(bar.engaged()).toBe(true);
	});

	it("拒绝回退：set 被拒后本页永久退回网页底栏，不再调用宿主", async () => {
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const h = fakeHost({ rejectSet: true });
		const sink = fakeSink();
		const bar = createNativeTabBar(h, sink);
		const sub = vi.fn();
		bar.subscribe(sub);
		bar.setSpec(SPEC);
		await flush();
		expect(bar.engaged()).toBe(false);
		expect(sub).toHaveBeenCalled();
		expect(warn).toHaveBeenCalled();
		bar.setSpec({ ...SPEC, selected: "more" });
		bar.setSpec(null);
		bar.setCovered(true);
		sink.flip(false);
		sink.flip(true);
		await flush();
		expect(h.set).toHaveBeenCalledTimes(1);
		expect(h.cover).not.toHaveBeenCalled();
	});

	it("cover 被拒只 warn，不影响接管", async () => {
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const { h, bar } = await engagedBar();
		h.cover.mockRejectedValueOnce("invalid_cover");
		bar.setCovered(true);
		await flush();
		expect(warn).toHaveBeenCalled();
		expect(bar.engaged()).toBe(true);
	});

	it("遮挡的载荷 = cover 夹具", async () => {
		const { h, bar } = await engagedBar();
		bar.setCovered(true);
		await flush();
		expect(covers(h).map((covered) => ({ covered }))).toEqual([
			JSON.parse(readFile(`${FIXTURES}/tab-bar-cover.json`)),
		]);
	});

	it("去重：setCovered(true) 两次只发一条 cover(true)；值变了照发", async () => {
		const { h, bar } = await engagedBar();
		bar.setCovered(false);
		await flush();
		expect(h.cover).not.toHaveBeenCalled();
		bar.setCovered(true);
		await flush();
		bar.setCovered(true);
		await flush();
		expect(covers(h)).toEqual([true]);
		bar.setCovered(false);
		await flush();
		expect(covers(h)).toEqual([true, false]);
	});

	it("盖住：网页胶囊立即顶上（shown 为假）；让开：网页胶囊 UNCOVER_MS 后才撤，接管不变", async () => {
		const { bar, timers } = await engagedBar();
		const sub = vi.fn();
		bar.subscribe(sub);
		expect(bar.shown()).toBe(true);
		bar.setCovered(true);
		expect(bar.shown()).toBe(false);
		expect(bar.engaged()).toBe(true);
		expect(sub).toHaveBeenCalled();
		bar.setCovered(false);
		timers.advance(UNCOVER_MS - 1);
		expect(bar.shown()).toBe(false);
		timers.advance(1);
		expect(bar.shown()).toBe(true);
	});

	it("让开后的重叠期内又被盖住：网页胶囊不撤", async () => {
		const { bar, timers } = await engagedBar();
		bar.setCovered(true);
		bar.setCovered(false);
		timers.advance(UNCOVER_MS / 2);
		bar.setCovered(true);
		timers.advance(UNCOVER_MS * 2);
		expect(bar.shown()).toBe(false);
	});

	it("接管时已被盖住：网页胶囊留着，让开后才撤", async () => {
		const h = fakeHost();
		const timers = fakeTimers();
		const bar = createNativeTabBar(h, fakeSink(), timers);
		bar.setCovered(true);
		bar.setSpec(SPEC);
		await flush();
		expect(bar.engaged()).toBe(true);
		expect(bar.shown()).toBe(false);
		bar.setCovered(false);
		timers.advance(UNCOVER_MS);
		expect(bar.shown()).toBe(true);
	});

	it("在途期间合并：至多一条在途，其间的多次只发最新值", async () => {
		const { h, bar } = await engagedBar();
		const pending: (() => void)[] = [];
		h.cover.mockImplementation(
			() =>
				new Promise<void>((r) => {
					pending.push(r);
				}),
		);
		bar.setCovered(true);
		bar.setCovered(false);
		bar.setCovered(true);
		bar.setCovered(false);
		expect(covers(h)).toEqual([true]);
		pending.shift()?.();
		await flush();
		expect(covers(h)).toEqual([true, false]);
		pending.shift()?.();
		await flush();
		expect(h.cover).toHaveBeenCalledTimes(2);
	});

	it("setSpec(null)：立即退出接管并撤栏；撤栏期间不发 cover；再 set 重新接管并补发当前值", async () => {
		const { h, bar } = await engagedBar();
		const sub = vi.fn();
		bar.subscribe(sub);
		bar.setSpec(null);
		expect(bar.engaged()).toBe(false);
		expect(sub).toHaveBeenCalledTimes(1);
		await flush();
		expect(h.set.mock.calls.at(-1)).toEqual([null]);
		bar.setCovered(true);
		await flush();
		expect(h.cover).not.toHaveBeenCalled();
		bar.setSpec(SPEC);
		await flush();
		expect(bar.engaged()).toBe(true);
		expect(covers(h)).toEqual([true]);
	});

	it("撤栏清空去重：再接管时与撤栏前相同的值也照发", async () => {
		const { h, bar } = await engagedBar();
		bar.setCovered(true);
		await flush();
		h.cover.mockClear();
		bar.setSpec(null);
		await flush();
		bar.setSpec(SPEC);
		await flush();
		expect(covers(h)).toEqual([true]);
	});

	it("宿主为 null 时全部空操作", () => {
		const sink = fakeSink();
		const bar: NativeTabBar = createNativeTabBar(null, sink);
		expect(() => {
			bar.setSpec(SPEC);
			bar.setCovered(true);
			bar.setCovered(false);
			sink.flip(false);
			sink.flip(true);
			bar.setSpec(null);
		}).not.toThrow();
		expect(bar.engaged()).toBe(false);
	});
});

describe("slotCovered", () => {
	function setup(
		vv: { height: number; offsetTop: number } | null = null,
		innerHeight = 852,
	) {
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
			innerHeight,
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

	it("innerHeight 随键盘缩、胶囊沉到视觉视口底边以下 → 遮挡", () => {
		const kb = setup({ height: 478, offsetTop: 0 }, 478);
		expect(slotCovered(kb.slot, kb.win)).toBe(true);
		expect(kb.hit).not.toHaveBeenCalled();
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
