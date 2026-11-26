/**
 * 全局语言设置的查询、写入与启动同步：`GET / PUT /api/v1/settings/language`。
 * 读写约定同 selection-actions-queries：新鲜期 5 分钟；写入前后取消在途读取，成功后更新缓存并切换 i18n。
 */
import {
	type QueryClient,
	type UseQueryResult,
	useQuery,
} from "@tanstack/react-query";
import { useEffect } from "react";
import i18n from "../i18n";
import { api, type Schemas, unwrap } from "./client";

export type LanguageCode = Schemas["LanguageCode"];
export type LanguageSettings = Schemas["LanguageSettings"];

export const languageSettingsKey = ["settings", "language"] as const;

const SETTINGS_STALE_MS = 5 * 60_000;

/** 读取全局语言设置；`enabled=false` 时不发请求。 */
export function useLanguageSettings(
	enabled = true,
): UseQueryResult<LanguageSettings> {
	return useQuery({
		queryKey: languageSettingsKey,
		queryFn: () => unwrap(api.GET("/api/v1/settings/language")),
		staleTime: SETTINGS_STALE_MS,
		enabled,
	});
}

/** 写入全局语言设置；成功后写回缓存并同步切换界面语言。 */
export async function saveLanguageSettings(
	qc: QueryClient,
	language: LanguageCode,
): Promise<LanguageSettings> {
	const queryKey = languageSettingsKey;
	await qc.cancelQueries({ queryKey });
	const saved = await unwrap(
		api.PUT("/api/v1/settings/language", { body: { language } }),
	);
	await qc.cancelQueries({ queryKey });
	qc.setQueryData(queryKey, saved);
	if (i18n.language !== saved.language) {
		await i18n.changeLanguage(saved.language);
	}
	return saved;
}

/** 鉴权通过后拉取服务端全局语言并同步到 i18n。 */
export function useLanguageSync(enabled: boolean): void {
	const lang = useLanguageSettings(enabled).data?.language;
	useEffect(() => {
		if (lang && i18n.language !== lang) {
			void i18n.changeLanguage(lang);
		}
	}, [lang]);
}
