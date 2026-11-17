/**
 * 全局助手设置（M19）的查询与写入：联网总开关、关掉的 MCP 服务器，以及全局 `mcp.json` 里服务器对助手的可用性。
 * 写入成功后直接写回设置缓存，并让 MCP 列表重新拉取（`enabled` 随禁用表变化）。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";

export type AssistantSettings = Schemas["AssistantSettings"];
export type AssistantMcpServer = Schemas["AssistantMcpServer"];

export const assistantKeys = {
	settings: ["settings", "assistant"] as const,
	mcpServers: ["assistant", "mcp-servers"] as const,
};

export function useAssistantSettings() {
	return useQuery({
		queryKey: assistantKeys.settings,
		queryFn: () => unwrap(api.GET("/api/v1/settings/assistant")),
	});
}

export function useAssistantMcpServers() {
	return useQuery({
		queryKey: assistantKeys.mcpServers,
		queryFn: () => unwrap(api.GET("/api/v1/assistant/mcp-servers")),
	});
}

/** 整体写入设置（PUT 是全量替换，调用方在当前值上改一处再提交）。 */
export function usePutAssistantSettings() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (body: AssistantSettings) =>
			unwrap(api.PUT("/api/v1/settings/assistant", { body })),
		onSuccess: (saved) => {
			qc.setQueryData(assistantKeys.settings, saved);
			void qc.invalidateQueries({ queryKey: assistantKeys.mcpServers });
		},
	});
}
