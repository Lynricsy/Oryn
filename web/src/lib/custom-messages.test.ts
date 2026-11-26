import { describe, expect, it } from "vitest";
import {
	ADVISOR_MESSAGE_TYPE,
	BACKGROUND_TAN_DISPATCH_MESSAGE_TYPE,
	firstCustomMessagePreview,
	HANDOFF_MESSAGE_TYPE,
	isSilentAbortMessage,
	LSP_LATE_DIAGNOSTIC_MESSAGE_TYPE,
	parseAdvisorMessage,
	parseHandoffMessage,
	parseIrcCustomMessage,
	parseLateDiagnostics,
	parseSkillPrompt,
	parseTanDispatch,
	SILENT_ABORT_MARKER,
	SKILL_PROMPT_MESSAGE_TYPE,
	unescapeXml,
} from "./custom-messages";

describe("parseAdvisorMessage", () => {
	it("优先解析 details.notes 并按最重严重度汇总计数与来源", () => {
		const parsed = parseAdvisorMessage({
			customType: ADVISOR_MESSAGE_TYPE,
			content: '<advisory severity="nit">ignored</advisory>',
			details: {
				notes: [
					{ note: "Nit note", severity: "nit", advisor: "default" },
					{
						note: "Blocker note",
						severity: "blocker",
						advisor: "security",
						turnsAgo: 2,
					},
					{ note: "Concern note", severity: "concern", advisor: "security" },
				],
			},
		});
		expect(parsed).toMatchObject({
			worstRank: "blocker",
			soleSource: undefined,
			mixedSources: true,
			sources: ["security"],
			counts: [
				{ rank: "blocker", count: 1 },
				{ rank: "concern", count: 1 },
				{ rank: "nit", count: 1 },
			],
		});
		expect(parsed?.notes[0]?.source).toBeUndefined();
		expect(parsed?.notes[1]).toMatchObject({
			source: "security",
			turnsAgo: 2,
		});
	});

	it("所有建议都来自同一具名顾问时识别 soleSource 且不标 mixedSources", () => {
		const parsed = parseAdvisorMessage({
			customType: ADVISOR_MESSAGE_TYPE,
			details: {
				notes: [
					{ note: "Check SQL", severity: "concern", advisor: "auditor" },
					{ note: "Add index", severity: "nit", advisor: "auditor" },
				],
			},
		});
		expect(parsed?.soleSource).toBe("auditor");
		expect(parsed?.mixedSources).toBe(false);
	});

	it("缺少 details.notes 时从 <advisory> XML 块恢复内容并反转义实体", () => {
		const parsed = parseAdvisorMessage({
			customType: ADVISOR_MESSAGE_TYPE,
			content:
				'<advisory advisor="arch" severity="concern" turns_ago="3" guidance="weigh">\nUse &lt;T&gt; &amp; check "null"\n</advisory>\n<advisory severity="nit" guidance="weigh">\nRename helper\n</advisory>',
		});
		expect(parsed).not.toBeNull();
		expect(parsed?.worstRank).toBe("concern");
		expect(parsed?.mixedSources).toBe(true);
		expect(parsed?.notes).toEqual([
			{
				note: 'Use <T> & check "null"',
				severity: "concern",
				rank: "concern",
				source: "arch",
				turnsAgo: 3,
			},
			{
				note: "Rename helper",
				severity: "nit",
				rank: "nit",
				source: undefined,
				turnsAgo: undefined,
			},
		]);
	});

	it("完全空内容返回 null", () => {
		expect(
			parseAdvisorMessage({
				customType: ADVISOR_MESSAGE_TYPE,
				content: "   ",
				details: { notes: [] },
			}),
		).toBeNull();
	});
});

describe("parseLateDiagnostics", () => {
	it("解析 details.files 并汇总错误态与消息总数", () => {
		const parsed = parseLateDiagnostics({
			customType: LSP_LATE_DIAGNOSTIC_MESSAGE_TYPE,
			content: "<system-notice>\nLate LSP diagnostics\n</system-notice>",
			details: {
				files: [
					{
						path: "src/a.ts",
						summary: "1 error",
						errored: true,
						messages: ["src/a.ts:1:1 [error] Type mismatch"],
					},
					{
						path: "src/b.ts",
						summary: "1 warning",
						errored: false,
						messages: ["src/b.ts:9:2 [warning] Unused var"],
					},
				],
			},
		});
		expect(parsed).toMatchObject({
			errored: true,
			totalMessages: 2,
			summary: "1 error · 1 warning",
		});
		expect(parsed?.files).toHaveLength(2);
	});

	it("缺少 details 时剥离 <system-notice> 作为纯文本兜底", () => {
		const parsed = parseLateDiagnostics({
			customType: LSP_LATE_DIAGNOSTIC_MESSAGE_TYPE,
			content: "<system-notice>\nLate diagnostics line 1\n</system-notice>",
		});
		expect(parsed?.fallbackText).toBe("Late diagnostics line 1");
	});
});

describe("parseSkillPrompt", () => {
	it("去掉开头 /skill:<name> 标记并保留完整技能提示词", () => {
		const parsed = parseSkillPrompt({
			customType: SKILL_PROMPT_MESSAGE_TYPE,
			content: '[IMPORTANT: User invoked "review"]\n\nFull skill body',
			details: {
				name: "review",
				path: "/skills/review/SKILL.md",
				prompt: "/skill:review 检查这次改动",
				lineCount: 42,
			},
		});
		expect(parsed).toEqual({
			name: "review",
			path: "/skills/review/SKILL.md",
			lineCount: 42,
			draft: "检查这次改动",
			promptText: '[IMPORTANT: User invoked "review"]\n\nFull skill body',
		});
	});

	it("只有 /skill:<name> 没有附加文字时 draft 为空", () => {
		const parsed = parseSkillPrompt({
			customType: SKILL_PROMPT_MESSAGE_TYPE,
			content: "Skill body",
			details: { name: "deploy", prompt: "/skill:deploy" },
		});
		expect(parsed?.draft).toBe("");
	});
});

describe("parseHandoffMessage", () => {
	it("剥离 <handoff-context> 标签保留交接正文", () => {
		expect(
			parseHandoffMessage({
				customType: HANDOFF_MESSAGE_TYPE,
				content:
					"Intro\n<handoff-context>\n## Goal\nFinish migration\n</handoff-context>",
			}),
		).toBe("## Goal\nFinish migration");
	});
});

describe("parseIrcCustomMessage & parseTanDispatch", () => {
	it("解析 irc:incoming 的 details 并在缺失时剥离 <irc> 标签", () => {
		const withDetails = parseIrcCustomMessage({
			customType: "irc:incoming",
			content: "<irc>\nwrapped\n</irc>",
			details: {
				from: "worker-1",
				fromLabel: "Scout",
				message: "Found 3 matches",
				progress: true,
			},
		});
		expect(withDetails).toMatchObject({
			kind: "incoming",
			from: "Scout",
			body: "Found 3 matches",
			progress: true,
		});

		const xmlOnly = parseIrcCustomMessage({
			customType: "irc:incoming",
			content: "<irc>\nIncoming message body\n</irc>",
		});
		expect(xmlOnly?.body).toBe("Incoming message body");
	});

	it("解析 background-tan-dispatch", () => {
		expect(
			parseTanDispatch({
				customType: BACKGROUND_TAN_DISPATCH_MESSAGE_TYPE,
				details: { jobId: "tan_1", work: "Check CI logs" },
			}),
		).toMatchObject({ jobId: "tan_1", work: "Check CI logs" });
	});
});

describe("firstCustomMessagePreview & unescapeXml & isSilentAbortMessage", () => {
	it("折叠摘要跳过带属性与不带属性的 XML 标签行", () => {
		expect(
			firstCustomMessagePreview(
				'<system-notice id="prelude-guide">\n实际第一行正文\n</system-notice>',
			),
		).toBe("实际第一行正文");
	});

	it("XML 实体反转义不二次展开 &amp;lt;", () => {
		expect(unescapeXml("&amp;lt;div&amp;gt; &lt;b&gt;")).toBe(
			"&lt;div&gt; <b>",
		);
	});

	it("识别 Core 静默中止哨兵", () => {
		expect(
			isSilentAbortMessage({
				stopReason: "aborted",
				errorMessage: SILENT_ABORT_MARKER,
			}),
		).toBe(true);
		expect(
			isSilentAbortMessage({
				stopReason: "aborted",
				errorMessage: "Interrupted by user",
			}),
		).toBe(false);
	});
});
