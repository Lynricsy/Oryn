/** `read` — file/URL/archive reads: path + selector summary, highlighted content, image thumbnails. */
import type { ReactNode } from "react";
import {
	Badge,
	Badges,
	Kv,
	KvGrid,
	MetaCount,
	PathText,
	ResultImages,
	ResultText,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	isRecord,
	languageFromPath,
	num,
	resultTextOf,
	shortenPath,
	splitPathSel,
	str,
} from "../util";

/** Fields of `ReadToolDetails` the web view surfaces (untrusted wire JSON). */
interface ReadDetails {
	resolvedPath: string | null;
	suffixTo: string | null;
	suffixFrom: string | null;
	elidedSpans: number | null;
	conflictCount: number | null;
	truncated: boolean;
}

function readDetails(details: Record<string, unknown> | null): ReadDetails {
	const suffix =
		details && isRecord(details.suffixResolution)
			? details.suffixResolution
			: null;
	const summary = details && isRecord(details.summary) ? details.summary : null;
	return {
		resolvedPath: details ? str(details.resolvedPath) : null,
		suffixTo: suffix ? str(suffix.to) : null,
		suffixFrom: suffix ? str(suffix.from) : null,
		elidedSpans: summary ? num(summary.elidedSpans) : null,
		conflictCount: details ? num(details.conflictCount) : null,
		truncated: details ? isRecord(details.truncation) : false,
	};
}

interface ReadArgs {
	path: string;
	sel: string | null;
	from: number | null;
	to: number | null;
}

function readArgs(args: Record<string, unknown>): ReadArgs {
	const rawPath = str(args.path) ?? str(args.file_path) ?? "";
	const split = splitPathSel(rawPath);
	const sel = str(args.sel) ?? split.sel;
	const offset = num(args.offset);
	const limit = num(args.limit);
	const from = offset !== null || limit !== null ? (offset ?? 1) : null;
	const to = from !== null && limit !== null ? from + limit - 1 : null;
	return { path: split.path || rawPath, sel, from, to };
}

function Summary(props: ToolRenderProps): ReactNode {
	const { path, sel, from, to } = readArgs(props.args);
	return <PathText path={path || "…"} from={from} to={to} sel={sel} />;
}

/** 折叠行里的路径只是摘要（窄屏会被省略号截断），输入段始终给出完整路径与范围。 */
function Input({ args }: ToolRenderProps): ReactNode {
	const { path, sel, from, to } = readArgs(args);
	if (!path) return null;
	return (
		<KvGrid>
			<Kv k="path">
				<PathText path={path} from={from} to={to} sel={sel} />
			</Kv>
		</KvGrid>
	);
}

function Result({ args, result }: ToolRenderProps): ReactNode {
	const { path } = readArgs(args);
	const d = readDetails(detailsRecord(result));
	const conflictBadge = d.conflictCount !== null && d.conflictCount > 0 && (
		<Badge tone="warn">
			{d.conflictCount} conflict{d.conflictCount === 1 ? "" : "s"}
		</Badge>
	);
	const elidedBadge = d.elidedSpans !== null && d.elidedSpans > 0 && (
		<Badge>
			{d.elidedSpans} elided span{d.elidedSpans === 1 ? "" : "s"}
		</Badge>
	);
	const truncatedBadge = d.truncated && <Badge tone="warn">truncated</Badge>;
	// 解析后的路径只是请求路径的绝对形式时不再重复
	const raw = d.suffixTo ?? d.resolvedPath;
	const resolved =
		raw !== null && path && (raw === path || raw.endsWith(`/${path}`))
			? null
			: raw;
	// 请求路径没有可识别的扩展名时（如纠正后的路径、目录别名），退回解析后的路径取语言
	const lang = languageFromPath(path) ?? (raw ? languageFromPath(raw) : null);
	return (
		<>
			{(resolved !== null || d.suffixFrom !== null) && (
				<KvGrid>
					{resolved !== null && (
						<Kv k="resolved">
							<PathText path={resolved} />
						</Kv>
					)}
					{d.suffixFrom !== null && (
						<Kv k="corrected from">{shortenPath(d.suffixFrom)}</Kv>
					)}
				</KvGrid>
			)}
			<Badges items={[conflictBadge, elidedBadge, truncatedBadge]} />
			<ResultImages result={result} />
			<ResultText result={result} maxLines={12} lang={lang} variant="code" />
		</>
	);
}

/** 右侧读数：读到的行数（图片等无文本结果不显示）。 */
function Meta({ result }: ToolRenderProps): ReactNode {
	if (!result || result.isError) return null;
	const text = resultTextOf(result).replace(/\n+$/, "");
	return text ? (
		<MetaCount unit="lines" count={text.split("\n").length} />
	) : null;
}

export const readRenderer: ToolRenderer = { Summary, Meta, Input, Result };
