/** `grep` (legacy `search`) — ripgrep content search across workspace files. */
import type { ReactNode } from "react";
import {
	Badge,
	Badges,
	InvalidArg,
	MetaCount,
	Note,
	ResultText,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	num,
	resultTextOf,
	scopePaths,
	shortenPath,
	str,
} from "../util";

/** Grep scope: current `path` (string, delimited, or JSON array) or legacy `paths`; defaults to workspace root. */
function pathsOf(args: Record<string, unknown>): string[] {
	const list = scopePaths(args).map(shortenPath);
	return list.length ? list : ["."];
}

/** Flag badges covering current and legacy arg dialects. */
function argBadges(args: Record<string, unknown>): ReactNode[] {
	const badges: ReactNode[] = [];
	const glob = str(args.glob);
	if (glob) badges.push(`glob=${glob}`);
	const type = str(args.type);
	if (type) badges.push(`type=${type}`);
	if (args.i === true) badges.push("i");
	if (args.multiline === true) badges.push("multiline");
	if (args.gitignore === false) badges.push("no-gitignore");
	const skip = num(args.skip);
	if (skip !== null && skip > 0) badges.push(`skip=${skip}`);
	return badges;
}

function Pattern({ args }: { args: Record<string, unknown> }): ReactNode {
	const pattern = str(args.pattern);
	if (pattern === null) return <InvalidArg what="pattern" />;
	return <span className="tv-pattern">/{pattern}/</span>;
}

function Summary({ args }: ToolRenderProps): ReactNode {
	return (
		<span>
			<Pattern args={args} /> <span className="tv-muted">in</span>{" "}
			<span className="tv-path">{pathsOf(args).join(", ")}</span>{" "}
			<Badges items={argBadges(args)} />
		</span>
	);
}

function Body({ args, result }: ToolRenderProps): ReactNode {
	const details = detailsRecord(result);
	// 匹配数在头部读数里，这里只补文件数与截断
	const fileCount = num(details?.fileCount);
	const truncated = details?.truncated === true;
	const error = str(details?.error);
	const missing: string[] = [];
	if (details && Array.isArray(details.missingPaths)) {
		for (const p of details.missingPaths) {
			if (typeof p === "string") missing.push(shortenPath(p));
		}
	}
	const counts: ReactNode[] = [];
	if (fileCount !== null)
		counts.push(`${fileCount} ${fileCount === 1 ? "file" : "files"}`);
	if (truncated)
		counts.push(
			<Badge key="truncated" tone="warn">
				truncated
			</Badge>,
		);
	// 折叠行只是摘要（会被截断），展开体始终给出完整的模式与范围
	const paths = pathsOf(args).join(", ");
	return (
		<>
			<div>
				<Pattern args={args} /> <span className="tv-muted">in</span>{" "}
				<span className="tv-path">{paths}</span>
			</div>
			<Badges items={counts} />
			{missing.length > 0 && (
				<Note tone="warn">skipped missing: {missing.join(", ")}</Note>
			)}
			{error !== null && !resultTextOf(result).trim() && (
				<Note tone="err">{error}</Note>
			)}
			<ResultText result={result} maxLines={14} variant="code" />
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

export const grepRenderer: ToolRenderer = { Summary, Meta, Body };
