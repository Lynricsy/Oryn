/**
 * 多文件条目导入的整理规则：路径清洗与杂项过滤、zip 解包、公共顶层目录、技能目录收窄、批量文件项的编码与权限。
 */
import { strToU8, zipSync } from "fflate";
import { describe, expect, it } from "vitest";
import {
	type BundleFile,
	cleanRelPath,
	filesFromZip,
	narrowToSkill,
	skillDescription,
	stripCommonRoot,
	suggestName,
	toBatchFiles,
} from "./agent-bundle";

const file = (path: string, text = ""): BundleFile => ({
	path,
	data: strToU8(text),
});

describe("cleanRelPath", () => {
	it("保留合法相对路径，反斜杠换成 /", () => {
		expect(cleanRelPath("scripts/run.sh")).toBe("scripts/run.sh");
		expect(cleanRelPath("a\\b.md")).toBe("a/b.md");
		expect(cleanRelPath(".env.example")).toBe(".env.example");
	});

	it("拒绝绝对路径、空段、. 与 ..，以及 .git、__MACOSX 和系统杂项文件", () => {
		for (const bad of [
			"/etc/passwd",
			"a//b",
			"a/./b",
			"../x",
			"a/",
			"",
			"repo/.git/config",
			"__MACOSX/a/._b",
			"a/.DS_Store",
			"Thumbs.db",
		])
			expect(cleanRelPath(bad)).toBeNull();
		// 与 Object 原型同名的文件不被误判为杂项
		expect(cleanRelPath("constructor")).toBe("constructor");
	});
});

describe("filesFromZip", () => {
	it("只取文件，跳过目录项与杂项，按路径排序", () => {
		const zip = zipSync({
			"skill/": new Uint8Array(),
			"skill/SKILL.md": strToU8("x"),
			"skill/.DS_Store": strToU8("junk"),
			"__MACOSX/skill/._SKILL.md": strToU8("junk"),
			"skill/a/b.txt": strToU8("b"),
		});
		expect(filesFromZip(zip).map((f) => f.path)).toEqual([
			"skill/a/b.txt",
			"skill/SKILL.md",
		]);
	});
});

describe("stripCommonRoot", () => {
	it("全部在同一个顶层目录下时去掉它", () => {
		const r = stripCommonRoot([file("foo/SKILL.md"), file("foo/a/b.md")]);
		expect(r.root).toBe("foo");
		expect(r.files.map((f) => f.path)).toEqual(["SKILL.md", "a/b.md"]);
	});

	it("顶层有文件或多个目录时不动", () => {
		expect(stripCommonRoot([file("SKILL.md")]).root).toBeNull();
		expect(stripCommonRoot([file("a/x"), file("b/y")]).root).toBeNull();
	});
});

describe("narrowToSkill", () => {
	it("根下有 SKILL.md 时就是根", () => {
		const r = narrowToSkill([file("SKILL.md"), file("examples/x/SKILL.md")]);
		expect(r).toEqual({
			ok: true,
			root: null,
			files: [file("SKILL.md"), file("examples/x/SKILL.md")],
		});
	});

	it("恰有一个最浅的技能目录时收窄到它（其下的示例子技能不算候选）", () => {
		const r = narrowToSkill([
			file("README.md"),
			file("skills/pdf/SKILL.md"),
			file("skills/pdf/examples/demo/SKILL.md"),
		]);
		expect(r).toEqual({
			ok: true,
			root: "pdf",
			files: [file("SKILL.md"), file("examples/demo/SKILL.md")],
		});
	});

	it("没有或有多个技能目录时给出候选", () => {
		expect(narrowToSkill([file("README.md")])).toEqual({
			ok: false,
			candidates: [],
		});
		expect(narrowToSkill([file("b/SKILL.md"), file("a/SKILL.md")])).toEqual({
			ok: false,
			candidates: ["a", "b"],
		});
	});
});

describe("toBatchFiles", () => {
	it("文本按原文、#! 开头给 0755、二进制或非 UTF-8 用 base64", () => {
		expect(
			toBatchFiles("tools/kit", [
				file("index.ts", "export {};\n"),
				file("run.sh", "#!/bin/sh\n"),
				{ path: "logo.png", data: new Uint8Array([0x89, 0x50, 0x00]) },
				{ path: "latin1.txt", data: new Uint8Array([0xe9]) },
			]),
		).toEqual([
			{ path: "tools/kit/index.ts", content: "export {};\n", encoding: "utf8" },
			{
				path: "tools/kit/run.sh",
				content: "#!/bin/sh\n",
				encoding: "utf8",
				mode: 0o755,
			},
			{ path: "tools/kit/logo.png", content: "iVAA", encoding: "base64" },
			{ path: "tools/kit/latin1.txt", content: "6Q==", encoding: "base64" },
		]);
	});
});

describe("skillDescription / suggestName", () => {
	it("读出 SKILL.md 的 description", () => {
		expect(
			skillDescription([file("SKILL.md", "---\ndescription: 审查\n---\n")]),
		).toBe("审查");
		expect(skillDescription([file("SKILL.md", "正文\n")])).toBe("");
	});

	it("建议名：去掉 zip 扩展名，技能转小写并把不合规字符换成 -", () => {
		expect(suggestName("My Skill_v2.zip", true)).toBe("my-skill-v2");
		expect(suggestName("Weather.Kit", false)).toBe("Weather.Kit");
		expect(suggestName("..hidden..", false)).toBe("hidden");
	});
});
