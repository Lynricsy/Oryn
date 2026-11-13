/**
 * setup / resume 失败的诊断：从 orbd 上报的 `error` 与日志尾部归纳「哪一步、为什么失败」，给出一条下一步建议。
 *
 * orbd 的错误格式（`internal/orbdrt/setup.go`）：
 * - 脚本失败：`<label>: exit status N` / `<label>: signal: killed`，label 为 `project setup script` 或仓库内相对路径（`.oryn/setup`）；
 * - 超时：`context deadline exceeded (...)`（setup 上限 20 分钟、resume 10 分钟）；
 * - 被取消：`context canceled (...)`（orb 停止或重启）。
 * 日志只看尾部：失败原因几乎总在最后几十行，扫全文会被早先已恢复的报错误导。
 */
import type { SetupRun } from "./env-banner";

export type SetupDiagnosisKind =
	| "timeout"
	| "cancelled"
	| "oom"
	| "notFound"
	| "package"
	| "permission"
	| "network"
	| "disk"
	| "exit"
	| "unknown";

export interface SetupDiagnosis {
	kind: SetupDiagnosisKind;
	/** 出错的脚本：`null` 表示项目设置里的脚本；`undefined` 表示无从得知 */
	script?: string | null;
	/** 脚本退出码 */
	code?: number;
	/** 找不到的命令名（notFound） */
	command?: string;
	/** 找不到的软件包名（package） */
	pkg?: string;
	/** 超时上限（分钟） */
	limitMin?: number;
}

/** 与 orbd 的 `setupTimeout` / `resumeTimeout` 一致 */
const LIMIT_MIN = { setup: 20, resume: 10 } as const;

/** 只看日志末尾这么多行 */
const TAIL = 60;

const RE_SCRIPT = /^(.+?): (?:exit status (\d+)|signal: (\w+))$/;
const RE_NOT_FOUND = [
	// bash：`line 3: foo: command not found`
	/(?:^|[\s:])([\w.+@/-]+): command not found/,
	// dash/sh：`foo: not found`
	/(?:^|\s)([\w.+@/-]+): not found$/,
];
const RE_OOM =
	/\bKilled\b|out of memory|OOMKilled|JavaScript heap out of memory|Cannot allocate memory/i;
const RE_DISK = /No space left on device|ENOSPC|Disk quota exceeded/i;
const RE_NETWORK =
	/Could not resolve host|ENOTFOUND|EAI_AGAIN|Temporary failure in name resolution|Network is unreachable|Connection (?:timed out|refused)|ECONNREFUSED|ETIMEDOUT|ECONNRESET|TLS handshake timeout|Failed to connect to|unable to access '/i;
const RE_PERMISSION = /Permission denied|EACCES|Operation not permitted/i;
/**
 * 软件源里找不到的包：apt / pip / npm / pnpm；捕获组是包名。
 * `url` 表示包名取自 URL 路径段（npm / pnpm 把 scope 的 `/` 编码成 `%2f`），只有这些需要解码。
 */
const RE_PACKAGE: { re: RegExp; url?: true }[] = [
	{ re: /Unable to locate package (\S+)/ },
	{ re: /Package '([^']+)' has no installation candidate/ },
	{ re: /No matching distribution found for (\S+)/ },
	{ re: /npm (?:ERR!|error) 404 Not Found - GET \S+\/([^/\s]+)$/, url: true },
	{ re: /ERR_PNPM_FETCH_404.*?\/([^/\s:]+):/, url: true },
];

/** 日志是任意文本：非法的 `%` 转义保留原文，不能让诊断在渲染中抛错 */
function decodeSegment(s: string): string {
	try {
		return decodeURIComponent(s);
	} catch {
		return s;
	}
}

function missingPackage(lines: string[]): string | undefined {
	for (let i = lines.length - 1; i >= 0; i--) {
		const line = lines[i]?.trimEnd() ?? "";
		for (const { re, url } of RE_PACKAGE) {
			const name = line.match(re)?.[1];
			if (name) return url ? decodeSegment(name) : name;
		}
	}
	return undefined;
}

function lastLines(log: string): string[] {
	const lines = log.split("\n");
	return lines.slice(Math.max(0, lines.length - TAIL));
}

/** 在日志尾部从后往前找第一条命中的行 */
function findLast(lines: string[], re: RegExp): RegExpMatchArray | null {
	for (let i = lines.length - 1; i >= 0; i--) {
		const m = lines[i]?.match(re);
		if (m) return m;
	}
	return null;
}

function notFoundCommand(lines: string[]): string | undefined {
	for (let i = lines.length - 1; i >= 0; i--) {
		const line = lines[i]?.trimEnd() ?? "";
		for (const re of RE_NOT_FOUND) {
			const cmd = line.match(re)?.[1];
			if (cmd) return cmd.split("/").pop();
		}
	}
	return undefined;
}

function scriptOf(label: string): string | null {
	return label.startsWith("project ") && label.endsWith(" script")
		? null
		: label;
}

/**
 * 归纳一次失败运行的原因。`log` 缺省时只按 `error` 判断（提示条在日志到达前也能给出结论）。
 * 成功的运行返回 `null`。
 */
export function diagnoseSetupRun(
	run: Pick<SetupRun, "ok" | "phase" | "error">,
	log?: string,
): SetupDiagnosis | null {
	if (run.ok) return null;
	const error = (run.error ?? "").trim();
	if (/context deadline exceeded/.test(error))
		return { kind: "timeout", limitMin: LIMIT_MIN[run.phase] };
	if (/context canceled/.test(error)) return { kind: "cancelled" };

	const m = error.match(RE_SCRIPT);
	const script = m?.[1] !== undefined ? scriptOf(m[1]) : undefined;
	const code = m?.[2] !== undefined ? Number(m[2]) : undefined;
	const signal = m?.[3];
	const lines = log ? lastLines(log) : [];

	if (signal === "killed" || code === 137) return { kind: "oom", script };
	if (code === 127)
		return { kind: "notFound", script, command: notFoundCommand(lines) };
	if (code === 126) return { kind: "permission", script };

	// 退出码说明不了原因时，看日志尾部的典型报错；越具体的越先判断
	if (findLast(lines, RE_DISK)) return { kind: "disk", script };
	if (findLast(lines, RE_OOM)) return { kind: "oom", script };
	const command = notFoundCommand(lines);
	if (command) return { kind: "notFound", script, command };
	const pkg = missingPackage(lines);
	if (pkg) return { kind: "package", script, pkg };
	if (findLast(lines, RE_NETWORK)) return { kind: "network", script };
	if (findLast(lines, RE_PERMISSION)) return { kind: "permission", script };

	if (code !== undefined) return { kind: "exit", script, code };
	return { kind: "unknown" };
}
