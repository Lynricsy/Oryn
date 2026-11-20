#!/usr/bin/env bun
/**
 * 由 docs/ui-audit/areas/*.md 生成 docs/ui-audit/inventory.md：总清单 + 各区问题汇总 + 每个界面的截图与断言状态。
 * 截图状态读 docs/ui-audit/shots/<id>/<project>-<phase>.json（本地生成，不入库）。
 *
 *   bun e2e/ui-audit/inventory.ts
 */
import { existsSync, readdirSync, readFileSync, writeFileSync } from "node:fs";

const ROOT = new URL("../../../docs/ui-audit/", import.meta.url).pathname;
const PROJECTS = [
	"d1920-light",
	"d1920-dark",
	"d1366-light",
	"d1366-dark",
	"m390-light",
	"m390-dark",
];

/** 取 Markdown 某个二级标题下的表格行（去掉表头与分隔行）。 */
function table(md: string, heading: string): string[][] {
	const start = md.indexOf(`## ${heading}`);
	if (start < 0) return [];
	const rest = md.slice(start).split("\n").slice(1);
	const rows: string[][] = [];
	for (const line of rest) {
		if (line.startsWith("## ")) break;
		if (!line.startsWith("|")) continue;
		const cells = line
			.split("|")
			.slice(1, -1)
			.map((c) => c.trim());
		if (cells.every((c) => /^-+$/.test(c))) continue;
		rows.push(cells);
	}
	return rows.slice(1);
}

type Result =
	| { status: "done"; issues: string[] }
	| { status: "skipped"; reason: string };

/** 读一个结果文件；不存在或是旧格式（没有 status，审计 harness 改为失败闭合之前写的）都当作没有结果。 */
function result(file: string): Result | undefined {
	if (!existsSync(file)) return undefined;
	const r: unknown = JSON.parse(readFileSync(file, "utf8"));
	return r && typeof r === "object" && "status" in r
		? (r as Result)
		: undefined;
}

/**
 * 一个界面在 6 个 project 下的状态（失败闭合，任何不确定都不算通过）：
 * ✓ after 跑完全程 0 问题，且 before / after 截图都在；— after 显式跳过（视口不适用）；
 * ✗ after 跑完但有问题；· 其余（没跑、中途失败或被打断、缺截图）。
 */
function status(id: string): { cells: string[]; ok: boolean } {
	const dir = `${ROOT}shots/${id}/`;
	const cells = PROJECTS.map((p) => {
		const after = result(`${dir}${p}-after.json`);
		if (after?.status === "skipped") return "—";
		if (after?.status !== "done") return "·";
		if (after.issues.length) return "✗";
		const shots =
			existsSync(`${dir}${p}-after.png`) && existsSync(`${dir}${p}-before.png`);
		return shots && result(`${dir}${p}-before.json`)?.status === "done"
			? "✓"
			: "·";
	});
	return {
		cells,
		ok: cells.every((c) => c === "✓" || c === "—") && cells.includes("✓"),
	};
}

const areas = readdirSync(`${ROOT}areas`)
	.filter((f) => f.endsWith(".md"))
	.sort();
const out: string[] = [
	"# UI 排版审计总清单",
	"",
	"由 `web/e2e/ui-audit/inventory.ts` 从 `areas/*.md` 生成，勿手改。状态列依次为 d1920 明/暗、d1366 明/暗、m390 明/暗：",
	"✓ after 跑完全程断言 0 问题且 before / after 截图都在；✗ after 有问题；— 显式跳过（视口不适用，清单里注明）；· 其余（没跑、中途失败、缺截图），不算通过。",
	"",
];
let total = 0;
let okCount = 0;
/** 清单里登记的全部 id（含重复），与 spec 对账用 */
const listed: string[] = [];
const issueCounts = new Map<string, number>();
const sections: string[] = [];
for (const f of areas) {
	const md = readFileSync(`${ROOT}areas/${f}`, "utf8");
	const area = f.replace(/\.md$/, "");
	const inv = table(md, "清单");
	const issues = table(md, "问题记录");
	sections.push(
		`## ${area}（${inv.length} 个界面，${issues.length} 条问题，详见 [areas/${f}](areas/${f})）`,
		"",
	);
	sections.push(
		"| id | 界面 | 打开路径 | 形态 | 状态 |",
		"|---|---|---|---|---|",
	);
	for (const [id = "", name = "", path = "", kind = ""] of inv) {
		const sid = id.replace(/`/g, "");
		listed.push(sid);
		const s = status(sid);
		total++;
		if (s.ok) okCount++;
		sections.push(
			`| ${id} | ${name} | ${path} | ${kind} | ${s.cells.join(" ")} |`,
		);
	}
	sections.push("");
	for (const row of issues) {
		const kind = (row[3] ?? "").replace(/（.*$/, "");
		issueCounts.set(kind, (issueCounts.get(kind) ?? 0) + 1);
	}
}
/**
 * 覆盖对账：spec 里登记的用例（Playwright --list）与清单 id 双向差集、清单内重复 id、spec 内重复标题，都必须为零。
 * 否则写了用例没登记、登记了没写用例的界面会从「全绿计数」里漏掉。
 */
const listing = Bun.spawnSync(
	["npx", "playwright", "test", "e2e/ui-audit", "--list", "--reporter=json"],
	{ cwd: new URL("../..", import.meta.url).pathname, stderr: "pipe" },
);
const report: unknown = JSON.parse(listing.stdout.toString());
const specIds: string[] = [];
const walk = (n: unknown): void => {
	if (!n || typeof n !== "object") return;
	if ("specs" in n && Array.isArray(n.specs))
		for (const s of n.specs)
			if (s && typeof s === "object" && "title" in s)
				specIds.push(String(s.title));
	if ("suites" in n && Array.isArray(n.suites))
		for (const c of n.suites) walk(c);
};
walk(report);
const dupes = (xs: string[]) => [
	...new Set(xs.filter((x, i) => xs.indexOf(x) !== i)),
];
const specSet = new Set(specIds);
const listedSet = new Set(listed);
const gaps = {
	"spec 有、清单没登记": [...specSet].filter((x) => !listedSet.has(x)),
	"清单登记了、spec 没有": [...listedSet].filter((x) => !specSet.has(x)),
	清单内重复: dupes(listed),
	"spec 内重复": dupes(specIds),
};
const gapLines = Object.entries(gaps)
	.filter(([, v]) => v.length)
	.map(
		([k, v]) => `- ${k}（${v.length}）：${v.map((x) => `\`${x}\``).join("、")}`,
	);
out.push(
	`界面 ${total} 个，全部视口通过 ${okCount} 个。问题 ${[...issueCounts.values()].reduce((a, b) => a + b, 0)} 条：${[...issueCounts].map(([k, n]) => `${k} ${n}`).join("、")}。`,
	"",
	`覆盖对账（spec ${specSet.size} 个用例 vs 清单 ${listedSet.size} 个 id）：${gapLines.length ? "**未对齐**" : "一致"}`,
	...gapLines,
	"",
	...sections,
);
writeFileSync(`${ROOT}inventory.md`, `${out.join("\n")}\n`);
console.log(`界面 ${total}，通过 ${okCount}`);
for (const l of gapLines) console.error(l);
// 对账不一致或有界面未全绿时非零退出，可直接当验收命令用
if (gapLines.length || okCount !== total) process.exit(1);
