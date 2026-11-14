/** 协调宿主（M9）：可用性与沙箱报告。助手页、Space 新建与设置 → 关于共用，60 秒过期。 */
import { useQuery } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";

export type CoordinatorStatus = Schemas["CoordinatorStatus"];

export const coordinatorKey = ["coordinator"] as const;

export function useCoordinatorStatus() {
	return useQuery({
		queryKey: coordinatorKey,
		queryFn: () => unwrap(api.GET("/api/v1/coordinator")),
		staleTime: 60_000,
	});
}
