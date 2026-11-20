/** `lsp` — language-server queries: diagnostics, definitions, references, hover, rename, … */
import type { ReactNode } from "react";
import type { Tone } from "../parts";
import {
	Badge,
	InvalidArg,
	Kv,
	KvGrid,
	Output,
	PathText,
	ResultText,
	Row,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	normalizeWs,
	num,
	resultTextOf,
	str,
	truncate,
} from "../util";

/** `file:line:col [severity] message` — the diagnostics line format the tool emits. */
const DIAG_RE = /^(.*):(\d+):(\d+)\s+\[(\w+)\]\s*(.*)$/;
/** Bare `file:line:col` location line (references, definitions, implementations). */
const LOC_RE = /^(.+):(\d+):(\d+)$/;
/** Actions whose result text is a list of locations. */
const LOCATION_ACTIONS: Record<string, true> = {
	definition: true,
	references: true,
	type_definition: true,
	implementation: true,
};

const MAX_ROWS = 24;

interface DiagRow {
	file: string;
	line: string;
	col: string;
	severity: string;
	message: string;
}

interface LocRow {
	file: string;
	line: string;
	col: string;
}

function parseDiagnostics(text: string): DiagRow[] {
	const rows: DiagRow[] = [];
	for (const raw of text.split("\n")) {
		const m = raw.trim().match(DIAG_RE);
		if (m)
			rows.push({
				file: m[1] ?? "",
				line: m[2] ?? "",
				col: m[3] ?? "",
				severity: (m[4] ?? "").toLowerCase(),
				message: normalizeWs(m[5] ?? ""),
			});
	}
	return rows;
}

function parseLocations(text: string): LocRow[] {
	const rows: LocRow[] = [];
	for (const raw of text.split("\n")) {
		const m = raw.trim().match(LOC_RE);
		if (m) rows.push({ file: m[1] ?? "", line: m[2] ?? "", col: m[3] ?? "" });
	}
	return rows;
}

function severityTone(severity: string): Tone | undefined {
	switch (severity) {
		case "error":
			return "err";
		case "warning":
			return "warn";
		case "info":
			return "accent";
		default:
			return undefined;
	}
}

/** Kv row for an optional arg: hidden when absent, InvalidArg when mistyped. */
function ArgKv({
	k,
	raw,
	val,
}: {
	k: string;
	raw: unknown;
	val: ReactNode;
}): ReactNode {
	if (raw === undefined) return null;
	return (
		<Kv k={k}>
			{val == null || val === false ? <InvalidArg what={k} /> : val}
		</Kv>
	);
}

function DiagnosticRows({
	text,
	rows,
}: {
	text: string;
	rows: DiagRow[];
}): ReactNode {
	const errMatch = text.match(/(\d+)\s+error\(s\)/);
	const warnMatch = text.match(/(\d+)\s+warning\(s\)/);
	const shown = rows.slice(0, MAX_ROWS);
	return (
		<>
			{(errMatch || warnMatch) && (
				<span className="tv-badges">
					{errMatch && (
						<Badge tone="err">
							{errMatch[1]} error{errMatch[1] === "1" ? "" : "s"}
						</Badge>
					)}
					{warnMatch && (
						<Badge tone="warn">
							{warnMatch[1]} warning{warnMatch[1] === "1" ? "" : "s"}
						</Badge>
					)}
				</span>
			)}
			<div className="tv-list">
				{shown.map((d, i) => (
					<Row
						key={i}
						k={<Badge tone={severityTone(d.severity)}>{d.severity}</Badge>}
					>
						<PathText path={d.file} sel={`${d.line}:${d.col}`} />
						{d.message && (
							<span className="tv-muted"> {truncate(d.message, 160)}</span>
						)}
					</Row>
				))}
				{rows.length > shown.length && (
					<Row>
						<span className="tv-faint">
							… {rows.length - shown.length} more
						</span>
					</Row>
				)}
			</div>
		</>
	);
}

function LocationRows({
	text,
	rows,
}: {
	text: string;
	rows: LocRow[];
}): ReactNode {
	const refMatch = text.match(/(\d+)\s+reference\(s\)/);
	const shown = rows.slice(0, MAX_ROWS);
	return (
		<>
			{refMatch && (
				<span className="tv-badges">
					<Badge tone="accent">
						{refMatch[1]} reference{refMatch[1] === "1" ? "" : "s"}
					</Badge>
				</span>
			)}
			<div className="tv-list">
				{shown.map((l, i) => (
					<Row key={i}>
						<PathText path={l.file} sel={`${l.line}:${l.col}`} />
					</Row>
				))}
				{rows.length > shown.length && (
					<Row>
						<span className="tv-faint">
							… {rows.length - shown.length} more
						</span>
					</Row>
				)}
			</div>
		</>
	);
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const action = str(args.action);
	const file = str(args.file);
	const line = num(args.line);
	const symbol = str(args.symbol);
	const query = str(args.query);
	const newName = str(args.new_name);
	return (
		<>
			<Badge tone="accent">
				{action ? action.replace(/_/g, " ") : "request"}
			</Badge>
			{file === "*" && <Badge>workspace</Badge>}
			{file && file !== "*" && <PathText path={file} from={line} />}
			{!file && line != null && <span className="tv-faint">line {line}</span>}
			{symbol && (
				<span className="tv-pattern">{truncate(normalizeWs(symbol), 48)}</span>
			)}
			{query && (
				<span className="tv-muted">{truncate(normalizeWs(query), 48)}</span>
			)}
			{newName && (
				<span className="tv-muted">→ {truncate(normalizeWs(newName), 48)}</span>
			)}
		</>
	);
}

/** 有可列的参数（动作只在类型不对时列出；有文件时行号并入路径）。 */
function hasArgRows(args: Record<string, unknown>): boolean {
	if (args.action !== undefined && str(args.action) === null) return true;
	if (args.line !== undefined && !str(args.file)) return true;
	return ["file", "symbol", "query", "new_name", "apply", "timeout"].some(
		(k) => args[k] !== undefined,
	);
}

function Input({ args }: ToolRenderProps): ReactNode {
	const file = str(args.file);
	const line = num(args.line);
	const symbol = str(args.symbol);
	const query = str(args.query);
	const newName = str(args.new_name);
	const apply = typeof args.apply === "boolean" ? args.apply : null;
	const timeout = num(args.timeout);
	const payload = str(args.payload);
	const invalidPayload = args.payload !== undefined && payload == null;

	return (
		<>
			{/* 动作已在折叠行里；文件与符号 / 查询 / 新名字在折叠行里只是摘要（会被截断），这里始终给出全文 */}
			{(hasArgRows(args) || invalidPayload) && (
				<KvGrid>
					<ArgKv
						k="action"
						raw={str(args.action) === null ? args.action : undefined}
						val={null}
					/>
					<ArgKv
						k="file"
						raw={args.file}
						val={
							file === "*" ? (
								"workspace"
							) : file === null ? null : (
								<PathText path={file} from={line} />
							)
						}
					/>
					{!file && <ArgKv k="line" raw={args.line} val={line} />}
					<ArgKv k="symbol" raw={args.symbol} val={symbol} />
					<ArgKv k="query" raw={args.query} val={query} />
					<ArgKv k="new name" raw={args.new_name} val={newName} />
					<ArgKv
						k="apply"
						raw={args.apply}
						val={apply == null ? null : apply ? "yes" : "no"}
					/>
					<ArgKv
						k="timeout"
						raw={args.timeout}
						val={timeout != null && `${timeout}s`}
					/>
					{invalidPayload && (
						<Kv k="payload">
							<InvalidArg what="payload" />
						</Kv>
					)}
				</KvGrid>
			)}
			{payload && (
				<Output
					text={payload}
					lang="json"
					variant="code"
					maxLines={8}
					title="payload"
				/>
			)}
		</>
	);
}

/** `request` 的结果首行是 `<server> ← <method>:`，其后是 JSON 序列化的响应。 */
const REQUEST_HEAD_RE = /^(.+ ← .+:)\n([\s\S]*)$/;

/** 没有诊断 / 位置列表时的结果文本：hover 是 markdown，request 的响应是 JSON。 */
function ResultBody({
	action,
	result,
}: {
	action: string | null;
	result: ToolRenderProps["result"];
}): ReactNode {
	const ok = result !== undefined && !result.isError;
	if (ok && action === "hover")
		return (
			<ResultText
				result={result}
				maxLines={12}
				lang="markdown"
				variant="plain"
			/>
		);
	const request =
		ok && action === "request"
			? REQUEST_HEAD_RE.exec(resultTextOf(result).trim())
			: null;
	if (request) {
		const body = request[2] ?? "";
		return (
			<>
				<div className="tv-muted">{request[1]}</div>
				<Output
					text={body}
					lang={/^[[{]|^(?:null|true|false|-?\d)/.test(body) ? "json" : null}
					variant="code"
					maxLines={12}
				/>
			</>
		);
	}
	return <ResultText result={result} maxLines={12} />;
}

function Result({ args, result }: ToolRenderProps): ReactNode {
	const details = detailsRecord(result);
	const serverName = details ? str(details.serverName) : null;
	const action = str(args.action) ?? (details ? str(details.action) : null);

	const text = result && !result.isError ? resultTextOf(result) : "";
	const diags = text ? parseDiagnostics(text) : [];
	const locs =
		diags.length === 0 &&
		text &&
		((action != null && LOCATION_ACTIONS[action]) ||
			/\d+\s+reference\(s\)/.test(text))
			? parseLocations(text)
			: [];

	return (
		<>
			{serverName && (
				<KvGrid>
					<Kv k="server">{serverName}</Kv>
				</KvGrid>
			)}
			{diags.length > 0 ? (
				<DiagnosticRows text={text} rows={diags} />
			) : locs.length > 0 ? (
				<LocationRows text={text} rows={locs} />
			) : (
				<ResultBody action={action} result={result} />
			)}
		</>
	);
}

export const lspRenderer: ToolRenderer = { Summary, Input, Result };
