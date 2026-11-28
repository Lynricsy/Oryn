/** 转录项上的模型来源轨迹（M9.6 §7.3）与启动文件的跳过规则。 */
import { describe, expect, it } from "vitest";
import type { Entry } from "../api/client";
import { EMPTY_TRAIL } from "../lib/turn-role";
import type { JsonRecord, LiveMessage } from "./message-builder";
import {
	activeBranch,
	buildTranscript,
	type TranscriptItem,
} from "./transcript";

let seq = 0;
function entry(
	file: string,
	type: string,
	payload: Record<string, unknown>,
	parentId?: string,
): Entry {
	seq++;
	return {
		seq,
		file,
		entryId: `e${seq}`,
		parentId,
		type,
		payload: { type, id: `e${seq}`, ...payload },
	};
}

/** 同一文件里顺次接上 parentId 的条目序列。 */
function chain(file: string, items: [string, Record<string, unknown>][]) {
	const out: Entry[] = [];
	for (const [type, payload] of items)
		out.push(entry(file, type, payload, out[out.length - 1]?.entryId));
	return out;
}

const SOL = "openai/gpt-6.1-sol";
const mare = {
	customType: "oryn-model-source",
	data: { v: 1, source: "role", role: "Mare", model: SOL },
};
const user = (text: string) => ({
	message: { role: "user", content: text, timestamp: seq + 1 },
});
const reply = (text: string) => ({
	message: {
		role: "assistant",
		provider: "openai",
		model: "gpt-6.1-sol",
		content: [{ type: "text", text }],
		timestamp: seq + 1,
	},
});

function build(entries: Entry[], liveMessage: LiveMessage | null = null) {
	return buildTranscript({
		entries,
		leafEntryId: undefined,
		liveMessage,
		liveTools: {},
		liveToolOrder: [],
		notices: [],
	}).items;
}

const messages = (items: TranscriptItem[]) =>
	items.flatMap((it) => (it.kind === "message" ? [it] : []));

describe("buildTranscript 的来源轨迹", () => {
	it("助手消息带走到它之前的轨迹；来源记录不产生转录项", () => {
		const entries = chain("a.jsonl", [
			["model_change", { model: SOL }],
			["message", user("1")],
			["message", reply("一")],
			["model_change", { model: SOL, role: "Mare" }],
			["custom", mare],
			["message", user("2")],
			["message", reply("二")],
		]);
		const items = build(entries);
		expect(items.some((it) => it.kind === "custom_message")).toBe(false);
		expect(items.map((it) => it.kind)).toEqual([
			"meta",
			"message",
			"message",
			"meta",
			"message",
			"message",
		]);
		const [u1, a1, u2, a2] = messages(items);
		expect(u1?.trail).toBeUndefined();
		expect(u2?.trail).toBeUndefined();
		expect(a1?.trail).toEqual({ fallback: false, tracked: false });
		expect(a2?.trail?.record).toEqual({
			source: "role",
			role: "Mare",
			model: SOL,
		});
	});

	it("在途消息带分支末端的轨迹", () => {
		const entries = chain("a.jsonl", [
			["custom", mare],
			["message", user("go")],
		]);
		const live: LiveMessage = {
			messageId: "m1",
			role: "assistant",
			timestamp: 999,
			rowKey: "live-0",
			message: reply("…").message as JsonRecord,
			partialJson: {},
			ended: false,
		};
		const last = messages(build(entries, live)).at(-1);
		expect(last?.live).toBe(true);
		expect(last?.trail?.record?.role).toBe("Mare");
	});

	it("只有启动元数据的非末尾文件不渲染、不出分隔线，但其记录照常生效；末尾文件照常渲染", () => {
		const first = chain("a.jsonl", [
			["model_change", { model: SOL }],
			["message", user("1")],
			["message", reply("一")],
		]);
		// 进程在第一条回复前重启留下的启动文件：model_change + 思考级别 + 来源记录
		const startup = chain("b.jsonl", [
			["session", {}],
			["model_change", { model: SOL, role: "Mare" }],
			["thinking_level_change", { thinkingLevel: "high" }],
			["custom", mare],
		]);
		const third = chain("c.jsonl", [
			["message", user("2")],
			["message", reply("二")],
		]);
		const items = build([...first, ...startup, ...third]);
		expect(items.filter((it) => it.kind === "session_divider")).toHaveLength(1);
		// 启动文件的 model_change / thinking_level_change 都不出现
		expect(items.filter((it) => it.kind === "meta")).toHaveLength(1);
		expect(messages(items).at(-1)?.trail?.record?.role).toBe("Mare");

		// 负向：启动文件是最后一个文件时照常渲染（分隔线 + 元信息行）
		const tail = build([...first, ...startup]);
		expect(tail.filter((it) => it.kind === "session_divider")).toHaveLength(1);
		expect(tail.filter((it) => it.kind === "meta")).toHaveLength(3);
		// 负向：有消息的中间文件不跳过
		const withMsg = chain("d.jsonl", [["message", user("x")]]);
		expect(
			build([...first, ...withMsg, ...third]).filter(
				(it) => it.kind === "session_divider",
			),
		).toHaveLength(2);
	});

	it("没有任何来源记录的分支：轨迹是空轨迹本身", () => {
		const entries = chain("a.jsonl", [
			["message", user("1")],
			["message", reply("一")],
		]);
		expect(messages(build(entries))[1]?.trail).toBe(EMPTY_TRAIL);
	});
});

describe("buildTranscript 的条目可见性", () => {
	const kindsOf = (type: string) =>
		build(chain("a.jsonl", [[type, {}]])).map((it) => it.kind);

	it("reset_boundary 在有消息之后以元信息行显示，连续重复只保留一条，无前置内容时不显示", () => {
		expect(kindsOf("reset_boundary")).toEqual([]);
		const items = build(
			chain("a.jsonl", [
				["message", user("hi")],
				["reset_boundary", {}],
				["reset_boundary", {}],
				["message", user("again")],
				["reset_boundary", {}],
			]),
		);
		expect(
			items.map((it) =>
				it.kind === "meta" ? `meta:${String(it.entry.type)}` : it.kind,
			),
		).toEqual([
			"message",
			"meta:reset_boundary",
			"message",
			"meta:reset_boundary",
		]);
	});

	it("只有启动元数据、reset_boundary 与 model_usage 的空会话文件不渲染「新会话」也不显示「上下文已清空」", () => {
		const emptyMain = (file: string, withUsage = false) =>
			chain(file, [
				["title", { title: "" }],
				["session", {}],
				["model_change", { model: SOL }],
				["thinking_level_change", { thinkingLevel: "high" }],
				["reset_boundary", {}],
				["custom", mare],
				...(withUsage
					? ([["model_usage", { purpose: "skill-description" }]] as [
							string,
							Record<string, unknown>,
						][])
					: []),
			]);
		const allEmpty = build([
			...emptyMain("s1.jsonl"),
			...emptyMain("s2.jsonl", true),
			...emptyMain("s3.jsonl"),
		]);
		expect(allEmpty.filter((it) => it.kind === "session_divider")).toHaveLength(
			0,
		);
		expect(
			allEmpty.filter(
				(it) => it.kind === "meta" && it.entry.type === "reset_boundary",
			),
		).toHaveLength(0);

		// 中间夹着多个空重启文件时，前后真实消息之间只留一条「新会话」分隔线，不把空文件算进去
		const first = chain("a.jsonl", [
			["message", user("1")],
			["message", reply("一")],
		]);
		const next = chain("d.jsonl", [
			["message", user("2")],
			["message", reply("二")],
		]);
		const mixed = build([
			...first,
			...emptyMain("b.jsonl", true),
			...emptyMain("c.jsonl"),
			...next,
		]);
		expect(mixed.filter((it) => it.kind === "session_divider")).toHaveLength(1);
		expect(
			mixed.filter(
				(it) => it.kind === "meta" && it.entry.type === "reset_boundary",
			),
		).toHaveLength(0);
	});

	it("未归类的新条目类型以元信息行显示，不被静默丢弃", () => {
		const items = build(chain("a.jsonl", [["future_thing", { x: 1 }]]));
		expect(items).toHaveLength(1);
		expect(items[0]).toMatchObject({
			kind: "meta",
			entry: { type: "future_thing" },
		});
	});

	it("已知的隐藏类型仍不渲染", () => {
		for (const type of [
			"model_usage",
			"label",
			"credential_pin",
			"session_init",
			"custom",
			"service_tier_change",
		])
			expect(kindsOf(type), type).toEqual([]);
	});

	it("在途的 display: false 注入消息（xd:// 设备挂载提示）不进转录，可显示的照常", () => {
		const entries = chain("a.jsonl", [["message", user("go")]]);
		const live = (display: boolean): LiveMessage => ({
			messageId: "n1",
			role: "custom",
			timestamp: 999,
			rowKey: "live-0",
			message: {
				role: "custom",
				customType: "xdev-mount-notice",
				content:
					"<system-notice>xd:// device inventory changed.</system-notice>",
				display,
				timestamp: 999,
			},
			partialJson: {},
			ended: true,
		});
		expect(messages(build(entries, live(false)))).toHaveLength(1);
		expect(messages(build(entries, live(true)))).toHaveLength(2);
	});
});

describe("mode_change 带上前一条模式", () => {
	it("同一轨迹上每条 mode_change 拿到前一条（跨会话文件延续），第一条没有", () => {
		const a = chain("a.jsonl", [
			["mode_change", { mode: "goal", data: { goal: { objective: "x" } } }],
			["message", user("hi")],
		]);
		const b = chain("b.jsonl", [
			["mode_change", { mode: "goal_paused" }],
			["mode_change", { mode: "none" }],
		]);
		const metas = build([...a, ...b]).filter((i) => i.kind === "meta");
		expect(
			metas.map((i) =>
				i.kind === "meta" ? [i.entry.mode, i.prevMode?.mode] : [],
			),
		).toEqual([
			["goal", undefined],
			["goal_paused", "goal"],
			["none", "goal_paused"],
		]);
	});
});

describe("activeBranch 的叶子", () => {
	const ids = (files: Entry[][]) => files.map((f) => f.map((e) => e.entryId));

	it("空串 = 明确的空分支：当前会话文件为空，更早的文件照常；之后从根分叉的新消息成为新分支，旧分支不再显示", () => {
		const older = chain("a.jsonl", [["message", user("旧文件")]]);
		const current = chain("b.jsonl", [
			["message", user("根上的第一条")],
			["message", reply("答")],
		]);
		const entries = [...older, ...current];
		const last = current[1]?.entryId;
		expect(ids(activeBranch(entries, undefined))).toEqual([
			[older[0]?.entryId],
			[current[0]?.entryId, last],
		]);
		expect(ids(activeBranch(entries, ""))).toEqual([[older[0]?.entryId], []]);

		const fork = entry("b.jsonl", "message", user("重写的第一条"));
		expect(ids(activeBranch([...entries, fork], fork.entryId))).toEqual([
			[older[0]?.entryId],
			[fork.entryId],
		]);
	});
});
