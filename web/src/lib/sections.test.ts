import { afterEach, describe, expect, it } from "vitest";
import { MORE_ENTRIES } from "../components/layout/more-entries";
import zhCN from "../i18n/locales/zh-CN.json";
import {
	defaultSectionPath,
	loadSectionPaths,
	SECTION_ORDER,
	SECTION_PATHS_KEY,
	sectionOf,
	sectionTarget,
	settles,
	type ThreadKindOf,
} from "./sections";

const KINDS: Record<string, "work" | "space" | "assistant"> = {
	"T-work": "work",
	"T-asst": "assistant",
	"T-chat": "space",
};
const kindOf: ThreadKindOf = (id) => KINDS[id];

afterEach(() => {
	sessionStorage.clear();
});

describe("sectionOf", () => {
	it.each([
		["/", "threads"],
		["/t/T-work", "threads"],
		["/space/spc_1", "threads"],
		["/inbox", "threads"],
		["/assistant", "assistant"],
		["/assistant/T-asst", "assistant"],
		["/projects", "projects"],
		["/projects/new", "projects"],
		["/projects/prj_1", "projects"],
		["/settings", "more"],
		["/settings/secrets", "more"],
		["/stats", "more"],
		["/stats/usage", "more"],
		["/artifacts", "more"],
		["/artifacts/trash", "more"],
		["/artifacts/art_01JDNAWEB0000000000000000A?history=1", "more"],
		["/settings/secrets?x=1#y", "more"],
		["/projects?tab=1", "projects"],
	] as const)("%s → %s", (path, want) => {
		expect(sectionOf(path)).toBe(want);
	});

	it("前缀按整段匹配，不吃掉同前缀的别的路径", () => {
		expect(sectionOf("/assistantx")).toBe("threads");
		expect(sectionOf("/projectsfoo")).toBe("threads");
		expect(sectionOf("/settingsx/a")).toBe("threads");
		expect(sectionOf("/statsx")).toBe("threads");
		expect(sectionOf("/artifactsx")).toBe("threads");
	});
});

describe("defaultSectionPath", () => {
	it("各分区的缺省落点；更多 = 注册表第一项", () => {
		expect(defaultSectionPath("threads")).toBe("/");
		expect(defaultSectionPath("assistant")).toBe("/assistant");
		expect(defaultSectionPath("projects")).toBe("/projects");
		expect(defaultSectionPath("more")).toBe(MORE_ENTRIES[0]?.to);
		expect(defaultSectionPath("more")).toBe("/settings/automations");
	});

	it("每个分区的缺省落点都属于该分区", () => {
		for (const s of SECTION_ORDER)
			expect(sectionOf(defaultSectionPath(s))).toBe(s);
	});
});

describe("settles", () => {
	it("协调对话的 /t/<id> 会被重定向，不算落定", () => {
		expect(settles("/t/T-asst", kindOf)).toBe(false);
		expect(settles("/t/T-chat", kindOf)).toBe(false);
		expect(settles("/t/T-asst?x=1", kindOf)).toBe(false);
	});

	it("工作线程、未知线程与其它路径都算落定", () => {
		expect(settles("/t/T-work", kindOf)).toBe(true);
		expect(settles("/t/T-unknown", kindOf)).toBe(true);
		expect(settles("/assistant/T-asst", kindOf)).toBe(true);
		expect(settles("/", kindOf)).toBe(true);
	});
});

describe("sectionTarget", () => {
	it("合法的记忆原样使用（含 search）", () => {
		expect(sectionTarget("more", "/settings/secrets", kindOf)).toBe(
			"/settings/secrets",
		);
		expect(sectionTarget("threads", "/t/T-work?q=1", kindOf)).toBe(
			"/t/T-work?q=1",
		);
		expect(sectionTarget("assistant", "/assistant/T-asst", kindOf)).toBe(
			"/assistant/T-asst",
		);
	});

	it("没有记忆、属于别的分区、协议相对路径、不落定 → 缺省路径", () => {
		expect(sectionTarget("more", undefined, kindOf)).toBe(
			"/settings/automations",
		);
		expect(sectionTarget("more", "/t/T-work", kindOf)).toBe(
			"/settings/automations",
		);
		expect(sectionTarget("threads", "//evil.example/t/x", kindOf)).toBe("/");
		expect(sectionTarget("threads", "t/T-work", kindOf)).toBe("/");
		expect(sectionTarget("threads", "/t/T-asst", kindOf)).toBe("/");
	});
});

describe("loadSectionPaths", () => {
	it("存储为空或不可解析 → {}", () => {
		expect(loadSectionPaths()).toEqual({});
		sessionStorage.setItem(SECTION_PATHS_KEY, "{not json");
		expect(loadSectionPaths()).toEqual({});
		sessionStorage.setItem(SECTION_PATHS_KEY, "[1,2]");
		expect(loadSectionPaths()).toEqual({});
	});

	it("逐项校验，坏项丢弃", () => {
		sessionStorage.setItem(
			SECTION_PATHS_KEY,
			JSON.stringify({
				threads: "/t/T-work",
				assistant: "/settings/secrets", // 不属于该分区
				projects: "//evil.example/projects", // 协议相对
				more: 42, // 不是字符串
				bogus: "/x", // 未知分区
			}),
		);
		expect(loadSectionPaths()).toEqual({ threads: "/t/T-work" });
	});

	it("合法项全部保留", () => {
		const all = {
			threads: "/",
			assistant: "/assistant/T-asst",
			projects: "/projects/prj_1",
			more: "/stats/usage",
		};
		sessionStorage.setItem(SECTION_PATHS_KEY, JSON.stringify(all));
		expect(loadSectionPaths()).toEqual(all);
	});
});

describe("MORE_ENTRIES", () => {
	it("键唯一、路径都属于「更多」分区、文案键存在", () => {
		const keys = MORE_ENTRIES.map((e) => e.key);
		expect(new Set(keys).size).toBe(keys.length);
		for (const e of MORE_ENTRIES) {
			expect(sectionOf(e.to)).toBe("more");
			const label = e.labelKey
				.split(".")
				.reduce<unknown>(
					(node, k) => (node as Record<string, unknown> | undefined)?.[k],
					zhCN,
				);
			expect(typeof label, e.labelKey).toBe("string");
		}
		expect(MORE_ENTRIES.filter((e) => e.desktopOnly).map((e) => e.key)).toEqual(
			["desktop"],
		);
	});
});
