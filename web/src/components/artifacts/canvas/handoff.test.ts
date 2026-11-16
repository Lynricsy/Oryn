import { describe, expect, it, vi } from "vitest";
import type { ArtifactThreadActivity } from "../../../api/artifact-queries";
import type { Thread } from "../../../api/client";
import i18n from "../../../i18n";
import {
	type CanvasDoc,
	parseCanvasDoc,
} from "../../../lib/artifacts/canvas-doc";
import { MARK_PREFIX } from "../../../lib/artifacts/prompt";
import { notesOver, pageTransform, shotSize, wrapText } from "./compose-image";
import {
	HANDOFF_MAX_IMAGES,
	type HandOffDeps,
	type HandOffInput,
	handOff,
	handOffSet,
	handOffTarget,
} from "./handoff";

const t = i18n.t.bind(i18n);
const ID = "art_01JDNAWEB0000000000000000A";

function docOf(raw: unknown): CanvasDoc {
	return parseCanvasDoc(JSON.stringify(raw));
}

const page = (id: string, x: number, y: number, canvas = "c1") => ({
	id,
	canvas,
	name: id.toUpperCase(),
	file: `${id}.html`,
	x,
	y,
	width: 400,
	height: 300,
});

const sticky = (
	id: string,
	x: number,
	y: number,
	text: string,
	canvas = "c1",
) => ({
	id,
	canvas,
	kind: "sticky",
	color: "yellow",
	x,
	y,
	width: 120,
	text,
});

// a 在左上，b 在它右边；n1 压在 a 上，n2 压在 b 上，n3 落在空白处，n4 在另一个画布页
const DOC = docOf({
	format: 1,
	canvases: [
		{ id: "c1", name: "首页" },
		{ id: "c2", name: "其它" },
	],
	pages: [page("b", 600, 0), page("a", 0, 0), page("z", 0, 0, "c2")],
	notes: [
		sticky("n1", 50, 50, "把按钮改成绿色"),
		sticky("n2", 650, 50, "这里不动"),
		sticky("n3", 0, 2000, "远处的便签"),
		sticky("n4", 50, 50, "别的页", "c2"),
	],
});

function split(msg: string): { inside: string; outside: string } {
	const lines = msg.split("\n");
	const first = lines.findIndex((l) => l.startsWith(MARK_PREFIX));
	const last = lines.findLastIndex((l) => l.startsWith(MARK_PREFIX));
	return {
		inside: lines.slice(first + 1, last).join("\n"),
		outside: [...lines.slice(0, first), ...lines.slice(last + 1)].join("\n"),
	};
}

describe("handOffSet", () => {
	it("选中一个画框：带上与它重叠的标注，不带不重叠的", () => {
		const set = handOffSet(DOC, "c1", ["a"]);
		expect(set.pages.map((p) => p.id)).toEqual(["a"]);
		expect(set.notes.map((n) => n.id)).toEqual(["n1"]);
	});

	it("被选中的标注压着的画框被带上", () => {
		const set = handOffSet(DOC, "c1", ["n2"]);
		expect(set.pages.map((p) => p.id)).toEqual(["b"]);
		expect(set.notes.map((n) => n.id)).toEqual(["n2"]);
	});

	it("画框按阅读顺序；空白处的标注只带它自己；不碰别的画布页", () => {
		const set = handOffSet(DOC, "c1", ["b", "a", "n3", "n4", "z"]);
		expect(set.pages.map((p) => p.id)).toEqual(["a", "b"]);
		expect(set.notes.map((n) => n.id)).toEqual(["n1", "n2", "n3"]);
	});
});

describe("handOffTarget", () => {
	const index = {
		creator: { id: "creator" } as Thread,
		old: { id: "old", archivedAt: 1 } as Thread,
	};
	const act = (threadId: string, doing: ArtifactThreadActivity["doing"]) => ({
		threadId,
		doing,
	});

	it("正在改的线程优先，只是在读的不算", () => {
		expect(
			handOffTarget({
				activity: [act("reader", "read"), act("writer", "write")],
				creator: "creator",
				detailThreads: [],
				index,
			}),
		).toBe("writer");
		expect(
			handOffTarget({
				activity: [act("reader", "read")],
				creator: "creator",
				detailThreads: [],
				index,
			}),
		).toBe("creator");
	});

	it("创建线程已归档或没有创建线程时走新线程", () => {
		expect(
			handOffTarget({ activity: [], creator: "old", detailThreads: [], index }),
		).toBeNull();
		expect(
			handOffTarget({
				activity: [],
				creator: "creator",
				detailThreads: [{ threadId: "creator", title: "x", archived: true }],
				index,
			}),
		).toBeNull();
		expect(
			handOffTarget({
				activity: [],
				creator: undefined,
				detailThreads: [],
				index,
			}),
		).toBeNull();
	});
});

describe("handOff", () => {
	function deps(busy = false) {
		const d = {
			t,
			image: vi.fn(
				async (p: { name: string }) =>
					new File(["png"], `${p.name}.png`, { type: "image/png" }),
			),
			upload: vi.fn(async (f: File, _threadId: string) => ({
				id: `att-${f.name}`,
			})),
			busy: vi.fn(() => busy),
			send: vi.fn(async (_threadId: string, _frame: unknown) => undefined),
			seedHome: vi.fn(),
			navigate: vi.fn(),
		};
		return d satisfies HandOffDeps;
	}
	const input = (patch: Partial<HandOffInput> = {}): HandOffInput => ({
		artifact: { id: ID, title: "原型", projectId: "prj_1" },
		version: 4,
		set: handOffSet(DOC, "c1", ["a", "b"]),
		allPages: DOC.pages,
		target: "thr_1",
		render: true,
		...patch,
	});

	it("渲染可用：每个画框一张合成图，上传后随消息带 attachmentIds", async () => {
		i18n.changeLanguage("zh-CN");
		const d = deps();
		await expect(handOff(input(), d)).resolves.toBe("sent");
		expect(d.image).toHaveBeenCalledTimes(2);
		expect(d.upload.mock.calls.map(([f, th]) => [f.name, th])).toEqual([
			["A.png", "thr_1"],
			["B.png", "thr_1"],
		]);
		const [threadId, frame] = d.send.mock.calls[0] as unknown as [
			string,
			{ type: string; message: string; orynAttachments?: string[] },
		];
		expect(threadId).toBe("thr_1");
		expect(frame.type).toBe("prompt");
		expect(frame.orynAttachments).toEqual(["att-A.png", "att-B.png"]);
		const { inside, outside } = split(frame.message);
		expect(outside).toContain("照这些画框上的标注改，别的地方不动。");
		expect(outside).toContain(`artifact ${ID} 版本 4`);
		// 标注的文字只出现在两行 mark 之间
		expect(outside).not.toContain("把按钮改成绿色");
		expect(inside).toContain('"把按钮改成绿色"');
		expect(inside).toContain('page a "A", file a.html, 400x300');
	});

	it(`最多附 ${HANDOFF_MAX_IMAGES} 张图；agent 忙时用 follow_up`, async () => {
		const many = docOf({
			format: 1,
			canvases: [{ id: "c1", name: "首页" }],
			pages: Array.from({ length: 6 }, (_, i) => page(`p${i}`, i * 500, 0)),
			notes: [],
		});
		const d = deps(true);
		await handOff(
			input({
				set: handOffSet(
					many,
					"c1",
					many.pages.map((p) => p.id),
				),
				allPages: many.pages,
			}),
			d,
		);
		expect(d.image).toHaveBeenCalledTimes(HANDOFF_MAX_IMAGES);
		const frame = d.send.mock.calls[0]?.[1] as unknown as { type: string };
		expect(frame.type).toBe("follow_up");
	});

	it("渲染不可用：只发文字", async () => {
		const d = deps();
		await handOff(input({ render: false }), d);
		expect(d.image).not.toHaveBeenCalled();
		expect(d.upload).not.toHaveBeenCalled();
		const frame = d.send.mock.calls[0]?.[1] as unknown as {
			orynAttachments?: string[];
		};
		expect(frame.orynAttachments).toBeUndefined();
	});

	it("新线程：交给首屏输入框（带项目），不带图、不发消息", async () => {
		const d = deps();
		await expect(handOff(input({ target: null }), d)).resolves.toBe("seeded");
		expect(d.image).not.toHaveBeenCalled();
		expect(d.send).not.toHaveBeenCalled();
		const seed = d.seedHome.mock.calls[0]?.[0] as {
			text: string;
			projectId?: string;
		};
		expect(seed.projectId).toBe("prj_1");
		expect(seed.text).toContain("照这些画框上的标注改");
		expect(d.navigate).toHaveBeenCalledWith("/");
	});

	it("上传失败时整次失败，不发消息", async () => {
		const d = deps();
		d.upload.mockRejectedValueOnce(new Error("boom"));
		await expect(handOff(input(), d)).rejects.toThrow("boom");
		expect(d.send).not.toHaveBeenCalled();
	});
});

describe("合成图", () => {
	it("宽超过 1600 的画框按比例请求 1600 宽", () => {
		expect(shotSize({ width: 3200, height: 2000 })).toEqual({
			width: 1600,
			height: 1000,
		});
		expect(shotSize({ width: 390, height: 844 })).toEqual({
			width: 390,
			height: 844,
		});
	});

	it("世界坐标换算到截图像素", () => {
		const k = pageTransform({ x: 100, y: 50, width: 3200 }, 1600);
		expect(k).toEqual({ scale: 0.5, dx: -50, dy: -25 });
		// 画框左上角落在原点
		expect(100 * k.scale + k.dx).toBe(0);
		expect(50 * k.scale + k.dy).toBe(0);
	});

	it("每个画框只画与它相交、同一画布页的标注", () => {
		const [b, a] = DOC.pages;
		if (!a || !b) throw new Error("fixture");
		expect(notesOver(a, DOC.notes, DOC.pages).map((n) => n.id)).toEqual(["n1"]);
		expect(notesOver(b, DOC.notes, DOC.pages).map((n) => n.id)).toEqual(["n2"]);
	});

	it("便签文字按宽度折行，保留原有换行", () => {
		const measure = (s: string) => s.length * 10;
		expect(wrapText("hello world foo\n第二段", 100, measure)).toEqual([
			"hello",
			"world foo",
			"第二段",
		]);
		expect(wrapText("一二三四五", 30, measure)).toEqual(["一二三", "四五"]);
	});
});
