/**
 * 生成式 UI 总开关（M33）：`GET / PUT /api/v1/settings/generative-ui`。
 * 关闭后新启动的 agent 不再有 render_ui / render_html 两个工具，系统提示词里也没有视图指引；已有的视图照常显示。
 * 写入是乐观的：先改缓存，失败时回滚。
 */
import {
	type UseMutationResult,
	type UseQueryResult,
	useMutation,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";

export type GenerativeUISettings = Schemas["GenerativeUISettings"];

export const generativeUiSettingsKey = ["settings", "generative-ui"] as const;

const SETTINGS_STALE_MS = 5 * 60_000;

export function useGenerativeUiSettings(): UseQueryResult<GenerativeUISettings> {
	return useQuery({
		queryKey: generativeUiSettingsKey,
		queryFn: () => unwrap(api.GET("/api/v1/settings/generative-ui")),
		staleTime: SETTINGS_STALE_MS,
	});
}

export function useSetGenerativeUi(): UseMutationResult<
	GenerativeUISettings,
	Error,
	boolean,
	{ prev: GenerativeUISettings | undefined }
> {
	const qc = useQueryClient();
	const queryKey = generativeUiSettingsKey;
	return useMutation({
		mutationFn: (enabled: boolean) =>
			unwrap(api.PUT("/api/v1/settings/generative-ui", { body: { enabled } })),
		onMutate: async (enabled) => {
			await qc.cancelQueries({ queryKey });
			const prev = qc.getQueryData<GenerativeUISettings>(queryKey);
			qc.setQueryData<GenerativeUISettings>(queryKey, { enabled });
			return { prev };
		},
		onError: (_e, _enabled, context) => {
			if (context?.prev) qc.setQueryData(queryKey, context.prev);
		},
		onSuccess: (saved) => qc.setQueryData(queryKey, saved),
	});
}
