/**
 * Core `/omfg`（`packages/coding-agent/src/modes/rpc/rpc-omfg.ts`）发来的候选 TTSR 规则：
 * - `setWidget("omfg", lines)`：首行 `TTSR rule — <校验状态>`，空行，然后是规则全文；
 * - confirm 对话框的 message 尾部拼上同一份规则（`withRule`：`${message}\n\n${rule}`）。
 * 规则全文由 Core `omfg-rule.ts` 的 `assembleRuleMarkdown` 生成：frontmatter（name / description /
 * condition / astCondition / question / scope，字符串用 JSON.stringify 引号）+ Markdown 正文。
 */
import {
	type FrontmatterDoc,
	fieldValue,
	splitFrontmatter,
} from "./frontmatter";

/** Core 的 widget key（`RPC_OMFG_UI_KEY`）。 */
export const OMFG_WIDGET_KEY = "omfg";

/** 校验状态：Core `VALIDATION_LABEL` 的三种英文句。 */
export type RuleValidation = "pending" | "matched" | "unconfirmed";

const VALIDATION_BY_LABEL: Record<string, RuleValidation> = {
	"validating against this conversation…": "pending",
	"matches this conversation": "matched",
	"could not confirm a match in this conversation": "unconfirmed",
};

const HEADER_RE = /^TTSR rule — (.+)$/;

export interface OmfgWidget {
	/** 认得的校验状态；Core 换了文案时为 null，界面显示 `label` 原文。 */
	validation: RuleValidation | null;
	label: string;
	rule: FrontmatterDoc;
}

/** frontmatter 里有规则名与至少一种触发（Core `parseGeneratedRulePayload` 的必填项）。 */
export function isRuleDoc(doc: FrontmatterDoc): boolean {
	return (
		typeof fieldValue(doc, "name") === "string" &&
		["condition", "astCondition", "question"].some(
			(k) => fieldValue(doc, k) !== undefined,
		)
	);
}

/** 解析 `/omfg` 规则预览 widget；格式不符时返回 null（调用方回落到通用渲染）。 */
export function parseOmfgWidget(
	key: string,
	lines: readonly string[],
): OmfgWidget | null {
	if (key !== OMFG_WIDGET_KEY || lines[1] !== "") return null;
	const label = HEADER_RE.exec(lines[0] ?? "")?.[1]?.trim();
	if (!label) return null;
	const rule = splitFrontmatter(lines.slice(2).join("\n"));
	if (!rule || !isRuleDoc(rule)) return null;
	return { validation: VALIDATION_BY_LABEL[label] ?? null, label, rule };
}

/** 拆出 confirm message 尾部拼接的规则；没有时返回 null。 */
export function splitConfirmRule(
	message: string,
): { message: string; rule: FrontmatterDoc } | null {
	const sep = "\n\n---\n";
	for (
		let at = message.indexOf(sep);
		at >= 0;
		at = message.indexOf(sep, at + 1)
	) {
		const rule = splitFrontmatter(message.slice(at + 2));
		if (rule && isRuleDoc(rule))
			return { message: message.slice(0, at).trimEnd(), rule };
	}
	return null;
}
