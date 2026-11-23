import { describe, expect, it } from "vitest";
import { parseAsyncResult } from "./async-jobs";

describe("parseAsyncResult", () => {
	it("单任务：取 details 的状态与时长，正文去掉通知包裹与标题行", () => {
		const jobs = parseAsyncResult({
			content:
				"<system-notice>\nBackground job bg_3 (go test ./...) has completed.\nResume your work using the result below.\nok  \toryn/internal\t1.9s\n</system-notice>",
			details: {
				jobs: [
					{
						jobId: "bg_3",
						type: "bash",
						status: "failed",
						label: "go test ./...",
						durationMs: 48_200,
					},
				],
			},
		});
		expect(jobs).toEqual([
			{
				jobId: "bg_3",
				type: "bash",
				status: "failed",
				label: "go test ./...",
				agentId: null,
				durationMs: 48_200,
				result: "ok  \toryn/internal\t1.9s",
			},
		]);
	});

	it("多任务：按分段行把正文分给各自的任务", () => {
		const jobs = parseAsyncResult({
			content: [
				{
					type: "text",
					text: "<system-notice>\n2 background jobs have completed. Resume your work using the results below.\n\n── Job bg_1 (make lint) ──\nlint ok\n── Job bg_2 ──\nline a\nline b\n</system-notice>",
				},
			],
			details: {
				jobs: [
					{ jobId: "bg_1", type: "bash", status: "completed" },
					{
						jobId: "bg_2",
						type: "task",
						status: "cancelled",
						agentId: "Scout",
					},
				],
			},
		});
		expect(jobs.map((j) => [j.jobId, j.status, j.result])).toEqual([
			["bg_1", "completed", "lint ok"],
			["bg_2", "cancelled", "line a\nline b"],
		]);
		expect(jobs[1]?.agentId).toBe("Scout");
	});

	it("没有 details 的旧消息退回正文标题行；认不出时为空", () => {
		expect(
			parseAsyncResult({
				content:
					"<system-notice>\nBackground job bg_9 has completed.\nResume your work using the result below.\ndone\n</system-notice>",
			}).map((j) => [j.jobId, j.status, j.result]),
		).toEqual([["bg_9", "completed", "done"]]);
		expect(parseAsyncResult({ content: "hello" })).toEqual([]);
	});
});
