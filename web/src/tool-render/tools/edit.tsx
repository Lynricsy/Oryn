/**
 * `edit` / `apply_patch` — 改文件。输入段是补丁本身，按文件拆成文件头条 + 操作行（`PUT 3.=3:`、`@@`）
 * 与新增 / 删除的正文；输出段逐文件给出真实改动：文件头条（首个改动行、新建 / 删除 / 移动、该文件的
 * +N −M）下接带行号栏的 diff，诊断按文件分组。头部读数是全部改动的 +N −M（多文件时附文件数）。
 * 结果形状见 oryn-core `tui/src/tools/edit.ts`（`EditToolDetails` / `perFileResults`）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	Badge,
	CodeLines,
	type CodeRow,
	DiagnosticList,
	DiffBlock,
	diffStats,
	ErrorBlock,
	FileHead,
	FootNotes,
	InvalidArg,
	MetaDiff,
	Output,
	PathText,
	parseDiagnostics,
} from "../parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "../types";
import {
	detailsRecord,
	isRecord,
	languageFromPath,
	normalizeWs,
	num,
	resultTextOf,
	str,
	truncate,
} from "../util";

/** Path from a hashline `[path#TAG]` / `[path]` header line, or null. */
function headerPath(line: string): string | null {
	const trimmed = line.trimEnd();
	if (!trimmed.startsWith("[") || !trimmed.endsWith("]")) return null;
	let body = trimmed.slice(1, -1).trim();
	const hash = /#[0-9a-fA-F]{4}$/.exec(body);
	if (hash) body = body.slice(0, hash.index);
	if (body.length >= 2) {
		const first = body[0];
		if ((first === '"' || first === "'") && first === body[body.length - 1])
			body = body.slice(1, -1);
	}
	return body.length > 0 && !/\s/.test(body) ? body : null;
}

const APPLY_PATCH_HEADER_RE = /^\*{3} (Update|Add|Delete) File:\s*(.+)$/;
/** hashline 操作行：`PUT 3.=3:`、`CUT 5*`、`REM`、`MV dest`（旧版 `replace` / `insert` / `delete`）。 */
const OP_LINE_RE = /^(?:PUT|CUT|REM|MV|replace|insert|delete)\b/;

/** 补丁里的一个文件：路径、apply_patch 的段类型，以及它的操作与正文行。 */
interface PatchFile {
	path: string;
	op: "create" | "delete" | null;
	rows: CodeRow[];
	ops: number;
}

/** hashline / apply_patch 补丁按文件头拆开；操作行与 `@@` 当作分段头，正文行按增删着色。 */
function parsePatch(input: string): PatchFile[] {
	const stripped = input.startsWith("\uFEFF") ? input.slice(1) : input;
	const files: PatchFile[] = [];
	let current: PatchFile | null = null;
	const open = (path: string, op: PatchFile["op"] = null) => {
		current = { path, op, rows: [], ops: 0 };
		files.push(current);
		return current;
	};
	for (const raw of stripped.replace(/\n+$/, "").split("\n")) {
		const line = raw.replace(/\r$/, "");
		if (/^\*{3} (?:Begin|End) Patch/.test(line.trim())) continue;
		const fromHashline = headerPath(line);
		if (fromHashline) {
			open(fromHashline);
			continue;
		}
		const applyPatch = APPLY_PATCH_HEADER_RE.exec(line.trim());
		if (applyPatch) {
			const kind = applyPatch[1];
			open(
				(applyPatch[2] ?? "").trim(),
				kind === "Add" ? "create" : kind === "Delete" ? "delete" : null,
			);
			continue;
		}
		const file: PatchFile = current ?? open("");
		if (OP_LINE_RE.test(line) || line.startsWith("@@")) {
			file.rows.push({ kind: "hunk", text: line });
			if (!line.startsWith("@@")) file.ops++;
		} else if (line.startsWith("+"))
			file.rows.push({ kind: "add", text: line.slice(1) });
		else if (line.startsWith("-"))
			file.rows.push({ kind: "del", text: line.slice(1) });
		else
			file.rows.push({
				kind: "ctx",
				text: line.startsWith(" ") ? line.slice(1) : line,
			});
	}
	return files;
}

/** One file's outcome — top-level `details` and `perFileResults[i]` share this shape. */
interface FileEntry {
	path: string | null;
	diff: string | null;
	firstChangedLine: number | null;
	op: string | null;
	move: string | null;
	isError: boolean;
	errorText: string | null;
	diagText: string;
}

function fileEntry(d: Record<string, unknown>): FileEntry {
	const diag = isRecord(d.diagnostics) ? d.diagnostics : null;
	const messages =
		diag && Array.isArray(diag.messages)
			? diag.messages.filter((m): m is string => typeof m === "string")
			: [];
	return {
		path: str(d.path),
		diff: str(d.diff),
		firstChangedLine: num(d.firstChangedLine),
		op: str(d.op),
		move: str(d.move),
		isError: d.isError === true,
		errorText: str(d.displayErrorText) ?? str(d.errorText),
		diagText: messages.join("\n"),
	};
}

/** 本次调用涉及的文件：补丁段头（hashline / apply_patch）→ path 参数 → edits[].path，去重保序。 */
function editPaths(args: Record<string, unknown>): string[] {
	const input = str(args.input) ?? str(args._input);
	const paths = input
		? parsePatch(input)
				.map((f) => f.path)
				.filter(Boolean)
		: [];
	const argPath = str(args.file_path) ?? str(args.path);
	if (paths.length === 0 && argPath) paths.push(argPath);
	if (paths.length === 0 && Array.isArray(args.edits)) {
		for (const e of args.edits) {
			const p = isRecord(e) ? str(e.path) : null;
			if (p) paths.push(p);
		}
	}
	return [...new Set(paths)];
}

/**
 * 逐文件的结局：perFileResults，或顶层 details 的单文件（路径缺省时取参数里的路径）。
 * 结果里的路径是绝对路径；能对上补丁里的相对路径时按补丁里的写法显示。
 */
function editOutcome(
	args: Record<string, unknown>,
	result: ToolResultLike | undefined,
): FileEntry[] {
	const details = detailsRecord(result);
	if (!details) return [];
	const named = editPaths(args);
	const relative = (p: string | null) =>
		p && (named.find((n) => p === n || p.endsWith(`/${n}`)) ?? p);
	if (Array.isArray(details.perFileResults)) {
		const entries = details.perFileResults.filter(isRecord).map(fileEntry);
		if (entries.length > 0)
			return entries.map((e) => ({ ...e, path: relative(e.path) }));
	}
	if (result?.isError === true) return [];
	const top = fileEntry(details);
	if (!top.diff && !top.diagText && top.move === null && top.op !== "delete")
		return [];
	return [{ ...top, path: relative(top.path) ?? named[0] ?? null }];
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const paths = editPaths(args);
	const input = str(args.input) ?? str(args._input);
	if (paths.length === 0)
		return (
			<span>
				{input ? truncate(normalizeWs(input.split("\n", 1)[0] ?? ""), 80) : "…"}
			</span>
		);
	return (
		<>
			<PathText path={paths[0] ?? ""} />
			{paths.length > 1 && (
				<span className="tv-faint">+{paths.length - 1}</span>
			)}
		</>
	);
}

/** 头部读数：全部改动的 +N −M；多个文件时附文件数。 */
function Meta({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result || result.isError) return null;
	const entries = editOutcome(args, result);
	let added = 0;
	let removed = 0;
	for (const e of entries) {
		if (!e.diff || e.isError) continue;
		const s = diffStats(e.diff);
		added += s.added;
		removed += s.removed;
	}
	return (
		<>
			<MetaDiff added={added} removed={removed} />
			{entries.length > 1 && (
				<span>{t("toolRender.meta.files", { count: entries.length })}</span>
			)}
		</>
	);
}

/** 文件的操作标签：新建 / 删除 / 移动到 … */
function OpLabel({ op, move }: { op: string | null; move?: string | null }) {
	const { t } = useTranslation();
	return (
		<>
			{op === "create" && (
				<Badge tone="ok">{t("toolRender.edit.create")}</Badge>
			)}
			{op === "delete" && (
				<Badge tone="err">{t("toolRender.edit.delete")}</Badge>
			)}
			{move && (
				<span className="tv-edit-move">
					{t("toolRender.edit.moveTo")} <PathText path={move} />
				</span>
			)}
		</>
	);
}

/** 替换模式的 `old_string` / `new_string` 拼成一段 diff：旧文本整段删、新文本整段加。 */
function replaceRows(oldText: string, newText: string): CodeRow[] {
	const rows: CodeRow[] = [];
	if (oldText)
		for (const l of oldText.split("\n")) rows.push({ kind: "del", text: l });
	if (newText)
		for (const l of newText.split("\n")) rows.push({ kind: "add", text: l });
	return rows;
}

/** 替换模式（顶层或批量项）：文件头条 + 旧 / 新文本。 */
function ReplaceBody({
	edit,
	path,
}: {
	edit: Record<string, unknown>;
	path: string | null;
}): ReactNode {
	const { t } = useTranslation();
	const oldText = str(edit.old_string);
	const newText = str(edit.new_string);
	if (oldText === null && newText === null) return null;
	return (
		<div className="tv-fgroup">
			{path && (
				<FileHead path={path}>
					{edit.replace_all === true && (
						<span>{t("toolRender.edit.replaceAll")}</span>
					)}
				</FileHead>
			)}
			<CodeLines
				rows={replaceRows(oldText ?? "", newText ?? "")}
				lang={path ? languageFromPath(path) : null}
				maxLines={20}
				signs
			/>
		</div>
	);
}

/** `edits[]` 的一项：patch 模式的 op / rename / diff，或替换批的旧 / 新文本。 */
function EditEntry({
	entry,
	path,
}: {
	entry: unknown;
	path: string | null;
}): ReactNode {
	if (!isRecord(entry)) return <InvalidArg what="edit" />;
	const op = str(entry.op);
	const filePath = str(entry.path) ?? path;
	const rename = str(entry.rename);
	const lang =
		(filePath ? languageFromPath(filePath) : null) ??
		(rename ? languageFromPath(rename) : null);
	const diff = str(entry.diff);
	if (str(entry.old_string) !== null || str(entry.new_string) !== null)
		return <ReplaceBody edit={entry} path={filePath} />;
	return (
		<div className="tv-fgroup">
			{filePath && (
				<FileHead path={filePath}>
					<OpLabel op={op} move={rename} />
				</FileHead>
			)}
			{/* create 的 diff 就是新文件全文 */}
			{diff &&
				(op === "create" ? (
					<CodeLines
						rows={diff
							.split("\n")
							.map((text, i) => ({ kind: "add", no: i + 1, text }))}
						lang={lang}
						maxLines={16}
					/>
				) : (
					<DiffBlock diff={diff} lang={lang} maxLines={20} />
				))}
		</div>
	);
}

/** 输入段：补丁按文件拆开（文件头条右侧是操作数），替换模式给旧 / 新文本，批量模式逐项列出。 */
function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const input = str(args.input) ?? str(args._input);
	const argPath = str(args.file_path) ?? str(args.path);
	const files = input ? parsePatch(input) : [];
	return (
		<>
			{files.map((f, i) => (
				<div key={`${f.path}:${i}`} className="tv-fgroup">
					{f.path && (
						<FileHead path={f.path}>
							<OpLabel op={f.op} />
							{f.ops > 0 && (
								<span>{t("toolRender.edit.ops", { count: f.ops })}</span>
							)}
						</FileHead>
					)}
					<CodeLines
						rows={f.rows}
						lang={languageFromPath(f.path)}
						maxLines={12}
						signs
					/>
				</div>
			))}
			{input === null &&
				(args.input !== undefined || args._input !== undefined) && (
					<InvalidArg what="input" />
				)}
			{Array.isArray(args.edits) &&
				args.edits.map((e, i) => (
					<EditEntry key={i} entry={e} path={argPath} />
				))}
			{!Array.isArray(args.edits) && <ReplaceBody edit={args} path={argPath} />}
		</>
	);
}

/** 输出段的一个文件：文件头条（首个改动行、操作、该文件的 +N −M）+ diff / 错误 + 诊断。 */
function FileSection({
	entry,
	many,
}: {
	entry: FileEntry;
	/** 多文件时每个文件头条带自己的 +N −M（单文件时它就是头部读数）。 */
	many: boolean;
}): ReactNode {
	const path = entry.path ?? entry.move;
	const stats =
		many && entry.diff && !entry.isError ? diffStats(entry.diff) : null;
	const { diags, notes } = parseDiagnostics(entry.diagText);
	return (
		<div className="tv-fgroup">
			{path && (
				<FileHead
					path={path}
					from={entry.isError ? null : entry.firstChangedLine}
				>
					<OpLabel
						op={entry.op}
						move={entry.move !== path ? entry.move : null}
					/>
					{stats && <MetaDiff added={stats.added} removed={stats.removed} />}
				</FileHead>
			)}
			{entry.isError
				? entry.errorText && <ErrorBlock text={entry.errorText} />
				: entry.diff && (
						<DiffBlock
							diff={entry.diff}
							lang={path ? languageFromPath(path) : null}
							maxLines={40}
						/>
					)}
			<DiagnosticList diags={diags} heads={false} />
			<FootNotes notes={notes} />
		</div>
	);
}

function Result({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result) return null;
	const details = detailsRecord(result);
	const errorKind = details ? str(details.errorKind) : null;
	const entries = editOutcome(args, result);
	// 第二个文本块是语法修复 / 不再能解析的提醒
	const extra = result.content
		.slice(1)
		.map((b) => (b.type === "text" && "text" in b ? b.text : null))
		.filter((s): s is string => typeof s === "string" && s.trim() !== "");
	if (entries.length === 0) {
		const text = resultTextOf(result).trim();
		if (result.isError)
			return (
				<>
					{errorKind && (
						<div className="tv-edit-kind">
							{t("toolRender.edit.errorKind")} <code>{errorKind}</code>
						</div>
					)}
					<ErrorBlock text={text} maxLines={15} />
				</>
			);
		return text ? <Output text={text} maxLines={10} variant="code" /> : null;
	}
	return (
		<>
			{entries.map((e, i) => (
				<FileSection
					key={`${e.path ?? ""}:${i}`}
					entry={e}
					many={entries.length > 1}
				/>
			))}
			{extra.length > 0 && (
				<FootNotes
					notes={extra.map((s) => s.split("\n")[0] ?? s)}
					tone="warn"
				/>
			)}
		</>
	);
}

export const editRenderer: ToolRenderer = { Summary, Meta, Input, Result };
