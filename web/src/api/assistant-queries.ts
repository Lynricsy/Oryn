/**
 * 全局助手设置（M19）的查询与写入：联网总开关、关掉的 MCP 服务器，以及全局 `mcp.json` 里服务器对助手的可用性。
 * 写入成功后直接写回设置缓存，并让 MCP 列表重新拉取（`enabled` 随禁用表变化）。
 * 另有主线的备忘（`GET / PUT assistant/main/notes`，上限 {@link NOTES_LIMIT} 个字符）。
 * 以及助手的电脑（`GET assistant/computer` 与 `POST …/wake | pause | reset`）：动作的响应体就是新的状态，直接写回缓存。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";
import { qk } from "./queries";

export type AssistantSettings = Schemas["AssistantSettings"];
export type AssistantMcpServer = Schemas["AssistantMcpServer"];
export type AssistantComputer = Schemas["AssistantComputer"];

/** 主线备忘的长度上限（Unicode 字符，同服务端）。 */
export const NOTES_LIMIT = 1500;

export const assistantKeys = {
	settings: ["settings", "assistant"] as const,
	mcpServers: ["assistant", "mcp-servers"] as const,
	notes: ["assistant", "main", "notes"] as const,
	computer: ["assistant", "computer"] as const,
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

export function useAssistantNotes(enabled = true) {
	return useQuery({
		queryKey: assistantKeys.notes,
		enabled,
		queryFn: () => unwrap(api.GET("/api/v1/assistant/main/notes")),
	});
}

export function usePutAssistantNotes() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (text: string) =>
			unwrap(api.PUT("/api/v1/assistant/main/notes", { body: { text } })),
		onSuccess: (saved) => qc.setQueryData(assistantKeys.notes, saved),
	});
}

/** 电脑的状态；有过渡态（唤醒、冻结中）时每 2 秒轮询一次，稳定后停。 */
export function useAssistantComputer() {
	return useQuery({
		queryKey: assistantKeys.computer,
		queryFn: () => unwrap(api.GET("/api/v1/assistant/computer")),
		refetchInterval: (q) => {
			const s = q.state.data?.state;
			return s && s !== "ready" && s !== "frozen" && s !== "stopped"
				? 2000
				: false;
		},
	});
}

export type ComputerAction = "wake" | "pause" | "reset";

/** 唤醒（没有则创建，等到就绪）、暂停（冻结）、重置（删除，下次用到时重建）。 */
export function useAssistantComputerAction() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (action: ComputerAction) => {
			switch (action) {
				case "wake":
					return unwrap(api.POST("/api/v1/assistant/computer/wake"));
				case "pause":
					return unwrap(api.POST("/api/v1/assistant/computer/pause"));
				case "reset":
					return unwrap(api.POST("/api/v1/assistant/computer/reset"));
			}
		},
		onSuccess: (saved) => qc.setQueryData(assistantKeys.computer, saved),
		// orb 列表也要刷新：电脑可能刚建出来或已删除（推送之外的兜底）
		onSettled: () => {
			void qc.invalidateQueries({ queryKey: assistantKeys.computer });
			void qc.invalidateQueries({ queryKey: qk.orbs });
		},
	});
}
