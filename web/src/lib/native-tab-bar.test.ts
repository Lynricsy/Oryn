/**
 * 原生底栏控制器：spec 生成、遮挡探测、头像栅格化；接管 / 回退 / 撤栏 / 在途合并；吸附终点（按绝对起步时刻插值）、
 * 拖动逐帧（合帧、向左预判）、遮挡让位（网页胶囊立即顶上、晚撤），以及常量与 CSS 不漂移。
 */
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
	NATIVE_LAG_MS,
	type NativeTabBar,
	rasterizeAvatar,
	SINK_EASE,
	SINK_MS,
	SYNC_BIAS_MS,
	slotCovered,
	type TabBarEnv,
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

/** 排空宏任务前的全部微任务（latest 的 then / finally 链、start 的 then），不等真实时长。 */
const flush = () => new Promise((r) => setTimeout(r, 0));

/** 绝对时钟的起点（Unix 毫秒）：此刻打开到 320 的 at = EPOCH + NATIVE_LAG_MS + SYNC_BIAS_MS = 夹具的 at。 */
const EPOCH = 1_795_000_000_000;

/** 可手动推进的假定时器、手动跑的假帧，钟从 EPOCH 起走。 */
function fakeEnv() {
	let now = 0;
	let n = 0;
	const timers = new Map<number, { at: number; cb: () => void }>();
	const frames = new Map<number, () => void>();
	const env: TabBarEnv = {
		setTimeout(cb, ms) {
			timers.set(++n, { at: now + ms, cb });
			return n;
		},
		clearTimeout(id) {
			timers.delete(id);
		},
		frame(cb) {
			frames.set(++n, cb);
			return n;
		},
		cancelFrame(id) {
			frames.delete(id);
		},
		now: () => EPOCH + now,
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
	return { env, advance, frame, frames: () => frames.size, now: env.now };
}

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
	const s = fakeEnv();
	const h = fakeHost();
	const bar = createNativeTabBar(h, s.env);
	bar.setSpec(SPEC);
	await flush();
	expect(bar.engaged()).toBe(true);
	h.reveal.mockClear();
	return { h, bar, s };
}

/** 手动兑现的起步时刻（tsconfig 的 lib 不含 Promise.withResolvers）。 */
function deferred() {
	let resolve: (at: number | null) => void = () => {};
	const promise = new Promise<number | null>((r) => {
		resolve = r;
	});
	return { promise, resolve };
}

const reveals = (h: FakeHost) => h.reveal.mock.calls.map((c) => c[0]);

/** 原生按卡片的时长与缓动从 `at` 起插值到 `x`（侧栏无遮挡）。 */
const timed = (x: number, at: number): NativeTabBarReveal => ({
	x,
	covered: false,
	ms: SINK_MS,
	ease: [...SINK_EASE],
	at,
});

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

describe("控制器：接管、回退与撤栏", () => {
	it("接管：set 首次成功才算接管，立刻补发当前卡片左缘与遮挡（ms 0）；接管前的吸附与遮挡不发；同一 spec 不重发", async () => {
		const s = fakeEnv();
		const h = fakeHost();
		const bar = createNativeTabBar(h, s.env);
		const sub = vi.fn();
		bar.subscribe(sub);
		bar.settle(true, 320, false);
		bar.setCovered(true);
		expect(h.reveal).not.toHaveBeenCalled();
		bar.setSpec(SPEC);
		expect(bar.engaged()).toBe(false);
		await flush();
		expect(h.set.mock.calls).toEqual([[SPEC]]);
		expect(bar.engaged()).toBe(true);
		expect(sub).toHaveBeenCalled();
		expect(reveals(h)).toEqual([{ x: 320, covered: true, ms: 0 }]);
		// 接管那一刻就被盖住：网页胶囊留着
		expect(bar.shown()).toBe(false);
		bar.setSpec(structuredClone(SPEC));
		await flush();
		expect(h.set).toHaveBeenCalledTimes(1);
	});

	it("接管时侧栏关着、无遮挡：补发 {x: 0, covered: false, ms: 0}，原生看得见（网页胶囊隐藏）", async () => {
		const s = fakeEnv();
		const h = fakeHost();
		const bar = createNativeTabBar(h, s.env);
		expect(bar.shown()).toBe(false);
		bar.setSpec(SPEC);
		await flush();
		expect(reveals(h)).toEqual([{ x: 0, covered: false, ms: 0 }]);
		expect(bar.shown()).toBe(true);
	});

	it("拒绝回退：set 被拒后本页永久退回网页底栏（网页胶囊可见），不再调用宿主", async () => {
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const s = fakeEnv();
		const h = fakeHost({ rejectSet: true });
		const bar = createNativeTabBar(h, s.env);
		const sub = vi.fn();
		bar.subscribe(sub);
		bar.setSpec(SPEC);
		await flush();
		expect(bar.engaged()).toBe(false);
		expect(bar.shown()).toBe(false);
		expect(sub).toHaveBeenCalled();
		expect(warn).toHaveBeenCalled();
		bar.setSpec({ ...SPEC, selected: "more" });
		bar.setSpec(null);
		bar.settle(true, 320, false);
		bar.drag(0.5, 320, 0);
		s.frame();
		bar.setCovered(true);
		await flush();
		expect(h.set).toHaveBeenCalledTimes(1);
		expect(h.reveal).not.toHaveBeenCalled();
	});

	it("reveal 被拒只 warn，不影响接管", async () => {
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const { h, bar } = await engagedBar();
		h.reveal.mockRejectedValueOnce("invalid_reveal");
		bar.settle(true, 320, true);
		await flush();
		expect(warn).toHaveBeenCalled();
		expect(bar.engaged()).toBe(true);
		expect(bar.shown()).toBe(true);
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
		bar.settle(true, 320, true);
		bar.settle(false, 320, true);
		bar.settle(true, 300, true);
		expect(reveals(h)).toEqual([{ x: 320, covered: false, ms: 0 }]);
		pending.shift()?.();
		await flush();
		expect(reveals(h)).toEqual([
			{ x: 320, covered: false, ms: 0 },
			{ x: 300, covered: false, ms: 0 },
		]);
		pending.shift()?.();
		await flush();
		expect(h.reveal).toHaveBeenCalledTimes(2);
	});

	it("setSpec(null)：立即退出接管、网页胶囊回来并撤栏；撤栏期间不发 reveal；再 set 重新接管并补发当前卡片左缘", async () => {
		const { h, bar } = await engagedBar();
		bar.settle(true, 320, true);
		await flush();
		h.reveal.mockClear();
		const sub = vi.fn();
		bar.subscribe(sub);
		bar.setSpec(null);
		expect(bar.engaged()).toBe(false);
		expect(bar.shown()).toBe(false);
		expect(sub).toHaveBeenCalled();
		await flush();
		expect(h.set.mock.calls.at(-1)).toEqual([null]);
		bar.settle(false, 320, true);
		await flush();
		expect(h.reveal).not.toHaveBeenCalled();
		bar.setSpec(SPEC);
		await flush();
		expect(bar.engaged()).toBe(true);
		expect(bar.shown()).toBe(true);
		expect(reveals(h)).toEqual([{ x: 0, covered: false, ms: 0 }]);
	});

	it("宿主为 null 时全部空操作", () => {
		const s = fakeEnv();
		const bar: NativeTabBar = createNativeTabBar(null, s.env);
		expect(() => {
			bar.setSpec(SPEC);
			bar.settle(true, 320, false);
			bar.drag(0.5, 320, -1);
			s.frame();
			bar.setCovered(true);
			bar.setCovered(false);
			s.advance(UNCOVER_MS);
			bar.setSpec(null);
		}).not.toThrow();
		expect(bar.engaged()).toBe(false);
		expect(bar.shown()).toBe(false);
	});
});

describe("控制器：吸附终点", () => {
	it("打开 = reveal 夹具：卡片的时长与缓动，起步 = 预计起步（now + NATIVE_LAG_MS）晚一帧", async () => {
		const { h, bar, s } = await engagedBar();
		expect(s.now()).toBe(EPOCH);
		bar.settle(true, 320, false);
		expect(reveals(h)).toEqual([
			JSON.parse(readFile(`${FIXTURES}/tab-bar-reveal.json`)),
		]);
		expect(reveals(h)).toEqual([
			timed(320, EPOCH + NATIVE_LAG_MS + SYNC_BIAS_MS),
		]);
	});

	it("关闭：回到 0，起步早一帧（底栏右缘始终落在卡片左缘之内）", async () => {
		const { h, bar, s } = await engagedBar();
		bar.settle(true, 320, false);
		await flush();
		s.advance(1000);
		h.reveal.mockClear();
		bar.settle(false, 320, false);
		expect(reveals(h)).toEqual([
			timed(0, s.now() + NATIVE_LAG_MS - SYNC_BIAS_MS),
		]);
	});

	it("同一终点不重发（松手与 store 变化各通知一次）；侧栏宽变了照发", async () => {
		const { h, bar } = await engagedBar();
		bar.settle(true, 320, false);
		await flush();
		bar.settle(true, 320, false);
		await flush();
		expect(h.reveal).toHaveBeenCalledTimes(1);
		bar.settle(true, 300, true);
		await flush();
		expect(reveals(h).at(-1)).toEqual({ x: 300, covered: false, ms: 0 });
	});

	it("instant（减少动效、视口尺寸变化）与卡片确认没有过渡（start 兑现 null）→ 原生直接就位（ms 0）", async () => {
		const { h, bar } = await engagedBar();
		bar.settle(true, 320, true);
		expect(reveals(h)).toEqual([{ x: 320, covered: false, ms: 0 }]);
		await flush();
		h.reveal.mockClear();
		const d = deferred();
		bar.settle(false, 320, false, d.promise);
		await flush();
		expect(h.reveal).not.toHaveBeenCalled();
		d.resolve(null);
		await flush();
		expect(reveals(h)).toEqual([{ x: 0, covered: false, ms: 0 }]);
	});

	it("给了 start：等它兑现才发，at 按卡片实际起步（主线程忙时比预计晚）加减偏置", async () => {
		const { h, bar, s } = await engagedBar();
		const open = deferred();
		bar.settle(true, 320, false, open.promise);
		s.advance(150);
		await flush();
		expect(h.reveal).not.toHaveBeenCalled();
		const began = s.now() + 20;
		open.resolve(began);
		await flush();
		expect(reveals(h)).toEqual([timed(320, began + SYNC_BIAS_MS)]);
		h.reveal.mockClear();
		const close = deferred();
		bar.settle(false, 320, false, close.promise);
		close.resolve(began + 1000);
		await flush();
		expect(reveals(h)).toEqual([timed(0, began + 1000 - SYNC_BIAS_MS)]);
	});

	it("等起步期间起拖或再吸附：旧的那条作废，不会把原生拽回旧终点", async () => {
		const { h, bar, s } = await engagedBar();
		const first = deferred();
		bar.settle(true, 320, false, first.promise);
		bar.drag(0.5, 320, 0);
		s.frame();
		first.resolve(s.now());
		await flush();
		expect(reveals(h)).toEqual([{ x: 160, covered: false, ms: 0 }]);
		h.reveal.mockClear();
		const a = deferred();
		const b = deferred();
		bar.settle(true, 320, false, a.promise);
		bar.settle(false, 320, false, b.promise);
		a.resolve(s.now());
		await flush();
		expect(h.reveal).not.toHaveBeenCalled();
		b.resolve(s.now() + 40);
		await flush();
		expect(reveals(h)).toEqual([timed(0, s.now() + 40 - SYNC_BIAS_MS)]);
	});
});

describe("控制器：拖动", () => {
	it("一帧只发一次：同帧多次取最新，ms 0；没有新位置的帧不发", async () => {
		const { h, bar, s } = await engagedBar();
		bar.drag(0.25, 320, 0.1);
		bar.drag(0.5, 320, 0.1);
		bar.drag(0.75, 320, 0.1);
		expect(s.frames()).toBe(1);
		expect(h.reveal).not.toHaveBeenCalled();
		s.frame();
		expect(reveals(h)).toEqual([{ x: 240, covered: false, ms: 0 }]);
		await flush();
		s.frame();
		expect(h.reveal).toHaveBeenCalledTimes(1);
	});

	it("向左（收起方向）按速度预判 NATIVE_LAG_MS，向右不预判；夹到 0", async () => {
		const { h, bar, s } = await engagedBar();
		const sent = async (progress: number, velocity: number) => {
			bar.drag(progress, 320, velocity);
			s.frame();
			await flush();
			return reveals(h).at(-1)?.x;
		};
		expect(await sent(0.5, -1)).toBe(160 - NATIVE_LAG_MS);
		expect(await sent(0.5, 1)).toBe(160);
		expect(await sent(0.1, -2)).toBe(0);
	});

	it("拖动后弹回原终点也照发（原生停在半路）；吸附取消还没跑的那帧", async () => {
		const { h, bar, s } = await engagedBar();
		bar.settle(true, 320, true);
		await flush();
		h.reveal.mockClear();
		bar.drag(0.8, 320, 0);
		s.frame();
		await flush();
		bar.drag(0.9, 320, 0);
		bar.settle(true, 320, false);
		expect(s.frames()).toBe(0);
		expect(reveals(h)).toEqual([
			{ x: 256, covered: false, ms: 0 },
			timed(320, s.now() + NATIVE_LAG_MS + SYNC_BIAS_MS),
		]);
	});
});

describe("控制器：遮挡", () => {
	it("盖住：原生隐藏、网页胶囊立即顶上；让开：原生淡回，网页胶囊 UNCOVER_MS 后才撤", async () => {
		const { h, bar, s } = await engagedBar();
		const sub = vi.fn();
		bar.subscribe(sub);
		expect(bar.shown()).toBe(true);
		bar.setCovered(true);
		expect(bar.shown()).toBe(false);
		expect(sub).toHaveBeenCalled();
		await flush();
		bar.setCovered(false);
		await flush();
		expect(reveals(h)).toEqual([
			{ x: 0, covered: true, ms: 0 },
			{ x: 0, covered: false, ms: 0 },
		]);
		s.advance(UNCOVER_MS - 1);
		expect(bar.shown()).toBe(false);
		s.advance(1);
		expect(bar.shown()).toBe(true);
	});

	it("让开后的重叠期内又被盖住：网页胶囊不撤", async () => {
		const { bar, s } = await engagedBar();
		bar.setCovered(true);
		bar.setCovered(false);
		s.advance(UNCOVER_MS / 2);
		bar.setCovered(true);
		s.advance(UNCOVER_MS * 2);
		expect(bar.shown()).toBe(false);
	});

	it("只在变化时发，带当前卡片左缘；之后的吸附也带着遮挡", async () => {
		const { h, bar } = await engagedBar();
		bar.setCovered(false);
		await flush();
		expect(h.reveal).not.toHaveBeenCalled();
		bar.settle(true, 320, true);
		await flush();
		bar.setCovered(true);
		await flush();
		bar.setCovered(true);
		await flush();
		bar.settle(false, 320, true);
		await flush();
		expect(reveals(h)).toEqual([
			{ x: 320, covered: false, ms: 0 },
			{ x: 320, covered: true, ms: 0 },
			{ x: 0, covered: true, ms: 0 },
		]);
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
	it("--ease、260ms 开合；原生页头或底栏接管时卡片开合两个方向都晚 NATIVE_LAG_MS 起步（拖动、减少动效除外）", () => {
		const css = readFile("src/index.css");
		expect(css).toContain(`--ease: cubic-bezier(${SINK_EASE.join(", ")});`);
		expect(css).toContain(`translate ${SINK_MS}ms var(--ease)`);
		const card = ".sink-card:is([data-native-head], [data-native-bar])";
		for (const sel of [`${card}:not([data-open])`, `${card}[data-open]`]) {
			const at = css.indexOf(sel);
			expect(at).toBeGreaterThan(-1);
			const rule = css.slice(at, css.indexOf("}", at));
			for (const s of ["[data-dragging]", "[data-instant]"])
				expect(rule).toContain(s);
			expect(rule).toContain(
				`translate ${SINK_MS}ms var(--ease) ${NATIVE_LAG_MS}ms`,
			);
		}
	});
});
