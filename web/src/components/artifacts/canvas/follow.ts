/**
 * 画布的「跟随 {线程}」（计划 M14 6.7）：查看器顶栏的在场头像发起，画布编辑器（`useAgentPresence`）据此让镜头跟着
 * 那个线程的 focus 走。按 artifact 记一个被跟随的线程；编辑器卸载、用户自己平移或缩放、那个线程不再出现在在场信息里时清掉。
 */
import { useSyncExternalStore } from "react";

const following = new Map<string, string>();
const listeners = new Set<() => void>();

function subscribe(cb: () => void): () => void {
	listeners.add(cb);
	return () => {
		listeners.delete(cb);
	};
}

/** 正在跟随的线程；没有时为 null。 */
export function getFollowing(artifactId: string): string | null {
	return following.get(artifactId) ?? null;
}

/** 开始跟随 threadId；null = 停止。 */
export function setFollowing(artifactId: string, threadId: string | null) {
	if (getFollowing(artifactId) === threadId) return;
	if (threadId === null) following.delete(artifactId);
	else following.set(artifactId, threadId);
	for (const cb of [...listeners]) cb();
}

export function useFollowing(artifactId: string): string | null {
	return useSyncExternalStore(subscribe, () => getFollowing(artifactId));
}
