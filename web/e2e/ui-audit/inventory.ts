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
		const s = status(id.replace(/`/g, ""));
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
out.push(
	`界面 ${total} 个，全部视口通过 ${okCount} 个。问题 ${[...issueCounts.values()].reduce((a, b) => a + b, 0)} 条：${[...issueCounts].map(([k, n]) => `${k} ${n}`).join("、")}。`,
	"",
	...sections,
);
writeFileSync(`${ROOT}inventory.md`, `${out.join("\n")}\n`);
console.log(`界面 ${total}，通过 ${okCount}`);
