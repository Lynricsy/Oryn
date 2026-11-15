/**
 * 内置 role 与内置 agent 的默认头像字形（前端常量，不入库）。
 * 码点取自 Nerd Fonts 3.4 官方 glyphnames（oryn-core `packages/coding-agent/src/utils/nerd-font-glyphs.txt`），
 * 按名字逐个核对过，不要凭记忆改码点。
 */

import type { BuiltinRole } from "./model-roles";

/** 内置 role 的默认字形；自定义 role 没有默认字形，回落到默认 Agent 身份。 */
export const ROLE_DEFAULT_GLYPHS: Readonly<Record<BuiltinRole, string>> = {
	default: "\u{f133c}", // md-head_cog
	smol: "\u{f140b}", // md-lightning_bolt
	slow: "\u{f051f}", // md-timer_sand
	vision: "\u{f0208}", // md-eye
	plan: "\u{f0756}", // md-format_list_checks
	commit: "\u{f0718}", // md-source_commit
	tiny: "\u{f06d3}", // md-feather
	memory: "\u{f035b}", // md-memory
	task: "\u{f1323}", // md-hammer_wrench
	advisor: "\u{f1344}", // md-head_lightbulb
	widget: "\u{f072c}", // md-widgets
};

/** 内置 agent（Core 打包的 task 定义与 advisor 类）的默认字形。 */
export const AGENT_DEFAULT_GLYPHS: Readonly<Record<string, string>> = {
	task: "\u{f05b5}", // md-account_hard_hat
	sonic: "\u{f046e}", // md-run_fast
	scout: "\u{f0b4e}", // md-telescope
	reviewer: "\u{f0349}", // md-magnify
	"security-reviewer": "\u{f0d9a}", // md-shield_search
	advisor: "\u{f0a8b}", // md-account_supervisor
};

/** 没设头像的自定义 agent 用的通用字形（md-robot_outline）。 */
export const GENERIC_AGENT_GLYPH = "\u{f167a}";

/** 内置 role 的默认字形；不是内置 role 时返回 undefined。 */
export function roleDefaultGlyph(role: string): string | undefined {
	return Object.hasOwn(ROLE_DEFAULT_GLYPHS, role)
		? ROLE_DEFAULT_GLYPHS[role as BuiltinRole]
		: undefined;
}

/** agent 的默认字形：内置 agent 用各自的字形，其余用通用字形。 */
export function agentDefaultGlyph(agent: string): string {
	// hasOwn 挡住 "constructor" 之类的原型属性名
	return (
		(Object.hasOwn(AGENT_DEFAULT_GLYPHS, agent) &&
			AGENT_DEFAULT_GLYPHS[agent]) ||
		GENERIC_AGENT_GLYPH
	);
}
