/**
 * `reflect` — ask the long-term memory backend a question; the answer comes
 * back as plain result text (details are always empty).
 */
import type { ReactNode } from "react";
import { Note, Output, ResultText } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import { normalizeWs, resultTextOf, str, truncate } from "../util";

function Summary({ args }: ToolRenderProps): ReactNode {
	const query = str(args.query);
	return <span>{query ? truncate(normalizeWs(query), 96) : ""}</span>;
}

function Input({ args }: ToolRenderProps): ReactNode {
	const query = str(args.query) ?? "";
	const context = str(args.context) ?? "";
	return (
		<>
			{/* 折叠行只是摘要（会被截断），展开体始终给出完整查询 */}
			{query && <Output text={query} title="query" maxLines={4} />}
			{context && <Output text={context} title="context" maxLines={6} />}
		</>
	);
}

function Result({ result }: ToolRenderProps): ReactNode {
	if (result?.isError === true && !resultTextOf(result))
		return <Note tone="err">Reflect failed</Note>;
	// 记忆后端的回答是 markdown 文本
	return (
		<ResultText result={result} lang="markdown" variant="plain" maxLines={12} />
	);
}

export const reflectRenderer: ToolRenderer = { Summary, Input, Result };
