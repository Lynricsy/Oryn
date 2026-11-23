/**
 * `ast_grep` — 结构化（AST）模式搜索。输入段是模式（按语言高亮的代码，语言标签在旁）与搜索范围；
 * 输出段与 grep 同形：按文件分组，文件头条右侧是该文件的命中数，命中行左缘一道标记，`meta:` 捕获
 * 作为附注行；解析问题可折叠，扫描的文件数与上限提示收在末尾。头部读数：命中数 · 文件数，到上限时警示。
 * 结果形状见 oryn-core `tui/src/tools/ast-grep.ts`（`AstGrepToolDetails`）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/ui/icons";
import {
	CodeLines,
	EmptyNote,
	ErrorBlock,
	FootNotes,
	GroupedCode,
	InvalidArg,
	MetaCount,
	Output,
	PathText,
	parseGrouped,
} from "../parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "../types";
import {
	detailsRecord,
	isRecord,
	normalizeWs,
	num,
	resultFiles,
	resultTextOf,
	scopePaths,
	sharedLanguage,
	shortenPath,
	str,
	truncate,
} from "../util";

/** `pat` is a string in the current schema, an array on the legacy wire. */
function patternsOf(args: Record<string, unknown>): string[] {
	if (typeof args.pat === "string") return [args.pat];
	if (Array.isArray(args.pat))
		return args.pat.filter((p): p is string => typeof p === "string");
	return [];
}

/** 搜索范围：`path`（`;` 分隔）或旧的 `paths`。 */
function scopeOf(args: Record<string, unknown>): string[] {
	return scopePaths(args)
		.flatMap((p) => p.split(";"))
		.map((p) => p.trim())
		.filter(Boolean);
}

/** 模式的高亮语言：`lang` 参数，否则命中文件（或搜索范围）共同的语言。 */
function patternLanguage(
	args: Record<string, unknown>,
	result: ToolResultLike | undefined,
): string | null {
	const files = resultFiles(result);
	return (
		str(args.lang) ?? sharedLanguage(files.length > 0 ? files : scopeOf(args))
	);
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const patterns = patternsOf(args);
	const paths = scopeOf(args);
	return (
		<>
			{patterns.length > 0 ? (
				<span className="tv-pattern">
					{truncate(normalizeWs(patterns[0] ?? ""), 64)}
				</span>
			) : (
				<InvalidArg what="pat" />
			)}
			{patterns.length > 1 && (
				<span className="tv-faint">+{patterns.length - 1}</span>
			)}
			{paths[0] && <PathText path={paths[0]} />}
			{paths.length > 1 && (
				<span className="tv-faint">+{paths.length - 1}</span>
			)}
		</>
	);
}

/** 头部读数：命中数（0 处时警示）· 文件数，到上限时警示。 */
function Meta({ result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result || result.isError) return null;
	const d = detailsRecord(result);
	const matches = d ? num(d.matchCount) : null;
	const files = d ? num(d.fileCount) : null;
	return (
		<>
			<MetaCount
				unit="matches"
				count={matches}
				tone={matches === 0 ? "warn" : undefined}
			/>
			{files !== null && files > 1 && <MetaCount unit="files" count={files} />}
			{d?.limitReached === true && (
				<span className="tv-meta-warn">{t("toolRender.astEdit.limit")}</span>
			)}
		</>
	);
}

function Input({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const patterns = patternsOf(args);
	const paths = scopeOf(args);
	const lang = patternLanguage(args, result);
	const skip = num(args.skip);
	const options: string[] = [];
	if (str(args.lang)) options.push(str(args.lang) ?? "");
	const glob = str(args.glob);
	if (glob) options.push(`glob ${glob}`);
	if (skip !== null && skip > 0)
		options.push(t("toolRender.grep.skip", { count: skip }));
	return (
		<>
			{patterns.length === 0 ? (
				<InvalidArg what="pat" />
			) : (
				patterns.map((pat, i) => (
					<CodeLines
						key={i}
						rows={pat.split("\n").map((text) => ({ kind: "ctx", text }))}
						lang={lang}
						maxLines={12}
					/>
				))
			)}
			{(paths.length > 0 || options.length > 0) && (
				<div className="tv-scope">
					{paths.length > 0 && (
						<span className="tv-scope-label">{t("toolRender.grep.in")}</span>
					)}
					{paths.map((p, i) => (
						<span key={i} className="tv-scope-path">
							<Icon
								name={/\.[a-z0-9]+$/i.test(p) ? "file" : "folder"}
								size={12}
							/>
							{shortenPath(p)}
						</span>
					))}
					{options.map((o) => (
						<span key={o} className="tv-badge">
							{o}
						</span>
					))}
				</div>
			)}
		</>
	);
}

function Result({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result) return null;
	const text = resultTextOf(result);
	if (result.isError) return <ErrorBlock text={text} />;
	const d = detailsRecord(result);
	const display = d ? str(d.displayContent) : null;
	const fromText = parseGrouped(text);
	const groups = display ? parseGrouped(display).groups : fromText.groups;
	const parseErrors =
		d && Array.isArray(d.parseErrors)
			? d.parseErrors.filter((e): e is string => typeof e === "string")
			: [];
	const parseTotal = (d ? num(d.parseErrorsTotal) : null) ?? parseErrors.length;
	const counts = new Map<string, number>();
	if (d && Array.isArray(d.fileMatches))
		for (const m of d.fileMatches) {
			const path = isRecord(m) ? str(m.path) : null;
			const count = isRecord(m) ? num(m.count) : null;
			if (path && count !== null) counts.set(path, count);
		}
	const notes = fromText.notes.filter(
		(n) =>
			!/^No matches found/.test(n) &&
			!/^Parse issues/.test(n) &&
			!parseErrors.some((p) => n.endsWith(p)),
	);
	const warn = notes.filter((n) => /limit reached/i.test(n));
	const rest = notes.filter((n) => !warn.includes(n));
	const searched = d ? num(d.filesSearched) : null;
	if (searched !== null)
		rest.push(t("toolRender.astEdit.searched", { count: searched }));
	const lang = patternLanguage(args, result);
	return (
		<>
			{groups.length > 0 ? (
				<GroupedCode
					groups={groups}
					lang={lang}
					maxRows={18}
					aside={(g) => {
						const n =
							counts.get(g.path) ??
							g.rows.filter((r) => r.kind === "hit").length;
						return <span>{t("toolRender.meta.matches", { count: n })}</span>;
					}}
				/>
			) : (
				<EmptyNote>{t("toolRender.grep.empty")}</EmptyNote>
			)}
			{parseErrors.length > 0 && (
				<div className="tv-fgroup">
					<div className="tv-out-title">
						{t("toolRender.astEdit.parseIssues", { count: parseTotal })}
					</div>
					<Output text={parseErrors.join("\n")} maxLines={6} variant="plain" />
				</div>
			)}
			<FootNotes notes={warn} tone="warn" />
			<FootNotes notes={rest} />
		</>
	);
}

export const astGrepRenderer: ToolRenderer = { Summary, Meta, Input, Result };
