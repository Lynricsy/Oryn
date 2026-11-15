/**
 * 全局助手文件库（M13 4.7）的 REST 读写。键都在 `["assistant-files", …]` 下：切换共享、删除后整组失效，
 * 「本对话 + 共享」与「全部」两份列表一起刷新。切换共享先就地改写已缓存的行，列表标记立刻跟上。
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { deleteAssistantFile } from "../lib/attachments";
import { api, type Schemas, unwrap } from "./client";

export type AssistantFile = Schemas["AssistantFile"];
export type AssistantFileScope = Schemas["AssistantFileScope"];

export const libraryQk = {
	all: ["assistant-files"] as const,
	/** `threadId` 缺省 = 全部文件；给出 = 该对话自己的 + 库内共享的 */
	list: (threadId?: string) => ["assistant-files", threadId ?? null] as const,
};

/** 文件内容地址（以附件下载；`inline` 只对安全的栅格图片生效，由服务端判定）。 */
export function assistantFileContentUrl(id: string, inline = false): string {
	const base = `/api/v1/assistant/files/${encodeURIComponent(id)}/content`;
	return inline ? `${base}?inline=true` : base;
}

export function useAssistantFiles(threadId?: string) {
	return useQuery({
		queryKey: libraryQk.list(threadId),
		queryFn: async () =>
			(
				await unwrap(
					api.GET("/api/v1/assistant/files", {
						params: { query: threadId ? { threadId } : {} },
					}),
				)
			).files,
	});
}

export function useSetAssistantFileScope() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: ({ id, scope }: { id: string; scope: AssistantFileScope }) =>
			unwrap(
				api.PATCH("/api/v1/assistant/files/{id}", {
					params: { path: { id } },
					body: { scope },
				}),
			),
		onSuccess: (file) => {
			qc.setQueriesData<AssistantFile[]>({ queryKey: libraryQk.all }, (list) =>
				list?.map((f) => (f.id === file.id ? file : f)),
			);
			// 别的对话的列表里，这个文件可能因此出现或消失
			void qc.invalidateQueries({ queryKey: libraryQk.all });
		},
	});
}

export function useDeleteAssistantFile() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (id: string) => deleteAssistantFile(id),
		onSuccess: (_, id) => {
			qc.setQueriesData<AssistantFile[]>({ queryKey: libraryQk.all }, (list) =>
				list?.filter((f) => f.id !== id),
			);
			void qc.invalidateQueries({ queryKey: libraryQk.all });
		},
	});
}
