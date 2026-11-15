/**
 * 中央模型目录（M13）：控制面用钉住的 Core 跑 `omp models --json --kind all`，按 omp-home 的配置缓存。
 * 模型对话框、`/model`、角色轮换与新线程的思考档位都读这一份，不再向线程的 agent 要模型列表。
 *
 * 新鲜期 5 分钟：服务端每次请求都会比对配置算出的缓存键，配置变了下一次请求就重跑，
 * 所以这里只在设置页保存 `models.yml` / `config.yml` 后失效（`invalidateModelCatalog`）。
 * 「刷新」走 `?refresh=true` 强制重跑，结果直接写回缓存。
 */
import {
	type QueryClient,
	type UseQueryResult,
	useQuery,
} from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";

export type ModelCatalog = Schemas["ModelCatalog"];
export type CatalogModel = Schemas["CatalogModel"];

export const modelCatalogKeys = {
	all: ["model-catalog"] as const,
};

const CATALOG_STALE_MS = 5 * 60_000;

/** 读目录；`refresh` 为真时让控制面强制重跑（约数秒）。 */
export function fetchModelCatalog(refresh = false): Promise<ModelCatalog> {
	return unwrap(
		api.GET("/api/v1/models/catalog", {
			params: { query: refresh ? { refresh: true } : {} },
		}),
	);
}

/**
 * 目录查询的选项：组件用 `useModelCatalog`，按键等非组件路径用 `qc.fetchQuery(modelCatalogQuery())`。
 * 不自动重试：失败多半是控制面跑目录失败（503），重试只会再跑几遍；页脚有「刷新」。
 */
export function modelCatalogQuery() {
	return {
		queryKey: modelCatalogKeys.all,
		queryFn: () => fetchModelCatalog(),
		staleTime: CATALOG_STALE_MS,
		retry: false,
	};
}

export function useModelCatalog(enabled = true): UseQueryResult<ModelCatalog> {
	return useQuery({ ...modelCatalogQuery(), enabled });
}

/** 强制重跑并写回缓存；先取消在途的普通读取，免得旧结果后到覆盖新结果。 */
export async function refreshModelCatalog(
	qc: QueryClient,
): Promise<ModelCatalog> {
	await qc.cancelQueries({ queryKey: modelCatalogKeys.all });
	const data = await fetchModelCatalog(true);
	qc.setQueryData(modelCatalogKeys.all, data);
	return data;
}

/** 配置文件改过之后让目录重新读取（服务端按新配置重跑）。 */
export function invalidateModelCatalog(qc: QueryClient): Promise<void> {
	return qc.invalidateQueries({ queryKey: modelCatalogKeys.all });
}

/** 模型选择器只列对话模型（kind `chat`）；embedding、tts 等不能当会话模型。 */
export function chatModels(catalog: ModelCatalog | undefined): CatalogModel[] {
	return catalog?.models.filter((m) => m.kind === "chat") ?? [];
}

/** 按选择器（`provider/id`，可带 omp 的 `:级别` 后缀）找目录里的模型。 */
export function findCatalogModel(
	catalog: ModelCatalog | undefined,
	model: string,
): CatalogModel | undefined {
	const key = model.trim().replace(/:[^/]*$/, "");
	if (!key || !catalog) return undefined;
	return catalog.models.find(
		(m) => m.selector === key || `${m.provider}/${m.id}` === key,
	);
}
