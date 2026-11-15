/**
 * 子 agent 的派发树：把实时（`subagent_lifecycle` / `subagent_progress`）、快照（`get_subagents`）与已入库存档
 * （`subagentFiles`）三路合并成行，再按派发层级排成带深度的先序序列。三种情形（热页面、刷新后、orb 冻结只剩存档）
 * 得到同一棵树：
 *
 * - 实时与快照行带 Core 的 `parentSubagentId`（嵌套派发时为父 agent 的 registry id，根上缺失）；
 * - 存档行按路径推父：Core 的子 agent 会话写在 `<父会话文件去掉 .jsonl>/<id>.jsonl`（`task/executor.ts` 的
 *   `path.join(artifactsDir, `${id}.jsonl`)`，`artifactsDir` 即派发者会话文件去掉 `.jsonl`，冷恢复
 *   `rpc-subagent-restore.ts` 也这样逐层找），所以 `a/b/c.jsonl` 的父是 `a/b.jsonl`；父文件不含 `/` 即主会话，挂在根上。
 *   在线行缺 `parentSubagentId` 时也用它对应存档的路径推父。
 */
import type { AgentVisualState } from "../components/ui/AgentStatusAvatar";
import { isRecord } from "../tool-render/util";
import type { SubagentFile } from "./subagent-entries";
import { subagentFileOf, subagentLabel } from "./subagent-entries";

/** 实时或快照里的一个子 agent。 */
export interface SubagentSource {
	id: string;
	agent?: string;
	status?: string;
	description?: string;
	parentSubagentId?: string;
	/** Core 的原始 `AgentProgress`（实时 `subagent_progress` 帧或快照里的 `progress`）。 */
	progress?: unknown;
}

export interface SubagentRow {
	/** 行标识：在线 / 快照行是 id，只有存档的行是完整存档路径（不同主会话可能重名）。 */
	key: string;
	id: string;
	/** 只有存档的行：打开对话框时指定的存档文件。 */
	file?: string;
	/** 该行对应的存档（在线行取该 id 当前主会话的存档）；推父用。 */
	archive?: string;
	agent: string;
	status: string;
	/** 单行标签：实时 description，其次存档的持久化标题。 */
	description?: string;
	/** 上级行的 key；根上缺失。未经防环，展示用 `subagentTree` 的结果。 */
	parent?: string;
	/** 在线行的最新进度里用到的字段：排队判定与详情里的 role / 模型。 */
	progress?: SubagentProgress;
	/** 只有存档的行：为什么只剩保存的转录（见 `SubagentArchiveReason`）。 */
	archiveReason?: SubagentArchiveReason;
}

export interface SubagentProgress {
	status?: string;
	modelRole?: string;
	resolvedModel?: string;
}

/**
 * 只剩保存的转录的原因：`offline` = 实时那一侧没有它（agent 未运行，或它已不在 registry 里）；
 * `superseded` = 较早主会话的同名存档，在线 / 当前主会话的同 id 子 agent 不是它。
 */
export type SubagentArchiveReason = "offline" | "superseded";

export interface SubagentNode {
	row: SubagentRow;
	depth: number;
	/** 上级行的 key（缺失或成环时为根，缺失）。 */
	parent?: string;
}

/** 存档 `file` 的派发者存档：`a/b/c.jsonl` → `a/b.jsonl`；顶层文件没有父。 */
export function parentFileOf(file: string): string | undefined {
	const i = file.lastIndexOf("/");
	return i < 0 ? undefined : `${file.slice(0, i)}.jsonl`;
}

/** Core 原始 `AgentProgress` 里头像与详情用到的字段；一个都没有时为 undefined。 */
function progressOf(raw: unknown): SubagentProgress | undefined {
	if (!isRecord(raw)) return undefined;
	const out: SubagentProgress = {};
	for (const k of ["status", "modelRole", "resolvedModel"] as const) {
		const v = raw[k];
		if (typeof v === "string" && v) out[k] = v;
	}
	return Object.keys(out).length > 0 ? out : undefined;
}

/**
 * 状态字符串 → 头像状态。生命周期帧是 `started` / `completed` / `failed` / `aborted`，快照与进度是 Core 的
 * `SubagentStatus`（`pending` / `running` / `completed` / `failed` / `aborted`）；`done` / `error` 是旧写法。
 */
const STATUS_VISUAL = new Map<string, AgentVisualState>([
	["started", "running"],
	["running", "running"],
	["pending", "pending"],
	["completed", "done"],
	["done", "done"],
	["failed", "failed"],
	["error", "failed"],
	["aborted", "aborted"],
]);

/**
 * 一行的头像状态：只剩存档（`archiveReason`）为 archived；行状态已到终态时以它为准（进度帧可能落后于生命周期帧）；
 * 否则以更细的进度状态为准——生命周期只有 `started`，排队要看 `progress.status === "pending"`。
 */
export function subagentVisualState(row: SubagentRow): AgentVisualState {
	if (row.archiveReason) return "archived";
	const own = STATUS_VISUAL.get(row.status);
	if (own && own !== "running" && own !== "pending") return own;
	const latest = row.progress?.status
		? STATUS_VISUAL.get(row.progress.status)
		: undefined;
	return latest ?? own ?? "unknown";
}

/**
 * 对话框里的只剩转录原因（与其「已保存的转录」提示同一判定）：agent 不可查询为 `offline`；
 * 可查询但打开的是较早主会话的同名存档（`current` 为假）为 `superseded`；都不是时为 undefined。
 * 与行上的 `archiveReason` 合用：`row.archiveReason ?? subagentArchiveReason(queryable, current)`。
 */
export function subagentArchiveReason(
	queryable: boolean,
	current: boolean,
): SubagentArchiveReason | undefined {
	if (!queryable) return "offline";
	return current ? undefined : "superseded";
}

/**
 * 合并三路来源：同一 id 实时优先于快照；没被它们对应上的存档各占一行（按完整路径区分）。行按存档入库顺序
 * （即派发顺序）排列，还没有存档的（刚派发尚未入库、只在快照里的）排在后面——热页面与冷加载的同级顺序因此一致。
 */
export function subagentRows(
	live: readonly SubagentSource[],
	snapshots: readonly SubagentSource[],
	files: readonly SubagentFile[],
): SubagentRow[] {
	const seen = new Set<string>();
	const parentIds = new Map<string, string>();
	const online: SubagentRow[] = [];
	for (const s of [...live, ...snapshots]) {
		if (!s.id || seen.has(s.id)) continue;
		seen.add(s.id);
		if (s.parentSubagentId) parentIds.set(s.id, s.parentSubagentId);
		const saved = subagentFileOf(files, s.id);
		const row: SubagentRow = {
			key: s.id,
			id: s.id,
			agent: s.agent ?? "",
			status: s.status ?? "",
			description: subagentLabel(s.description, saved),
		};
		const progress = progressOf(s.progress);
		if (progress) row.progress = progress;
		if (saved) row.archive = saved.file;
		online.push(row);
	}
	const claimed = new Map(online.map((r) => [r.archive, r]));
	const rows: SubagentRow[] = files.map(
		(f) =>
			claimed.get(f.file) ?? {
				key: f.file,
				id: f.id,
				file: f.file,
				archive: f.file,
				agent: f.agent ?? f.id,
				status: "",
				description: subagentLabel(undefined, f),
				// 同 id 的最新存档属于当前主会话（在线行有存档时取的也是它）；不是它的就是较早主会话的同名存档
				archiveReason:
					subagentFileOf(files, f.id)?.file === f.file
						? "offline"
						: "superseded",
			},
	);
	for (const r of online) if (!r.archive) rows.push(r);
	const byId = new Map<string, string>();
	const byFile = new Map<string, string>();
	for (const r of rows) {
		if (!r.file) byId.set(r.id, r.key);
		if (r.archive) byFile.set(r.archive, r.key);
	}
	for (const r of rows) {
		const pid = parentIds.get(r.key);
		const pfile = r.archive && parentFileOf(r.archive);
		const parent =
			(pid &&
				(byId.get(pid) ??
					byFile.get(subagentFileOf(files, pid)?.file ?? ""))) ||
			(pfile && byFile.get(pfile));
		if (parent && parent !== r.key) r.parent = parent;
	}
	return rows;
}

/**
 * 按 `parent` 排成先序序列：上级缺失的行挂到根；同级保持输入顺序；成环时从环上最早出现的行断开、作为根，
 * 每行恰好出现一次。
 */
export function subagentTree(rows: readonly SubagentRow[]): SubagentNode[] {
	const byKey = new Map(rows.map((r) => [r.key, r]));
	const parentOf = (r: SubagentRow) =>
		r.parent && r.parent !== r.key && byKey.has(r.parent)
			? r.parent
			: undefined;
	const children = new Map<string, SubagentRow[]>();
	for (const r of rows) {
		const p = parentOf(r);
		if (!p) continue;
		const list = children.get(p);
		if (list) list.push(r);
		else children.set(p, [r]);
	}
	const out: SubagentNode[] = [];
	const visited = new Set<string>();
	const visit = (row: SubagentRow, depth: number, parent?: string) => {
		visited.add(row.key);
		out.push(parent === undefined ? { row, depth } : { row, depth, parent });
		for (const c of children.get(row.key) ?? [])
			if (!visited.has(c.key)) visit(c, depth + 1, row.key);
	};
	for (const r of rows) if (!parentOf(r)) visit(r, 0);
	// 还没走到的行一定通向一个环（链若止于根早已走到）：沿上级找到环，从环上最早出现的行断开
	for (const r of rows) {
		if (visited.has(r.key)) continue;
		const path: string[] = [];
		let k: string | undefined = r.key;
		while (k !== undefined && !path.includes(k)) {
			path.push(k);
			const row: SubagentRow | undefined = byKey.get(k);
			k = row && parentOf(row);
		}
		const cycle = new Set(k === undefined ? path : path.slice(path.indexOf(k)));
		const root = rows.find((x) => cycle.has(x.key));
		if (root) visit(root, 0);
	}
	return out;
}

/**
 * 对话框的 `id` / `file` 对应的树节点：给了 `file` 先找对应该存档的行；只有 id 时先找在线行，
 * 再找该 id 当前主会话的存档行。
 */
export function subagentNodeOf(
	nodes: readonly SubagentNode[],
	files: readonly SubagentFile[],
	id: string,
	file?: string,
): SubagentNode | undefined {
	const archive = file ?? subagentFileOf(files, id)?.file;
	return (
		nodes.find(
			(n) => n.row.key === id && (file === undefined || n.row.archive === file),
		) ?? nodes.find((n) => archive !== undefined && n.row.archive === archive)
	);
}
