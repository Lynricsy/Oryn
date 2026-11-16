/**
 * Artifacts 总览页的纯逻辑：视图与查询串互转、筛选、三种排序、按项目与会话分组、方向键移动、导入字段名。
 */
import { describe, expect, it } from "vitest";
import type { Artifact } from "../../api/artifact-queries";
import {
	clearFilters,
	DEFAULT_VIEW,
	filterArtifacts,
	flattenGroups,
	gridMove,
	groupArtifacts,
	hasFilters,
	importEntries,
	parseView,
	recentAt,
	sortArtifacts,
	titleMatches,
	viewSearch,
} from "./artifacts-model";

function art(id: string, patch: Partial<Artifact> = {}): Artifact {
	return {
		id,
		origin: "agent",
		kind: "web",
		title: id,
		version: 1,
		size: 1,
		pages: 1,
		createdAt: 1,
		updatedAt: 1,
		...patch,
	};
}

describe("parseView / viewSearch", () => {
	it("缺省视图写成空查询串，读回仍是缺省", () => {
		expect(viewSearch(DEFAULT_VIEW)).toBe("");
		expect(parseView(new URLSearchParams())).toEqual(DEFAULT_VIEW);
	});

	it("全部筛选、排序与布局都进查询串并能原样读回", () => {
		const v = {
			kind: "slides" as const,
			projects: ["prj_1", "none"],
			origins: ["import" as const],
			sort: "name" as const,
			layout: "project-thread" as const,
			q: "周报 ä",
		};
		const s = viewSearch(v);
		expect(s).toContain("kind=slides");
		expect(s).toContain("project=prj_1&project=none");
		expect(s).toContain("sort=name");
		expect(s).toContain("layout=project-thread");
		expect(parseView(new URLSearchParams(s))).toEqual(v);
	});

	it("不认识的值回落到缺省", () => {
		expect(
			parseView(
				new URLSearchParams("kind=video&sort=size&layout=x&origin=robot"),
			),
		).toEqual(DEFAULT_VIEW);
	});

	it("hasFilters 不算排序与布局；clearFilters 保留它们", () => {
		const v = {
			...DEFAULT_VIEW,
			sort: "name" as const,
			layout: "project-thread" as const,
		};
		expect(hasFilters(v)).toBe(false);
		const f = { ...v, kind: "web" as const, q: "x" };
		expect(hasFilters(f)).toBe(true);
		expect(clearFilters(f)).toEqual(v);
	});
});

describe("筛选", () => {
	const list = [
		art("a", { title: "Café 报告", kind: "web", projectId: "p1" }),
		art("b", { title: "幻灯片", kind: "slides", origin: "import" }),
		art("c", { title: "CAFE notes", kind: "document", projectId: "p2" }),
	];

	it("标题搜索折叠大小写与变音符，多个词都要命中", () => {
		expect(titleMatches("Café 报告", "cafe")).toBe(true);
		expect(titleMatches("Café 报告", "CAFÉ 报")).toBe(true);
		expect(titleMatches("Café 报告", "cafe 幻")).toBe(false);
		expect(
			filterArtifacts(list, { ...DEFAULT_VIEW, q: "cafe" }).map((a) => a.id),
		).toEqual(["a", "c"]);
	});

	it("类型、项目（含无项目）与来源", () => {
		expect(
			filterArtifacts(list, { ...DEFAULT_VIEW, kind: "slides" }).map(
				(a) => a.id,
			),
		).toEqual(["b"]);
		expect(
			filterArtifacts(list, { ...DEFAULT_VIEW, projects: ["none", "p2"] }).map(
				(a) => a.id,
			),
		).toEqual(["b", "c"]);
		expect(
			filterArtifacts(list, { ...DEFAULT_VIEW, origins: ["import"] }).map(
				(a) => a.id,
			),
		).toEqual(["b"]);
	});
});

describe("排序", () => {
	const list = [
		art("x", { title: "b 第 10 版", updatedAt: 100, openedAt: 500 }),
		art("y", { title: "a", updatedAt: 300 }),
		art("z", { title: "b 第 9 版", updatedAt: 200, openedAt: 50 }),
	];

	it("recent 取 max(openedAt, updatedAt)，新的在前", () => {
		expect(recentAt(list[0] as Artifact)).toBe(500);
		expect(sortArtifacts(list, "recent").map((a) => a.id)).toEqual([
			"x",
			"y",
			"z",
		]);
	});

	it("updated 只看更新时间", () => {
		expect(sortArtifacts(list, "updated").map((a) => a.id)).toEqual([
			"y",
			"z",
			"x",
		]);
	});

	it("name 按标题自然序（数字按数值）", () => {
		expect(sortArtifacts(list, "name").map((a) => a.id)).toEqual([
			"y",
			"z",
			"x",
		]);
	});

	it("不改动输入数组", () => {
		const copy = [...list];
		sortArtifacts(list, "name");
		expect(list).toEqual(copy);
	});
});

describe("按项目与会话分组", () => {
	it("项目内按线程分段；导入的、创建线程已删除的各成一段；顺序沿用输入", () => {
		const sorted = [
			art("1", { projectId: "p1", threadId: "T1" }),
			art("2", { threadId: "T2" }),
			art("3", { projectId: "p1", origin: "import" }),
			art("4", { projectId: "p1", threadId: "T1" }),
			art("5", { projectId: "p1" }),
		];
		const groups = groupArtifacts(sorted);
		expect(
			groups.map((g) => ({
				project: g.projectId,
				sections: g.sections.map((s) => [
					s.type,
					s.threadId ?? null,
					s.items.map((a) => a.id),
				]),
			})),
		).toEqual([
			{
				project: "p1",
				sections: [
					["thread", "T1", ["1", "4"]],
					["imported", null, ["3"]],
					["deletedThread", null, ["5"]],
				],
			},
			{ project: null, sections: [["thread", "T2", ["2"]]] },
		]);
		expect(flattenGroups(groups).map((a) => a.id)).toEqual([
			"1",
			"4",
			"3",
			"5",
			"2",
		]);
	});
});

describe("gridMove", () => {
	it("左右 ±1、上下 ±一行、Home / End，越界停在边上", () => {
		expect(gridMove(0, "ArrowRight", 7, 3)).toBe(1);
		expect(gridMove(0, "ArrowLeft", 7, 3)).toBe(0);
		expect(gridMove(1, "ArrowDown", 7, 3)).toBe(4);
		expect(gridMove(5, "ArrowDown", 7, 3)).toBe(6);
		expect(gridMove(4, "ArrowUp", 7, 3)).toBe(1);
		expect(gridMove(1, "ArrowUp", 7, 3)).toBe(0);
		expect(gridMove(3, "Home", 7, 3)).toBe(0);
		expect(gridMove(3, "End", 7, 3)).toBe(6);
		expect(gridMove(3, "Enter", 7, 3)).toBeNull();
		expect(gridMove(0, "End", 0, 3)).toBeNull();
	});
});

describe("importEntries", () => {
	function file(name: string, rel = ""): File {
		const f = new File(["x"], name);
		Object.defineProperty(f, "webkitRelativePath", { value: rel });
		return f;
	}

	it("单个 HTML 文件：字段名是文件名，回落标题去掉扩展名", () => {
		const r = importEntries([file("周报.html")]);
		expect(r.entries.map((e) => e.path)).toEqual(["周报.html"]);
		expect(r.name).toBe("周报");
	});

	it("文件夹：去掉顶层文件夹名，回落标题是文件夹名", () => {
		const r = importEntries([
			file("index.html", "site/index.html"),
			file("a.png", "site/img/a.png"),
		]);
		expect(r.entries.map((e) => e.path)).toEqual(["index.html", "img/a.png"]);
		expect(r.name).toBe("site");
	});
});
