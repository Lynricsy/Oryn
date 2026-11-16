/**
 * artifact 的在场信息（`artifact.activity`，M14 6.7）：谁在看、各线程在读 / 改 / 画 / 挪什么。
 * 只有正在观看这个 artifact 的连接收得到（观看登记由查看器负责）；实时客户端缓存每个观看中的 artifact
 * 最近一条，所以晚挂载的组件也能立刻拿到。没在观看或还没收到时为 `null`。
 */
import { useCallback, useSyncExternalStore } from "react";
import {
	type ArtifactActivityEvent,
	type RealtimeClient,
	realtime,
} from "../../realtime/socket";

export function useArtifactActivity(
	id: string,
	client: Pick<
		RealtimeClient,
		"onArtifact" | "lastArtifactActivity"
	> = realtime,
): ArtifactActivityEvent | null {
	const subscribe = useCallback(
		(notify: () => void) =>
			client.onArtifact(id, (msg) => {
				if (msg.type === "artifact.activity") notify();
			}),
		[client, id],
	);
	return useSyncExternalStore(
		subscribe,
		() => client.lastArtifactActivity(id) ?? null,
	);
}
