/**
 * `recall` — search long-term memory. Details are always empty; everything
 * lives in the result text: a `Found N relevant memories (as of … UTC):`
 * header followed by blank-line-separated bullets. 两种后端的写法（见 oryn-core）：
 * - hindsight `formatMemories`：`- <text> [type] (date)`
 * - mnemopi `formatScopedRecallWithIds`：`- <content> (id: X) [source] (YYYY-MM-DD) c:0.8`
 * 命中画成记忆卡片列表（内容、类型标签、日期、id、相关度细条）；`MemoryCard` 也给 retain / memory_edit 用。
 */
import type { CSSProperties, ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/ui/icons";
import { EmptyNote, ErrorBlock, InvalidArg, ResultText } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import { normalizeWs, resultTextOf, str, truncate } from "../util";

/** 一张记忆卡：正文 + 底栏（标签、日期、相关度、来源、id）。`added` 给将要写入的记忆（左缘一道成功色）。 */
export function MemoryCard({
	content,
	tags = [],
	date,
	id,
	score,
	note,
	added,
}: {
	content: ReactNode;
	tags?: readonly (string | null | undefined | false)[];
	date?: string | null;
	id?: string | null;
	score?: number | null;
	/** 底栏末尾的弱化说明（来源上下文等）。 */
	note?: string | null;
	added?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const shownTags = tags.filter((tag): tag is string => !!tag);
	const foot = shownTags.length > 0 || date || id || score != null || note;
	return (
		<li className={`tv-mem${added ? " tv-mem--add" : ""}`}>
			<div className="tv-mem-text">{content}</div>
			{foot && (
				<div className="tv-mem-foot">
					{shownTags.map((tag) => (
						<span key={tag} className="tv-mem-tag">
							{tag}
						</span>
					))}
					{date && <span className="tv-mem-date">{date}</span>}
					{score != null && (
						<span
							className="tv-mem-score"
							title={t("toolRender.memory.score", { n: score.toFixed(2) })}
						>
							<span
								className="tv-mem-score-bar"
								style={
									{
										"--tv-frac": Math.max(0, Math.min(1, score)),
									} as CSSProperties
								}
								aria-hidden="true"
							/>
							{score.toFixed(1)}
						</span>
					)}
					{note && <span className="tv-mem-note">{note}</span>}
					{id && <span className="tv-mem-id">{id}</span>}
				</div>
			)}
		</li>
	);
}

interface RecallEntry {
	text: string;
	type: string | null;
	date: string | null;
	id: string | null;
	score: number | null;
}

/** 从尾部逐个剥下元信息：相关度 → 日期 → 类型/来源 → id。 */
function parseEntry(raw: string): RecallEntry {
	let text = raw.replace(/^-\s+/, "").trim();
	const take = (re: RegExp): string | null => {
		const m = re.exec(text);
		if (!m) return null;
		text = text.slice(0, m.index).trimEnd();
		return m[1] ?? "";
	};
	const c = take(/\s+c:(\d+(?:\.\d+)?)$/);
	const date = take(/\s\((?!id[:\s])([^()]+)\)$/);
	const type = take(/\s\[([^[\]]+)\]$/);
	const id = take(/\s\(id: ([^()]+)\)$/);
	take(/\s\(id unavailable\)$/);
	return { text, type, date, id, score: c === null ? null : Number(c) };
}

/** 结果文本里的命中：null = 不是 recall 的格式（出错、旧版），[] = 明确没有命中。 */
function recallHits(
	text: string,
): { asOf: string | null; entries: RecallEntry[] } | null {
	if (/^No relevant memories found/i.test(text.trim()))
		return { asOf: null, entries: [] };
	if (!/^Found \d+ relevant/.test(text)) return null;
	return {
		asOf: text.match(/\(as of ([^()]+) UTC\)/)?.[1] ?? null,
		entries: text
			.replace(/^[^\n]*\n+/, "")
			.split(/\n{2,}/)
			.map(parseEntry)
			.filter((entry) => entry.text.length > 0),
	};
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const query = str(args.query);
	return query !== null ? (
		<span>{truncate(normalizeWs(query), 96)}</span>
	) : (
		<InvalidArg what="query" />
	);
}

/** 右侧读数：命中条数；没有命中用警告色。 */
function Meta({ result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result || result.isError) return null;
	const hits = recallHits(resultTextOf(result));
	if (!hits) return null;
	const n = hits.entries.length;
	return (
		<span className={n === 0 ? "tv-meta-warn" : undefined}>
			{n === 0
				? t("toolRender.memory.noHits")
				: t("toolRender.memory.hits", { count: n })}
		</span>
	);
}

/** 输入：查询，画成一条搜索框。 */
function Input({ args }: ToolRenderProps): ReactNode {
	const query = str(args.query)?.trim();
	if (!query) return null;
	return (
		<div className="tv-mem-query">
			<Icon name="search" size={12} />
			<span>{query}</span>
		</div>
	);
}

/** 输出：命中的记忆卡片（附「截至」时刻）；没有命中给空状态；出错给错误。 */
function Result({ result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result) return null;
	if (result.isError) return <ErrorBlock text={resultTextOf(result)} />;
	const hits = recallHits(resultTextOf(result));
	if (!hits) return <ResultText result={result} maxLines={12} />;
	if (hits.entries.length === 0)
		return <EmptyNote>{t("toolRender.memory.empty")}</EmptyNote>;
	return (
		<>
			{hits.asOf && (
				<div className="tv-mem-asof">
					{t("toolRender.memory.asOf", { at: `${hits.asOf} UTC` })}
				</div>
			)}
			<ul className="tv-mems">
				{hits.entries.map((entry, i) => (
					<MemoryCard
						key={i}
						content={entry.text}
						tags={[entry.type]}
						date={entry.date}
						id={entry.id}
						score={entry.score}
					/>
				))}
			</ul>
		</>
	);
}

export const recallRenderer: ToolRenderer = { Summary, Meta, Input, Result };
