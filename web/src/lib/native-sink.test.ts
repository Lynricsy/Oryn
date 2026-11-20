/** 根滚动模式：静止位置判定、根滚动跟随的接管 / 回退、根滚动与 sidebarOpen 的同步和守卫。 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { NativeSinkHost } from "./host";
import {
	createNativeSink,
	createSinkScroll,
	SINK_REQUEST_MS,
	SINK_REST_MS,
	type SinkRest,
	sinkRest,
} from "./native-sink";

function win(scrollX: number, max: number, innerWidth = 393): Window {
	return {
		scrollX,
		innerWidth,
		document: { documentElement: { scrollWidth: innerWidth + max } },
	} as unknown as Window;
}

describe("sinkRest", () => {
	it("max 320：0、0.4 → open；0.6、160 → null；319.6、320 → closed", () => {
		expect(sinkRest(win(0, 320))).toBe("open");
		expect(sinkRest(win(0.4, 320))).toBe("open");
		expect(sinkRest(win(0.6, 320))).toBeNull();
		expect(sinkRest(win(160, 320))).toBeNull();
		expect(sinkRest(win(319.6, 320))).toBe("closed");
		expect(sinkRest(win(320, 320))).toBe("closed");
	});

	it("文档不可横滚（max 0）→ null", () => {
		expect(sinkRest(win(0, 0))).toBeNull();
	});
});

describe("createNativeSink", () => {
	/** 排空微任务（桥的应答与 latest 的续发都在微任务里）。 */
	const flush = async () => {
		for (let i = 0; i < 10; i++) await Promise.resolve();
	};
	const api = (reject = false) => ({
		set: vi.fn(() =>
			reject ? Promise.reject("unsupported") : Promise.resolve(),
		),
		show: vi.fn(() => Promise.resolve()),
	});

	it("set(true) 应答后才接管并通知；show 只在接管后发", async () => {
		const a = api();
		const sink = createNativeSink(a as NativeSinkHost);
		const cb = vi.fn();
		sink.subscribe(cb);
		sink.show(true, true);
		sink.setOn(true);
		expect(sink.engaged()).toBe(false);
		await flush();
		expect(sink.engaged()).toBe(true);
		expect(cb).toHaveBeenCalledTimes(1);
		sink.show(true, true);
		await flush();
		expect(a.set.mock.calls).toEqual([[true]]);
		expect(a.show.mock.calls).toEqual([[true, true]]);
	});

	it("setOn(false)：立即撤掉接管并通知，再发 set(false)", async () => {
		const a = api();
		const sink = createNativeSink(a as NativeSinkHost);
		sink.setOn(true);
		await flush();
		const cb = vi.fn();
		sink.subscribe(cb);
		sink.setOn(false);
		expect(sink.engaged()).toBe(false);
		expect(cb).toHaveBeenCalledTimes(1);
		await flush();
		expect(a.set.mock.calls).toEqual([[true], [false]]);
	});

	it("set 被拒：不接管，本页不再尝试", async () => {
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const a = api(true);
		const sink = createNativeSink(a as NativeSinkHost);
		sink.setOn(true);
		await flush();
		expect(sink.engaged()).toBe(false);
		sink.setOn(false);
		sink.setOn(true);
		await flush();
		expect(a.set.mock.calls).toEqual([[true]]);
		warn.mockRestore();
	});

	it("没有宿主能力：一切空操作", () => {
		const sink = createNativeSink(null);
		sink.setOn(true);
		sink.show(true, true);
		expect(sink.engaged()).toBe(false);
	});
});

describe("createSinkScroll", () => {
	beforeEach(() => {
		vi.useFakeTimers();
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	/** 假根滚动：rest 由测试直接设，scrollTo 记录调用。 */
	function setup(initial: SinkRest = "closed") {
		const state = { rest: initial as SinkRest | null };
		const scrollTo = vi.fn();
		const pull = vi.fn();
		const commit = vi.fn();
		const s = createSinkScroll(
			{
				rest: () => state.rest,
				scrollTo,
				pull,
				setTimeout: (cb, ms) => setTimeout(cb, ms) as unknown as number,
				clearTimeout: (id) => clearTimeout(id),
			},
			initial,
			commit,
		);
		/** 根滚到某处并派发 scroll 事件。 */
		const scroll = (rest: SinkRest | null) => {
			state.rest = rest;
			s.scrolled();
		};
		return { s, state, scrollTo, pull, commit, scroll };
	}

	it("T1 按下、滚到 open、抬起：静止 SINK_REST_MS 后提交 open 恰一次", () => {
		const { s, commit, scroll } = setup();
		s.touch(true);
		scroll(null);
		scroll("open");
		s.touch(false);
		vi.advanceTimersByTime(SINK_REST_MS - 1);
		expect(commit).not.toHaveBeenCalled();
		vi.advanceTimersByTime(1);
		expect(commit.mock.calls).toEqual([["open"]]);
		vi.advanceTimersByTime(1000);
		expect(commit).toHaveBeenCalledTimes(1);
	});

	it("T2 无触摸无请求时根离开关闭位：立即 pull 回已提交的位置（不经 scrollTo），不提交", () => {
		const { scrollTo, pull, commit, scroll } = setup();
		scroll(null);
		expect(pull.mock.calls).toEqual([["closed"]]);
		expect(scrollTo).not.toHaveBeenCalled();
		vi.advanceTimersByTime(1000);
		expect(commit).not.toHaveBeenCalled();
	});

	it("T3 request(open, smooth)：途中不拉回，到 open 后静止 SINK_REST_MS 提交", () => {
		const { s, scrollTo, commit, scroll } = setup();
		s.request("open", true);
		expect(scrollTo.mock.calls).toEqual([["open", true]]);
		scroll(null);
		scroll(null);
		expect(scrollTo).toHaveBeenCalledTimes(1);
		scroll("open");
		vi.advanceTimersByTime(SINK_REST_MS);
		expect(commit.mock.calls).toEqual([["open"]]);
	});

	it("T4 抬起后停在中途（含原生拖动时 WebKit 发来 touchcancel、手指其实还按着）：网页不吸附、不提交，系统吸附到位后才提交", () => {
		const { s, scrollTo, commit, scroll } = setup();
		s.touch(true);
		scroll(null);
		s.touch(false);
		vi.advanceTimersByTime(SINK_REST_MS * 5);
		expect(scrollTo).not.toHaveBeenCalled();
		expect(commit).not.toHaveBeenCalled();
		scroll("closed");
		vi.advanceTimersByTime(SINK_REST_MS);
		expect(commit.mock.calls).toEqual([["closed"]]);
	});

	it("T5 按住停在 open：不提交；抬起后 SINK_REST_MS 提交", () => {
		const { s, commit, scroll } = setup();
		s.touch(true);
		scroll("open");
		vi.advanceTimersByTime(500);
		expect(commit).not.toHaveBeenCalled();
		s.touch(false);
		vi.advanceTimersByTime(SINK_REST_MS);
		expect(commit.mock.calls).toEqual([["open"]]);
	});

	it("T6 已在目标位且已提交的 request：不调 scrollTo", () => {
		const { s, scrollTo, commit } = setup("closed");
		s.request("closed", true);
		expect(scrollTo).not.toHaveBeenCalled();
		vi.advanceTimersByTime(SINK_REQUEST_MS);
		expect(commit).not.toHaveBeenCalled();
	});

	it("T7 committed 为 closed 而 rest 已是 open 时 request(open)：最终提交 open", () => {
		const { s, state, commit } = setup("closed");
		state.rest = "open";
		s.request("open", true);
		vi.advanceTimersByTime(SINK_REQUEST_MS);
		expect(commit.mock.calls).toEqual([["open"]]);
	});

	it("T8 request 之后根一直没动（原生动画晚起步或丢了）：不提交旧位置；SINK_REQUEST_MS 时兜底一步落位，落稳后提交", () => {
		const { s, scrollTo, commit, scroll } = setup("closed");
		s.request("open", true);
		vi.advanceTimersByTime(SINK_REQUEST_MS - 1);
		expect(commit).not.toHaveBeenCalled();
		expect(scrollTo.mock.calls).toEqual([["open", true]]);
		vi.advanceTimersByTime(1);
		expect(scrollTo).toHaveBeenLastCalledWith("open", false);
		scroll("open");
		vi.advanceTimersByTime(SINK_REST_MS);
		expect(commit.mock.calls).toEqual([["open"]]);
	});

	it("T8a 兜底落位 200ms 后才到（原生命令异步）：宽限期内不提交旧位置，到位后提交目的地", () => {
		const { s, commit, scroll } = setup("closed");
		s.request("open", true);
		vi.advanceTimersByTime(SINK_REQUEST_MS + 199);
		expect(commit).not.toHaveBeenCalled();
		vi.advanceTimersByTime(1);
		scroll("open");
		vi.advanceTimersByTime(SINK_REST_MS);
		expect(commit.mock.calls).toEqual([["open"]]);
	});

	it("T8b 兜底落位在宽限期内也没到：不重发，宽限到期后按实际位置提交", () => {
		const { s, scrollTo, commit } = setup("closed");
		s.request("open", true);
		vi.advanceTimersByTime(SINK_REQUEST_MS * 2 - 1);
		expect(commit).not.toHaveBeenCalled();
		vi.advanceTimersByTime(1 + SINK_REST_MS);
		expect(commit.mock.calls).toEqual([["closed"]]);
		vi.advanceTimersByTime(SINK_REQUEST_MS * 3);
		expect(scrollTo.mock.calls).toEqual([
			["open", true],
			["open", false],
		]);
	});

	it("T9 request 动画途中 scroll 事件隔了超过 SINK_REST_MS：不提交、不改目的地，到位后照常提交", () => {
		const { s, scrollTo, commit, scroll } = setup("closed");
		s.request("open", true);
		scroll(null);
		vi.advanceTimersByTime(SINK_REST_MS * 3);
		expect(scrollTo.mock.calls).toEqual([["open", true]]);
		expect(commit).not.toHaveBeenCalled();
		scroll("open");
		vi.advanceTimersByTime(SINK_REST_MS);
		expect(commit.mock.calls).toEqual([["open"]]);
	});

	it("T10 request 动画停在中途一直不动（原生动画丢了）：等满 SINK_REQUEST_MS 直接落到目的地", () => {
		const { s, scrollTo, commit, scroll } = setup("closed");
		s.request("open", true);
		scroll(null);
		vi.advanceTimersByTime(SINK_REQUEST_MS);
		expect(scrollTo).not.toHaveBeenCalledWith("closed", true);
		expect(scrollTo).toHaveBeenLastCalledWith("open", false);
		scroll("open");
		vi.advanceTimersByTime(SINK_REST_MS);
		expect(commit.mock.calls).toEqual([["open"]]);
	});

	it("T10b 未到目的地的 scroll 每 90ms 来一条（静止定时器一直被重排）：截止仍在 SINK_REQUEST_MS 准时落到目的地", () => {
		const { s, scrollTo, scroll } = setup("closed");
		s.request("open", true);
		for (let t = 90; t < SINK_REQUEST_MS; t += 90) {
			vi.advanceTimersByTime(90);
			scroll(null);
		}
		expect(scrollTo.mock.calls).toEqual([["open", true]]);
		vi.advanceTimersByTime(SINK_REQUEST_MS % 90 || 90);
		expect(scrollTo).toHaveBeenLastCalledWith("open", false);
	});

	it("T10c 到位提交、被新请求替换、卸载都取消截止：之后不再落位", () => {
		const done = setup("closed");
		done.s.request("open", true);
		done.scroll("open");
		vi.advanceTimersByTime(SINK_REST_MS);
		expect(done.commit.mock.calls).toEqual([["open"]]);
		const gone = setup("closed");
		gone.s.request("open", true);
		gone.s.dispose();
		const swapped = setup("closed");
		swapped.s.request("open", true);
		vi.advanceTimersByTime(SINK_REQUEST_MS / 2);
		swapped.s.request("closed", true);
		swapped.scroll("closed");
		vi.advanceTimersByTime(SINK_REQUEST_MS * 2);
		expect(done.scrollTo.mock.calls).toEqual([["open", true]]);
		expect(gone.scrollTo.mock.calls).toEqual([["open", true]]);
		expect(swapped.scrollTo.mock.calls).toEqual([
			["open", true],
			["closed", true],
		]);
		expect(swapped.commit.mock.calls).toEqual([["closed"]]);
	});

	it("T11 request 途中手指接管：目的地作废，截止不再落位；抬起后停在中途等系统吸附", () => {
		const { s, scrollTo, scroll } = setup("closed");
		s.request("open", true);
		s.touch(true);
		scroll(null);
		s.touch(false);
		vi.advanceTimersByTime(SINK_REQUEST_MS * 3);
		expect(scrollTo.mock.calls).toEqual([["open", true]]);
	});

	it("T12 关闭位 request(open) 后、动画首帧前又 request(closed)：照发关闭（停掉旧动画），最终提交 closed 而不是重新打开", () => {
		const { s, scrollTo, commit, scroll } = setup("closed");
		s.request("open", true);
		s.request("closed", true);
		expect(scrollTo.mock.calls).toEqual([
			["open", true],
			["closed", true],
		]);
		scroll("closed");
		vi.advanceTimersByTime(SINK_REQUEST_MS);
		expect(commit.mock.calls).toEqual([["closed"]]);
	});

	it("提交之后恢复守卫：非本页引起的位移再次被 pull 回", () => {
		const { s, pull, commit, scroll } = setup();
		s.request("open", true);
		scroll("open");
		vi.advanceTimersByTime(SINK_REST_MS);
		expect(commit.mock.calls).toEqual([["open"]]);
		scroll(null);
		expect(pull.mock.calls).toEqual([["open"]]);
	});

	it("dispose 后不再提交", () => {
		const { s, commit, scroll } = setup();
		s.touch(true);
		scroll("open");
		s.touch(false);
		s.dispose();
		vi.advanceTimersByTime(1000);
		expect(commit).not.toHaveBeenCalled();
	});
});
