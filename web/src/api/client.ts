/**
 * REST 客户端：openapi-fetch + 规格生成的类型。
 * - Cookie 会话；变更类请求自动带 `X-Oryn-CSRF: 1`。
 * - 401 时广播 `oryn:unauthorized`，由路由守卫跳转登录页。
 */
import createClient, { type Middleware } from "openapi-fetch";
import type { components, paths } from "./schema";

export type Schemas = components["schemas"];
export type Thread = Schemas["Thread"];
export type ThreadState = Schemas["ThreadState"];
export type Orb = Schemas["Orb"];
export type OrbState = Schemas["OrbState"];
export type Entry = Schemas["Entry"];
export type Project = Schemas["Project"];
export type Provider = Schemas["Provider"];
export type Device = Schemas["Device"];
export type SecretMeta = Schemas["SecretMeta"];
export type AgentConfigFile = Schemas["AgentConfigFile"];
export type AgentStatus = Schemas["AgentStatus"];
export type Me = Schemas["Me"];
export type ImageInput = Schemas["ImageContent"];
export type WsServerMessage = Schemas["WsServerMessage"];
export type WsClientMessage = Schemas["WsClientMessage"];
export type WsThreadSnapshot = Schemas["WsServerThreadSnapshot"];
export type WsEntries = Schemas["WsServerEntries"];
export type OmpFrameObject = Schemas["OmpFrame"];

export const UNAUTHORIZED_EVENT = "oryn:unauthorized";

/** 服务端错误体 `{code, message}` 包装成的异常。 */
export class ApiError extends Error {
	readonly status: number;
	readonly code: string;
	/** 429 时 `Retry-After` 给出的秒数 */
	readonly retryAfter?: number;
	/** 原始错误体（带额外字段的错误，如 409 `ProfileInUseConflict` 的持有者） */
	readonly body?: unknown;
	constructor(
		status: number,
		code: string,
		message: string,
		retryAfter?: number,
		body?: unknown,
	) {
		super(message || code);
		this.name = "ApiError";
		this.status = status;
		this.code = code;
		this.retryAfter = retryAfter;
		this.body = body;
	}
}

export function isMutation(method: string): boolean {
	const m = method.toUpperCase();
	return m !== "GET" && m !== "HEAD" && m !== "OPTIONS";
}

const csrf: Middleware = {
	onRequest({ request }) {
		if (isMutation(request.method)) request.headers.set("X-Oryn-CSRF", "1");
		return request;
	},
	onResponse({ response }) {
		if (response.status === 401 && typeof window !== "undefined") {
			window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
		}
		return response;
	},
};

// 每次调用时取 globalThis.fetch，便于 mock 模式与测试替换。
// baseUrl 用当前 origin 拼成绝对地址：浏览器里与相对路径等价，Node（测试）的 Request 不接受相对 URL。
export const api = createClient<paths>({
	baseUrl: globalThis.location?.origin ?? "",
	credentials: "same-origin",
	fetch: (input: Request) => globalThis.fetch(input),
});
api.use(csrf);

interface FetchResult<T> {
	data?: T;
	error?: unknown;
	response: Response;
}

function toApiError(res: Response, error: unknown): ApiError {
	const status = res.status;
	const fallbackCode =
		status === 429
			? "rate_limited"
			: status === 401
				? "unauthorized"
				: "http_error";
	const ra = Number(res.headers.get("Retry-After"));
	const retryAfter = Number.isFinite(ra) && ra > 0 ? ra : undefined;
	if (error && typeof error === "object") {
		const code =
			"code" in error && typeof error.code === "string"
				? error.code
				: fallbackCode;
		const message =
			"message" in error && typeof error.message === "string"
				? error.message
				: `HTTP ${status}`;
		return new ApiError(status, code, message, retryAfter, error);
	}
	const message = typeof error === "string" && error ? error : `HTTP ${status}`;
	return new ApiError(status, fallbackCode, message, retryAfter);
}

/** 把 openapi-fetch 的 `{data, error}` 展开：失败抛 ApiError。 */
export async function unwrap<T>(p: Promise<FetchResult<T>>): Promise<T> {
	const { data, error, response } = await p;
	if (!response.ok || error !== undefined) throw toApiError(response, error);
	return data as T;
}

/** 不经 openapi-fetch 的原始请求（octet-stream 读写等）。 */
export async function rawFetch(
	path: string,
	init: RequestInit = {},
): Promise<Response> {
	const headers = new Headers(init.headers);
	if (isMutation(init.method ?? "GET")) headers.set("X-Oryn-CSRF", "1");
	const res = await globalThis.fetch(path, {
		credentials: "same-origin",
		...init,
		headers,
	});
	if (res.status === 401 && typeof window !== "undefined")
		window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
	if (!res.ok) {
		let body: unknown;
		try {
			body = await res.json();
		} catch {
			body = undefined;
		}
		throw toApiError(res, body);
	}
	return res;
}

/** agent-config 路径参数：`/` 编码为 `%2F`。 */
export function encodePathParam(path: string): string {
	return encodeURIComponent(path);
}

export function blobUrl(sha256: string): string {
	return `/api/v1/blobs/${sha256}`;
}
