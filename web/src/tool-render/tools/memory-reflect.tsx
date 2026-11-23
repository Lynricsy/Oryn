/**
 * `reflect` — ask the long-term memory backend a question; the answer comes
 * back as plain result text (details are always empty). mnemopi 的回答带
 * `Based on recalled memories:` 前缀，找不到时是 `No relevant information found to reflect on.`（见 oryn-core）。
 * 输入段是问题（附补充上下文），输出段是 Markdown 渲染的回答。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/ui/icons";
import { EmptyNote, ErrorBlock, MarkdownBlock } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import { normalizeWs, resultTextOf, str, truncate } from "../util";

const BASED_ON = /^Based on recalled memories:\s*/i;
const NOTHING = /^No relevant information found to reflect on\.?$/i;

function Summary({ args }: ToolRenderProps): ReactNode {
	const query = str(args.query);
	return <span>{query ? truncate(normalizeWs(query), 96) : ""}</span>;
}

/** 输入：问题（醒目）+ 补充上下文（弱化）。 */
function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const query = str(args.query)?.trim();
	const context = str(args.context)?.trim();
	if (!query && !context) return null;
	return (
		<div className="tv-reflect-q">
			{query && (
				<div className="tv-reflect-question">
					<Icon name="lightbulb" size={13} />
					<span>{query}</span>
				</div>
			)}
			{context && (
				<div className="tv-reflect-context">
					<span className="tv-reflect-label">
						{t("toolRender.memory.context")}
					</span>
					<span>{context}</span>
				</div>
			)}
		</div>
	);
}

/** 输出：回答（Markdown，超高折叠；「基于已召回的记忆」作为小标）；没有可反思的内容给空状态。 */
function Result({ result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result) return null;
	const text = resultTextOf(result).trim();
	if (result.isError)
		return <ErrorBlock text={text || t("toolRender.memory.reflectFailed")} />;
	if (!text || NOTHING.test(text))
		return <EmptyNote>{t("toolRender.memory.nothingToReflect")}</EmptyNote>;
	const based = BASED_ON.test(text);
	return (
		<>
			{based && (
				<div className="tv-mem-asof">{t("toolRender.memory.basedOn")}</div>
			)}
			<MarkdownBlock text={text.replace(BASED_ON, "")} maxHeight={320} />
		</>
	);
}

export const reflectRenderer: ToolRenderer = { Summary, Input, Result };
