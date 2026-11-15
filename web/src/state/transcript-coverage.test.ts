/**
 * Core 升级守卫：直接读 omp 的类型声明 `src/omp/rpc-types.d.ts`（随 Core 版本重新生成），
 * 确认会话条目类型与扩展 UI 请求方法都已在 Web 侧归类。Core 新增成员时这里变红，提示去对应位置归类，
 * 而不是让新条目被静默丢弃、新请求被当成未知帧。
 */
import { beforeAll, describe, expect, it } from "vitest";
import { KNOWN_ENTRY_TYPES } from "./transcript";

let DTS = "";
beforeAll(async () => {
	// 工作目录是 web/；前端 tsconfig 不带 Node 类型，只声明用到的函数
	const fs = (await import("node:fs" as string)) as {
		readFileSync(path: string, encoding: "utf8"): string;
	};
	DTS = fs.readFileSync("src/omp/rpc-types.d.ts", "utf8");
});

/** 接口体内第一处 `type:` 的字符串字面量；`typeof CONST` 按同文件的 `declare const CONST = "…"` 解析。 */
function entryTypeOf(name: string): string | undefined {
	const decl = new RegExp(
		`^(?:export )?interface ${name}\\b[^{]*\\{([\\s\\S]*?)^\\}`,
		"m",
	).exec(DTS);
	const body = decl?.[1];
	if (body === undefined) return undefined;
	const m = /\btype: (?:"([^"]+)"|typeof (\w+))/.exec(body);
	if (!m) return undefined;
	if (m[1] !== undefined) return m[1];
	return new RegExp(`declare const ${m[2]} = "([^"]+)"`).exec(DTS)?.[1];
}

describe("omp 会话条目类型都已归类", () => {
	it("SessionEntry 联合的每个成员的 type 都在 KNOWN_ENTRY_TYPES 里", () => {
		const line = DTS.split("\n").find((l) =>
			l.startsWith("type SessionEntry ="),
		);
		expect(line, "rpc-types.d.ts 里找不到 `type SessionEntry =`").toBeDefined();
		const members = (line ?? "")
			.slice((line ?? "").indexOf("=") + 1)
			.replace(/;\s*$/, "")
			.split("|")
			.map((s) => s.trim())
			.filter(Boolean);
		// 正则失效（联合改成多行等）时不能空转
		expect(members.length).toBeGreaterThan(0);
		const types = members.map((name) => [name, entryTypeOf(name)] as const);
		for (const [name, type] of types)
			expect(type, `解析不出 ${name} 的 type 字面量`).toBeDefined();
		const missing = types
			.filter(([, type]) => type && !KNOWN_ENTRY_TYPES.includes(type))
			.map(([name, type]) => `${name} (${type})`);
		expect(
			missing,
			"omp 新增了会话条目类型：在 web/src/state/transcript.ts 归入 HIDDEN_ENTRY_TYPES / META_ENTRY_TYPES 或专门渲染，并在 Transcript.tsx 的 MetaLine 补文案",
		).toEqual([]);
	});
});

describe("omp 扩展 UI 请求方法都已归类", () => {
	it("RpcExtensionUIRequest 的 method 集合与已知集合一致", () => {
		const start = DTS.indexOf("export type RpcExtensionUIRequest = {");
		expect(
			start,
			"rpc-types.d.ts 里找不到 `export type RpcExtensionUIRequest = {`",
		).toBeGreaterThanOrEqual(0);
		const rest = DTS.slice(start + 1);
		const end = rest.search(/^export /m);
		const block = end < 0 ? rest : rest.slice(0, end);
		const methods = [...block.matchAll(/\bmethod: "([^"]+)"/g)].map(
			(m) => m[1],
		);
		expect(
			[...new Set(methods)].sort(),
			"omp 的扩展 UI 请求方法有变化：在 web/src/state/thread-reducer.ts 的 handleUiRequest、internal/threads/live.go 的 pendingMethods、internal/orbdrt/pendingui.go 的 nonPendingMethods 三处归类后再更新这里",
		).toEqual(
			[
				"select",
				"confirm",
				"askDialog",
				"input",
				"editor",
				"ask",
				"form",
				"cancel",
				"notify",
				"setStatus",
				"setWidget",
				"setTitle",
				"set_editor_text",
				"open_url",
			].sort(),
		);
	});
});
