/**
 * AI 数据共享同意（App Store 审核指南 5.1.2(i)）：`GET / PUT / DELETE /api/v1/settings/ai-data-consent`。
 * 服务端按当前配置推导接收方（模型 provider、记忆接口、助手联网）；`required` 为真时 AiDataConsentGate 整页拦住。
 * 刷新时机：窗口重新获得焦点时，以及模型配置、记忆、助手设置相关的查询刷新成功后（新接收方出现即重新征求同意）。
 */
import {
	type QueryClient,
	type UseQueryResult,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { useEffect } from "react";
import { api, type Schemas, unwrap } from "./client";

export type AiDataConsent = Schemas["AiDataConsent"];
export type AiDataRecipient = Schemas["AiDataRecipient"];
export type AiDataPurpose = Schemas["AiDataPurpose"];

export const aiDataConsentKey = ["settings", "ai-data-consent"] as const;

/** 刷新成功后可能改变接收方的查询（前缀匹配）：models.yml、模型目录、记忆与助手记忆设置、助手联网开关。 */
const SOURCE_KEYS: readonly (readonly string[])[] = [
	["agent-config", "files"],
	["agent-config", "text"],
	["model-catalog"],
	["memory", "settings"],
	["memory", "models"],
	["memory", "endpoints"],
	["owner-memory", "settings"],
	["settings", "assistant"],
];

/** 读取同意状态；窗口重新获得焦点时重新读取。 */
export function useAiDataConsent(
	enabled = true,
): UseQueryResult<AiDataConsent> {
	return useQuery({
		queryKey: aiDataConsentKey,
		queryFn: () => unwrap(api.GET("/api/v1/settings/ai-data-consent")),
		refetchOnWindowFocus: "always",
		enabled,
	});
}

/** 相关配置的查询刷新成功后让同意状态失效（AppShell 挂一次）。 */
export function useAiDataConsentInvalidation(): void {
	const qc = useQueryClient();
	useEffect(
		() =>
			qc.getQueryCache().subscribe((ev) => {
				if (
					ev.type === "updated" &&
					ev.action.type === "success" &&
					SOURCE_KEYS.some((p) =>
						p.every((seg, i) => ev.query.queryKey[i] === seg),
					)
				)
					void qc.invalidateQueries({ queryKey: aiDataConsentKey });
			}),
		[qc],
	);
}

/** 同意向这些接收方发送数据（传当前全部接收方的 id）。 */
export async function agreeAiDataConsent(
	qc: QueryClient,
	recipientIds: string[],
): Promise<AiDataConsent> {
	await qc.cancelQueries({ queryKey: aiDataConsentKey });
	const saved = await unwrap(
		api.PUT("/api/v1/settings/ai-data-consent", { body: { recipientIds } }),
	);
	qc.setQueryData(aiDataConsentKey, saved);
	return saved;
}

/** 撤回同意；之后同意框随即出现。 */
export async function revokeAiDataConsent(
	qc: QueryClient,
): Promise<AiDataConsent> {
	await qc.cancelQueries({ queryKey: aiDataConsentKey });
	const saved = await unwrap(api.DELETE("/api/v1/settings/ai-data-consent"));
	qc.setQueryData(aiDataConsentKey, saved);
	return saved;
}
