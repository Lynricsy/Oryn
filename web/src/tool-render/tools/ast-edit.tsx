/**
 * `ast_edit` — 结构化（AST）改写。它总是先出预览：文件尚未修改，等 `resolve` 应用或 `reject` 放弃。
 * 输入段把每条规则写成一小段 diff（`−` 匹配模式、`+` 替换），下附改写范围；输出段先标明预览状态，
 * 再逐文件列出将要发生的改动（文件头条右侧是该文件的替换数）。头部读数：替换数、文件数、预览 / 上限。
 * 结果形状见 oryn-core `tui/src/tools/ast-edit.ts`（`AstEditToolDetails`）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/ui/icons";
import {
	CodeLines,
	type CodeRow,
	DiffBlock,
	EmptyNote,
	ErrorBlock,
	type FileGroup,
	FootNotes,
	GroupedCode,
	InvalidArg,
	MetaCount,
	Output,
	PathText,
	parseDiff,
	parseGrouped,
} from "../parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "../types";
import {
	detailsRecord,
	isRecord,
	normalizeWs,
	num,
	resultTextOf,
	sharedLanguage,
	str,
	truncate,
} from "../util";

interface AstEditOp {
	pat: string;
	out: string;
}

interface AstEditDetails {
	totalReplacements: number | null;
	filesTouched: number | null;
	filesSearched: number | null;
	applied: boolean | null;
	limitReached: boolean;
	fileReplacements: Array<{ path: string; count: number | null }>;
	parseErrors: string[];
	parseErrorsTotal: number | null;
	displayContent: string | null;
}

function pathsOf(args: Record<string, unknown>): string[] {
	const raw = args.paths ?? args.path;
	if (typeof raw === "string") return [raw];
	return Array.isArray(raw)
		? raw.filter((p): p is string => typeof p === "string" && p !== "")
		: [];
}

function opsOf(args: Record<string, unknown>): AstEditOp[] {
	if (!Array.isArray(args.ops)) return [];
	return args.ops
		.filter(isRecord)
		.map((op) => ({ pat: str(op.pat) ?? "", out: str(op.out) ?? "" }));
}

function detailsOf(result: ToolResultLike | undefined): AstEditDetails | null {
	const d = detailsRecord(result);
	if (!d) return null;
	const fileReplacements: AstEditDetails["fileReplacements"] = [];
	if (Array.isArray(d.fileReplacements)) {
		for (const fr of d.fileReplacements) {
			if (!isRecord(fr)) continue;
			const path = str(fr.path);
			if (path) fileReplacements.push({ path, count: num(fr.count) });
		}
	}
	return {
		totalReplacements: num(d.totalReplacements),
		filesTouched: num(d.filesTouched),
		filesSearched: num(d.filesSearched),
		applied: typeof d.applied === "boolean" ? d.applied : null,
		limitReached: d.limitReached === true,
		fileReplacements,
		parseErrors: Array.isArray(d.parseErrors)
			? d.parseErrors.filter((e): e is string => typeof e === "string")
			: [],
		parseErrorsTotal: num(d.parseErrorsTotal),
		displayContent: str(d.displayContent),
	};
}

/** 预览结果是否还没应用（ast_edit 自己的结果总是预览；`applied: true` 只出现在 resolve 之后）。 */
function isStaged(details: AstEditDetails | null): boolean {
	return (
		details !== null &&
		details.applied !== true &&
		(details.totalReplacements ?? 0) > 0
	);
}

/** 改写目标的语言：参数路径共同的语言，否则被改文件共同的语言。 */
function editLanguage(
	args: Record<string, unknown>,
	details: AstEditDetails | null,
): string | null {
	return (
		str(args.lang) ??
		sharedLanguage(pathsOf(args)) ??
		(details
			? sharedLanguage(details.fileReplacements.map((f) => f.path))
			: null)
	);
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const ops = opsOf(args);
	const first = ops[0];
	const paths = pathsOf(args);
	return (
		<>
			{first ? (
				<span className="tv-pattern">
					{truncate(normalizeWs(first.pat), 40)}
					<span className="tv-faint"> → </span>
					{first.out ? truncate(normalizeWs(first.out), 40) : "∅"}
				</span>
			) : (
				<InvalidArg what="ops" />
			)}
			{ops.length > 1 && <span className="tv-faint">+{ops.length - 1}</span>}
			{paths[0] && <PathText path={paths[0]} />}
		</>
	);
}

/** 头部读数：替换数（0 处时提示）、文件数、预览中 / 已达上限。 */
function Meta({ result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result || result.isError) return null;
	const d = detailsOf(result);
	if (!d) return null;
	const total = d.totalReplacements;
	return (
		<>
			<MetaCount
				unit="replacements"
				count={total}
				tone={total === 0 ? "warn" : undefined}
			/>
			{(d.filesTouched ?? 0) > 1 && (
				<span>{t("toolRender.meta.files", { count: d.filesTouched })}</span>
			)}
			{d.limitReached && (
				<span className="tv-meta-warn">{t("toolRender.astEdit.limit")}</span>
			)}
			{isStaged(d) && <span>{t("toolRender.astEdit.preview")}</span>}
		</>
	);
}

/** 一条改写规则写成一小段 diff：`−` 匹配模式、`+` 替换；没有替换时说明是删除。 */
function OpRule({
	op,
	index,
	count,
	lang,
}: {
	op: AstEditOp;
	index: number;
	count: number;
	lang: string | null;
}): ReactNode {
	const { t } = useTranslation();
	const rows: CodeRow[] = [];
	for (const text of op.pat.split("\n")) rows.push({ kind: "del", text });
	if (op.out)
		for (const text of op.out.split("\n")) rows.push({ kind: "add", text });
	else rows.push({ kind: "note", text: t("toolRender.astEdit.deletion") });
	return (
		<div className="tv-fgroup">
			{count > 1 && (
				<div className="tv-out-title">
					{t("toolRender.astEdit.rule", { n: index + 1 })}
				</div>
			)}
			{op.pat ? (
				<CodeLines rows={rows} lang={lang} maxLines={12} signs />
			) : (
				<InvalidArg what="pat" />
			)}
		</div>
	);
}

function Input({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const ops = opsOf(args);
	const paths = pathsOf(args);
	const lang = editLanguage(args, detailsOf(result));
	return (
		<>
			{ops.map((op, i) => (
				<OpRule key={i} op={op} index={i} count={ops.length} lang={lang} />
			))}
			{paths.length > 0 && (
				<div className="tv-scope">
					<span className="tv-scope-label">
						{t("toolRender.astEdit.scope")}
					</span>
					{paths.map((p, i) => (
						<span key={i} className="tv-scope-path">
							<Icon
								name={p.endsWith("/") || !p.includes(".") ? "folder" : "file"}
								size={12}
							/>
							<PathText path={p} />
						</span>
					))}
				</div>
			)}
		</>
	);
}

const STAGED_BANNER_RE = /^Staged as a proposal[^\n]*\n*/;

/** 统一 diff 形式的 displayContent（旧版）按 `+++ b/<path>` 拆成逐文件的行。 */
function unifiedGroups(diff: string): FileGroup[] {
	const chunks: { path: string; lines: string[] }[] = [];
	for (const line of diff.split("\n")) {
		const plus = /^\+\+\+ (?:b\/)?(.+)$/.exec(line);
		if (plus) chunks.push({ path: plus[1] ?? "", lines: [] });
		else if (!/^(?:--- |diff --git |index )/.test(line))
			chunks[chunks.length - 1]?.lines.push(line);
	}
	return chunks.map((c) => ({
		path: c.path,
		label: null,
		rows: parseDiff(c.lines.join("\n")),
	}));
}

function Result({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result) return null;
	const text = resultTextOf(result);
	if (result.isError) return <ErrorBlock text={text} />;
	const d = detailsOf(result);
	const lang = editLanguage(args, d);
	const source = d?.displayContent ?? text.replace(STAGED_BANNER_RE, "");
	const parsed = parseGrouped(source);
	const notes = parsed.notes;
	const groups =
		parsed.groups.length > 0 ? parsed.groups : unifiedGroups(source);
	const counts = new Map(
		d?.fileReplacements.map((f) => [f.path, f.count]) ?? [],
	);
	// 解析出的说明里去掉已单独呈现的部分（上限、解析问题列表）；认不出分组（统一 diff）时整段交给 diff
	const parseIssues = d?.parseErrors ?? [];
	const rest =
		parsed.groups.length === 0
			? []
			: notes.filter(
					(n) =>
						!/^Limit reached/.test(n) &&
						!/^Parse issues/.test(n) &&
						!parseIssues.some((p) => n.endsWith(p)) &&
						!/^No replacements made/.test(n),
				);
	if (d?.filesSearched != null)
		rest.push(t("toolRender.astEdit.searched", { count: d.filesSearched }));
	const empty =
		d?.totalReplacements === 0 || /^No replacements made/m.test(text);
	return (
		<>
			{isStaged(d) && (
				<div className="tv-stage">
					<Icon name="eye" size={13} />
					<span>{t("toolRender.astEdit.staged")}</span>
				</div>
			)}
			{empty ? (
				<EmptyNote>{t("toolRender.astEdit.empty")}</EmptyNote>
			) : groups.length > 0 ? (
				<GroupedCode
					groups={groups}
					lang={lang}
					maxRows={40}
					aside={(g) => {
						const n =
							counts.get(g.path) ??
							[...counts].find(([p]) => p.endsWith(`/${g.path}`))?.[1] ??
							null;
						return n != null ? (
							<span>{t("toolRender.meta.replacements", { count: n })}</span>
						) : (
							g.label && <span>{g.label}</span>
						);
					}}
				/>
			) : (
				source.trim() && <DiffBlock diff={source} lang={lang} maxLines={40} />
			)}
			{d?.limitReached && (
				<FootNotes notes={[t("toolRender.astEdit.limitNote")]} tone="warn" />
			)}
			{parseIssues.length > 0 && (
				<div className="tv-fgroup">
					<div className="tv-out-title">
						{t("toolRender.astEdit.parseIssues", {
							count: d?.parseErrorsTotal ?? parseIssues.length,
						})}
					</div>
					<Output text={parseIssues.join("\n")} maxLines={6} variant="plain" />
				</div>
			)}
			<FootNotes notes={rest} />
		</>
	);
}

export const astEditRenderer: ToolRenderer = { Summary, Meta, Input, Result };
