/**
 * Core `/mcp list`（`slash-commands/helpers/mcp.ts` 的 `handleListCommand`）与 `/ssh list`（`helpers/ssh.ts`）的输出：
 * 每行一条，` | ` 分隔，行尾 `[user]` / `[project]`。
 * - mcp：`${name} | ${type} | ${enabled|disabled} | ${location ?? "(unknown)"} [${scope}]`
 * - ssh：`${name} | ${host} | ${user ?? "-"} | ${port ?? 22} [${scope}]`
 * 全部行都匹配同一种才算；空列表时 Core 输出一句话（不匹配），原样显示。
 */

export type ConfigListKind = "mcp" | "ssh";

export interface ConfigList {
	kind: ConfigListKind;
	/** 每行四列（不含 scope）。 */
	rows: { cells: [string, string, string, string]; scope: string }[];
}

const ROW_RE: Record<ConfigListKind, RegExp> = {
	mcp: /^(.+?) \| (\S+) \| (enabled|disabled) \| (.+) \[(user|project)\]$/,
	ssh: /^(.+?) \| (.+?) \| (.+?) \| (\d+) \[(user|project)\]$/,
};

export function parseConfigList(text: string): ConfigList | null {
	const lines = text.split("\n");
	for (const kind of ["mcp", "ssh"] as const) {
		const rows: ConfigList["rows"] = [];
		for (const line of lines) {
			const m = ROW_RE[kind].exec(line);
			if (!m) break;
			rows.push({
				cells: [m[1] ?? "", m[2] ?? "", m[3] ?? "", m[4] ?? ""],
				scope: m[5] ?? "",
			});
		}
		if (rows.length === lines.length) return { kind, rows };
	}
	return null;
}
