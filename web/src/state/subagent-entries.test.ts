import { describe, expect, it } from "vitest";
import type { Entry } from "../api/client";
import { subagentFiles, subagentMessages } from "./subagent-entries";

const MAIN = "2026-11-15T00-00-00_abc.jsonl";
const ART = "2026-11-15T00-00-00_abc";

function entry(
	seq: number,
	file: string,
	type: string,
	payload: Record<string, unknown> = {},
): Entry {
	return { seq, file, entryId: `e${seq}`, type, payload };
}

function msg(seq: number, file: string, text: string): Entry {
	return entry(seq, file, "message", {
		type: "message",
		message: { role: "user", content: text },
	});
}

describe("subagentFiles", () => {
	it("按文件分组、保持首次出现顺序，主会话文件被排除", () => {
		const rows = subagentFiles([
			msg(1, MAIN, "main"),
			msg(2, `${ART}/1-Worker.jsonl`, "w"),
			msg(3, `${ART}/0-Scout.jsonl`, "s"),
			msg(4, `${ART}/1-Worker.jsonl`, "w2"),
			msg(5, MAIN, "main2"),
		]);
		expect(rows).toEqual([
			{ id: "1-Worker", file: `${ART}/1-Worker.jsonl` },
			{ id: "0-Scout", file: `${ART}/0-Scout.jsonl` },
		]);
	});

	it("agent / task 取自该文件的 session_init", () => {
		const rows = subagentFiles([
			entry(1, `${ART}/0-Scout.jsonl`, "session_init", {
				type: "session_init",
				agent: "scout",
				task: "find foo",
				tools: [],
			}),
			msg(2, `${ART}/0-Scout.jsonl`, "hi"),
			msg(3, `${ART}/1-Other.jsonl`, "x"),
		]);
		expect(rows).toEqual([
			{
				id: "0-Scout",
				file: `${ART}/0-Scout.jsonl`,
				agent: "scout",
				task: "find foo",
			},
			{ id: "1-Other", file: `${ART}/1-Other.jsonl` },
		]);
	});

	it("嵌套子 agent：id 取文件名（带点号），不取目录", () => {
		const rows = subagentFiles([
			msg(1, `${ART}/0-Scout/0-Scout.1-Worker.jsonl`, "n"),
		]);
		expect(rows).toEqual([
			{
				id: "0-Scout.1-Worker",
				file: `${ART}/0-Scout/0-Scout.1-Worker.jsonl`,
			},
		]);
	});
});

describe("subagentMessages", () => {
	it("只取该 id 文件的 message 条目，按 seq 升序", () => {
		const entries = [
			msg(9, `${ART}/0-Scout.jsonl`, "third"),
			msg(1, MAIN, "main"),
			msg(3, `${ART}/0-Scout.jsonl`, "first"),
			entry(4, `${ART}/0-Scout.jsonl`, "session_init", { agent: "scout" }),
			msg(5, `${ART}/0-Scout.jsonl`, "second"),
			msg(6, `${ART}/1-Worker.jsonl`, "other"),
			msg(7, `${ART}/0-Scout/0-Scout.1-Worker.jsonl`, "nested"),
		];
		expect(subagentMessages(entries, "0-Scout").map((m) => m.content)).toEqual([
			"first",
			"second",
			"third",
		]);
		expect(
			subagentMessages(entries, "0-Scout.1-Worker").map((m) => m.content),
		).toEqual(["nested"]);
	});

	it("主会话文件即便同名也不算子 agent", () => {
		expect(
			subagentMessages([msg(1, "0-Scout.jsonl", "main")], "0-Scout"),
		).toEqual([]);
	});
});
