/**
 * `.env` 文本解析（M24 阶段 ④，项目设置「密钥与环境变量」的批量添加）：每行一个 `KEY=value`，
 * 支持 `#` 注释、`export` 前缀、单双引号（双引号里认 `\n` `\"` `\\`）与不带引号值的行内注释；不支持跨行的值。
 * 不可导入的行记进 `issues`，同名以后者为准。
 */

/** 环境变量名（与服务端 `envNameRe` 同规则）；全前端只有这一份。 */
export const ENV_NAME_RE = /^[A-Z_][A-Z0-9_]*$/;

export interface DotenvEntry {
	line: number;
	name: string;
	value: string;
}

export type DotenvIssueReason =
	| "noEquals"
	| "badName"
	| "emptyValue"
	| "unterminatedQuote"
	| "trailingText"
	| "duplicate";

export interface DotenvIssue {
	line: number;
	reason: DotenvIssueReason;
	/** duplicate：同名的后一行 */
	other?: number;
}

type ValueResult = { value: string } | { reason: DotenvIssueReason };

/** 引号闭合后剩下的部分只能是空白或注释。 */
function restOk(rest: string): boolean {
	const r = rest.trim();
	return r === "" || r.startsWith("#");
}

/** `after` 是 `=` 之后的原文（未去前导空白）。 */
function parseValue(after: string): ValueResult {
	const raw = after.trimStart();
	if (raw.startsWith('"')) {
		let out = "";
		for (let i = 1; i < raw.length; i++) {
			const c = raw[i];
			if (c === "\\" && i + 1 < raw.length) {
				const n = raw[i + 1];
				i++;
				if (n === "n") out += "\n";
				else if (n === '"') out += '"';
				else if (n === "\\") out += "\\";
				else out += `\\${n}`;
				continue;
			}
			if (c === '"')
				return restOk(raw.slice(i + 1))
					? { value: out }
					: { reason: "trailingText" };
			out += c;
		}
		return { reason: "unterminatedQuote" };
	}
	if (raw.startsWith("'")) {
		const end = raw.indexOf("'", 1);
		if (end < 0) return { reason: "unterminatedQuote" };
		return restOk(raw.slice(end + 1))
			? { value: raw.slice(1, end) }
			: { reason: "trailingText" };
	}
	// 行内注释要求 `#` 前有空白（`a#b` 原样保留）；在去掉前导空白之前找，`KEY= #c` 才是空值
	const hash = after.search(/\s#/);
	return { value: (hash < 0 ? after : after.slice(0, hash)).trim() };
}

export function parseDotenv(text: string): {
	entries: DotenvEntry[];
	issues: DotenvIssue[];
} {
	const issues: DotenvIssue[] = [];
	const byName = new Map<string, DotenvEntry>();
	text.split(/\r?\n/).forEach((rawLine, i) => {
		const line = i + 1;
		const trimmed = rawLine.trim();
		if (!trimmed || trimmed.startsWith("#")) return;
		const body = trimmed.replace(/^export\s+/, "");
		const eq = body.indexOf("=");
		if (eq < 0) {
			issues.push({ line, reason: "noEquals" });
			return;
		}
		const name = body.slice(0, eq).trim();
		if (!ENV_NAME_RE.test(name)) {
			issues.push({ line, reason: "badName" });
			return;
		}
		const res = parseValue(body.slice(eq + 1));
		if ("reason" in res) {
			issues.push({ line, reason: res.reason });
			return;
		}
		if (res.value === "") {
			issues.push({ line, reason: "emptyValue" });
			return;
		}
		const prev = byName.get(name);
		if (prev) {
			issues.push({ line: prev.line, reason: "duplicate", other: line });
			byName.delete(name);
		}
		byName.set(name, { line, name, value: res.value });
	});
	issues.sort((a, b) => a.line - b.line);
	return {
		entries: [...byName.values()].sort((a, b) => a.line - b.line),
		issues,
	};
}
