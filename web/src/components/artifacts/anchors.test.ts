import { describe, expect, it } from "vitest";
import type { PageSelection } from "../../../sandbox/runtime/messages";
import type { ArtifactCommentThread } from "../../api/artifact-queries";
import i18n from "../../i18n";
import {
	anchorLabel,
	anchorOf,
	anchorOfViewer,
	anchorPinsOf,
	isAnswered,
	orderThreads,
	pinsOf,
} from "./anchors";

const t = i18n.t.bind(i18n);

const STYLE: PageSelection["style"] = {
	color: "#000000",
	"background-color": "#ffffff",
	"font-size": "16px",
	"font-weight": "400",
	"text-align": "left",
	padding: "0px",
	"border-radius": "0px",
};

function selection(patch: Partial<PageSelection> = {}): PageSelection {
	return {
		path: "index.html",
		src: 120,
		tag: "h2",
		text: "本周概览",
		textOnly: true,
		style: STYLE,
		...patch,
	};
}

let seq = 0;
function thread(
	patch: Omit<Partial<ArtifactCommentThread>, "anchor"> & {
		anchor?: Partial<ArtifactCommentThread["anchor"]>;
	} = {},
): ArtifactCommentThread {
	seq++;
	const { anchor, ...rest } = patch;
	return {
		id: `act_${String(seq).padStart(26, "0")}`,
		createdAt: seq,
		comments: [{ n: 1, authorLabel: "", body: "改一下", createdAt: seq }],
		...rest,
		anchor: {
			version: 1,
			path: "index.html",
			page: null,
			offset: 100 + seq,
			tag: "p",
			text: "",
			missing: false,
			...anchor,
		},
	};
}

describe("anchorOf", () => {
	it("选中元素：offset 取 src，标签、文本与路径取自选中", () => {
		expect(
			anchorOf(selection({ path: "about.html" }), {
				version: 4,
				path: "index.html",
			}),
		).toEqual({
			version: 4,
			path: "about.html",
			page: null,
			offset: 120,
			tag: "h2",
			text: "本周概览",
			missing: false,
		});
	});

	it("脚本生成的元素没有 offset，标签与文本仍在", () => {
		const a = anchorOf(selection({ src: null, tag: "div", text: "动态" }), {
			version: 2,
			path: "index.html",
		});
		expect(a.offset).toBeNull();
		expect(a.tag).toBe("div");
		expect(a.text).toBe("动态");
	});

	it("没有选中元素是整个页面：offset、tag 为空，路径取上下文", () => {
		expect(
			anchorOf(null, {
				version: 3,
				path: "page-a.html",
				page: { number: 2, name: "首页" },
			}),
		).toEqual({
			version: 3,
			path: "page-a.html",
			page: { number: 2, name: "首页" },
			offset: null,
			tag: null,
			text: "",
			missing: false,
		});
	});
});

describe("anchorLabel", () => {
	it("整个页面、带文本与不带文本的元素、画布页前缀", () => {
		const base = anchorOf(null, { version: 1, path: "index.html" });
		expect(anchorLabel(base, t)).toBe(t("artifacts.comments.wholePage"));
		const el = anchorOf(selection(), { version: 1, path: "index.html" });
		expect(anchorLabel(el, t)).toContain("<h2>");
		expect(anchorLabel(el, t)).toContain("本周概览");
		expect(anchorLabel({ ...el, text: "" }, t)).toBe("<h2>");
		const onPage = anchorOf(selection(), {
			version: 1,
			path: "a.html",
			page: { number: 1, name: "封面" },
		});
		expect(anchorLabel(onPage, t)).toMatch(/^封面 › <h2>/);
	});
});

describe("orderThreads / pinsOf", () => {
	it("未解决的按创建先后编号，已解决的另列且不编号", () => {
		const a = thread();
		const b = thread({ resolvedAt: 99 });
		const c = thread();
		// 输入顺序打乱也按创建时间排
		const { open, resolved } = orderThreads([c, b, a]);
		expect(open.map((x) => [x.thread.id, x.n])).toEqual([
			[a.id, 1],
			[c.id, 2],
		]);
		expect(resolved.map((x) => x.id)).toEqual([b.id]);
	});

	it("图钉：只画本页、有 offset、元素还在的未解决串；编号与面板一致；agent 回复过的是 answered", () => {
		const whole = thread({ anchor: { offset: null, tag: null } });
		const done = thread({ resolvedAt: 5 });
		const gone = thread({ anchor: { missing: true } });
		const other = thread({ anchor: { path: "b.html" } });
		const replied = thread({
			comments: [
				{ n: 1, authorLabel: "", body: "改", createdAt: 1 },
				{
					n: 2,
					authorThreadId: "T1",
					authorLabel: "线程",
					body: "改好了",
					createdAt: 2,
				},
			],
		});
		const pins = pinsOf([whole, done, gone, other, replied], "index.html");
		// 编号：whole=1、gone=2、other=3、replied=4（done 已解决不编号）
		expect(pins).toEqual([
			{ n: 4, src: replied.anchor.offset, answered: true },
		]);
		expect(isAnswered(whole)).toBe(false);
		expect(pinsOf([other], "b.html")).toEqual([
			{ n: 1, src: other.anchor.offset, answered: false },
		]);
	});

	it("最多 200 个图钉", () => {
		const many = Array.from({ length: 250 }, () => thread());
		const pins = pinsOf(many, "index.html");
		expect(pins).toHaveLength(200);
		expect(pins[199]?.n).toBe(200);
	});
});

describe("类型化锚点（M31）", () => {
	const pick = {
		page: { number: 2, name: "" },
		x: 0.25,
		y: 0.5,
		range: null,
		block: null,
		text: "季度报告",
	};

	it("anchorOfViewer：点选带页与坐标；空点选是整篇", () => {
		expect(anchorOfViewer(pick, { version: 3, path: "document.docx" })).toEqual(
			{
				version: 3,
				path: "document.docx",
				page: { number: 2, name: "" },
				offset: null,
				tag: null,
				text: "季度报告",
				missing: false,
				x: 0.25,
				y: 0.5,
				range: null,
				block: null,
			},
		);
		expect(anchorOfViewer(null, { version: 1, path: "document.md" })).toEqual({
			version: 1,
			path: "document.md",
			page: null,
			offset: null,
			tag: null,
			text: "",
			missing: false,
			x: null,
			y: null,
			range: null,
			block: null,
		});
	});

	it("anchorLabel：页、张、工作表区域、工作表、段", () => {
		const shown = (path: string) => ({ version: 1, path });
		expect(anchorLabel(anchorOfViewer(pick, shown("document.pdf")), t)).toBe(
			t("artifacts.comments.pageAt", { n: 2 }),
		);
		expect(
			anchorLabel(anchorOfViewer(pick, shown("presentation.pptx")), t),
		).toBe(t("artifacts.comments.slideAt", { n: 2 }));
		expect(anchorLabel(anchorOfViewer(pick, shown("slides.json")), t)).toBe(
			t("artifacts.comments.slideAt", { n: 2 }),
		);
		const sheet = {
			...pick,
			page: { number: 1, name: "明细" },
			x: null,
			y: null,
		};
		expect(
			anchorLabel(
				anchorOfViewer({ ...sheet, range: "B2:C3" }, shown("workbook.xlsx")),
				t,
			),
		).toBe(
			t("artifacts.comments.sheetRange", { sheet: "明细", range: "B2:C3" }),
		);
		expect(anchorLabel(anchorOfViewer(sheet, shown("workbook.xlsm")), t)).toBe(
			t("artifacts.comments.sheetOnly", { sheet: "明细" }),
		);
		const block = { ...pick, page: null, x: null, y: null, block: 1 };
		expect(anchorLabel(anchorOfViewer(block, shown("document.md")), t)).toBe(
			t("artifacts.comments.blockAt", { n: 2 }),
		);
		expect(anchorLabel(anchorOfViewer(null, shown("document.md")), t)).toBe(
			t("artifacts.comments.wholePage"),
		);
	});

	it("anchorPinsOf：只画入口上未解决、没有 missing 的类型化锚点", () => {
		const typed = (patch: Partial<ArtifactCommentThread["anchor"]> = {}) =>
			thread({
				anchor: {
					...anchorOfViewer(pick, { version: 1, path: "document.docx" }),
					...patch,
				},
			});
		const a = typed();
		const done = thread({
			resolvedAt: 9,
			anchor: anchorOfViewer(pick, { version: 1, path: "document.docx" }),
		});
		const gone = typed({ missing: true });
		const other = typed({ path: "document.pdf" });
		const html = thread();
		expect(anchorPinsOf([a, done, gone, other, html], "document.docx")).toEqual(
			[{ n: 1, answered: false, anchor: pick }],
		);
		expect(anchorPinsOf([a], "index.html")).toEqual([]);
	});
});
