import { describe, expect, it } from "vitest";
import type { Entry } from "../api/client";
import {
	type ContextBreakdown,
	contextParts,
	contextPath,
	MESSAGE_KINDS,
	messageWeights,
	splitMessages,
} from "./context-segments";

/** 一条链式条目（parentId 指向上一条）。 */
function chain(
	rows: [type: string, payload: Record<string, unknown>][],
): Entry[] {
	return rows.map(([type, payload], i) => ({
		seq: i + 1,
		file: "s.jsonl",
		entryId: `e${i + 1}`,
		parentId: i > 0 ? `e${i}` : undefined,
		type,
		payload: { type, ...payload },
	}));
}

const user = (text: string): [string, Record<string, unknown>] => [
	"message",
	{ message: { role: "user", content: text } },
];

const BREAKDOWN: ContextBreakdown = {
	contextWindow: 200_000,
	anchored: true,
	usedTokens: 20_000,
	systemPromptTokens: 3000,
	systemToolsTokens: 4000,
	systemContextTokens: 1000,
	instructionFilesTokens: 1500,
	skillsTokens: 500,
	messagesTokens: 10_000,
};

describe("上下文分段", () => {
	it("按字符量把消息分给 5 类，取整后总和恰为 messagesTokens", () => {
		const path = chain([
			user("x".repeat(100)),
			[
				"message",
				{
					message: {
						role: "assistant",
						content: [
							{ type: "thinking", thinking: "t".repeat(300) },
							{ type: "text", text: "a".repeat(200) },
							{
								type: "toolCall",
								name: "read",
								arguments: { path: "src/a.ts" },
							},
						],
					},
				},
			],
			[
				"message",
				{
					message: {
						role: "toolResult",
						content: [{ type: "text", text: "o".repeat(377) }],
					},
				},
			],
			[
				"message",
				{
					message: {
						role: "bashExecution",
						command: "ls",
						output: "abc",
					},
				},
			],
		]);
		const w = messageWeights(path);
		expect(w).toEqual({
			userMessages: 100,
			assistantMessages: 200,
			thinking: 300,
			toolCalls: 4 + JSON.stringify({ path: "src/a.ts" }).length,
			toolOutput: 377 + 5,
		});
		for (const total of [10_000, 7, 12_345, 1]) {
			const split = splitMessages(total, w);
			expect(split).not.toBeNull();
			expect(MESSAGE_KINDS.reduce((a, k) => a + (split?.[k] ?? 0), 0)).toBe(
				total,
			);
		}
		const parts = contextParts(BREAKDOWN, path);
		expect(parts.map((p) => p.id)).toEqual([
			"systemPrompt",
			"systemContext",
			"instructions",
			"skills",
			"systemTools",
			...MESSAGE_KINDS,
		]);
		expect(parts.filter((p) => p.estimated)).toHaveLength(5);
		expect(
			parts.filter((p) => p.estimated).reduce((a, p) => a + p.tokens, 0),
		).toBe(BREAKDOWN.messagesTokens);
	});

	it("压缩之前、保留段之外的条目不计入；压缩摘要算用户消息", () => {
		const path = chain([
			user("old".repeat(1000)),
			user("kept".repeat(10)),
			["compaction", { summary: "s".repeat(50), firstKeptEntryId: "e2" }],
			user("new".repeat(10)),
		]);
		const ctx = contextPath(path, "e4");
		expect(ctx?.map((e) => e.entryId)).toEqual(["e3", "e2", "e4"]);
		expect(messageWeights(ctx ?? []).userMessages).toBe(50 + 40 + 30);
	});

	it("/clear 晚于压缩时只算边界之后；保留段起点不在分支上时认不出边界", () => {
		const cleared = chain([
			user("a"),
			["compaction", { summary: "s", firstKeptEntryId: "" }],
			["reset_boundary", {}],
			user("b"),
		]);
		expect(contextPath(cleared, "e4")?.map((e) => e.entryId)).toEqual(["e4"]);
		const broken = chain([
			user("a"),
			["compaction", { summary: "s", firstKeptEntryId: "missing" }],
		]);
		expect(contextPath(broken, "e2")).toBeNull();
		expect(contextParts(BREAKDOWN, broken, "e2").at(-1)).toEqual({
			id: "messages",
			tokens: 10_000,
			estimated: false,
		});
	});

	it("没有转录或转录里没有可计字符时退回一整段「消息」", () => {
		for (const entries of [null, [], chain([["model_change", {}]])]) {
			const parts = contextParts(BREAKDOWN, entries);
			expect(parts).toHaveLength(6);
			expect(parts.at(-1)).toEqual({
				id: "messages",
				tokens: 10_000,
				estimated: false,
			});
		}
	});
});
