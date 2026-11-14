/** 协调动作审计（M9）：协调者与 orb 内 agent 的操作记录，新的在前。 */
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";

export type AutomationAction = Schemas["AutomationAction"];

export const automationKeys = {
	all: ["automation", "actions"] as const,
	list: (thread: string | undefined, limit: number) =>
		["automation", "actions", { thread, limit }] as const,
};

/** 「加载更多」靠增大 `limit` 重取；换页期间保留上一页，列表不闪空。 */
export function useAutomationActions({
	thread,
	limit,
}: {
	thread?: string;
	limit: number;
}) {
	return useQuery({
		queryKey: automationKeys.list(thread, limit),
		placeholderData: keepPreviousData,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/automation/actions", {
					params: { query: { ...(thread ? { thread } : {}), limit } },
				}),
			),
	});
}
