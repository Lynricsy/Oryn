import { describe, expect, it } from "vitest";
import type { Entry } from "../../api/client";
import {
	buildRewindTargets,
	matchRewindTargets,
	siblingBranchesFor,
	stepUserTurn,
} from "./rewind-model";

let seq = 0;
function mkEntry(
	entryId: string,
	parentId: string | null,
	type: string,
	payload: Record<string, unknown>,
	file = "s1.jsonl",
): Entry {
	return {
		seq: ++seq,
		file,
		entryId,
		parentId: parentId ?? undefined,
		type,
		payload,
	};
}

const userMsg = (
	id: string,
	parent: string | null,
	text: string,
	extra?: Record<string, unknown>,
	file?: string,
) =>
	mkEntry(
		id,
		parent,
		"message",
		{ message: { role: "user", content: text, ...extra } },
		file,
	);

const assistantMsg = (
	id: string,
	parent: string | null,
	content: unknown[],
	extra?: Record<string, unknown>,
	file?: string,
) =>
	mkEntry(
		id,
		parent,
		"message",
		{ message: { role: "assistant", content, ...extra } },
		file,
	);

const toolResultMsg = (
	id: string,
	parent: string | null,
	toolCallId: string,
	toolName: string,
	text: string,
	file?: string,
) =>
	mkEntry(
		id,
		parent,
		"message",
		{
			message: {
				role: "toolResult",
				toolCallId,
				toolName,
				content: [{ type: "text", text }],
			},
		},
		file,
	);

describe("buildRewindTargets", () => {
	it("包含用户消息、助手回复、工具调用与可见 custom_message，且 toolResult 折叠推进 targetEntryId", () => {
		seq = 0;
		const entries: Entry[] = [
			mkEntry(
				"old_u",
				null,
				"message",
				{ message: { role: "user", content: "旧会话" } },
				"s0.jsonl",
			),
			mkEntry("__session__", null, "session", { type: "session" }),
			mkEntry("m1", null, "model_change", {
				provider: "anthropic",
				modelId: "claude",
			}),
			userMsg("u1", "m1", "查一下数据库报错"),
			assistantMsg("a1", "u1", [
				{ type: "thinking", thinking: "先查日志" },
				{
					type: "toolCall",
					id: "tc1",
					name: "bash",
					intent: "查询最近报错",
					arguments: { command: "ls -la /var/log" },
				},
				{
					type: "toolCall",
					id: "tc2",
					name: "read",
					arguments: { path: "app.log" },
				},
			]),
			toolResultMsg("tr1", "a1", "tc1", "bash", "total 42"),
			toolResultMsg("tr2", "tr1", "tc2", "read", "ERROR 502 upstream"),
			// 空助手消息不渲染、不进目标
			assistantMsg("a_empty", "tr2", []),
			assistantMsg("a2", "a_empty", [
				{ type: "text", text: "找到根因：上游 502" },
			]),
			mkEntry("cm1", "a2", "custom_message", {
				customType: "note",
				content: "补充备注",
				display: true,
			}),
			mkEntry("cm_hidden", "cm1", "custom_message", {
				customType: "internal",
				content: "隐藏",
				display: false,
			}),
			mkEntry("mu1", "cm_hidden", "model_usage", { activeLeafId: "cm_hidden" }),
		];

		const targets = buildRewindTargets(entries, undefined);
		expect(
			targets.map((t) => [
				t.anchorEntryId,
				t.targetEntryId,
				t.kind,
				t.rewindsBefore,
			]),
		).toEqual([
			["u1", "u1", "user", true],
			["a1", "tr2", "tool", false],
			["a2", "a2", "assistant", false],
			["cm1", "cm1", "custom", true],
		]);
		// 工具调用目标涵盖自身和两条折叠进来的 toolResult
		expect(targets[1]?.entryIds).toEqual(["a1", "tr1", "tr2"]);
		// 仅限当前会话文件，不混入 s0.jsonl
		expect(targets.some((t) => t.anchorEntryId === "old_u")).toBe(false);
	});

	it("最后一条是工具调用时，最后一个可 rewind 目标就是该工具调用且 targetEntryId 指向其 toolResult", () => {
		seq = 0;
		const entries: Entry[] = [
			userMsg("u1", null, "跑测试"),
			assistantMsg("a1", "u1", [
				{
					type: "toolCall",
					id: "tc1",
					name: "bash",
					arguments: { command: "go test" },
				},
			]),
			toolResultMsg("tr1", "a1", "tc1", "bash", "PASS"),
		];
		const targets = buildRewindTargets(entries, "tr1");
		const last = targets[targets.length - 1];
		expect(last?.anchorEntryId).toBe("a1");
		expect(last?.targetEntryId).toBe("tr1");
		expect(last?.kind).toBe("tool");
	});

	it("根前空分支（leafEntryId 为空串）返回空列表", () => {
		seq = 0;
		const entries: Entry[] = [userMsg("u1", null, "hi")];
		expect(buildRewindTargets(entries, "")).toEqual([]);
	});
});

describe("siblingBranchesFor", () => {
	it("识别同一分叉点（含跨过单链元数据与根分叉）的兄弟分支，并将当前分支排在首位", () => {
		seq = 0;
		const entries: Entry[] = [
			userMsg("u1", null, "根分支 A"),
			assistantMsg("a1", "u1", [{ type: "text", text: "A 的回复" }]),
			// 在 a1 后分叉：分支 1 经 u2 -> a2，分支 2 经 model_change -> u2b -> a2b
			userMsg("u2", "a1", "分支一问题"),
			assistantMsg("a2", "u2", [{ type: "text", text: "分支一回答" }]),
			mkEntry("mc", "a1", "model_change", { modelId: "opus" }),
			userMsg("u2b", "mc", "分支二问题"),
			assistantMsg("a2b", "u2b", [{ type: "text", text: "分支二回答" }]),
			// 离分支标记不应误判为兄弟分支
			mkEntry("lf1", "a2", "custom", {
				customType: "oryn-leaf",
				activeLeafId: "a2",
			}),
			// 根分叉：另一条从 null 起的根分支
			userMsg("u1_alt", null, "根分支 B"),
		];

		const sibs = siblingBranchesFor(entries, "u2", "a2");
		expect(sibs).toHaveLength(2);
		expect(sibs[0]).toMatchObject({
			rootEntryId: "u2",
			leafEntryId: "a2",
			isCurrent: true,
		});
		expect(sibs[1]).toMatchObject({
			rootEntryId: "mc",
			firstTargetId: "u2b",
			leafEntryId: "a2b",
			isCurrent: false,
		});

		const rootSibs = siblingBranchesFor(entries, "u1", "a2");
		expect(rootSibs).toHaveLength(2);
		expect(rootSibs[0]).toMatchObject({
			rootEntryId: "u1",
			leafEntryId: "a2",
			isCurrent: true,
		});
		expect(rootSibs[1]).toMatchObject({
			rootEntryId: "u1_alt",
			leafEntryId: "u1_alt",
			isCurrent: false,
		});

		// 没有分叉的节点返回空数组
		expect(siblingBranchesFor(entries, "a1", "a2")).toEqual([]);
	});
});

describe("matchRewindTargets & stepUserTurn", () => {
	it("英文整词匹配、中文子串匹配、并检索工具名与 toolResult 输出", () => {
		seq = 0;
		const entries: Entry[] = [
			userMsg("u1", null, "请检查 tools 目录"),
			assistantMsg("a1", "u1", [
				{
					type: "toolCall",
					id: "tc1",
					name: "bash",
					arguments: { command: "ls -la" },
				},
			]),
			toolResultMsg("tr1", "a1", "tc1", "bash", "upstream 502 error"),
			userMsg("u2", "tr1", "[oryn:cron] 自动巡检", { synthetic: true }),
			userMsg("u3", "u2", "继续排查超时"),
		];
		const targets = buildRewindTargets(entries, "u3");
		// 英文整词：ls 命中 `ls -la`（索引 1），不误命中 `tools`（索引 0）
		expect(matchRewindTargets(targets, "ls")).toEqual([1]);
		// 跨工具名 + toolResult 多词组合
		expect(matchRewindTargets(targets, "bash 502")).toEqual([1]);
		// 中文子串匹配
		expect(matchRewindTargets(targets, "排查")).toEqual([3]);
		// stepUserTurn 跳过合成/自动消息（u2），直接从 u3 跳回 u1
		expect(stepUserTurn(targets, 3, -1)).toBe(0);
		expect(stepUserTurn(targets, 0, 1)).toBe(3);
	});
});
