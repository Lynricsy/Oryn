/**
 * `glob`（旧名 `find`）— 按通配模式找文件。输入段是模式（醒目）与选项；输出段把找到的路径按所在目录
 * 分组：目录行（文件夹图标、目录、该目录的文件数）下列文件（按类型的文件图标 + 文件名），保持工具给的
 * 按修改时间的顺序。头部读数：文件数；被上限截断、超时时警示。没有结果时给明确的空状态。
 * 结果形状见 oryn-core `tui/src/tools/glob.ts`（`GlobToolDetails`）。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { FileTypeIcon } from "../../components/ui/FileTypeIcon";
import {
	EmptyNote,
	ErrorBlock,
	ExpandToggle,
	FootNotes,
	InvalidArg,
	MetaCount,
	splitDir,
} from "../parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "../types";
import {
	detailsRecord,
	isRecord,
	num,
	resultTextOf,
	scopePaths,
	shortenPath,
	str,
	truncate,
} from "../util";

/** 模式：`path`（`;` 分隔）/ 旧的 `paths`，以及模型常顺手给的 `pattern`。 */
function patternsOf(args: Record<string, unknown>): string[] {
	const list = scopePaths(args).flatMap((p) => p.split(";"));
	const pattern = str(args.pattern);
	if (pattern) list.push(...pattern.split(";"));
	return list.map((p) => p.trim()).filter(Boolean);
}

interface GlobInfo {
	fileCount: number | null;
	files: string[];
	limit: number | null;
	truncated: boolean;
	timedOut: boolean;
	missing: string[];
	error: string | null;
}

function globInfo(result: ToolResultLike | undefined): GlobInfo {
	const d = detailsRecord(result);
	const meta = d && isRecord(d.meta) ? d.meta : null;
	const limits = meta && isRecord(meta.limits) ? meta.limits : null;
	const limit = d ? num(d.resultLimitReached) : null;
	return {
		fileCount: d ? num(d.fileCount) : null,
		files:
			d && Array.isArray(d.files)
				? d.files.filter((f): f is string => typeof f === "string")
				: [],
		limit,
		truncated:
			d?.truncated === true ||
			limit !== null ||
			(d !== null && isRecord(d.truncation)) ||
			(limits !== null && isRecord(limits.resultLimit)),
		timedOut: d?.timedOut === true,
		missing:
			d && Array.isArray(d.missingPaths)
				? d.missingPaths.filter((p): p is string => typeof p === "string")
				: [],
		error: d ? str(d.error) : null,
	};
}

const DIR_HEAD_RE = /^(#{1,6}) (.+\/)$/;

/**
 * 结果文本转成路径：`formatGroupedPaths` 的 `# dir/` 标题 + 裸文件名，或旧的每行一个路径。
 * 空行之后认不出的行（跳过的路径、超时、上限提示）收进 `notes`。
 */
function parsePaths(text: string): { paths: string[]; notes: string[] } {
	const paths: string[] = [];
	const notes: string[] = [];
	const dirs: string[] = [];
	for (const raw of text.replace(/\n+$/, "").split("\n")) {
		const line = raw.trim();
		// 空行只出现在目录标题与根目录下的文件之前：之后的裸文件名属于根目录
		if (!line) {
			dirs.length = 0;
			continue;
		}
		const head = DIR_HEAD_RE.exec(line);
		if (head) {
			const depth = head[1]?.length ?? 1;
			dirs.length = Math.min(dirs.length, depth - 1);
			dirs[depth - 1] = head[2] ?? "";
			continue;
		}
		// 文件名不含空白；带空白或方括号的是说明（跳过的路径、超时、上限提示）
		if (/\s/.test(line) || /^\[.*\]$/.test(line)) {
			notes.push(line.replace(/^\[(.*)\]$/, "$1"));
			continue;
		}
		paths.push(dirs.join("") + line);
	}
	return { paths, notes };
}

/** 按所在目录分组，保持首次出现的顺序（工具按修改时间排序）。 */
function groupByDir(paths: string[]): { dir: string; names: string[] }[] {
	const groups: { dir: string; names: string[] }[] = [];
	const index = new Map<string, number>();
	for (const p of paths) {
		const { dir, base } = splitDir(shortenPath(p));
		let at = index.get(dir);
		if (at === undefined) {
			at = groups.length;
			index.set(dir, at);
			groups.push({ dir, names: [] });
		}
		groups[at]?.names.push(base);
	}
	return groups;
}

const MAX_ROWS = 30;

function PathTree({ paths }: { paths: string[] }): ReactNode {
	const [expanded, setExpanded] = useState(false);
	const groups = groupByDir(paths);
	let budget =
		expanded || paths.length <= MAX_ROWS + 4
			? Number.POSITIVE_INFINITY
			: MAX_ROWS;
	const total = paths.length;
	return (
		<div className="tv-ptree">
			{groups.map((g) => {
				if (budget <= 0) return null;
				const names = g.names.slice(0, budget);
				budget -= names.length;
				return (
					<div key={g.dir} className="tv-ptree-group">
						{g.dir && (
							<div className="tv-ptree-dir">
								<FileTypeIcon name={g.dir} kind="dir" open size={14} />
								<span className="tv-ptree-dirname">{g.dir}</span>
								<span className="tv-ptree-count">{g.names.length}</span>
							</div>
						)}
						{names.map((name, i) => (
							<div
								key={`${name}:${i}`}
								className={`tv-ptree-file${g.dir ? "" : " tv-ptree-file--root"}`}
							>
								<FileTypeIcon name={name} size={14} />
								<span>{name}</span>
							</div>
						))}
					</div>
				);
			})}
			{total > MAX_ROWS + 4 && (
				<ExpandToggle
					expanded={expanded}
					hidden={total - MAX_ROWS}
					onToggle={() => setExpanded((v) => !v)}
				/>
			)}
		</div>
	);
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const raw = args.path ?? args.paths;
	if (raw !== undefined && typeof raw !== "string" && !Array.isArray(raw))
		return <InvalidArg what="path" />;
	const globs = patternsOf(args).map(shortenPath).join(", ");
	return <span className="tv-pattern">{truncate(globs || "*", 120)}</span>;
}

/** 头部读数：文件数（0 个时警示），被上限截断、超时时警示。 */
function Meta({ result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result || result.isError) return null;
	const info = globInfo(result);
	const count = info.fileCount ?? (info.files.length || null);
	return (
		<>
			<MetaCount
				unit="files"
				count={count}
				tone={count === 0 ? "warn" : undefined}
			/>
			{info.timedOut ? (
				<span className="tv-meta-warn">{t("toolRender.glob.timedOut")}</span>
			) : (
				info.truncated && (
					<span className="tv-meta-warn">
						{info.limit !== null
							? t("toolRender.glob.limit", { count: info.limit })
							: t("toolRender.grep.truncated")}
					</span>
				)
			)}
		</>
	);
}

/** 模式（每个一行，醒目）与选项标签。 */
function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const patterns = patternsOf(args);
	const limit = num(args.limit);
	const options: string[] = [];
	if (limit !== null)
		options.push(t("toolRender.glob.limitArg", { count: limit }));
	if (args.hidden === false) options.push(t("toolRender.glob.noHidden"));
	if (args.gitignore === false) options.push(t("toolRender.grep.noGitignore"));
	const timeout = num(args.timeout);
	if (timeout !== null)
		options.push(t("toolRender.glob.timeout", { count: timeout }));
	if (patterns.length === 0 && options.length === 0) return null;
	return (
		<div className="tv-query">
			{patterns.map((p, i) => (
				<div key={i} className="tv-query-line">
					<span className="tv-query-pat">{shortenPath(p)}</span>
				</div>
			))}
			{options.length > 0 && (
				<div className="tv-query-line">
					{options.map((o) => (
						<span key={o} className="tv-badge">
							{o}
						</span>
					))}
				</div>
			)}
		</div>
	);
}

function Result({ result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result) return null;
	const text = resultTextOf(result);
	if (result.isError) return <ErrorBlock text={text} />;
	const info = globInfo(result);
	const parsed = parsePaths(text);
	const paths = info.files.length > 0 ? info.files : parsed.paths;
	const empty = paths.length === 0 || info.fileCount === 0;
	const notes = parsed.notes.filter((n) => !/^No files found/.test(n));
	const warn = [
		...(info.missing.length > 0
			? [
					t("toolRender.grep.missing", {
						paths: info.missing.map(shortenPath).join(", "),
					}),
				]
			: []),
		...notes.filter((n) => /timed out|limit reached|Skipped/i.test(n)),
	];
	const rest = notes.filter(
		(n) => !warn.includes(n) && !/^Skipped missing/.test(n),
	);
	return (
		<>
			{empty ? (
				<EmptyNote>{info.error ?? t("toolRender.glob.empty")}</EmptyNote>
			) : (
				<PathTree paths={paths} />
			)}
			<FootNotes notes={warn} tone="warn" />
			<FootNotes notes={rest} />
		</>
	);
}

export const globRenderer: ToolRenderer = { Summary, Meta, Input, Result };
