/** `edit` / `apply_patch` — hashline patch application rendered as colored diffs. */
import type { ReactNode } from "react";
import {
	Badge,
	Badges,
	CodeBlock,
	DiffBlock,
	diffStats,
	InvalidArg,
	Kv,
	KvGrid,
	MetaDiff,
	Note,
	Output,
	PathText,
	ResultText,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	isRecord,
	languageFromPath,
	normalizeWs,
	num,
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
	return body.length > 0 ? body : null;
}

const APPLY_PATCH_HEADER_RE = /^\*{3} (?:Update|Add|Delete) File:\s*(.+)$/;

/** File paths named by hashline or apply_patch section headers, in order. */
function inputPaths(input: string): string[] {
	const stripped = input.startsWith("\uFEFF") ? input.slice(1) : input;
	const paths: string[] = [];
	for (const rawLine of stripped.split("\n")) {
		const line = rawLine.replace(/\r$/, "");
		const fromHashline = headerPath(line);
		if (fromHashline) {
			paths.push(fromHashline);
			continue;
		}
		const fromApplyPatch = APPLY_PATCH_HEADER_RE.exec(line.trim());
		if (fromApplyPatch) paths.push((fromApplyPatch[1] ?? "").trim());
	}
	return paths;
}

const OP_HEADER_RE = /^(?:replace|insert|delete)\b/;

function countOps(input: string): number {
	let count = 0;
	for (const line of input.split("\n")) if (OP_HEADER_RE.test(line)) count++;
	return count;
}

interface DiagnosticsEntry {
	summary: string | null;
	messages: string[];
	errored: boolean;
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
	diagnostics: DiagnosticsEntry | null;
}

function fileEntry(d: Record<string, unknown>): FileEntry {
	const diag = isRecord(d.diagnostics) ? d.diagnostics : null;
	const messages: string[] = [];
	if (diag && Array.isArray(diag.messages)) {
		for (const m of diag.messages) if (typeof m === "string") messages.push(m);
	}
	return {
		path: str(d.path),
		diff: str(d.diff),
		firstChangedLine: num(d.firstChangedLine),
		op: str(d.op),
		move: str(d.move),
		isError: d.isError === true,
		errorText: str(d.displayErrorText) ?? str(d.errorText),
		diagnostics: diag
			? { summary: str(diag.summary), messages, errored: diag.errored === true }
			: null,
	};
}

/** 本次调用涉及的文件：补丁段头（hashline / apply_patch）→ path 参数 → edits[].path，去重保序。 */
function editPaths(
	args: Record<string, unknown>,
	input: string | null,
): string[] {
	const paths = input ? inputPaths(input) : [];
	const argPath = str(args.file_path) ?? str(args.path);
	if (paths.length === 0 && argPath) paths.push(argPath);
	if (paths.length === 0 && Array.isArray(args.edits)) {
		for (const e of args.edits) {
			const p = isRecord(e) ? str(e.path) : null;
			if (p && !paths.includes(p)) paths.push(p);
		}
	}
	// 同一文件的多个段只算一次
	return [...new Set(paths)];
}

function Summary({ args, result }: ToolRenderProps): ReactNode {
	const input = str(args.input) ?? str(args._input);
	const paths = editPaths(args, input);
	const opCount = input ? countOps(input) : 0;
	const firstLine = input
		? truncate(normalizeWs(input.split("\n", 1)[0] ?? ""), 80)
		: "";
	return (
		<>
			{paths.length > 0 ? (
				<PathText path={paths[0] ?? ""} />
			) : (
				<span>{firstLine}</span>
			)}
			{paths.length > 1 && (
				<>
					{" "}
					<Badge>+{paths.length - 1} more</Badge>
				</>
			)}
			{opCount > 0 && (
				<>
					{" "}
					<Badge>
						{opCount} op{opCount === 1 ? "" : "s"}
					</Badge>
				</>
			)}
			{result?.isError === true && (
				<>
					{" "}
					<Badge tone="err">failed</Badge>
				</>
			)}
		</>
	);
}

function FileSection({
	entry,
	fallbackPath,
}: {
	entry: FileEntry;
	fallbackPath?: string | null;
}): ReactNode {
	const path = entry.path ?? fallbackPath ?? null;
	const op = entry.op === "create" || entry.op === "delete" ? entry.op : null;
	const diag = entry.diagnostics;
	return (
		<div>
			{(path !== null || op !== null || entry.move !== null) && (
				<div className="tv-row">
					<span className="tv-row-val">
						{path !== null && (
							<PathText
								path={path}
								from={entry.isError ? null : entry.firstChangedLine}
							/>
						)}
						{entry.move !== null && (
							<>
								{" → "}
								<PathText path={entry.move} />
							</>
						)}
						{op !== null && (
							<>
								{" "}
								<Badge tone={op === "delete" ? "err" : "ok"}>{op}</Badge>
							</>
						)}
						{entry.isError && (
							<>
								{" "}
								<Badge tone="err">failed</Badge>
							</>
						)}
					</span>
				</div>
			)}
			{entry.isError
				? entry.errorText !== null && (
						<Output text={entry.errorText} error maxLines={10} />
					)
				: entry.diff !== null &&
					entry.diff.length > 0 && (
						<DiffBlock
							diff={entry.diff}
							maxLines={40}
							lang={path !== null ? languageFromPath(path) : null}
						/>
					)}
			{diag?.summary && (
				<Note tone={diag.errored ? "err" : "warn"}>{diag.summary}</Note>
			)}
			{diag !== null && diag.messages.length > 0 && (
				<Output text={diag.messages.join("\n")} maxLines={6} />
			)}
		</div>
	);
}

/** 输出段的形态：逐文件小节（perFileResults 或顶层 details 单文件），否则退回结果文本。 */
interface Outcome {
	sections: { entry: FileEntry; fallbackPath: string | null }[];
	/** 没有小节时结果文本的折叠行数。 */
	textLines: number;
}

function editOutcome(
	args: Record<string, unknown>,
	result: ToolRenderProps["result"],
): Outcome {
	const details = detailsRecord(result);
	if (details && Array.isArray(details.perFileResults)) {
		const sections: Outcome["sections"] = [];
		for (const f of details.perFileResults)
			if (isRecord(f))
				sections.push({ entry: fileEntry(f), fallbackPath: null });
		if (sections.length > 0) return { sections, textLines: 8 };
	}
	// Failed matches embed numbered file context — keep a generous window.
	if (result?.isError === true) return { sections: [], textLines: 15 };
	if (details) {
		const top = fileEntry(details);
		if (top.diff !== null || top.diagnostics !== null || top.move !== null) {
			const input = str(args.input) ?? str(args._input);
			const fallbackPath =
				str(args.file_path) ??
				str(args.path) ??
				(input ? (inputPaths(input)[0] ?? null) : null);
			return { sections: [{ entry: top, fallbackPath }], textLines: 8 };
		}
	}
	return { sections: [], textLines: 8 };
}

/** 替换模式的 `old_string` / `new_string` 拼成一段 diff：旧文本整段删、新文本整段加。 */
function replaceDiff(oldText: string, newText: string): string {
	const rows: string[] = [];
	if (oldText) for (const l of oldText.split("\n")) rows.push(`-${l}`);
	if (newText) for (const l of newText.split("\n")) rows.push(`+${l}`);
	return rows.join("\n");
}

/** 替换模式（顶层或批量项）的旧 / 新文本；两者都不是字符串时不渲染。 */
function ReplaceBody({
	edit,
	lang,
}: {
	edit: Record<string, unknown>;
	lang: string | null;
}): ReactNode {
	const oldText = str(edit.old_string);
	const newText = str(edit.new_string);
	if (oldText === null && newText === null) return null;
	return (
		<>
			<Badges items={[edit.replace_all === true && "replace all"]} />
			<DiffBlock
				diff={replaceDiff(oldText ?? "", newText ?? "")}
				maxLines={20}
				lang={lang}
			/>
		</>
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
	if (!isRecord(entry))
		return (
			<KvGrid>
				<Kv k="edit">
					<InvalidArg what="edit" />
				</Kv>
			</KvGrid>
		);
	const op = str(entry.op);
	const target =
		str(entry.sel) ?? str(entry.path) ?? str(entry.rename) ?? str(entry.move);
	const filePath = str(entry.path) ?? path;
	const lang =
		(filePath ? languageFromPath(filePath) : null) ??
		languageFromPath(str(entry.rename) ?? "");
	const diff = str(entry.diff);
	return (
		<div>
			{(op !== null || target !== null) && (
				<KvGrid>
					<Kv k={op ?? "edit"}>{target ?? "?"}</Kv>
				</KvGrid>
			)}
			{/* create 的 diff 就是新文件全文 */}
			{diff ? (
				op === "create" ? (
					<CodeBlock code={diff} lang={lang} maxLines={12} />
				) : (
					<DiffBlock diff={diff} maxLines={20} lang={lang} />
				)
			) : null}
			<ReplaceBody edit={entry} lang={lang} />
		</div>
	);
}

function Input({ args, result }: ToolRenderProps): ReactNode {
	const input = str(args.input) ?? str(args._input);
	const argPath = str(args.file_path) ?? str(args.path);
	// 折叠行只写第一个文件（且窄屏会被省略号截断），输入段按文件给出完整路径；
	// 输出段逐文件小节已带路径行的文件不再重复（结果里可能是绝对路径）
	const sectionPaths: string[] = [];
	for (const s of editOutcome(args, result).sections) {
		const p = s.entry.path ?? s.fallbackPath;
		if (p) sectionPaths.push(p);
	}
	const headPaths = editPaths(args, input).filter(
		(p) => !sectionPaths.some((s) => s === p || s.endsWith(`/${p}`)),
	);
	return (
		<>
			{headPaths.length > 0 && (
				<KvGrid>
					{headPaths.map((p, i) => (
						<Kv key={`${p}:${i}`} k="path">
							<PathText path={p} />
						</Kv>
					))}
				</KvGrid>
			)}
			{Array.isArray(args.edits) &&
				args.edits.map((e, i) => (
					<EditEntry key={`${i}`} entry={e} path={argPath} />
				))}
			{/* 替换模式：顶层的 old_string / new_string */}
			<ReplaceBody
				edit={args}
				lang={argPath ? languageFromPath(argPath) : null}
			/>
			{/* hashline / apply_patch 补丁：`+` 正文行、段头按 diff 语法着色 */}
			{input !== null && input.length > 0 && (
				<Output text={input} lang="diff" variant="code" maxLines={10} />
			)}
			{input === null &&
				(args.input !== undefined || args._input !== undefined) && (
					<InvalidArg what="input" />
				)}
		</>
	);
}

function Result({ args, result }: ToolRenderProps): ReactNode {
	const { sections, textLines } = editOutcome(args, result);
	if (sections.length === 0)
		return <ResultText result={result} maxLines={textLines} />;
	return sections.map((s, i) => (
		<FileSection
			key={`${s.entry.path ?? ""}:${i}`}
			entry={s.entry}
			fallbackPath={s.fallbackPath}
		/>
	));
}

/** 右侧读数：本次改动的增删行数。 */
function Meta({ result }: ToolRenderProps): ReactNode {
	const details = detailsRecord(result);
	const diff = details && result?.isError !== true ? str(details.diff) : null;
	if (!diff) return null;
	const { added, removed } = diffStats(diff);
	return <MetaDiff added={added} removed={removed} />;
}

export const editRenderer: ToolRenderer = { Summary, Meta, Input, Result };
