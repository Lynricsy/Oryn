/**
 * M5 各步骤（读取分享、开关推送、保存 / 测试通知渠道）失败时的诊断：把错误归成一个原因并给一句下一步建议，
 * 原始错误串另行留作「详细信息」。守则：失败提示要说清「哪一步、为什么、下一步怎么做」。
 *
 * - 控制面请求失败（`diagnoseRequestError`）：`ApiError` 先看 M5 的错误码（`invalid_channel` / `channel_exists` /
 *   `channel_not_linked` / `push_invalid`），再看 HTTP 状态；`fetch` 的 `TypeError` = 连不上服务器；
 *   浏览器推送 API 的 `NotAllowedError` = 通知权限被拒，`AbortError` / `InvalidStateError` = 浏览器推送服务不可用。
 * - 渠道投递失败（`diagnoseDeliveryError`）：服务端 `TestChannel` 回传的发送错误，格式见 `internal/notify`：
 *   `HTTP <状态>: <正文>`、`bark: code <n>: …`、`telegram <方法>: …`，或 Go 网络错误（超时、DNS、拒绝连接、证书）。
 */
import { ApiError } from "../api/client";

export type ActionStep =
	| "shareLoad"
	| "pushEnable"
	| "pushDisable"
	| "pushList"
	| "pushRemove"
	| "channelSave"
	| "channelTest"
	/** 桌面壳里读取 `/me` 失败（非 401）：应用壳显示失败与重试，见 AppShell */
	| "meLoad";

export type RequestReason =
	| "offline"
	| "unauthorized"
	| "notFound"
	| "rateLimited"
	| "server"
	| "invalidChannel"
	| "channelExists"
	| "channelNotLinked"
	| "pushInvalid"
	| "permission"
	| "pushService"
	| "unsupported"
	| "unknown";

export type DeliveryReason =
	| "rejected"
	| "notFound"
	| "rateLimited"
	| "server"
	| "timeout"
	| "unreachable"
	| "tls"
	| "unknown";

export interface Diagnosis<R extends string> {
	reason: R;
	/** 原始错误串（给「详细信息」） */
	detail: string;
}

const CODE_REASON: Record<string, RequestReason> = {
	invalid_channel: "invalidChannel",
	channel_exists: "channelExists",
	channel_not_linked: "channelNotLinked",
	push_invalid: "pushInvalid",
	unauthorized: "unauthorized",
	rate_limited: "rateLimited",
	not_found: "notFound",
};

export function diagnoseRequestError(error: unknown): Diagnosis<RequestReason> {
	// DOMException 不一定是 Error 的实例（jsdom 等环境），按 name / message 字段判断
	const e = (typeof error === "object" && error) || {};
	const name = "name" in e ? String(e.name) : "";
	const detail = "message" in e ? String(e.message) : String(error ?? "");
	if (error instanceof ApiError) {
		const byCode = CODE_REASON[error.code];
		if (byCode) return { reason: byCode, detail };
		if (error.status === 401) return { reason: "unauthorized", detail };
		if (error.status === 404) return { reason: "notFound", detail };
		if (error.status === 429) return { reason: "rateLimited", detail };
		if (error.status >= 500) return { reason: "server", detail };
		return { reason: "unknown", detail };
	}
	if (error instanceof TypeError) return { reason: "offline", detail };
	if (name === "NotAllowedError") return { reason: "permission", detail };
	if (name === "AbortError" || name === "InvalidStateError")
		return { reason: "pushService", detail };
	if (detail === "push not supported") return { reason: "unsupported", detail };
	return { reason: "unknown", detail };
}

const HTTP_RE = /^HTTP (\d{3})\b/;

export function diagnoseDeliveryError(
	message: string,
): Diagnosis<DeliveryReason> {
	const detail = message;
	const http = HTTP_RE.exec(message);
	if (http) {
		const status = Number(http[1]);
		if (status === 401 || status === 403) return { reason: "rejected", detail };
		if (status === 404 || status === 410) return { reason: "notFound", detail };
		if (status === 429) return { reason: "rateLimited", detail };
		if (status >= 500) return { reason: "server", detail };
		return { reason: "rejected", detail };
	}
	if (/deadline exceeded|timeout|timed out/i.test(message))
		return { reason: "timeout", detail };
	if (/x509|tls:|certificate/i.test(message)) return { reason: "tls", detail };
	if (
		/no such host|connection refused|dial tcp|network is unreachable|request failed/i.test(
			message,
		)
	)
		return { reason: "unreachable", detail };
	if (/^bark: code|unauthorized|forbidden|blocked/i.test(message))
		return { reason: "rejected", detail };
	return { reason: "unknown", detail };
}
