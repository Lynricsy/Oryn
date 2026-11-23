import { describe, expect, it } from "vitest";
import type { Entry } from "../api/client";
import { type JobsSnapshot, mergeJobs } from "./jobs";
import type { LiveTool } from "./thread-reducer";

let seq = 0;
function entry(
	type: string,
	payload: Record<string, unknown>,
	ts?: number,
): Entry {
	seq++;
	return {
		seq,
		file: "main.jsonl",
		entryId: `e${seq}`,
		type,
		ts,
		payload: payload as Entry["payload"],
	};
}

/** 一次转入后台的 bash 调用：助手的 toolCall + 带 `details.async` 的工具结果。 */
function backgroundCall(
	callId: string,
	jobId: string,
	command: string,
	ts: number,
): Entry[] {
	return [
		entry("message", {
			message: {
				role: "assistant",
				content: [
					{
						type: "toolCall",
						id: callId,
						name: "bash",
						arguments: { command, cwd: "/repo", i: "后台跑测试" },
					},
				],
			},
		}),
		entry(
			"message",
			{
				message: {
					role: "toolResult",
					toolCallId: callId,
					toolName: "bash",
					content: [{ type: "text", text: `Background job ${jobId} started` }],
					details: { async: { state: "running", jobId, type: "bash" } },
				},
			},
			ts,
		),
	];
}

function delivery(
	jobId: string,
	status: string,
	text: string,
	ts: number,
): Entry {
	return entry(
		"custom_message",
		{
			customType: "async-result",
			display: true,
			content: `<system-notice>\nBackground job ${jobId} has completed.\nResume your work using the result below.\n${text}\n</system-notice>`,
			details: { jobs: [{ jobId, type: "bash", status, durationMs: 5000 }] },
		},
		ts,
	);
}

const NO_LIVE: Record<string, LiveTool> = {};

describe("mergeJobs", () => {
	it("后台调用 + 实时帧 + 交付：终态取交付，输出取交付的结果，按 toolCallId 能找到任务", () => {
		const entries = [
			...backgroundCall("c1", "bg_1", "go test ./...", 1000),
			delivery("bg_1", "failed", "FAIL oryn/x", 6000),
		];
		const live: Record<string, LiveTool> = {
			c1: {
				toolCallId: "c1",
				toolName: "bash",
				ended: false,
				startedAt: 0,
				partialResult: {
					content: [{ type: "text", text: "=== RUN TestX" }],
					details: { async: { state: "running", jobId: "bg_1", type: "bash" } },
				},
			},
		};
		const { jobs, byToolCall, byKey } = mergeJobs(
			entries,
			live,
			undefined,
			false,
		);
		expect(jobs).toHaveLength(1);
		const job = byKey.get(byToolCall.get("c1") ?? "");
		expect(job).toMatchObject({
			id: "bg_1",
			status: "failed",
			command: "go test ./...",
			cwd: "/repo",
			intent: "后台跑测试",
			startedAt: 1000,
			endedAt: 6000,
			durationMs: 5000,
			error: "FAIL oryn/x",
		});
		// 交付之后实时帧里的尾巴不再盖过最终结果
		expect(job?.output).toBe("FAIL oryn/x");
		expect(job?.events.map((e) => e.kind)).toEqual(["started", "failed"]);
	});

	it("快照里的 running 不盖过已知终态；运行中的任务从快照补上可取消与小组件", () => {
		const entries = [
			...backgroundCall("c1", "bg_1", "sleep 1", 1000),
			delivery("bg_1", "completed", "ok", 2000),
			...backgroundCall("c2", "bg_2", "make build", 3000),
		];
		const snapshot = {
			jobs: [],
			tree: [
				{
					depth: 0,
					job: {
						id: "bg_1",
						type: "bash",
						status: "running",
						label: "sleep 1",
						startTime: 1000,
					},
				},
				{
					depth: 0,
					cancellable: true,
					job: {
						id: "bg_2",
						type: "bash",
						status: "running",
						label: "make build",
						startTime: 3000,
						caption: "compiling",
					},
				},
			],
		} as unknown as JobsSnapshot;
		const { jobs } = mergeJobs(entries, NO_LIVE, snapshot, true);
		expect(jobs.map((j) => [j.id, j.status, j.cancellable])).toEqual([
			["bg_1", "completed", false],
			["bg_2", "running", true],
		]);
		expect(jobs[1]?.caption).toBe("compiling");
	});

	it("快照是在任务出现之后取的、却没列出它：判为已结束；快照较旧时保持运行中", () => {
		const entries = backgroundCall("c1", "bg_1", "sleep 99", 1000);
		const empty = { jobs: [] } as unknown as JobsSnapshot;
		expect(mergeJobs(entries, NO_LIVE, empty, true).jobs[0]?.status).toBe(
			"ended",
		);
		expect(mergeJobs(entries, NO_LIVE, empty, false).jobs[0]?.status).toBe(
			"running",
		);
	});

	it("Core 重启后复用同一 id：各算一次，交付落在最近的那次；重投的交付不重复记终态", () => {
		const entries = [
			...backgroundCall("c1", "bg_1", "first", 1000),
			delivery("bg_1", "completed", "one", 2000),
			...backgroundCall("c2", "bg_1", "second", 3000),
			delivery("bg_1", "completed", "two", 4000),
			delivery("bg_1", "completed", "two", 4100),
		];
		const { jobs, byToolCall } = mergeJobs(entries, NO_LIVE, undefined, false);
		expect(jobs.map((j) => [j.key, j.command, j.output])).toEqual([
			["bg_1", "first", "one"],
			["bg_1#2", "second", "two"],
		]);
		expect(byToolCall.get("c2")).toBe("bg_1#2");
		expect(jobs[1]?.events.map((e) => e.kind)).toEqual([
			"started",
			"completed",
		]);
	});
});
