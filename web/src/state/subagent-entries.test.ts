import { describe, expect, it } from "vitest";
import type { Entry } from "../api/client";
import {
	subagentFileOf,
	subagentFiles,
	subagentLabel,
	subagentMessages,
} from "./subagent-entries";

const MAIN = "2026-11-15T00-00-00_abc.jsonl";
const ART = "2026-11-15T00-00-00_abc";
/** 同一线程里较晚的另一个主会话：子 agent 编号同样从 0 开始。 */
const ART2 = "2026-11-15T09-00-00_def";

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

	it("顾问转录（主会话与子 agent 目录下的 __advisor*.jsonl）不是子 agent", () => {
		const rows = subagentFiles([
			msg(1, `${ART}/__advisor.jsonl`, "主顾问"),
			msg(2, `${ART}/0-Scout/__advisor.jsonl`, "子顾问"),
			msg(3, `${ART}/__advisor.reviewer.jsonl`, "命名顾问"),
			msg(4, `${ART}/0-Scout.jsonl`, "s"),
		]);
		expect(rows).toEqual([{ id: "0-Scout", file: `${ART}/0-Scout.jsonl` }]);
	});

	it("主会话产物目录顶层的改动日志 changes.jsonl 不是子 agent", () => {
		const rows = subagentFiles([
			entry(1, `${ART}/changes.jsonl`, "change", {
				type: "change",
				agent: "Main",
				tool: "edit",
			}),
			entry(2, `${ART}/changes.jsonl`, "call", { type: "call" }),
			msg(3, `${ART}/0-Scout.jsonl`, "s"),
		]);
		expect(rows).toEqual([{ id: "0-Scout", file: `${ART}/0-Scout.jsonl` }]);
	});

	it("名为 changes 的根级子 agent 与改动日志同路径：转录照常成行，日志记录不进消息", () => {
		const file = `${ART}/changes.jsonl`;
		const entries = [
			entry(1, file, "session", { type: "session", title: "改动审计" }),
			entry(2, file, "session_init", {
				type: "session_init",
				agent: "task",
				task: "audit",
			}),
			entry(3, file, "change", { type: "change", agent: "Main" }),
			msg(4, file, "hi"),
		];
		expect(subagentFiles(entries)).toEqual([
			{ id: "changes", file, agent: "task", task: "audit", title: "改动审计" },
		]);
		expect(subagentMessages(entries, file).map((m) => m.content)).toEqual([
			"hi",
		]);
	});

	it("不同主会话目录下的同名存档各自一行", () => {
		const rows = subagentFiles([
			msg(1, `${ART}/0-Scout.jsonl`, "old"),
			msg(2, `${ART2}/0-Scout.jsonl`, "new"),
		]);
		expect(rows.map((r) => r.file)).toEqual([
			`${ART}/0-Scout.jsonl`,
			`${ART2}/0-Scout.jsonl`,
		]);
	});

	it("title 取最新的 title_change，标题晚于初始化到达也更新；task 保持完整正文", () => {
		const longTask = `你是负责调查的子 agent。${"请逐条核对以下文件。".repeat(20)}`;
		const file = `${ART}/0-Scout.jsonl`;
		const [row] = subagentFiles([
			entry(1, file, "session", {
				type: "session",
				id: "s",
				title: "旧头标题",
			}),
			entry(2, file, "session_init", { agent: "scout", task: longTask }),
			msg(3, file, "hi"),
			entry(5, file, "title_change", { title: "核对重连逻辑" }),
			entry(4, file, "title_change", { title: "更早的标题" }),
		]);
		expect(row).toMatchObject({ task: longTask, title: "核对重连逻辑" });
	});

	it("没有 title_change 时用会话头的 title", () => {
		const file = `${ART}/0-Scout.jsonl`;
		const [row] = subagentFiles([
			entry(1, file, "session", { type: "session", id: "s", title: "头标题" }),
			entry(2, file, "session_init", { agent: "scout", task: "长任务" }),
		]);
		expect(row?.title).toBe("头标题");
	});
});

describe("subagentFileOf / subagentLabel", () => {
	const files = subagentFiles([
		entry(1, `${ART}/0-Scout.jsonl`, "title_change", { title: "旧会话" }),
		entry(2, `${ART2}/0-Scout.jsonl`, "title_change", { title: "当前会话" }),
	]);

	it("给 file 精确匹配；只给 id 取最后出现（当前主会话）的存档", () => {
		expect(
			subagentFileOf(files, "0-Scout", `${ART}/0-Scout.jsonl`)?.title,
		).toBe("旧会话");
		expect(subagentFileOf(files, "0-Scout")?.title).toBe("当前会话");
		expect(subagentFileOf(files, "1-None")).toBeUndefined();
	});

	it("实时 description 优先，其次持久化标题，不回退到 task", () => {
		const saved = {
			id: "x",
			file: `${ART}/x.jsonl`,
			task: "完整任务",
			title: "短标题",
		};
		expect(subagentLabel("实时描述", saved)).toBe("实时描述");
		expect(subagentLabel(undefined, saved)).toBe("短标题");
		expect(
			subagentLabel(undefined, {
				id: "x",
				file: `${ART}/x.jsonl`,
				task: "完整任务",
			}),
		).toBeUndefined();
	});
});

describe("subagentMessages", () => {
	it("只取该存档文件的 message 条目，按 seq 升序", () => {
		const entries = [
			msg(9, `${ART}/0-Scout.jsonl`, "third"),
			msg(1, MAIN, "main"),
			msg(3, `${ART}/0-Scout.jsonl`, "first"),
			entry(4, `${ART}/0-Scout.jsonl`, "session_init", { agent: "scout" }),
			msg(5, `${ART}/0-Scout.jsonl`, "second"),
			msg(6, `${ART}/1-Worker.jsonl`, "other"),
			msg(7, `${ART}/0-Scout/0-Scout.1-Worker.jsonl`, "nested"),
		];
		expect(
			subagentMessages(entries, `${ART}/0-Scout.jsonl`).map((m) => m.content),
		).toEqual(["first", "second", "third"]);
		expect(
			subagentMessages(entries, `${ART}/0-Scout/0-Scout.1-Worker.jsonl`).map(
				(m) => m.content,
			),
		).toEqual(["nested"]);
	});

	it("不同目录下的同名文件不合并", () => {
		const entries = [
			msg(1, `${ART}/__advisor.jsonl`, "主顾问"),
			msg(2, `${ART}/0-Scout/__advisor.jsonl`, "子顾问"),
			msg(3, `${ART}/0-Scout.jsonl`, "旧会话"),
			msg(4, `${ART2}/0-Scout.jsonl`, "新会话"),
		];
		expect(
			subagentMessages(entries, `${ART}/__advisor.jsonl`).map((m) => m.content),
		).toEqual(["主顾问"]);
		expect(
			subagentMessages(entries, `${ART2}/0-Scout.jsonl`).map((m) => m.content),
		).toEqual(["新会话"]);
	});
});
