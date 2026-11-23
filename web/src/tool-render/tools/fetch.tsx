/**
 * `fetch` — 以阅读模式抓取网址：输出是去掉页面杂物后的 Markdown（或 JSON / 原文），前面带一段元数据头。
 * 结果形状见 oryn-core `tools/fetch.ts`：details 带 `url`、`finalUrl`、`contentType`、`method`（提取方式，
 * 如 readability、github-raw、各站点抓取器）、`truncated` 与 `notes`。
 *
 * 卡片：头部摘要是去掉协议的地址，读数是内容类型与截断；展开体输入段是一条地址栏，输出段先给重定向与
 * 提取方式，再按内容类型渲染正文：Markdown 直接排版（超高折叠），JSON 与代码高亮。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/ui/icons";
import { normalizeLanguage } from "../../lib/highlight";
import {
	Badge,
	InvalidArg,
	MarkdownBlock,
	Note,
	Output,
	ResultImages,
	SiteMark,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	hostOf,
	num,
	resultTextOf,
	str,
	truncate,
} from "../util";

/** Subset of the tool's `ReadUrlToolDetails` we render. */
interface FetchDetails {
	url: string | null;
	finalUrl: string | null;
	contentType: string | null;
	method: string | null;
	truncated: boolean;
	notes: string[];
}

function fetchDetails(
	record: Record<string, unknown> | null,
): FetchDetails | null {
	if (!record) return null;
	const notes: string[] = [];
	if (Array.isArray(record.notes)) {
		for (const note of record.notes) {
			const text = str(note);
			if (text) notes.push(text);
		}
	}
	return {
		url: str(record.url),
		finalUrl: str(record.finalUrl),
		contentType: str(record.contentType),
		method: str(record.method),
		truncated: record.truncated === true,
		notes,
	};
}

/** 地址拆成协议、主机与其余部分（解析不了时整串当作「其余」）。 */
function splitUrl(url: string): { scheme: string; host: string; rest: string } {
	try {
		const u = new URL(url);
		return {
			scheme: `${u.protocol}//`,
			host: u.host,
			rest: `${u.pathname === "/" ? "" : u.pathname}${u.search}${u.hash}`,
		};
	} catch {
		return { scheme: "", host: "", rest: url };
	}
}

/** 一行地址：协议弱化、主机正文色、路径次一级；整串可换行。 */
function UrlText({ url }: { url: string }): ReactNode {
	const { scheme, host, rest } = splitUrl(url);
	return (
		<span className="tv-url">
			{scheme && <span className="tv-url-scheme">{scheme}</span>}
			{host && <span className="tv-url-host">{host}</span>}
			<span className="tv-url-rest">{rest}</span>
		</span>
	);
}

/** 内容类型的短名：`text/html; charset=…` → `HTML`，`application/vnd.api+json` → `JSON`。 */
function shortType(contentType: string | null): string | null {
	if (!contentType) return null;
	const mime = (contentType.split(";", 1)[0] ?? "").trim().toLowerCase();
	if (!mime) return null;
	const subtype = mime.slice(mime.indexOf("/") + 1);
	const name = subtype.slice(subtype.lastIndexOf("+") + 1).replace(/^x-/, "");
	if (name === "plain") return "Text";
	return name.length <= 8 ? name.toUpperCase() : name;
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const url = str(args.url) ?? str(args.path);
	const method = (str(args.method) ?? "").toUpperCase();
	const { host, rest } = url ? splitUrl(url) : { host: "", rest: "" };
	return (
		<>
			{url ? (
				<span className="tv-path">
					{host}
					<span className="tv-faint">{truncate(rest, 90)}</span>
				</span>
			) : (
				<InvalidArg what="url" />
			)}
			{method && method !== "GET" && <Badge tone="accent">{method}</Badge>}
			{args.raw === true && <Badge>raw</Badge>}
		</>
	);
}

/** 右侧读数：内容类型短名；被截断时警示色（收起时头部显示意图，读数仍然看得见）。 */
function Meta({ result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const details = fetchDetails(detailsRecord(result));
	if (!details) return null;
	const type = shortType(details.contentType);
	return (
		<>
			{type && <span>{type}</span>}
			{details.truncated && (
				<span className="tv-meta-warn">{t("toolRender.fetch.truncated")}</span>
			)}
		</>
	);
}

/** 输入：一条地址栏（站点标记 + 完整地址）与超时、方法等选项。 */
function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const url = str(args.url) ?? str(args.path);
	const timeout = num(args.timeout);
	const method = (str(args.method) ?? "").toUpperCase();
	if (url === null) return <InvalidArg what="url" />;
	return (
		<div className="tv-addr">
			<SiteMark host={hostOf(url) || url} />
			<UrlText url={url} />
			{method && method !== "GET" && (
				<span className="tv-addr-opt">{method}</span>
			)}
			{args.raw === true && (
				<span className="tv-addr-opt">{t("toolRender.fetch.raw")}</span>
			)}
			{timeout !== null && (
				<span className="tv-addr-opt">
					{t("toolRender.fetch.timeout", { seconds: timeout })}
				</span>
			)}
		</div>
	);
}

/**
 * 读取模式的输出带元数据头（工具的 buildUrlReadOutput）：`URL: …` 开头、每行 `Key: value`，以空行与 `---` 分隔线结束。
 * 头里的信息已在读数与信息行里，这里只取正文；头核对不上时原样返回，不误删内容。
 */
function stripEnvelope(text: string): string {
	if (!text.startsWith("URL: ")) return text;
	const sep = text.indexOf("\n\n---\n\n");
	if (sep === -1) return text;
	const header = text.slice(0, sep).split("\n");
	if (!header.every((line) => /^[A-Z][A-Za-z-]*: /.test(line))) return text;
	return text.slice(sep + 7);
}

/** 响应内容类型对应的 highlight.js 语言：取子类型（`+json` / `+xml` 后缀优先，去掉 `x-` 前缀）；未知返回 null。 */
function contentTypeLanguage(contentType: string | null): string | null {
	if (!contentType) return null;
	const mime = (contentType.split(";", 1)[0] ?? "").trim().toLowerCase();
	const subtype = mime.slice(mime.indexOf("/") + 1);
	const name = subtype.slice(subtype.lastIndexOf("+") + 1).replace(/^x-/, "");
	return normalizeLanguage(name === "ecmascript" ? "javascript" : name);
}

/** 正文本身能解析成 JSON 对象 / 数组。 */
function parsesAsJson(body: string): boolean {
	if (!body.startsWith("{") && !body.startsWith("[")) return false;
	try {
		JSON.parse(body);
		return true;
	} catch {
		return false;
	}
}

/**
 * 正文的高亮语言：raw 与读取模式里原样返回的文本（`via text`）按响应内容类型；读取模式的 JSON 按 JSON；
 * 其余读取模式输出（HTML 提取、feed、文档转换）是 Markdown。raw 的未知类型不高亮。
 */
function bodyLanguage(
	details: FetchDetails | null,
	raw: boolean,
	body: string,
): string | null {
	if (raw)
		return (
			contentTypeLanguage(details?.contentType ?? null) ??
			(parsesAsJson(body) ? "json" : null)
		);
	if (details?.method === "json" || parsesAsJson(body)) return "json";
	if (details?.method === "text")
		return contentTypeLanguage(details.contentType) ?? "markdown";
	return "markdown";
}

function Result({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const details = fetchDetails(detailsRecord(result));
	const redirected = Boolean(
		details?.finalUrl && details.url && details.finalUrl !== details.url,
	);
	// raw 原样返回响应体，没有元数据头：不剥，免得误删以 `URL: ` 开头的内容
	const raw = args.raw === true || details?.method === "raw";
	const text = resultTextOf(result).trim();
	const body = raw ? text : stripEnvelope(text).trim();
	const lang = bodyLanguage(details, raw, body);
	const chips = [details?.contentType, details?.method].filter(
		(v): v is string => Boolean(v),
	);
	return (
		<>
			{redirected && details?.finalUrl && (
				<div className="tv-fetch-redirect">
					<Icon name="arrowUpRight" size={12} />
					<span className="tv-fetch-redirect-label">
						{t("toolRender.fetch.redirected")}
					</span>
					<a href={details.finalUrl} target="_blank" rel="noopener noreferrer">
						<UrlText url={details.finalUrl} />
					</a>
				</div>
			)}
			{(chips.length > 0 || (details?.notes.length ?? 0) > 0) && (
				<div className="tv-fetch-info">
					{chips.map((c) => (
						<span key={c} className="tv-fetch-chip">
							{c}
						</span>
					))}
					{details?.notes.map((note) => (
						<span key={note} className="tv-fetch-note">
							{note}
						</span>
					))}
				</div>
			)}
			<ResultImages result={result} />
			{result?.isError ? (
				<Note tone="err">{text}</Note>
			) : !body ? (
				result && (
					<span className="tv-faint tv-sans">
						{t("toolRender.fetch.empty")}
					</span>
				)
			) : lang === "markdown" ? (
				<div className="tv-fetch-doc">
					<MarkdownBlock text={body} maxHeight={320} images={false} />
				</div>
			) : (
				<Output
					text={body}
					lang={lang}
					variant={lang ? "code" : "plain"}
					maxLines={14}
				/>
			)}
		</>
	);
}

export const fetchRenderer: ToolRenderer = { Summary, Meta, Input, Result };
