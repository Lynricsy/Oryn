/**
 * `web_search` — 由搜索提供方（或带搜索的模型）完成的网页搜索。结果形状见 oryn-core `web/search/`：
 * `details.response` 带综合答案 `answer`、来源 `sources`（标题、地址、摘要、时效、作者）、引用 `citations`、
 * 相关问题、模型实际搜过的查询、用量与提供方；回退时 `details.failures` 列出失败的提供方。
 *
 * 卡片：头部读数是来源的站点标记 + 来源数；展开体先是 Markdown 渲染的答案（原样，不按位置给 `[n]` 补链接：
 * Core 会按查询约束单独过滤 `sources`，答案里的编号未必还对得上），
 * 再是来源卡片网格，之后引用、相关问题与查询，最末一行灰字给出提供方与用量。
 * 结果文本（给模型看的、末尾附来源清单的纯文本）只在没有结构化 details 时才渲染。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/ui/icons";
import { formatRelative, formatTokens } from "../../lib/format";
import { Badge, InvalidArg, MarkdownBlock, Note, SiteMark } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	hostOf,
	isRecord,
	normalizeWs,
	num,
	resultTextOf,
	str,
	truncate,
} from "../util";

interface Source {
	url: string;
	title: string;
	host: string;
	snippet: string;
	age: string;
	author: string;
}

interface Citation {
	url: string;
	title: string;
	text: string;
}

interface SearchView {
	answer: string;
	sources: Source[];
	citations: Citation[];
	related: string[];
	queries: string[];
	provider: string;
	model: string;
	authMode: string;
	usage: {
		input: number | null;
		output: number | null;
		total: number | null;
		searches: number | null;
	};
	failures: { provider: string; reason: string }[];
	error: string;
}

const RECENCY = ["day", "week", "month", "year"] as const;

function strings(value: unknown): string[] {
	return Array.isArray(value)
		? value.filter((v): v is string => typeof v === "string" && v.trim() !== "")
		: [];
}

/** 来源的时效：`ageSeconds` 按界面语言写成相对时间，否则原样用 `publishedDate`。 */
function ageOf(source: Record<string, unknown>): string {
	const seconds = num(source.ageSeconds);
	if (seconds !== null && seconds >= 0)
		return formatRelative(Date.now() - seconds * 1000);
	return str(source.publishedDate) ?? "";
}

function viewOf(details: Record<string, unknown> | null): SearchView | null {
	const response =
		details && isRecord(details.response) ? details.response : null;
	if (!details || !response) return null;
	const sources: Source[] = [];
	if (Array.isArray(response.sources)) {
		for (const s of response.sources) {
			if (!isRecord(s)) continue;
			const url = str(s.url) ?? "";
			sources.push({
				url,
				title: normalizeWs(str(s.title) ?? ""),
				host: hostOf(url),
				snippet: normalizeWs(str(s.snippet) ?? ""),
				age: ageOf(s),
				author: str(s.author) ?? "",
			});
		}
	}
	const citations: Citation[] = [];
	if (Array.isArray(response.citations)) {
		for (const c of response.citations) {
			if (!isRecord(c)) continue;
			const url = str(c.url) ?? "";
			if (!url) continue;
			citations.push({
				url,
				title: normalizeWs(str(c.title) ?? ""),
				text: normalizeWs(str(c.citedText) ?? ""),
			});
		}
	}
	const usage = isRecord(response.usage) ? response.usage : {};
	const failures: SearchView["failures"] = [];
	if (Array.isArray(details.failures)) {
		for (const f of details.failures) {
			if (!isRecord(f)) continue;
			failures.push({
				provider: str(f.label) ?? str(f.provider) ?? "?",
				reason: str(f.reason) ?? "",
			});
		}
	}
	return {
		answer: str(response.answer)?.trim() ?? "",
		sources,
		citations,
		related: strings(response.relatedQuestions),
		queries: strings(response.searchQueries),
		provider: str(response.provider) ?? "",
		model: str(response.model) ?? "",
		authMode: str(response.authMode) ?? "",
		usage: {
			input: num(usage.inputTokens),
			output: num(usage.outputTokens),
			total: num(usage.totalTokens),
			searches: num(usage.searchRequests),
		},
		failures,
		error: str(details.error) ?? "",
	};
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const query = str(args.query);
	const recency = str(args.recency);
	return (
		<>
			{query === null ? (
				<InvalidArg what="query" />
			) : (
				<span className="tv-pattern">{truncate(normalizeWs(query), 80)}</span>
			)}
			{recency && (
				<Badge>
					{(RECENCY as readonly string[]).includes(recency)
						? t(
								`toolRender.webSearch.recency.${recency as (typeof RECENCY)[number]}`,
							)
						: recency}
				</Badge>
			)}
		</>
	);
}

/** 头部读数：前三个不同站点的标记叠放 + 来源数；失败时红字。 */
function Meta({ result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const view = viewOf(detailsRecord(result));
	if (!view) return null;
	if (result?.isError || (view.error && view.sources.length === 0))
		return (
			<span className="tv-meta-err">{t("toolRender.webSearch.noResults")}</span>
		);
	const hosts = [...new Set(view.sources.map((s) => s.host).filter(Boolean))];
	if (view.sources.length === 0)
		return view.answer ? null : (
			<span className="tv-meta-warn">
				{t("toolRender.webSearch.noResults")}
			</span>
		);
	return (
		<>
			<span className="tv-ws-stack" aria-hidden="true">
				{hosts.slice(0, 3).map((host) => (
					<SiteMark key={host} host={host} />
				))}
			</span>
			<span>
				{t("toolRender.webSearch.sources", { count: view.sources.length })}
			</span>
		</>
	);
}

/** 输入：像一个搜索框，查询全文 + 时效、条数等选项。 */
function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const query = str(args.query);
	const recency = str(args.recency);
	const limit = num(args.limit) ?? num(args.num_search_results);
	if (query === null) return null;
	return (
		<div className="tv-ws-query">
			<Icon name="search" size={14} className="tv-ws-query-icon" />
			<span className="tv-ws-query-text">{query}</span>
			{recency && (RECENCY as readonly string[]).includes(recency) && (
				<span className="tv-ws-query-opt">
					{t(
						`toolRender.webSearch.recency.${recency as (typeof RECENCY)[number]}`,
					)}
				</span>
			)}
			{limit !== null && (
				<span className="tv-ws-query-opt">
					{t("toolRender.webSearch.limit", { count: limit })}
				</span>
			)}
		</div>
	);
}

function SourceCard({
	source,
	index,
}: {
	source: Source;
	index: number;
}): ReactNode {
	const { t } = useTranslation();
	const title =
		source.title ||
		source.host ||
		source.url ||
		t("toolRender.webSearch.untitled");
	const meta = [source.host, source.author, source.age].filter(Boolean);
	const body = (
		<>
			<span className="tv-ws-src-head">
				<span className="tv-ws-src-num">{index + 1}</span>
				<SiteMark host={source.host || title} />
				<span className="tv-ws-src-meta">{meta.join(" · ")}</span>
			</span>
			<span className="tv-ws-src-title">{title}</span>
			{source.snippet && (
				<span className="tv-ws-src-snippet">{source.snippet}</span>
			)}
		</>
	);
	return source.url ? (
		<a
			className="tv-ws-src"
			href={source.url}
			target="_blank"
			rel="noopener noreferrer"
			title={source.url}
		>
			{body}
		</a>
	) : (
		<div className="tv-ws-src">{body}</div>
	);
}

function SectionLabel({ children }: { children: ReactNode }): ReactNode {
	return <div className="tv-ws-label">{children}</div>;
}

function Result({ result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const view = viewOf(detailsRecord(result));
	const text = resultTextOf(result).trim();
	// 没有结构化结果（旧转录、提供方异常）：结果文本按 Markdown 渲染，错误按错误样式
	if (!view) {
		if (!text) return null;
		return result?.isError ? (
			<Note tone="err">{text}</Note>
		) : (
			<MarkdownBlock text={text} images={false} />
		);
	}
	const usage: string[] = [];
	if (view.usage.input !== null && view.usage.output !== null)
		usage.push(
			t("toolRender.webSearch.tokens", {
				input: formatTokens(view.usage.input),
				output: formatTokens(view.usage.output),
			}),
		);
	else if (view.usage.total !== null)
		usage.push(formatTokens(view.usage.total));
	if (view.usage.searches !== null && view.usage.searches > 0)
		usage.push(
			t("toolRender.webSearch.searches", { count: view.usage.searches }),
		);
	const auth =
		view.authMode === "oauth"
			? "OAuth"
			: view.authMode === "api_key"
				? "API key"
				: view.authMode;
	const provider = [view.model, view.provider !== "none" && view.provider, auth]
		.filter(Boolean)
		.join(" · ");
	const empty =
		!view.answer && view.sources.length === 0 && view.citations.length === 0;
	return (
		<>
			{view.failures.length > 0 && (
				<Note tone="warn">
					<span
						title={view.failures
							.map((f) => `${f.provider}: ${f.reason}`)
							.join("\n")}
					>
						{t("toolRender.webSearch.fallback", {
							count: view.failures.length,
						})}
						{": "}
						{view.failures.map((f) => f.provider).join(", ")}
					</span>
				</Note>
			)}
			{(view.error || result?.isError) && (
				<Note tone="err">{view.error || text}</Note>
			)}
			{view.answer && (
				<div className="tv-ws-answer">
					<MarkdownBlock text={view.answer} images={false} />
				</div>
			)}
			{empty && !view.error && !result?.isError && (
				<div className="tv-ws-empty">
					<Icon name="search" size={14} />
					{t("toolRender.webSearch.noResults")}
				</div>
			)}
			{view.sources.length > 0 && (
				<div className="tv-ws-block">
					{view.answer && (
						<SectionLabel>
							{t("toolRender.webSearch.sourcesTitle")}
						</SectionLabel>
					)}
					<div className="tv-ws-sources">
						{view.sources.map((source, i) => (
							<SourceCard
								key={`${i}:${source.url}`}
								source={source}
								index={i}
							/>
						))}
					</div>
				</div>
			)}
			{view.citations.length > 0 && (
				<div className="tv-ws-block">
					<SectionLabel>{t("toolRender.webSearch.citations")}</SectionLabel>
					<div className="tv-ws-cites">
						{view.citations.map((c, i) => (
							<a
								key={`${i}:${c.url}`}
								className="tv-ws-cite"
								href={c.url}
								target="_blank"
								rel="noopener noreferrer"
							>
								<span className="tv-ws-cite-title">
									<SiteMark host={hostOf(c.url) || c.url} />
									{c.title || hostOf(c.url) || c.url}
								</span>
								{c.text && <q className="tv-ws-cite-text">{c.text}</q>}
							</a>
						))}
					</div>
				</div>
			)}
			{view.related.length > 0 && (
				<div className="tv-ws-block">
					<SectionLabel>{t("toolRender.webSearch.related")}</SectionLabel>
					<div className="tv-ws-related">
						{view.related.map((q, i) => (
							<span key={i} className="tv-ws-related-item">
								<Icon name="arrowUpRight" size={11} />
								{q}
							</span>
						))}
					</div>
				</div>
			)}
			{view.queries.length > 0 && (
				<div className="tv-ws-foot-queries">
					{t("toolRender.webSearch.queries")}
					{view.queries.map((q, i) => (
						<span key={i} className="tv-ws-foot-query">
							{q}
						</span>
					))}
				</div>
			)}
			{(provider || usage.length > 0) && (
				<div className="tv-ws-foot">
					{provider && <span>{provider}</span>}
					{usage.map((u) => (
						<span key={u}>{u}</span>
					))}
				</div>
			)}
		</>
	);
}

export const webSearchRenderer: ToolRenderer = { Summary, Meta, Input, Result };
