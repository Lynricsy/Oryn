/**
 * 项目级密钥与环境变量（M24 阶段 ④）的查询与写入。键挂在 `["projects", id]` 下，项目查询失效时一并失效。
 * 没有 WS 推送：写入方自己让列表失效，别的设备在下次重新取数时看到。
 */
import { useQuery } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";

export type ProjectSecret = Schemas["ProjectSecret"];
export type ProjectSecretPut = Schemas["ProjectSecretPut"];

export const projectSecretKeys = {
	list: (projectId: string) => ["projects", projectId, "secrets"] as const,
};

export function useProjectSecrets(projectId: string) {
	return useQuery({
		queryKey: projectSecretKeys.list(projectId),
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/projects/{id}/secrets", {
					params: { path: { id: projectId } },
				}),
			),
	});
}

export function putProjectSecret(
	projectId: string,
	name: string,
	body: ProjectSecretPut,
): Promise<ProjectSecret> {
	return unwrap(
		api.PUT("/api/v1/projects/{id}/secrets/{name}", {
			params: { path: { id: projectId, name } },
			body,
		}),
	);
}

export async function deleteProjectSecret(
	projectId: string,
	name: string,
): Promise<void> {
	await unwrap(
		api.DELETE("/api/v1/projects/{id}/secrets/{name}", {
			params: { path: { id: projectId, name } },
		}),
	);
}
