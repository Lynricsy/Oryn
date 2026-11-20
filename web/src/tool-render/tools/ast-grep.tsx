/** `ast_grep` — structural AST pattern search across files. */
import type { ReactNode } from "react";
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
	SearchOutput,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	normalizeWs,
	num,
	resultFiles,
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

function Summary({ args }: ToolRenderProps): ReactNode {
	const patterns = patternsOf(args);
	const paths = scopePaths(args);
	const lang = str(args.lang);
	return (
		<>
			<span className="tv-pattern">
				{patterns.length ? truncate(normalizeWs(patterns[0]!), 64) : "?"}
			</span>
			{patterns.length > 1 && (
				<span className="tv-faint">+{patterns.length - 1}</span>
			)}
			{paths.length > 0 && <PathText path={paths[0]!} />}
			{paths.length > 1 && (
				<span className="tv-faint">+{paths.length - 1}</span>
			)}
			{lang && <Badge tone="accent">{lang}</Badge>}
		</>
	);
}

/** 模式的高亮语言：`lang` 参数，否则命中文件（或搜索范围）共同的语言。 */
function patternLanguage(
	args: Record<string, unknown>,
	result: ToolRenderProps["result"],
): string | null {
	const files = resultFiles(result);
	return (
		str(args.lang) ??
		sharedLanguage(files.length > 0 ? files : scopePaths(args))
	);
}

function Input({ args, result }: ToolRenderProps): ReactNode {
	const patterns = patternsOf(args);
	const paths = scopePaths(args);
	const glob = str(args.glob);
	const sel = str(args.sel);
	const skip = num(args.skip);
	const lang = patternLanguage(args, result);
	// 语言在折叠行里；模式与路径在折叠行里只是摘要（会被截断），这里始终给出全文
	return (
		<>
			<Badges
				items={[
					glob && <Badge key="glob">glob={glob}</Badge>,
					sel && <Badge key="sel">sel={sel}</Badge>,
					skip !== null && skip > 0 && <Badge key="skip">skip:{skip}</Badge>,
				]}
			/>
			{patterns.length === 0 ? (
				<InvalidArg what="pat" />
			) : (
				patterns.map((pat, i) => (
					<CodeBlock
						key={i}
						code={pat}
						lang={lang}
						title={patterns.length > 1 ? `pattern ${i + 1}` : "pattern"}
						maxLines={12}
					/>
				))
			)}
			{paths.length > 0 && (
				<KvGrid>
					<Kv k={paths.length === 1 ? "path" : "paths"}>
						{paths.map((p, i) => (
							<span key={i}>
								{i > 0 && ", "}
								<PathText path={p} />
							</span>
						))}
					</Kv>
				</KvGrid>
			)}
		</>
	);
}

function Result({ args, result }: ToolRenderProps): ReactNode {
	const paths = scopePaths(args);
	const details = detailsRecord(result);
	const fileCount = num(details?.fileCount);
	const filesSearched = num(details?.filesSearched);
	const limitReached = details?.limitReached === true;
	const scopePath = str(details?.scopePath);
	const parseErrors = Array.isArray(details?.parseErrors)
		? details.parseErrors.filter((e): e is string => typeof e === "string")
		: [];
	const parseErrorsTotal = num(details?.parseErrorsTotal) ?? parseErrors.length;
	const showScope =
		scopePath !== null &&
		(paths.length !== 1 || shortenPath(scopePath) !== shortenPath(paths[0]!));
	// 匹配数在头部读数里
	const resultBadges: ReactNode[] =
		result && !result.isError
			? [
					fileCount !== null && fileCount > 0 && (
						<Badge key="files">
							{fileCount} {fileCount === 1 ? "file" : "files"}
						</Badge>
					),
					filesSearched !== null && (
						<Badge key="searched">searched {filesSearched}</Badge>
					),
					limitReached && (
						<Badge key="limit" tone="warn">
							limit reached
						</Badge>
					),
				]
			: [];

	return (
		<>
			<Badges items={resultBadges} />
			{showScope && scopePath && (
				<KvGrid>
					<Kv k="scope">
						<PathText path={scopePath} />
					</Kv>
				</KvGrid>
			)}
			{parseErrors.length > 0 && (
				<Output
					text={parseErrors.join("\n")}
					maxLines={6}
					title={
						parseErrorsTotal > parseErrors.length
							? `parse issues (${parseErrorsTotal} total)`
							: "parse issues"
					}
				/>
			)}
			<SearchOutput
				result={result}
				maxLines={12}
				lang={patternLanguage(args, result)}
			/>
		</>
	);
}

/** 右侧读数：匹配数（0 处时提示）。 */
function Meta({ result }: ToolRenderProps): ReactNode {
	if (!result || result.isError) return null;
	const matchCount = num(detailsRecord(result)?.matchCount);
	return (
		<MetaCount
			unit="matches"
			count={matchCount}
			tone={matchCount === 0 ? "warn" : undefined}
		/>
	);
}

export const astGrepRenderer: ToolRenderer = { Summary, Meta, Input, Result };
