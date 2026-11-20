/**
 * `ida` — IDA Pro (idalib) 数据库：生命周期（list/open/save/close）、结构化修改
 * （rename/comment/set_type/make_function）与库内 Python `exec`。
 *
 * 结果形状见 oryn-core `tools/ida.ts`：details 只有 `{ action, db? }`（db 为解析后的库 id），
 * 其余都在结果文本里，按动作有固定句式：
 * - list：每库一行 `<id>  <module>  <format>  <arch>/<bitness>  <idb>[  [<method> running]]`，
 *   或 `<id>  (opening)  <ref>`；没有库时 `No IDA databases open.`
 * - open：`Opened <id> (<module>, <format>, <arch> <bitness>-bit) → <idb>. read <ref> for the overview.`
 * - save：`Saved <idb>`；close：`Closed <id> (saved)` / `Closed <id> (discarded unsaved changes)`
 * - rename：`Renamed <ea> <old|(unnamed)> → <new>`；comment：`Set [repeatable ]comment at <ea>`
 * - set_type：`Typed <ea> as <type>`；make_function：`Created function <name> at <ea>`
 * - exec：依次拼接 output（trimEnd）、`=> <value repr>`、error（traceback，或
 *   `KeyboardInterrupt: interrupted (timeout or cancel)`），全空时 `(no output)`；有 error 时 isError。
 *   超长时中间省略并附 `[raw output: artifact://<id>]`。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	Badge,
	Badges,
	CodeBlock,
	InvalidArg,
	Kv,
	KvGrid,
	MetaCount,
	Output,
	PathText,
	ResultText,
	Row,
} from "../parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "../types";
import {
	detailsRecord,
	normalizeWs,
	num,
	resultTextOf,
	str,
	truncate,
} from "../util";

const TRACEBACK_RE = /(?:^|\n)Traceback \(most recent call last\):/;
const INTERRUPT_LINE = "KeyboardInterrupt: interrupted (timeout or cancel)";
const ARTIFACT_RE = /\n?\[raw output: artifact:\/\/([^\]\s]+)\]\s*$/;
const MAX_ROWS = 24;

interface ExecParts {
	output: string;
	value: string | null;
	error: string | null;
	artifact: string | null;
}

interface ListRow {
	id: string;
	opening: boolean;
	module: string | null;
	format: string | null;
	arch: string | null;
	path: string;
	running: string | null;
}

type Parsed =
	| { kind: "exec"; exec: ExecParts }
	| { kind: "list"; rows: ListRow[] }
	| {
			kind: "open";
			id: string;
			module: string;
			format: string;
			arch: string;
			bitness: string;
			idb: string;
	  }
	| { kind: "save"; idb: string }
	| { kind: "close"; id: string; saved: boolean }
	| { kind: "rename"; ea: string; old: string; name: string }
	| { kind: "comment"; ea: string }
	| { kind: "set_type"; ea: string; type: string }
	| { kind: "make_function"; ea: string; name: string };

/** 把 exec 的拼接文本拆回 output / value / error（与 Core 的拼接顺序相反）。 */
function parseExec(raw: string, isError: boolean): ExecParts {
	let text = raw.replace(/\s+$/, "");
	let artifact: string | null = null;
	const art = ARTIFACT_RE.exec(text);
	if (art) {
		artifact = art[1] ?? null;
		text = text.slice(0, art.index).replace(/\s+$/, "");
	}
	if (text === "(no output)")
		return { output: "", value: null, error: null, artifact };
	if (isError) {
		const tb = TRACEBACK_RE.exec(text);
		let at = -1;
		if (tb) at = tb.index + (tb[0].startsWith("\n") ? 1 : 0);
		else if (text.endsWith(INTERRUPT_LINE))
			at = text.length - INTERRUPT_LINE.length;
		// 没有 traceback：调用在执行前就失败（库没打开、找不到 db……），整段都是错误
		if (at < 0) return { output: "", value: null, error: text, artifact };
		return {
			output: text.slice(0, at).replace(/\s+$/, ""),
			value: null,
			error: text.slice(at),
			artifact,
		};
	}
	const lines = text.split("\n");
	let at = -1;
	for (let i = lines.length - 1; i >= 0; i--) {
		if (lines[i]?.startsWith("=> ")) {
			at = i;
			break;
		}
	}
	if (at < 0) return { output: text, value: null, error: null, artifact };
	return {
		output: lines.slice(0, at).join("\n"),
		value: lines.slice(at).join("\n").slice(3),
		error: null,
		artifact,
	};
}

function parseList(text: string): ListRow[] {
	const rows: ListRow[] = [];
	for (const line of text.split("\n")) {
		const parts = line.trim().split(/\s{2,}/);
		const id = parts[0];
		if (!id || parts.length < 3) continue;
		if (parts[1] === "(opening)") {
			rows.push({
				id,
				opening: true,
				module: null,
				format: null,
				arch: null,
				path: parts.slice(2).join("  "),
				running: null,
			});
			continue;
		}
		if (parts.length < 5) continue;
		const last = parts[parts.length - 1] ?? "";
		const run = /^\[(\S+) running\]$/.exec(last);
		const pathParts = parts.slice(4, run ? -1 : undefined);
		rows.push({
			id,
			opening: false,
			module: parts[1] ?? null,
			format: parts[2] ?? null,
			arch: parts[3] ?? null,
			path: pathParts.join("  "),
			running: run?.[1] ?? null,
		});
	}
	return rows;
}

/** 按动作解析结果文本；未出错的非 exec 结果解析不出句式时返回 null（交给 ResultText 兜底）。 */
function parseResult(
	action: string | null,
	result: ToolResultLike | undefined,
): Parsed | null {
	if (!result) return null;
	const text = resultTextOf(result).trim();
	if (action === "exec")
		return { kind: "exec", exec: parseExec(text, result.isError === true) };
	if (result.isError) return null;
	let m: RegExpExecArray | null;
	switch (action) {
		case "list": {
			const rows = parseList(text);
			return rows.length > 0 || /^No IDA databases open/.test(text)
				? { kind: "list", rows }
				: null;
		}
		case "open":
			m =
				/^Opened (\S+) \((.*), ([^,]+), (\S+) (\d+)-bit\) → ([\s\S]+?)\. read [\s\S]* for the overview\.$/.exec(
					text,
				);
			return m
				? {
						kind: "open",
						id: m[1] ?? "",
						module: m[2] ?? "",
						format: m[3] ?? "",
						arch: m[4] ?? "",
						bitness: m[5] ?? "",
						idb: m[6] ?? "",
					}
				: null;
		case "save":
			m = /^Saved ([\s\S]+)$/.exec(text);
			return m ? { kind: "save", idb: m[1] ?? "" } : null;
		case "close":
			m = /^Closed (\S+) \((saved|discarded unsaved changes)\)$/.exec(text);
			return m
				? { kind: "close", id: m[1] ?? "", saved: m[2] === "saved" }
				: null;
		case "rename":
			m = /^Renamed (\S+) ([\s\S]*) → ([\s\S]*)$/.exec(text);
			return m
				? { kind: "rename", ea: m[1] ?? "", old: m[2] ?? "", name: m[3] ?? "" }
				: null;
		case "comment":
			m = /^Set (?:repeatable )?comment at (\S+)$/.exec(text);
			return m ? { kind: "comment", ea: m[1] ?? "" } : null;
		case "set_type":
			m = /^Typed (\S+) as ([\s\S]+)$/.exec(text);
			return m ? { kind: "set_type", ea: m[1] ?? "", type: m[2] ?? "" } : null;
		case "make_function":
			m = /^Created function ([\s\S]+) at (\S+)$/.exec(text);
			return m
				? { kind: "make_function", name: m[1] ?? "", ea: m[2] ?? "" }
				: null;
		default:
			return null;
	}
}

/** Python 异常类型：错误末行 `NameError: …` → `NameError`。 */
function exceptionType(error: string): string | null {
	const lines = error.trimEnd().split("\n");
	const last = lines[lines.length - 1]?.trim() ?? "";
	return /^([A-Za-z_][\w.]*)(?::|$)/.exec(last)?.[1] ?? null;
}

/** 可选参数的一行：缺省不显示，类型不对显示 InvalidArg。 */
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

function Summary({ args, result }: ToolRenderProps): ReactNode {
	const action = str(args.action);
	if (action === null) return <InvalidArg what="action" />;
	const db = str(args.db) ?? str(detailsRecord(result)?.db);
	const target = str(args.target);
	const name = str(args.name);
	const text = str(args.text);
	const decl = str(args.decl);
	const code = str(args.code);
	const lifecycle =
		action === "open" || action === "save" || action === "close";
	return (
		<>
			<Badge>{action}</Badge> {lifecycle && db && <PathText path={db} />}
			{action === "exec" && code && (
				<span>
					{truncate(
						normalizeWs(code.split("\n").find((l) => l.trim() !== "") ?? ""),
						72,
					)}
				</span>
			)}
			{target && <span className="tv-pattern">{truncate(target, 48)}</span>}
			{action === "rename" && name && (
				<span className="tv-muted"> → {truncate(name, 48)}</span>
			)}
			{action === "comment" && text && (
				<span className="tv-muted"> {truncate(normalizeWs(text), 60)}</span>
			)}
			{action === "set_type" && decl && (
				<span className="tv-muted"> {truncate(normalizeWs(decl), 60)}</span>
			)}
			{!lifecycle && action !== "list" && db && (
				<>
					{" "}
					<PathText path={db} />
				</>
			)}
		</>
	);
}

function Meta({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const parsed = parseResult(str(args.action), result);
	if (!parsed) return null;
	switch (parsed.kind) {
		case "exec": {
			const { output, error } = parsed.exec;
			const type = error ? exceptionType(error) : null;
			return (
				<>
					{type && <span className="tv-meta-err">{type}</span>}
					{output && (
						<MetaCount unit="lines" count={output.split("\n").length} />
					)}
				</>
			);
		}
		case "list":
			return (
				<span>{t("toolRender.ida.dbs", { count: parsed.rows.length })}</span>
			);
		case "open":
			return (
				<span>
					{parsed.arch} {parsed.bitness}-bit
				</span>
			);
		case "close":
			return parsed.saved ? (
				<span className="tv-meta-ok">{t("toolRender.ida.saved")}</span>
			) : (
				<span className="tv-meta-warn">{t("toolRender.ida.discarded")}</span>
			);
		case "rename":
		case "comment":
		case "set_type":
		case "make_function":
			return <span>{parsed.ea}</span>;
		default:
			return null;
	}
}

/** IDA 架构名 → 反汇编的高亮语言；认不出的架构不高亮。 */
function asmLang(arch: string | null): string | null {
	if (!arch) return null;
	if (/^(?:x86|x64|amd64|i[3-6]86|metapc)/i.test(arch)) return "x86asm";
	if (/^(?:arm|aarch64)/i.test(arch)) return "armasm";
	return null;
}

/**
 * exec 输出的高亮语言：代码打印反编译伪代码时按 C；打印反汇编时按已知架构的汇编（架构不明不高亮）；
 * 认不出或两者都有时不高亮。
 */
function execOutputLang(
	code: string | null,
	arch: string | null,
): string | null {
	if (!code) return null;
	const decompiles = /\b(?:decompile|pseudocode)\s*\(/.test(code);
	const disasm =
		/\b(?:GetDisasm|generate_disasm_line|disasm\w*|asm)\s*\(/i.test(code);
	if (decompiles === disasm) return null;
	return decompiles ? "c" : asmLang(arch);
}

function ExecResult({
	exec,
	lang,
}: {
	exec: ExecParts;
	lang: string | null;
}): ReactNode {
	const { t } = useTranslation();
	const empty = !exec.output && exec.value === null && exec.error === null;
	return (
		<>
			{exec.output && (
				<Output
					text={exec.output}
					title={t("toolRender.ida.output")}
					lang={lang}
					maxLines={14}
					variant="code"
				/>
			)}
			{exec.value !== null && (
				<Output
					text={exec.value}
					title={t("toolRender.ida.value")}
					lang="python"
					variant="code"
					maxLines={10}
				/>
			)}
			{exec.error !== null && (
				<Output
					text={exec.error}
					title={t("toolRender.ida.error")}
					error
					variant="code"
					maxLines={14}
				/>
			)}
			{empty && (
				<span className="tv-faint">{t("toolRender.ida.noOutput")}</span>
			)}
			{exec.artifact && (
				<KvGrid>
					<Kv k={t("toolRender.ida.artifact")}>artifact://{exec.artifact}</Kv>
				</KvGrid>
			)}
		</>
	);
}

function ListResult({ rows }: { rows: ListRow[] }): ReactNode {
	const { t } = useTranslation();
	if (rows.length === 0) return null;
	const shown = rows.slice(0, MAX_ROWS);
	return (
		<div className="tv-list">
			{shown.map((row) => (
				<Row key={row.id} k={row.id}>
					<PathText path={row.path} />{" "}
					<Badges
						items={[
							row.opening && t("toolRender.ida.opening"),
							row.module,
							row.format,
							row.arch,
							row.running &&
								t("toolRender.ida.execRunning", { method: row.running }),
						]}
					/>
				</Row>
			))}
			{rows.length > shown.length && (
				<Row>
					<span className="tv-faint">… {rows.length - shown.length}</span>
				</Row>
			)}
		</div>
	);
}

/** 结果里参数之外的新信息（地址已在头部读数里，不重复）。 */
function ParsedResult({
	parsed,
	execLang,
}: {
	parsed: Parsed;
	execLang: string | null;
}): ReactNode {
	const { t } = useTranslation();
	switch (parsed.kind) {
		case "exec":
			return <ExecResult exec={parsed.exec} lang={execLang} />;
		case "list":
			return <ListResult rows={parsed.rows} />;
		case "open":
			return (
				<KvGrid>
					<Kv k={t("toolRender.ida.dbId")}>{parsed.id}</Kv>
					<Kv k={t("toolRender.ida.module")}>{parsed.module}</Kv>
					<Kv k={t("toolRender.ida.format")}>{parsed.format}</Kv>
					<Kv k={t("toolRender.ida.idb")}>
						<PathText path={parsed.idb} />
					</Kv>
				</KvGrid>
			);
		case "save":
			return (
				<KvGrid>
					<Kv k={t("toolRender.ida.idb")}>
						<PathText path={parsed.idb} />
					</Kv>
				</KvGrid>
			);
		case "rename":
			return (
				<KvGrid>
					<Kv k={t("toolRender.ida.oldName")}>{parsed.old}</Kv>
				</KvGrid>
			);
		case "set_type":
			return (
				<KvGrid>
					<Kv k={t("toolRender.ida.appliedType")}>{parsed.type}</Kv>
				</KvGrid>
			);
		case "make_function":
			return (
				<KvGrid>
					<Kv k={t("toolRender.ida.funcName")}>{parsed.name}</Kv>
				</KvGrid>
			);
		default:
			return null;
	}
}

/** 参数网格里会出现的可选参数；都缺省时不渲染空网格。 */
const GRID_ARGS = ["db", "target", "name", "repeatable", "save", "timeout"];

function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const action = str(args.action);
	const db = str(args.db);
	const target = str(args.target);
	const name = str(args.name);
	const text = str(args.text);
	const decl = str(args.decl);
	const code = str(args.code);
	const timeout = num(args.timeout);
	const badAction = args.action !== undefined && action === null;
	const badText = args.text !== undefined && text === null;
	const badDecl = args.decl !== undefined && decl === null;
	const badCode = args.code !== undefined && code === null;
	const hasGrid =
		badAction ||
		badText ||
		badDecl ||
		badCode ||
		GRID_ARGS.some((k) => args[k] !== undefined);
	return (
		<>
			{/* 动作已在头部；db / 目标 / 新名字 / 声明在头部只是截断的摘要，这里给全文 */}
			{hasGrid && (
				<KvGrid>
					{badAction && (
						<Kv k="action">
							<InvalidArg what="action" />
						</Kv>
					)}
					<ArgKv k="db" raw={args.db} val={db && <PathText path={db} />} />
					<ArgKv k="target" raw={args.target} val={target} />
					<ArgKv k="name" raw={args.name} val={name} />
					<ArgKv
						k="repeatable"
						raw={args.repeatable}
						val={
							typeof args.repeatable === "boolean"
								? String(args.repeatable)
								: null
						}
					/>
					<ArgKv
						k="save"
						raw={args.save}
						val={typeof args.save === "boolean" ? String(args.save) : null}
					/>
					<ArgKv
						k="timeout"
						raw={args.timeout}
						val={timeout !== null && `${timeout}s`}
					/>
					{badText && (
						<Kv k="text">
							<InvalidArg what="text" />
						</Kv>
					)}
					{badDecl && (
						<Kv k="decl">
							<InvalidArg what="decl" />
						</Kv>
					)}
					{badCode && (
						<Kv k="code">
							<InvalidArg what="code" />
						</Kv>
					)}
				</KvGrid>
			)}
			{text && <Output text={text} title="text" maxLines={8} />}
			{decl && <CodeBlock code={decl} lang="c" title="decl" maxLines={8} />}
			{code && (
				<CodeBlock code={code} lang="python" title={t("toolRender.ida.code")} />
			)}
		</>
	);
}

function Result({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const action = str(args.action);
	const db = str(args.db);
	const details = detailsRecord(result);
	const resolvedId = str(details?.db);
	// Core 目前的 exec 结果不带架构；参数或 details 里有时才据此选汇编高亮
	const arch = str(args.arch) ?? str(details?.arch);
	const parsed = parseResult(action, result);
	// open 的结果自带库 id，避免再写一遍；与参数里的 db 相同时也不重复
	const showId =
		resolvedId !== null && resolvedId !== db && parsed?.kind !== "open";
	return (
		<>
			{showId && (
				<KvGrid>
					<Kv k={t("toolRender.ida.dbId")}>{resolvedId}</Kv>
				</KvGrid>
			)}
			{parsed ? (
				<ParsedResult
					parsed={parsed}
					execLang={execOutputLang(str(args.code), arch)}
				/>
			) : (
				<ResultText result={result} maxLines={12} />
			)}
		</>
	);
}

export const idaRenderer: ToolRenderer = { Summary, Meta, Input, Result };
