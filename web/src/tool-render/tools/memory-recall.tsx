/**
 * `recall` — search long-term memory. Details are always empty; everything
 * lives in the result text: a `Found N relevant memories (as of … UTC):`
 * header followed by blank-line-separated `- <text> [type] (date)` bullets.
 */
import type { ReactNode } from "react";
import {
	Badges,
	InvalidArg,
	MetaCount,
	Output,
	ResultText,
	Row,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import { normalizeWs, resultTextOf, str, truncate } from "../util";

function foundCount(props: ToolRenderProps): number | null {
	const { result } = props;
	if (!result || result.isError) return null;
	const match = resultTextOf(result).match(/^Found (\d+) relevant/);
	return match ? Number(match[1]) : 0;
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
function Meta(props: ToolRenderProps): ReactNode {
	const found = foundCount(props);
	return (
		<MetaCount
			unit="matches"
			count={found}
			tone={found === 0 ? "warn" : undefined}
		/>
	);
}

interface RecallEntry {
	text: string;
	type: string | null;
	date: string | null;
}

/** Best-effort parse of one `- <text> [type] (date)` bullet. */
function parseEntry(raw: string): RecallEntry {
	let text = raw.replace(/^-\s+/, "").trim();
	let date: string | null = null;
	let type: string | null = null;
	const dateMatch = text.match(/\s\(([^()]+)\)$/);
	if (dateMatch) {
		date = dateMatch[1] ?? null;
		text = text.slice(0, -dateMatch[0].length);
	}
	const typeMatch = text.match(/\s\[([^[\]]+)\]$/);
	if (typeMatch) {
		type = typeMatch[1] ?? null;
		text = text.slice(0, -typeMatch[0].length);
	}
	return { text, type, date };
}

function Input({ args }: ToolRenderProps): ReactNode {
	const query = str(args.query) ?? "";
	// 折叠行只是摘要（会被截断），展开体始终给出完整查询
	return query ? <Output text={query} title="query" maxLines={4} /> : null;
}

function Result(props: ToolRenderProps): ReactNode {
	const { result } = props;
	const text = resultTextOf(result);
	const found = foundCount(props);
	let asOf: string | null = null;
	let entries: RecallEntry[] = [];
	if (found !== null && found > 0) {
		asOf = text.match(/\(as of ([^()]+) UTC\)/)?.[1] ?? null;
		entries = text
			.replace(/^[^\n]*\n+/, "")
			.split(/\n{2,}/)
			.map(parseEntry)
			.filter((entry) => entry.text.length > 0);
	}
	if (entries.length === 0) return <ResultText result={result} maxLines={12} />;
	return (
		<>
			{asOf && <Badges items={[`as of ${asOf} UTC`]} />}
			<div className="tv-list">
				{entries.map((entry, i) => (
					<Row key={i}>
						<span>{entry.text}</span>
						{(entry.type !== null || entry.date !== null) && (
							<Badges items={[entry.type, entry.date]} />
						)}
					</Row>
				))}
			</div>
		</>
	);
}

export const recallRenderer: ToolRenderer = { Summary, Meta, Input, Result };
