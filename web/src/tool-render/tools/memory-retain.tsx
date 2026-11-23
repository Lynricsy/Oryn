/**
 * `retain` — store facts in long-term memory (one item per stored fact). 参数 `items[]`：
 * `{ content, context?, scope?: project | global }`；结果 `N memories stored.`（同步写入）或 `queued.`（排队），
 * details `{ count }`（见 oryn-core `tools/memory-retain.ts`）。输入段是将写入的记忆卡片（带写入标记），
 * 头部读数给已存 / 已排队条数。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ErrorBlock, InvalidArg, ResultText } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	isRecord,
	normalizeWs,
	num,
	replaceTabs,
	resultTextOf,
	str,
	truncate,
} from "../util";
import { MemoryCard } from "./memory-recall";

interface RetainItem {
	content: string;
	context: string | null;
	scope: string | null;
}

/** Narrow `args.items`; null = present but malformed, [] = absent/empty. */
function retainItems(args: Record<string, unknown>): RetainItem[] | null {
	const raw = args.items;
	if (raw === undefined) return [];
	if (!Array.isArray(raw)) return null;
	const items: RetainItem[] = [];
	for (const entry of raw) {
		if (!isRecord(entry)) continue;
		const content = replaceTabs((str(entry.content) ?? "").trim());
		if (!content) continue;
		items.push({
			content,
			context: str(entry.context),
			scope: str(entry.scope),
		});
	}
	return items;
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const items = retainItems(args);
	if (items === null) return <InvalidArg what="items" />;
	const first = items[0] ? truncate(normalizeWs(items[0].content), 80) : "";
	return first ? <span className="tv-trunc">{first}</span> : null;
}

/** 右侧读数：已存 / 已排队 n 条（绿）；没结果时写将要写入的条数。 */
function Meta({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const items = retainItems(args) ?? [];
	if (result?.isError) return null;
	if (!result)
		return items.length > 1 ? (
			<span>{t("toolRender.memory.items", { count: items.length })}</span>
		) : null;
	const count = num(detailsRecord(result)?.count) ?? items.length;
	const queued = /\bqueued\b/i.test(resultTextOf(result));
	return (
		<span className="tv-meta-ok">
			{t(queued ? "toolRender.memory.queued" : "toolRender.memory.stored", {
				count,
			})}
		</span>
	);
}

/** 输入：要写入的记忆卡片（写入标记 + 范围标签 + 来源上下文）。 */
function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const items = retainItems(args);
	if (items === null) return <InvalidArg what="items" />;
	if (items.length === 0) return null;
	return (
		<ul className="tv-mems">
			{items.map((item, i) => (
				<MemoryCard
					key={i}
					added
					content={item.content}
					tags={[
						item.scope === "global"
							? t("toolRender.memory.scope.global")
							: item.scope === "project"
								? t("toolRender.memory.scope.project")
								: item.scope,
					]}
					note={item.context}
				/>
			))}
		</ul>
	);
}

/** 输出：出错时给错误；成功的确认已在头部读数里，其余文本（非标准回执）原样给出。 */
function Result({ result }: ToolRenderProps): ReactNode {
	if (!result) return null;
	const text = resultTextOf(result).trim();
	if (result.isError) return <ErrorBlock text={text} />;
	return /^\d+ memor(?:y|ies) (?:stored|queued)\.?$/i.test(text) ||
		text === "" ? null : (
		<ResultText result={result} maxLines={6} />
	);
}

export const retainRenderer: ToolRenderer = { Summary, Meta, Input, Result };
