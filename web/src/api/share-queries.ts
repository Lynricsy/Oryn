/**
 * 公开分享与文件发布的 REST 读写（M5 计划 5.3）。键都在 `["shares", …]` 下：任一变更后整组失效，
 * 线程对话框、附件对话框与设置页总览一起刷新。附件列表（`["attachments", threadId]`）带 `publicUrl`，发布 / 撤回后同样失效。
 */
import {
	type QueryClient,
	useMutation,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";

export type ShareMode = Schemas["ShareMode"];
export type ThreadShare = Schemas["ThreadShare"];
export type ShareScan = Schemas["ShareScan"];
export type ShareFinding = Schemas["ShareFinding"];
export type PublishedFile = Schemas["PublishedFile"];
export type PublicShare = Schemas["PublicShare"];

export const shareQk = {
	all: ["shares"] as const,
	thread: (threadId: string) => ["shares", "thread", threadId] as const,
	overview: ["shares", "overview"] as const,
};

/** 创建分享的结果：建好了，或扫描到疑似密钥（409 `secrets_found`，确认后带 `acknowledgeFindings` 重发）。 */
export type CreateShareResult =
	| { kind: "created"; share: ThreadShare }
	| { kind: "findings"; scan: ShareScan };

function invalidateShares(qc: QueryClient, attachments = false) {
	void qc.invalidateQueries({ queryKey: shareQk.all });
	if (attachments) void qc.invalidateQueries({ queryKey: ["attachments"] });
}

export function useThreadShares(threadId: string) {
	return useQuery({
		queryKey: shareQk.thread(threadId),
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/threads/{id}/shares", {
					params: { path: { id: threadId } },
				}),
			),
	});
}

export function useCreateShare(threadId: string) {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async ({
			mode,
			acknowledgeFindings = false,
		}: {
			mode: ShareMode;
			acknowledgeFindings?: boolean;
		}): Promise<CreateShareResult> => {
			const res = await api.POST("/api/v1/threads/{id}/shares", {
				params: { path: { id: threadId } },
				body: { mode, acknowledgeFindings },
			});
			const err = res.error as
				| Partial<Schemas["ShareScanConflict"]>
				| undefined;
			if (
				res.response.status === 409 &&
				err?.code === "secrets_found" &&
				err.scan
			)
				return { kind: "findings", scan: err.scan };
			return { kind: "created", share: await unwrap(Promise.resolve(res)) };
		},
		onSuccess: (r) => {
			if (r.kind === "created") invalidateShares(qc);
		},
	});
}

export function useRevokeShare() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async (id: string) => {
			await unwrap(
				api.DELETE("/api/v1/shares/{id}", { params: { path: { id } } }),
			);
		},
		onSuccess: () => invalidateShares(qc),
	});
}

export function useShareOverview() {
	return useQuery({
		queryKey: shareQk.overview,
		queryFn: () => unwrap(api.GET("/api/v1/shares")),
	});
}

/** 发布附件（幂等：已发布则返回现有的那条）。 */
export function usePublishAttachment() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (attachmentId: string) =>
			unwrap(
				api.POST("/api/v1/attachments/{id}/publish", {
					params: { path: { id: attachmentId } },
				}),
			),
		onSuccess: () => invalidateShares(qc, true),
	});
}

/** 把 orb 里的文件复制成附件并发布：发布的是此刻的副本，之后 orb 里的修改不影响它。 */
export function usePublishOrbFile(orbId: string) {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: ({ path, threadId }: { path: string; threadId: string }) =>
			unwrap(
				api.POST("/api/v1/orbs/{id}/fs/publish", {
					params: { path: { id: orbId } },
					body: { path, threadId },
				}),
			),
		onSuccess: () => invalidateShares(qc, true),
	});
}

export function useRevokePublishedFile() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async (id: string) => {
			await unwrap(
				api.DELETE("/api/v1/published-files/{id}", {
					params: { path: { id } },
				}),
			);
		},
		onSuccess: () => invalidateShares(qc, true),
	});
}
