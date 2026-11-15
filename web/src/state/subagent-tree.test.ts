import { describe, expect, it } from "vitest";
import type { SubagentFile } from "./subagent-entries";
import {
	parentFileOf,
	type SubagentNode,
	type SubagentRow,
	type SubagentSource,
	subagentArchiveReason,
	subagentNodeOf,
	subagentRows,
	subagentTree,
	subagentVisualState,
} from "./subagent-tree";

const ART = "2026-11-15T00-00-00_abc";
/** 同一线程里较晚的另一个主会话：子 agent 编号同样从 0 开始。 */
const ART2 = "2026-11-15T09-00-00_def";

/** 三层嵌套：主会话 → 0-Scout → 0-Scout.0-Worker → 0-Scout.0-Worker.0-Leaf，外加根上的 1-Other。 */
const SCOUT = `${ART}/0-Scout.jsonl`;
const WORKER = `${ART}/0-Scout/0-Scout.0-Worker.jsonl`;
const LEAF = `${ART}/0-Scout/0-Scout.0-Worker/0-Scout.0-Worker.0-Leaf.jsonl`;
const OTHER = `${ART}/1-Other.jsonl`;

function file(path: string, agent?: string): SubagentFile {
	const base = path.slice(path.lastIndexOf("/") + 1, -".jsonl".length);
	return agent ? { id: base, file: path, agent } : { id: base, file: path };
}

/** 树的形状：每行 `深度:id`，与行来源（实时 / 快照 / 存档）无关。 */
function shape(nodes: readonly SubagentNode[]): string[] {
	return nodes.map((n) => `${n.depth}:${n.row.id}`);
}

function row(key: string, parent?: string): SubagentRow {
	return parent
		? { key, id: key, agent: key, status: "", parent }
		: { key, id: key, agent: key, status: "" };
}

const LIVE: SubagentSource[] = [
	{ id: "0-Scout", agent: "scout", status: "started" },
	{ id: "1-Other", agent: "other", status: "completed" },
	{
		id: "0-Scout.0-Worker",
		agent: "worker",
		status: "started",
		parentSubagentId: "0-Scout",
	},
	{
		id: "0-Scout.0-Worker.0-Leaf",
		agent: "leaf",
		status: "started",
		parentSubagentId: "0-Scout.0-Worker",
	},
];

const EXPECTED = [
	"0:0-Scout",
	"1:0-Scout.0-Worker",
	"2:0-Scout.0-Worker.0-Leaf",
	"0:1-Other",
];

describe("parentFileOf", () => {
	it("子 agent 会话在派发者会话去掉 .jsonl 的产物目录里", () => {
		expect(parentFileOf(LEAF)).toBe(WORKER);
		expect(parentFileOf(WORKER)).toBe(SCOUT);
		expect(parentFileOf(SCOUT)).toBe(`${ART}.jsonl`);
		expect(parentFileOf(`${ART}.jsonl`)).toBeUndefined();
	});
});

describe("subagentTree", () => {
	it("多层：先序排列，深度逐层加一，同级保持输入顺序", () => {
		const nodes = subagentTree([
			row("a"),
			row("b", "a"),
			row("c", "b"),
			row("d"),
			row("e", "a"),
		]);
		expect(shape(nodes)).toEqual(["0:a", "1:b", "2:c", "1:e", "0:d"]);
		expect(nodes.map((n) => n.parent)).toEqual([
			undefined,
			"a",
			"b",
			"a",
			undefined,
		]);
	});

	it("子在父之前出现也挂到父下", () => {
		expect(
			shape(subagentTree([row("c", "b"), row("b", "a"), row("a")])),
		).toEqual(["0:a", "1:b", "2:c"]);
	});

	it("上级缺失或指向自己：挂到根", () => {
		const nodes = subagentTree([row("a", "gone"), row("b", "b")]);
		expect(shape(nodes)).toEqual(["0:a", "0:b"]);
		expect(nodes.every((n) => n.parent === undefined)).toBe(true);
	});

	it("成环：每行恰好出现一次，从环上最早出现的行断开，挂在环下的行跟着环", () => {
		const nodes = subagentTree([
			row("x", "a"),
			row("a", "b"),
			row("b", "a"),
			row("root"),
		]);
		expect(shape(nodes)).toEqual(["0:root", "0:a", "1:x", "1:b"]);
		expect(nodes.find((n) => n.row.key === "a")?.parent).toBeUndefined();
	});
});

describe("subagentRows", () => {
	it("实时行按 parentSubagentId 挂父", () => {
		expect(shape(subagentTree(subagentRows(LIVE, [], [])))).toEqual(EXPECTED);
	});

	it("只有存档（orb 冻结）：按存档路径推父，子在父之前列出也挂到父下", () => {
		const files = [file(OTHER), file(LEAF, "leaf"), file(WORKER), file(SCOUT)];
		const nodes = subagentTree(subagentRows([], [], files));
		expect(shape(nodes)).toEqual([
			"0:1-Other",
			"0:0-Scout",
			"1:0-Scout.0-Worker",
			"2:0-Scout.0-Worker.0-Leaf",
		]);
		// 存档行打开的是自己的文件
		expect(nodes.map((n) => n.row.file)).toEqual([OTHER, SCOUT, WORKER, LEAF]);
	});

	it("实时、快照、存档三种情形得到同一棵树，同级按存档顺序（实时帧顺序不同也一样）", () => {
		const files = [file(OTHER), file(SCOUT), file(WORKER), file(LEAF)];
		const want = [
			"0:1-Other",
			"0:0-Scout",
			"1:0-Scout.0-Worker",
			"2:0-Scout.0-Worker.0-Leaf",
		];
		const hot = subagentTree(subagentRows(LIVE, [], files));
		const refreshed = subagentTree(
			subagentRows([], [...LIVE].reverse(), files),
		);
		const frozen = subagentTree(subagentRows([], [], files));
		expect(shape(hot)).toEqual(want);
		expect(shape(refreshed)).toEqual(want);
		expect(shape(frozen)).toEqual(want);
		// 在线行仍以 id 为标识（对话框按 id 控制），不带存档文件
		expect(hot.map((n) => n.row.file)).toEqual([
			undefined,
			undefined,
			undefined,
			undefined,
		]);
	});

	it("还没入库的在线子 agent 排在已入库的之后", () => {
		const nodes = subagentTree(
			subagentRows(
				[{ id: "2-New", agent: "new", status: "started" }, ...LIVE],
				[],
				[file(SCOUT), file(OTHER)],
			),
		);
		expect(shape(nodes)).toEqual([...EXPECTED, "0:2-New"]);
	});

	it("部分在线、部分只剩存档：在线的孙 agent 挂到存档里的父行下", () => {
		const nodes = subagentTree(
			subagentRows(
				[LIVE[3] as SubagentSource],
				[],
				[file(SCOUT), file(WORKER)],
			),
		);
		expect(shape(nodes)).toEqual(EXPECTED.slice(0, 3));
		expect(nodes[2]?.parent).toBe(WORKER);
	});

	it("在线行缺 parentSubagentId 时用其存档路径推父", () => {
		const live = LIVE.map(({ parentSubagentId: _, ...s }) => s);
		const files = [file(SCOUT), file(WORKER), file(LEAF), file(OTHER)];
		expect(shape(subagentTree(subagentRows(live, [], files)))).toEqual(
			EXPECTED,
		);
	});

	it("不同主会话的同名存档各成一棵树，较早会话的嵌套存档挂在它自己会话的父下", () => {
		const old = [file(SCOUT), file(WORKER)];
		const cur = [
			file(`${ART2}/0-Scout.jsonl`),
			file(`${ART2}/0-Scout/0-Scout.0-Worker.jsonl`),
		];
		const nodes = subagentTree(
			subagentRows(LIVE.slice(0, 1), [], [...old, ...cur]),
		);
		expect(
			nodes.map((n) => `${n.depth}:${n.row.archive}${n.row.file ? "" : "*"}`),
		).toEqual([
			`0:${SCOUT}`,
			`1:${WORKER}`,
			`0:${ART2}/0-Scout.jsonl*`,
			`1:${ART2}/0-Scout/0-Scout.0-Worker.jsonl`,
		]);
	});

	it("同一 id 实时优先于快照", () => {
		const rows = subagentRows(
			[{ id: "0-Scout", agent: "scout", status: "started" }],
			[
				{ id: "0-Scout", agent: "scout", status: "completed" },
				{ id: "1-Other", agent: "other", status: "idle" },
			],
			[],
		);
		expect(rows.map((r) => [r.id, r.status])).toEqual([
			["0-Scout", "started"],
			["1-Other", "idle"],
		]);
	});
});

describe("subagentNodeOf", () => {
	const files = [file(SCOUT), file(WORKER), file(`${ART2}/0-Scout.jsonl`)];
	const nodes = subagentTree(subagentRows(LIVE.slice(0, 1), [], files));

	it("只给 id：在线行", () => {
		expect(subagentNodeOf(nodes, files, "0-Scout")?.row.key).toBe("0-Scout");
	});

	it("给了较早主会话的存档：对应那份存档的行，不是同 id 的在线行", () => {
		expect(subagentNodeOf(nodes, files, "0-Scout", SCOUT)?.row.key).toBe(SCOUT);
	});

	it("只给 id 且不在线：该 id 当前主会话的存档行", () => {
		expect(subagentNodeOf(nodes, files, "0-Scout.0-Worker")?.row.key).toBe(
			WORKER,
		);
	});
});

describe("行的进度与只剩存档的原因", () => {
	it("在线行取进度里的 status / modelRole / resolvedModel，忽略其他字段与非字符串值", () => {
		const rows = subagentRows(
			[
				{
					id: "0-Scout",
					status: "started",
					progress: {
						id: "0-Scout",
						status: "running",
						modelRole: "smol",
						resolvedModel: "anthropic/claude-haiku",
						toolCount: 3,
					},
				},
			],
			[{ id: "1-Other", status: "pending", progress: { status: 1 } }],
			[],
		);
		expect(rows.map((r) => r.progress)).toEqual([
			{
				status: "running",
				modelRole: "smol",
				resolvedModel: "anthropic/claude-haiku",
			},
			undefined,
		]);
		expect(rows.map((r) => r.archiveReason)).toEqual([undefined, undefined]);
	});

	it("只有存档的行：该 id 的最新存档为 offline，较早主会话的同名存档为 superseded；在线行没有原因", () => {
		const files = [file(SCOUT), file(WORKER), file(`${ART2}/0-Scout.jsonl`)];
		const rows = subagentRows(LIVE.slice(0, 1), [], files);
		expect(rows.map((r) => [r.key, r.archiveReason])).toEqual([
			[SCOUT, "superseded"],
			[WORKER, "offline"],
			["0-Scout", undefined],
		]);
	});

	it("对话框的原因：不可查询为 offline，可查询但不是当前存档为 superseded", () => {
		expect(subagentArchiveReason(false, true)).toBe("offline");
		expect(subagentArchiveReason(false, false)).toBe("offline");
		expect(subagentArchiveReason(true, false)).toBe("superseded");
		expect(subagentArchiveReason(true, true)).toBeUndefined();
	});
});

describe("subagentVisualState", () => {
	const at = (
		status: string,
		extra: Partial<SubagentRow> = {},
	): SubagentRow => ({ ...row("a"), status, ...extra });

	it.each([
		["started", "running"],
		["running", "running"],
		["pending", "pending"],
		["completed", "done"],
		["done", "done"],
		["failed", "failed"],
		["error", "failed"],
		["aborted", "aborted"],
		["", "unknown"],
		["idle", "unknown"],
		["constructor", "unknown"],
	] as const)("状态 %s → %s", (status, want) => {
		expect(subagentVisualState(at(status))).toBe(want);
	});

	it("生命周期 started 时看进度：pending 为排队，running 为运行中", () => {
		expect(
			subagentVisualState(at("started", { progress: { status: "pending" } })),
		).toBe("pending");
		expect(
			subagentVisualState(at("started", { progress: { status: "running" } })),
		).toBe("running");
		expect(
			subagentVisualState(at("", { progress: { status: "pending" } })),
		).toBe("pending");
	});

	it("行状态已到终态时不被落后的进度覆盖；进度先到终态时以进度为准", () => {
		expect(
			subagentVisualState(at("completed", { progress: { status: "running" } })),
		).toBe("done");
		expect(
			subagentVisualState(at("started", { progress: { status: "failed" } })),
		).toBe("failed");
	});

	it.each(["offline", "superseded"] as const)(
		"只剩存档（%s）为 archived，不论状态",
		(archiveReason) => {
			expect(subagentVisualState(at("", { archiveReason }))).toBe("archived");
			expect(subagentVisualState(at("started", { archiveReason }))).toBe(
				"archived",
			);
		},
	);

	it("存档行与较早主会话的同名存档经 subagentRows 得到 archived", () => {
		const files = [file(SCOUT), file(`${ART2}/0-Scout.jsonl`), file(OTHER)];
		const rows = subagentRows(LIVE.slice(0, 1), [], files);
		expect(rows.map((r) => [r.key, subagentVisualState(r)])).toEqual([
			[SCOUT, "archived"],
			["0-Scout", "running"],
			[OTHER, "archived"],
		]);
	});
});
