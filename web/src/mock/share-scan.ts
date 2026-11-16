/**
 * mock 的密钥扫描规则（同 orynd `internal/share/scan.go`；mock 没有密钥库明文，不做 `vault_secret`）。
 * 线程分享（`MockServer.scanShare`）与 artifact 发布（`scanFiles`）共用。
 */
import type { Schemas } from "../api/client";

export const SHARE_RULES: { rule: Schemas["ShareFindingRule"]; re: RegExp }[] =
	[
		{ rule: "private_key", re: /-----BEGIN [A-Z ]*PRIVATE KEY-----/g },
		{ rule: "api_key_sk", re: /sk-[A-Za-z0-9_-]{20,}/g },
		{
			rule: "github_token",
			re: /gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{22,}/g,
		},
		{ rule: "aws_access_key", re: /AKIA[0-9A-Z]{16}/g },
		{ rule: "google_api_key", re: /AIza[0-9A-Za-z_-]{35}/g },
		{ rule: "slack_token", re: /xox[baprs]-[A-Za-z0-9-]{10,}/g },
		{
			rule: "jwt",
			re: /eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}/g,
		},
		{
			rule: "oryn_token",
			re: /(?:opv|oot|odt|ost|ows|owt)_[A-Za-z0-9_-]{43}/g,
		},
		{
			rule: "bearer_header",
			re: /authorization["':\s]+bearer\s+[A-Za-z0-9._~+/-]{20,}/gi,
		},
		{
			rule: "url_credentials",
			re: /[a-z][a-z0-9+.-]{1,15}:\/\/[^/\s:@"]+:[^/\s:@"]{6,}@/g,
		},
	];

/** 脱敏预览：前 4 字符 + `…` + 后 2 字符；短于 12 字符只留前 2。 */
export function redact(s: string): string {
	return s.length < 12
		? `${s.slice(0, 2)}…`
		: `${s.slice(0, 4)}…${s.slice(-2)}`;
}

/** 扫描一组文本文件（同 `share.Service.ScanContent`）：每个文件算一个条目，命中项的 `entryId` 是路径、按路径顺序。 */
export function scanFiles(
	files: Iterable<[path: string, text: string]>,
): Schemas["ShareScan"] {
	const sorted = [...files].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
	const findings: Schemas["ShareFinding"][] = [];
	for (const [path, text] of sorted)
		for (const { rule, re } of SHARE_RULES) {
			const hits = text.match(re);
			if (hits)
				findings.push({
					rule,
					seq: 0,
					entryId: path,
					preview: redact(hits[0] ?? ""),
					count: hits.length,
				});
		}
	return {
		findings: findings.slice(0, 200),
		scanned: sorted.length,
		truncated: findings.length > 200,
	};
}
