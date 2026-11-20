/** `fetch` — reader-mode URL fetch; output is markdown with a metadata header. */
import type { ReactNode } from "react";
import { normalizeLanguage } from "../../lib/highlight";
import {
	Badge,
	InvalidArg,
	Kv,
	KvGrid,
	Output,
	ResultImages,
	ResultText,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import { detailsRecord, num, resultTextOf, str, truncate } from "../util";

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

function Summary({ args }: ToolRenderProps): ReactNode {
	const url = str(args.url) ?? str(args.path);
	const method = (str(args.method) ?? "").toUpperCase();
	return (
		<>
			{url ? (
				<span className="tv-path">{truncate(url, 90)}</span>
			) : (
				<InvalidArg what="url" />
			)}
			{method && method !== "GET" && (
				<>
					{" "}
					<Badge tone="accent">{method}</Badge>
				</>
			)}
			{args.raw === true && (
				<>
					{" "}
					<Badge>raw</Badge>
				</>
			)}
		</>
	);
}

/** 右侧读数：内容被截断（收起时头部显示意图，截断标记放在读数里才一直看得见）。 */
function Meta({ result }: ToolRenderProps): ReactNode {
	return fetchDetails(detailsRecord(result))?.truncated ? (
		<span className="tv-meta-warn">truncated</span>
	) : null;
}

function Input({ args }: ToolRenderProps): ReactNode {
	const url = str(args.url) ?? str(args.path);
	// 非 GET 方法与 raw 在展开后的头部摘要里；地址在摘要里会被截断，这里始终给出全文
	const timeout = num(args.timeout);
	return (
		<KvGrid>
			<Kv k="url">{url === null ? <InvalidArg what="url" /> : url}</Kv>
			<Kv k="timeout">{timeout != null && `${timeout}s`}</Kv>
		</KvGrid>
	);
}

/**
 * 读取模式的输出带元数据头（工具的 buildUrlReadOutput）：`URL: …` 开头、每行 `Key: value`，以空行与 `---` 分隔线结束。
 * 头里的信息已在读数表里，这里只取正文；头核对不上时原样返回，不误删内容。
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
	const details = fetchDetails(detailsRecord(result));
	const redirected = Boolean(
		details?.finalUrl && details.url && details.finalUrl !== details.url,
	);
	const notes = details && details.notes.length > 0 && details.notes.join("; ");
	// raw 原样返回响应体，没有元数据头：不剥，免得误删以 `URL: ` 开头的内容
	const raw = args.raw === true || details?.method === "raw";
	const text = resultTextOf(result).trim();
	const body = raw ? text : stripEnvelope(text);
	const lang = bodyLanguage(details, raw, body);
	return (
		<>
			{(redirected || details?.contentType || details?.method || notes) && (
				<KvGrid>
					<Kv k="final url">{redirected && details?.finalUrl}</Kv>
					<Kv k="content-type">{details?.contentType}</Kv>
					<Kv k="via">{details?.method}</Kv>
					<Kv k="notes">{notes}</Kv>
				</KvGrid>
			)}
			<ResultImages result={result} />
			{result?.isError ? (
				<ResultText result={result} maxLines={12} />
			) : (
				body && (
					<Output
						text={body}
						lang={lang}
						variant={lang ? "code" : "plain"}
						maxLines={12}
					/>
				)
			)}
		</>
	);
}

export const fetchRenderer: ToolRenderer = { Summary, Meta, Input, Result };
