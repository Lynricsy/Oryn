/**
 * `write` — 新建或覆盖文件。输入段是要写入的文件本身（文件头条 + 带行号的内容），输出段是结局：
 * 新建 / 覆盖、写入的字节数、是否设为可执行，覆盖时附上与旧内容的 diff，语言服务器诊断按文件分组。
 * 头部读数：新建时是新增行数，覆盖时是 +N −M。结果形状见 oryn-core `tui/src/tools/write.ts`。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/ui/icons";
import { formatBytes } from "../../lib/format";
import {
	CodeLines,
	type CodeRow,
	DiagnosticList,
	DiffBlock,
	diffStats,
	EmptyNote,
	ErrorBlock,
	FileHead,
	FootNotes,
	InvalidArg,
	MetaDiff,
	Note,
	PathText,
	parseDiagnostics,
	SeverityCounts,
} from "../parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "../types";
import {
	detailsRecord,
	isRecord,
	languageFromPath,
	num,
	resultTextOf,
	str,
} from "../util";

function pathOf(args: Record<string, unknown>): string | null {
	return str(args.file_path) ?? str(args.path);
}

/** 写入结果里卡片用得上的字段。 */
interface WriteOutcome {
	overwritten: boolean;
	madeExecutable: boolean;
	diff: string | null;
	firstChangedLine: number | null;
	formatter: string | null;
	diagText: string;
	bytes: number | null;
	/** 结果文本里除了「已写入」、hashline 头与诊断尾巴之外的说明。 */
	notes: string[];
}

const WROTE_RE = /^Successfully wrote (\d+) bytes? to /;
const HASHLINE_HEAD_RE = /^\[[^\s\]]+\]$/;

function outcomeOf(result: ToolResultLike | undefined): WriteOutcome {
	const d = detailsRecord(result);
	const diag = d && isRecord(d.diagnostics) ? d.diagnostics : null;
	const messages =
		diag && Array.isArray(diag.messages)
			? diag.messages.filter((m): m is string => typeof m === "string")
			: [];
	let bytes: number | null = null;
	const notes: string[] = [];
	// 诊断已在 details 里，文本末尾的 `LSP Diagnostics (…):` 尾巴不再重复
	const text = resultTextOf(result).split(/\n\nLSP Diagnostics \(/)[0] ?? "";
	for (const raw of text.split("\n")) {
		const line = raw.trim();
		if (!line || HASHLINE_HEAD_RE.test(line)) continue;
		const wrote = WROTE_RE.exec(line);
		if (wrote) {
			bytes = Number(wrote[1]);
			continue;
		}
		if (/^\[Notice: Made executable/.test(line)) continue;
		notes.push(line.replace(/^\[(.*)\]$/, "$1"));
	}
	return {
		overwritten: d?.overwritten === true,
		madeExecutable: d?.madeExecutable === true,
		diff: d ? str(d.diff) : null,
		firstChangedLine: d ? num(d.firstChangedLine) : null,
		formatter: diag ? str(diag.formatter) : null,
		diagText: messages.join("\n"),
		bytes,
		notes,
	};
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const path = pathOf(args);
	return path === null ? <InvalidArg what="path" /> : <PathText path={path} />;
}

/** 头部读数：覆盖时 +N −M，新建时 +N 行（结果出来前是要写入的行数），诊断有错误时附错误数。 */
function Meta({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const content = str(args.content);
	const lines = content ? content.replace(/\n$/, "").split("\n").length : 0;
	if (!result)
		return lines > 0 ? (
			<span>{t("toolRender.meta.lines", { count: lines })}</span>
		) : null;
	if (result.isError) return null;
	const out = outcomeOf(result);
	const { diags } = parseDiagnostics(out.diagText);
	const stats = out.diff ? diffStats(out.diff) : null;
	return (
		<>
			{stats ? (
				<MetaDiff added={stats.added} removed={stats.removed} />
			) : (
				lines > 0 &&
				!out.overwritten && <span className="tv-meta-add">+{lines}</span>
			)}
			{diags.some((d) => d.severity === "error") && (
				<SeverityCounts diags={diags.filter((d) => d.severity === "error")} />
			)}
		</>
	);
}

/** 输入段：要写入的文件（文件头条右侧是语言、行数、大小）与带行号的内容。 */
function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const path = pathOf(args);
	const content = str(args.content);
	const lang = path ? languageFromPath(path) : null;
	const rows: CodeRow[] = content
		? content
				.replace(/\n$/, "")
				.split("\n")
				.map((text, i) => ({ kind: "ctx", no: i + 1, text }))
		: [];
	const aside: string[] = [];
	if (lang) aside.push(lang);
	if (content) {
		aside.push(t("toolRender.meta.lines", { count: rows.length }));
		aside.push(formatBytes(new TextEncoder().encode(content).length));
	}
	return (
		<>
			{path !== null && (
				<FileHead path={path}>
					{aside.length > 0 && <span>{aside.join(" · ")}</span>}
				</FileHead>
			)}
			{content === null ? (
				args.content !== undefined && (
					<Note tone="err">
						<InvalidArg what="content" />
					</Note>
				)
			) : content === "" ? (
				<EmptyNote>{t("toolRender.write.emptyContent")}</EmptyNote>
			) : (
				<CodeLines rows={rows} lang={lang} maxLines={16} />
			)}
		</>
	);
}

/** 输出段：结局一行（新建 / 覆盖、字节数、可执行、格式化），覆盖的 diff，诊断，其余说明。 */
function Result({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result) return null;
	if (result.isError) return <ErrorBlock text={resultTextOf(result)} />;
	const path = pathOf(args);
	const out = outcomeOf(result);
	const { diags, notes: diagNotes } = parseDiagnostics(out.diagText);
	const facts: string[] = [];
	if (out.bytes !== null) facts.push(formatBytes(out.bytes));
	if (out.madeExecutable) facts.push(t("toolRender.write.executable"));
	if (out.formatter === "formatted")
		facts.push(t("toolRender.write.formatted"));
	return (
		<>
			<div className={`tv-wstat${out.overwritten ? "" : " tv-wstat--new"}`}>
				<Icon name={out.overwritten ? "edit" : "filePlus"} size={13} />
				<span className="tv-wstat-label">
					{out.overwritten
						? t("toolRender.write.overwritten")
						: t("toolRender.write.created")}
				</span>
				{facts.length > 0 && (
					<span className="tv-wstat-facts">{facts.join(" · ")}</span>
				)}
			</div>
			{out.diff && (
				<DiffBlock
					diff={out.diff}
					lang={path ? languageFromPath(path) : null}
					maxLines={40}
				/>
			)}
			<DiagnosticList diags={diags} heads={false} />
			<FootNotes notes={[...diagNotes, ...out.notes]} />
		</>
	);
}

export const writeRenderer: ToolRenderer = { Summary, Meta, Input, Result };
