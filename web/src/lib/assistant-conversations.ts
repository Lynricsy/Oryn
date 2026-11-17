/** 全局助手的对话列表（M9.6）：助手页与「助手」分区侧栏共用。主线对话（M19）不在列表里，单独取。 */
import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useRef } from "react";
import { useLocation, useNavigate } from "react-router";
import { api, type Schemas, type Thread, unwrap } from "../api/client";
import { patchThread } from "../state/actions";
import { indexStore, useIndex } from "../state/stores";

/** 「已归档」折叠组的查询键：`GET threads?archived=true&kind=assistant`。 */
export const archivedAssistantKey = [
	"threads",
	{ archived: true, kind: "assistant" },
] as const;

/** 未归档的助手对话（不含主线），最近活动的在前；`/assistant` 落点取它的第一项，因此不会落到主线。 */
export function useAssistantConversations(): Thread[] {
	const threads = useIndex((s) => s.threads);
	return useMemo(
		() =>
			Object.values(threads)
				.filter((t) => t.kind === "assistant" && !t.main && !t.archivedAt)
				.sort(
					(a, b) =>
						(b.lastMessageAt ?? b.updatedAt) - (a.lastMessageAt ?? a.updatedAt),
				),
		[threads],
	);
}

/** 线程索引里的主线对话（全库至多一条）；还没有时为 undefined，由 {@link ensureAssistantMain} 创建。 */
export function useAssistantMain(): Thread | undefined {
	return useIndex((s) => {
		for (const t of Object.values(s.threads)) if (t.main) return t;
		return undefined;
	});
}

/** `GET assistant/main`：取主线对话，没有则由服务端创建；结果并入线程索引。 */
export async function ensureAssistantMain(): Promise<Thread> {
	const res = await unwrap(api.GET("/api/v1/assistant/main"));
	indexStore.getState().upsertThread(res.thread);
	return res.thread;
}

/**
 * 归档一个助手对话（侧栏行与页头共用这一条路径）：成功后把它合入「已归档」缓存并让该查询失效（展开着的组立即
 * 出现它，随后按服务端重取对账）；完成那一刻若仍停在该对话上（读最新路由，不用点击时的闭包）则回 `/assistant`。
 * 调用方组件已卸载（用户已离开该页）时不跳转。失败时 Promise 拒绝，由调用方显示错误。
 */
export function useArchiveAssistantConversation(): (
	threadId: string,
) => Promise<void> {
	const qc = useQueryClient();
	const navigate = useNavigate();
	const { pathname } = useLocation();
	const here = useRef(pathname);
	here.current = pathname;
	const alive = useRef(true);
	useEffect(() => {
		alive.current = true;
		return () => {
			alive.current = false;
		};
	}, []);
	return useCallback(
		async (threadId) => {
			const thread = await patchThread(threadId, { archived: true });
			qc.setQueryData<Schemas["ThreadList"]>(archivedAssistantKey, (old) =>
				old && !old.threads.some((t) => t.id === thread.id)
					? { ...old, threads: [thread, ...old.threads] }
					: old,
			);
			void qc.invalidateQueries({ queryKey: archivedAssistantKey });
			if (alive.current && here.current === `/assistant/${threadId}`)
				navigate("/assistant", { replace: true });
		},
		[qc, navigate],
	);
}
