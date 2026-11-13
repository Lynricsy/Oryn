/** `ast_edit` — structural AST rewrites: per-op pattern/replacement pairs, replacement counts, diffs. */
import type { ReactNode } from "react";
import {
	Badge,
	Badges,
	CodeBlock,
	DiffBlock,
	InvalidArg,
	MetaCount,
	Note,
	Output,
	PathText,
	ResultText,
	Row,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	isRecord,
	languageFromPath,
	num,
	shortenPath,
	str,
} from "../util";

interface AstEditOp {
	pat: string;
	out: string;
}

interface AstEditDetails {
	totalReplacements: number | null;
	filesTouched: number | null;
	filesSearched: number | null;
	limitReached: boolean;
	scopePath: string | null;
	fileReplacements: Array<{ path: string; count: number | null }>;
	parseErrors: string[];
	parseErrorsTotal: number | null;
	displayContent: string | null;
}

function pathsOf(args: Record<string, unknown>): string[] {
	if (!Array.isArray(args.paths)) return [];
	const paths: string[] = [];
	for (const p of args.paths) {
		const s = str(p);
		if (s) paths.push(s);
	}
	return paths;
}

function opsOf(args: Record<string, unknown>): AstEditOp[] {
	if (!Array.isArray(args.ops)) return [];
	const ops: AstEditOp[] = [];
	for (const op of args.ops) {
		if (!isRecord(op)) continue;
		ops.push({ pat: str(op.pat) ?? "", out: str(op.out) ?? "" });
	}
	return ops;
}

function detailsOf(result: ToolRenderProps["result"]): AstEditDetails | null {
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
	const parseErrors: string[] = [];
	if (Array.isArray(d.parseErrors)) {
		for (const e of d.parseErrors) {
			const s = str(e);
			if (s) parseErrors.push(s);
		}
	}
	return {
		totalReplacements: num(d.totalReplacements),
		filesTouched: num(d.filesTouched),
		filesSearched: num(d.filesSearched),
		limitReached: d.limitReached === true,
		scopePath: str(d.scopePath),
		fileReplacements,
		parseErrors,
		parseErrorsTotal: num(d.parseErrorsTotal),
		displayContent: str(d.displayContent),
	};
}

function Summary({ args, result }: ToolRenderProps): ReactNode {
	const paths = pathsOf(args);
	const first = paths[0];
	const opCount = Array.isArray(args.ops) ? args.ops.length : 0;
	const details = detailsOf(result);
	// 替换数在头部右侧读数里
	return (
		<>
			{first ? <PathText path={first} /> : <InvalidArg what="paths" />}
			{paths.length > 1 && (
				<span className="tv-faint">+{paths.length - 1} more</span>
			)}
			<Badge tone="accent">
				{opCount} op{opCount === 1 ? "" : "s"}
			</Badge>
			{details?.limitReached && <Badge tone="warn">limit</Badge>}
		</>
	);
}

function OpCell({
	op,
	lang,
}: {
	op: AstEditOp;
	lang: string | null;
}): ReactNode {
	return (
		<div className="tv-cell">
			{op.pat ? (
				<CodeBlock code={op.pat} lang={lang} title="pattern" maxLines={10} />
			) : (
				<InvalidArg what="pattern" />
			)}
			{op.out ? (
				<CodeBlock
					code={op.out}
					lang={lang}
					title="replacement"
					maxLines={10}
				/>
			) : (
				<div className="tv-muted">deletion — matched code is removed</div>
			)}
		</div>
	);
}

function Body({ args, result }: ToolRenderProps): ReactNode {
	const paths = pathsOf(args);
	const first = paths[0];
	const ops = opsOf(args);
	const details = result?.isError ? null : detailsOf(result);
	const lang = first ? languageFromPath(first) : null;
	const parseErrorsTotal = details
		? (details.parseErrorsTotal ?? details.parseErrors.length)
		: 0;
	return (
		<>
			{paths.length > 1 && (
				<div className="tv-list">
					{paths.map((p, i) => (
						<Row key={i}>
							<PathText path={p} />
						</Row>
					))}
				</div>
			)}
			{ops.length > 0 && (
				<div className="tv-cells">
					{ops.map((op, i) => (
						<OpCell key={i} op={op} lang={lang} />
					))}
				</div>
			)}
			{/* 替换总数与 limit 已在折叠行里（limit 另有下面的提示），这里只补文件数与搜索范围 */}
			{details && (
				<Badges
					items={[
						details.filesTouched != null &&
							`${details.filesTouched} file${details.filesTouched === 1 ? "" : "s"}`,
						details.filesSearched != null &&
							`searched ${details.filesSearched}`,
						details.scopePath &&
							shortenPath(details.scopePath) !== shortenPath(first ?? "") &&
							`in ${shortenPath(details.scopePath)}`,
					]}
				/>
			)}
			{details && details.fileReplacements.length > 0 && (
				<div className="tv-list">
					{details.fileReplacements.map((fr, i) => (
						<Row key={i} k={fr.count != null ? `×${fr.count}` : undefined}>
							<PathText path={fr.path} />
						</Row>
					))}
				</div>
			)}
			{details?.limitReached && (
				<Note tone="warn">limit reached; narrow path</Note>
			)}
			{details && details.parseErrors.length > 0 && (
				<Output
					text={details.parseErrors.join("\n")}
					maxLines={6}
					title={`parse issues (${parseErrorsTotal})`}
					variant="plain"
				/>
			)}
			{details?.displayContent ? (
				<DiffBlock diff={details.displayContent} maxLines={40} />
			) : (
				<ResultText result={result} maxLines={12} />
			)}
		</>
	);
}

/** 右侧读数：替换数（0 处时提示）。 */
function Meta({ result }: ToolRenderProps): ReactNode {
	const total = detailsOf(result)?.totalReplacements ?? null;
	return (
		<MetaCount
			unit="replacements"
			count={total}
			tone={total === 0 ? "warn" : undefined}
		/>
	);
}

export const astEditRenderer: ToolRenderer = { Summary, Meta, Body };
