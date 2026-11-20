import { describe, expect, it } from "vitest";
import type { TranscriptItem } from "../../state/transcript";
import { activeToolCallIds } from "./active-tools";

function assistant(key: string, content: unknown[]): TranscriptItem {
	return {
		kind: "message",
		key,
		seq: 0,
		live: false,
		message: { role: "assistant", content },
	};
}

const call = (id: string) => ({
	type: "toolCall",
	id,
	name: "bash",
	arguments: {},
});

describe("activeToolCallIds：当前一批工具调用", () => {
	it("最后一条带调用的助手消息里的并行调用同属一批；更早的批次不算", () => {
		const rows = [
			assistant("a", [call("t1")]),
			assistant("b", [
				{ type: "text", text: "并行跑两个" },
				call("t2"),
				call("t3"),
			]),
		];
		expect([...activeToolCallIds(rows)]).toEqual(["t2", "t3"]);
	});

	it("之后只有文字（还没开始下一批调用）时上一批仍是当前一批", () => {
		const rows = [
			assistant("a", [call("t1")]),
			assistant("b", [{ type: "text", text: "看一下结果……" }]),
		];
		expect([...activeToolCallIds(rows)]).toEqual(["t1"]);
	});

	it("还没并入消息的在途工具行连成一批", () => {
		const rows: TranscriptItem[] = [
			assistant("a", [call("t1")]),
			{ kind: "tool", key: "x", seq: 0, toolCallId: "t2", toolName: "bash" },
			{ kind: "tool", key: "y", seq: 0, toolCallId: "t3", toolName: "read" },
		];
		expect([...activeToolCallIds(rows)]).toEqual(["t3", "t2"]);
	});

	it("A 批 → 回合结束 → 新回合只有文字：A 不重新展开；新回合发起 B 批后只展开 B", () => {
		const user = (key: string): TranscriptItem => ({
			kind: "message",
			key,
			seq: 0,
			live: false,
			message: { role: "user", content: "继续" },
		});
		const newTurnText = [
			user("u1"),
			assistant("a", [call("t1")]),
			assistant("a2", [{ type: "text", text: "做完了" }]),
			user("u2"),
			assistant("b0", [{ type: "text", text: "好的，先想一下" }]),
		];
		expect(activeToolCallIds(newTurnText).size).toBe(0);
		const withB = [...newTurnText, assistant("b", [call("t2")])];
		expect([...activeToolCallIds(withB)]).toEqual(["t2"]);
	});

	it("A 批 → 运行中 steer → 同一轮续写文字：A 仍展开（steer 不是回合边界）", () => {
		for (const flag of ["steering", "liveSteered"] as const) {
			const rows: TranscriptItem[] = [
				assistant("a", [call("t1")]),
				{
					kind: "message",
					key: "s",
					seq: 0,
					live: false,
					message: { role: "user", content: "顺便看下日志", [flag]: true },
				},
				assistant("a2", [{ type: "text", text: "好，接着看" }]),
			];
			expect([...activeToolCallIds(rows)]).toEqual(["t1"]);
		}
	});
});
