/**
 * 自定义选区快捷动作（M22）的查询与写入：`GET / PUT /api/v1/settings/selection-actions`，整体替换。
 * 读写约定同 identity-queries：新鲜期 5 分钟；写入前后都取消在途读取，成功后直接写回缓存；queryFn 不接 `signal`。
 */
import {
	type QueryClient,
	type UseQueryResult,
	useQuery,
} from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";

type SelectionActions = Schemas["SelectionActions"];

export const selectionActionsKey = ["settings", "selection-actions"] as const;

const SETTINGS_STALE_MS = 5 * 60_000;

/** 读取自定义选区快捷动作；`enabled=false` 时不发请求。 */
export function useSelectionActions(
	enabled = true,
): UseQueryResult<SelectionActions> {
	return useQuery({
		queryKey: selectionActionsKey,
		queryFn: () => unwrap(api.GET("/api/v1/settings/selection-actions")),
		staleTime: SETTINGS_STALE_MS,
		enabled,
	});
}

/** 整体替换；成功后把服务端规整过的列表写回缓存。 */
export async function saveSelectionActions(
	qc: QueryClient,
	value: SelectionActions,
): Promise<void> {
	const queryKey = selectionActionsKey;
	await qc.cancelQueries({ queryKey });
	const saved = await unwrap(
		api.PUT("/api/v1/settings/selection-actions", { body: value }),
	);
	await qc.cancelQueries({ queryKey });
	qc.setQueryData(queryKey, saved);
}
