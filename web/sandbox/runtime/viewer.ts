// 类型化 artifact 查看器（`config.viewer`）的评论桥：查看器 bundle 自己做点选与图钉，经 `window.__orynViewer`
// 收父页面的评论模式、图钉与定位，把点选到的锚点报给父页面。与选择模式（`select.ts`）二选一。

import { now } from "./dom";
import {
	type AnchorPin,
	type PageMessageBody,
	type ParentMessage,
	parseViewerAnchor,
	type SelectMode,
	type ViewerAnchor,
} from "./messages";
import { createPickLimiter, MAX_PICKS_PER_SECOND } from "./select";

/** 查看器 bundle 看到的 `window.__orynViewer`；`on*` 注册后立即重放最近一次的模式与图钉，返回注销函数。 */
export interface ViewerBridge {
	onMode(cb: (mode: SelectMode) => void): () => void;
	onPins(cb: (pins: readonly AnchorPin[]) => void): () => void;
	onLocate(cb: (anchor: ViewerAnchor) => void): () => void;
	/** 评论模式下报一次点选；`anchor` 为 `null` 表示点在可评论的内容之外，`at` 是视口坐标。别的模式下忽略。 */
	pick(anchor: ViewerAnchor | null, at: { x: number; y: number }): void;
}

export interface ViewerBridgeHandle {
	bridge: ViewerBridge;
	/** 处理父页面发来的 `select-mode`、`anchor-pins`、`anchor-locate`；别的消息忽略。 */
	handle(msg: ParentMessage): void;
}

type Listener<T> = (value: T) => void;

// 只用下标循环与手工删除，不依赖页面能改写的数组方法
function listeners<T>() {
	const list: (Listener<T> | null)[] = [];
	return {
		add(cb: Listener<T>): () => void {
			const i = list.length;
			list[i] = cb;
			return () => {
				list[i] = null;
			};
		},
		emit(value: T) {
			for (let i = 0; i < list.length; i++) {
				const cb = list[i];
				if (cb === null || cb === undefined) continue;
				try {
					cb(value);
				} catch {
					// 查看器回调出错不影响别的回调与协议处理
				}
			}
		},
	};
}

/** 建查看器桥；`freeze` 是页面加载前缓存的 `Object.freeze`，`send` 把消息发给父页面。 */
export function createViewerBridge(
	freeze: typeof Object.freeze,
	send: (body: PageMessageBody) => void,
): ViewerBridgeHandle {
	let mode: SelectMode = null;
	let pins: readonly AnchorPin[] = freeze([]);
	const modes = listeners<SelectMode>();
	const pinSubs = listeners<readonly AnchorPin[]>();
	const locates = listeners<ViewerAnchor>();
	const allowPick = createPickLimiter(MAX_PICKS_PER_SECOND, 1000);

	const bridge: ViewerBridge = freeze({
		onMode(cb: Listener<SelectMode>) {
			if (typeof cb !== "function") return () => {};
			const off = modes.add(cb);
			cb(mode);
			return off;
		},
		onPins(cb: Listener<readonly AnchorPin[]>) {
			if (typeof cb !== "function") return () => {};
			const off = pinSubs.add(cb);
			cb(pins);
			return off;
		},
		onLocate(cb: Listener<ViewerAnchor>) {
			if (typeof cb !== "function") return () => {};
			return locates.add(cb);
		},
		pick(raw: unknown, at: unknown) {
			if (mode !== "comment" || !allowPick(now())) return;
			const anchor = raw === null ? null : parseViewerAnchor(raw);
			if (raw !== null && anchor === null) return;
			if (typeof at !== "object" || at === null || !("x" in at) || !("y" in at))
				return;
			const { x, y } = at;
			if (typeof x !== "number" || !Number.isFinite(x)) return;
			if (typeof y !== "number" || !Number.isFinite(y)) return;
			send({ kind: "anchor-pick", anchor, at: { x, y } });
		},
	});

	return {
		bridge,
		handle(msg) {
			switch (msg.kind) {
				case "select-mode":
					mode = msg.mode;
					modes.emit(mode);
					break;
				case "anchor-pins":
					pins = freeze(msg.pins);
					pinSubs.emit(pins);
					break;
				case "anchor-locate":
					locates.emit(msg.anchor);
					break;
			}
		},
	};
}
