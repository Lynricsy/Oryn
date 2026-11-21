/**
 * 按 id 取项目（M24）：先查未归档项目列表；列表已返回而里面没有时再按 id 单取（已归档的项目就是这样拿到的，
 * `GET /projects/{id}` 不按归档过滤）。
 */
import { useQuery } from "@tanstack/react-query";
import { ApiError, api, type Project, unwrap } from "../../api/client";
import { qk, useProjects } from "../../api/queries";

export type ProjectLookup =
	| { status: "loading" }
	/** error 为 null = 项目不存在（404）；否则是读取失败的原因 */
	| { status: "missing"; error: unknown }
	| { status: "ok"; project: Project };

export function useProject(id: string | undefined): ProjectLookup {
	const list = useProjects();
	const listed = id ? list.data?.projects.find((p) => p.id === id) : undefined;
	const single = useQuery({
		queryKey: qk.project(id ?? ""),
		enabled: Boolean(id) && list.isSuccess && !listed,
		retry: false,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/projects/{id}", {
					params: { path: { id: id ?? "" } },
				}),
			),
	});
	if (!id) return { status: "missing", error: null };
	if (listed) return { status: "ok", project: listed };
	if (list.isError) return { status: "missing", error: list.error };
	if (!list.isSuccess || single.isPending) return { status: "loading" };
	if (single.isError)
		return {
			status: "missing",
			error:
				single.error instanceof ApiError && single.error.status === 404
					? null
					: single.error,
		};
	return { status: "ok", project: single.data };
}
