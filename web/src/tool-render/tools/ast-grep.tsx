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
	ResultText,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	fitsSummary,
	normalizeWs,
	num,
	scopePaths,
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

function Body({ args, result }: ToolRenderProps): ReactNode {
	const patterns = patternsOf(args);
	const paths = scopePaths(args);
	const lang = str(args.lang);
	const glob = str(args.glob);
	const sel = str(args.sel);
	const skip = num(args.skip);

	const details = detailsRecord(result);
	const fileCount = num(details?.fileCount);
	const filesSearched = num(details?.filesSearched);
	const limitReached = details?.limitReached === true;
	const scopePath = str(details?.scopePath);
	const parseErrors = Array.isArray(details?.parseErrors)
		? details.parseErrors.filter((e): e is string => typeof e === "string")
		: [];
	const parseErrorsTotal = num(details?.parseErrorsTotal) ?? parseErrors.length;

	// 语言、首个模式与首个路径已在折叠行里；这里只补折叠行没有的参数
	const argBadges: ReactNode[] = [
		glob && <Badge key="glob">glob={glob}</Badge>,
		sel && <Badge key="sel">sel={sel}</Badge>,
		skip !== null && skip > 0 && <Badge key="skip">skip:{skip}</Badge>,
	];
	const showPatterns =
		patterns.length > 1 ||
		(patterns[0] !== undefined && !fitsSummary(patterns[0]));
	const showPaths =
		paths.length > 1 || (paths[0] !== undefined && !fitsSummary(paths[0]));
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
			<Badges items={[...argBadges, ...resultBadges]} />
			{patterns.length === 0 ? (
				<InvalidArg what="pat" />
			) : (
				showPatterns &&
				patterns.map((pat, i) => (
					<CodeBlock
						key={i}
						code={pat}
						lang={lang ?? undefined}
						title={patterns.length > 1 ? `pattern ${i + 1}` : "pattern"}
						maxLines={12}
					/>
				))
			)}
			{(showPaths || showScope) && (
				<KvGrid>
					{showPaths && (
						<Kv k={paths.length === 1 ? "path" : "paths"}>
							{paths.map((p, i) => (
								<span key={i}>
									{i > 0 && ", "}
									<PathText path={p} />
								</span>
							))}
						</Kv>
					)}
					{showScope && scopePath && (
						<Kv k="scope">
							<PathText path={scopePath} />
						</Kv>
					)}
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
			<ResultText result={result} maxLines={12} />
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

export const astGrepRenderer: ToolRenderer = { Summary, Meta, Body };
