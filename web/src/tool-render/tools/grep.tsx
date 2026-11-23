/**
 * `grep`（旧名 `search`）— 正则内容搜索。输入段是查询本身：醒目的 `/模式/`、选项标签与搜索范围；
 * 输出段按文件分组：文件头条（右侧是该文件的命中数）下接带行号栏的命中行与上下文，命中行左缘一道
 * 标记、上下文退后，匹配片段用 CSS 自定义高亮标出。翻页、上限、跳过的路径收在末尾的附注里，
 * 没有命中时给明确的空状态。头部读数：命中数 · 文件数，截断时警示。
 * 结果形状见 oryn-core `tui/src/tools/grep.ts`（`GrepToolDetails`）。
 */
import type { TFunction } from "i18next";
import { type ReactNode, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/ui/icons";
import {
	EmptyNote,
	ErrorBlock,
	FootNotes,
	GroupedCode,
	InvalidArg,
	MetaCount,
	parseGrouped,
} from "../parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "../types";
import {
	detailsRecord,
	isRecord,
	num,
	resultFiles,
	resultTextOf,
	scopePaths,
	sharedLanguage,
	shortenPath,
	str,
	truncate,
} from "../util";

/** 搜索范围：`path`（`;` 分隔、JSON 数组）或旧的 `paths`；缺省是工作区根。 */
function scopeOf(args: Record<string, unknown>): string[] {
	const list = scopePaths(args)
		.flatMap((p) => p.split(";"))
		.map((p) => p.trim())
		.filter(Boolean);
	return list.length > 0 ? list : ["."];
}

/** 是否忽略大小写：当前的 `case: false`，或旧的 `i: true`。 */
function ignoreCase(args: Record<string, unknown>): boolean {
	return args.case === false || args.i === true;
}

/** 选项标签（当前与旧版参数都认）。 */
function optionLabels(args: Record<string, unknown>, t: TFunction): string[] {
	const out: string[] = [];
	if (ignoreCase(args)) out.push(t("toolRender.grep.ignoreCase"));
	if (args.multiline === true) out.push(t("toolRender.grep.multiline"));
	const glob = str(args.glob);
	if (glob) out.push(`glob ${glob}`);
	const type = str(args.type);
	if (type) out.push(`type ${type}`);
	if (args.gitignore === false) out.push(t("toolRender.grep.noGitignore"));
	const skip = num(args.skip);
	if (skip !== null && skip > 0)
		out.push(t("toolRender.grep.skip", { count: skip }));
	return out;
}

/** 模式转成浏览器正则，用来在命中行里标出匹配片段；JS 不认的写法（Rust 专有语法）时不标。 */
function hitRegExp(args: Record<string, unknown>): RegExp | null {
	const pattern = str(args.pattern);
	if (!pattern) return null;
	try {
		return new RegExp(pattern, ignoreCase(args) ? "gi" : "g");
	} catch {
		return null;
	}
}

interface GrepInfo {
	matchCount: number | null;
	fileCount: number | null;
	truncated: boolean;
	error: string | null;
	missing: string[];
	fileMatches: Map<string, number>;
	display: string | null;
}

function grepInfo(result: ToolResultLike | undefined): GrepInfo {
	const d = detailsRecord(result);
	const fileMatches = new Map<string, number>();
	if (d && Array.isArray(d.fileMatches))
		for (const m of d.fileMatches) {
			const path = isRecord(m) ? str(m.path) : null;
			const count = isRecord(m) ? num(m.count) : null;
			if (path && count !== null) fileMatches.set(path, count);
		}
	return {
		matchCount: d ? num(d.matchCount) : null,
		fileCount: d ? num(d.fileCount) : null,
		truncated: d?.truncated === true,
		error: d ? str(d.error) : null,
		missing:
			d && Array.isArray(d.missingPaths)
				? d.missingPaths.filter((p): p is string => typeof p === "string")
				: [],
		fileMatches,
		display: d ? str(d.displayContent) : null,
	};
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const pattern = str(args.pattern);
	const scope = scopeOf(args);
	return (
		<>
			{pattern === null ? (
				<InvalidArg what="pattern" />
			) : (
				<span className="tv-pattern">/{truncate(pattern, 60)}/</span>
			)}
			<span className="tv-path">{scope.map(shortenPath).join(", ")}</span>
		</>
	);
}

/** 头部读数：命中数（0 处时警示）· 文件数，截断时警示。 */
function Meta({ result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result || result.isError) return null;
	const info = grepInfo(result);
	return (
		<>
			<MetaCount
				unit="matches"
				count={info.matchCount}
				tone={info.matchCount === 0 ? "warn" : undefined}
			/>
			{info.fileCount !== null && info.fileCount > 1 && (
				<MetaCount unit="files" count={info.fileCount} />
			)}
			{info.truncated && (
				<span className="tv-meta-warn">{t("toolRender.grep.truncated")}</span>
			)}
		</>
	);
}

/** 查询：醒目的模式 + 选项标签，下一行是搜索范围（每个路径一枚带图标的标签）。 */
function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const pattern = str(args.pattern);
	const options = optionLabels(args, t);
	return (
		<div className="tv-query">
			<div className="tv-query-line">
				{pattern === null ? (
					<InvalidArg what="pattern" />
				) : (
					<span className="tv-query-pat">
						<span className="tv-query-slash">/</span>
						{pattern}
						<span className="tv-query-slash">/</span>
					</span>
				)}
				{options.map((o) => (
					<span key={o} className="tv-badge">
						{o}
					</span>
				))}
			</div>
			<div className="tv-scope">
				<span className="tv-scope-label">{t("toolRender.grep.in")}</span>
				{scopeOf(args).map((p, i) => (
					<span key={i} className="tv-scope-path">
						<Icon
							name={/\.[a-z0-9]+$/i.test(p) ? "file" : "folder"}
							size={12}
						/>
						{shortenPath(p)}
					</span>
				))}
			</div>
		</div>
	);
}

function Result({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const hitRe = useMemo(() => hitRegExp(args), [args]);
	if (!result) return null;
	const text = resultTextOf(result);
	if (result.isError) return <ErrorBlock text={text} />;
	const info = grepInfo(result);
	// displayContent 是给人看的版式（`│` 行号栏、没有 hashline 标签）；翻页等附注只在模型文本里
	const fromText = parseGrouped(text);
	const groups = info.display
		? parseGrouped(info.display).groups
		: fromText.groups;
	const notes = fromText.notes;
	// 没有文件头（单文件搜索）时的高亮语言：命中文件或搜索范围共同的语言
	const files = resultFiles(result);
	const lang = sharedLanguage(files.length > 0 ? files : scopeOf(args));
	const single = scopeOf(args);
	const fallbackPath =
		single.length === 1 && single[0] !== "." ? single[0] : "";
	const named = groups.map((g) =>
		g.path || !fallbackPath ? g : { ...g, path: fallbackPath },
	);
	const countOf = (path: string): number => {
		const exact = info.fileMatches.get(path);
		if (exact !== undefined) return exact;
		for (const [p, n] of info.fileMatches)
			if (p.endsWith(`/${path}`) || path.endsWith(`/${p}`)) return n;
		return (
			named.find((g) => g.path === path)?.rows.filter((r) => r.kind === "hit")
				.length ?? 0
		);
	};
	const missing =
		info.missing.length > 0
			? [
					t("toolRender.grep.missing", {
						paths: info.missing.map(shortenPath).join(", "),
					}),
				]
			: [];
	const warnNotes = notes.filter((n) =>
		/^(?:More than|Skipped|Binary files)/.test(n),
	);
	const infoNotes = notes.filter(
		(n) => !warnNotes.includes(n) && !/^No (?:matches|more results)/.test(n),
	);
	const emptyLine = notes.find((n) => /^No (?:matches|more results)/.test(n));
	return (
		<>
			{named.length > 0 ? (
				<GroupedCode
					groups={named}
					lang={lang}
					hitRe={hitRe}
					maxRows={18}
					aside={(g) => (
						<span>
							{t("toolRender.meta.matches", { count: countOf(g.path) })}
						</span>
					)}
				/>
			) : (
				<EmptyNote>
					{emptyLine ?? info.error ?? t("toolRender.grep.empty")}
				</EmptyNote>
			)}
			<FootNotes notes={[...missing, ...warnNotes]} tone="warn" />
			<FootNotes notes={infoNotes} />
		</>
	);
}

export const grepRenderer: ToolRenderer = { Summary, Meta, Input, Result };
