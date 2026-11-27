/**
 * HTML 视图帧（M33，docs/plan-m33.md 的 C8 / S7）：申请、按 toolCallId 缓存、统一关闭。
 *
 * 线程页与分享页各持有一个 `ViewFrameCache`（离开页面时 `closeAll`）；`ViewHost.openFrame` 委托给它。
 * 帧令牌就是访问凭证，orynd 重启或帧被淘汰后旧地址失效——HtmlView 收不到 `ready` 时以 `fresh` 重新申请。
 */
import { api, unwrap } from "../../api/client";

export interface ViewFrameHandle {
	url: string;
	token: string;
}

/** 为线程转录里的一次 render_html 调用申请帧。 */
export function openThreadViewFrame(
	threadId: string,
	toolCallId: string,
): Promise<ViewFrameHandle> {
	return unwrap(
		api.POST("/api/v1/threads/{id}/view-frames", {
			params: { path: { id: threadId } },
			body: { toolCallId },
		}),
	);
}

/** 为公开分享里的一次 render_html 调用申请帧（匿名）。 */
export function openShareViewFrame(
	slug: string,
	toolCallId: string,
): Promise<ViewFrameHandle> {
	return unwrap(
		api.POST("/api/v1/public/shares/{slug}/view-frames", {
			params: { path: { slug } },
			body: { toolCallId },
		}),
	);
}

/** 关帧；失败（帧已过期、orynd 重启过、匿名页没有权限）无需处理，服务端会按空闲过期回收。 */
export async function closeViewFrame(token: string): Promise<void> {
	await api
		.DELETE("/api/v1/artifact-frames/{token}", {
			params: { path: { token } },
		})
		.catch(() => undefined);
}

/**
 * 按 toolCallId 缓存帧地址：同一次调用在转录里重新挂载（虚拟滚动、折叠展开）时复用同一个帧，不重复申请。
 * 并发的同一申请合并成一个；申请失败不缓存。
 */
export class ViewFrameCache {
	private readonly frames = new Map<string, Promise<ViewFrameHandle>>();
	private readonly open: (toolCallId: string) => Promise<ViewFrameHandle>;
	private readonly close: (token: string) => Promise<void>;

	constructor(
		open: (toolCallId: string) => Promise<ViewFrameHandle>,
		close: (token: string) => Promise<void> = closeViewFrame,
	) {
		this.open = open;
		this.close = close;
	}

	/** 帧根地址；`fresh` 时关掉旧帧重新申请（旧帧失效：orynd 重启或被淘汰）。 */
	async get(toolCallId: string, fresh = false): Promise<string> {
		let pending = this.frames.get(toolCallId);
		if (pending && fresh) {
			this.frames.delete(toolCallId);
			pending.then((f) => this.close(f.token)).catch(() => undefined);
			pending = undefined;
		}
		if (!pending) {
			pending = this.open(toolCallId);
			this.frames.set(toolCallId, pending);
			pending.catch(() => {
				if (this.frames.get(toolCallId) === pending)
					this.frames.delete(toolCallId);
			});
		}
		return (await pending).url;
	}

	/** 关掉全部已申请的帧（离开线程页 / 分享页时）。 */
	closeAll(): void {
		for (const pending of this.frames.values())
			pending.then((f) => this.close(f.token)).catch(() => undefined);
		this.frames.clear();
	}
}
