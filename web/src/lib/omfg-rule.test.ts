import { describe, expect, it } from "vitest";
import { parseOmfgWidget, splitConfirmRule } from "./omfg-rule";

const RULE_LINES = [
	"---",
	"name: no-npm",
	'description: "Use bun"',
	'condition: "\\\\bnpm install\\\\b"',
	'scope: ["text", "tool:bash"]',
	"---",
	"",
	"Use `bun install`.",
];

describe("parseOmfgWidget（Core rpc-omfg.ts 的 setWidget 形状）", () => {
	it("首行校验状态 + 空行 + 规则全文", () => {
		const w = parseOmfgWidget("omfg", [
			"TTSR rule — matches this conversation",
			"",
			...RULE_LINES,
		]);
		expect(w?.validation).toBe("matched");
		expect(w?.rule.fields.find((f) => f.key === "condition")?.value).toBe(
			"\\bnpm install\\b",
		);
		expect(w?.rule.body).toBe("Use `bun install`.");
	});

	it("三种已知状态与未知状态（保留原文）", () => {
		const of = (label: string) =>
			parseOmfgWidget("omfg", [`TTSR rule — ${label}`, "", ...RULE_LINES]);
		expect(of("validating against this conversation…")?.validation).toBe(
			"pending",
		);
		expect(
			of("could not confirm a match in this conversation")?.validation,
		).toBe("unconfirmed");
		const unknown = of("something new");
		expect(unknown?.validation).toBeNull();
		expect(unknown?.label).toBe("something new");
	});

	it("格式不符 → null：别的 key、缺空行、没有 frontmatter、frontmatter 不是规则", () => {
		const header = "TTSR rule — matches this conversation";
		expect(parseOmfgWidget("other", [header, "", ...RULE_LINES])).toBeNull();
		expect(parseOmfgWidget("omfg", [header, ...RULE_LINES])).toBeNull();
		expect(
			parseOmfgWidget("omfg", [header, "", "## Never use npm"]),
		).toBeNull();
		expect(
			parseOmfgWidget("omfg", [header, "", "---", "title: x", "---"]),
		).toBeNull();
	});
});

describe("splitConfirmRule（Core withRule：message + 空行 + 规则）", () => {
	it("拆出前半句与规则", () => {
		const r = splitConfirmRule(
			`Couldn't confirm this rule matches the conversation. Save anyway?\n\n${RULE_LINES.join("\n")}`,
		);
		expect(r?.message).toBe(
			"Couldn't confirm this rule matches the conversation. Save anyway?",
		);
		expect(r?.rule.fields[0]).toEqual({ key: "name", value: "no-npm" });
	});

	it("没有规则的 confirm 原样", () => {
		expect(splitConfirmRule("a.md already exists. Overwrite it?")).toBeNull();
		expect(splitConfirmRule("x\n\n---\nnot a rule\n---")).toBeNull();
	});
});
