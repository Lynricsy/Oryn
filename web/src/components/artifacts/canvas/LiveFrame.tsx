/**
 * 设计画布的实时帧（M29 T6）：画框换成 orb 里 dev 服务的 portal 地址 `portalFrameSrc(模板, 端口, "/frames/<帧 id>")`，
 * 改源码后 vite 热更直接反映在画布上。沙箱同 Portal 面板（`PORTAL_SANDBOX`，portal 主机与主站不同源）。
 *
 * 帧里的引擎入口加载同一份页面运行时并回 `ready`（带 `design`），通道与静态帧相同（`useFrameLink`），只是消息的目标源
 * 是 portal 源。WebKit 下 portal 握手会被带到主站 `/login`（跨站 iframe 不带主站的 SameSite=Lax 会话 Cookie，探针 P3），
 * 父页面读不到跨源帧的地址，所以判据是「`LIVE_READY_MS` 内没收到 `ready`」：超时报给编辑器，由它退回静态帧并提示授权。
 */
import { type ReactNode, useEffect, useRef } from "react";
import type {
	SelectMode,
	SelectPin,
} from "../../../../sandbox/runtime/messages";
import { PORTAL_SANDBOX } from "../../portal/PortalPanel";
import { portalFrameSrc } from "../../portal/portal-url";
import type { FrameLink } from "../frame-link";
import { useFrameLink } from "./use-frame-link";

/** 等实时帧 `ready` 的上限（P3：WebKit 握手失败时帧停在主站登录页，永远等不到）。 */
export const LIVE_READY_MS = 5000;

/** 同时开着的实时帧数（每个都是一条到 orb 的连接加一个 vite 客户端）。 */
export const MAX_LIVE = 6;

/** 帧 id：`source` 去掉 `src/frames/` 与 `.tsx`（`src/frames/login.tsx` → `login`）；不是帧源码时为 null。 */
export function frameIdOf(source: string | undefined): string | null {
	return (
		/^src\/frames\/([a-z0-9][a-z0-9-]{0,63})\.tsx$/.exec(source ?? "")?.[1] ??
		null
	);
}

/** 实时帧的 portal 地址与帧源（`postMessage` 的目标源）；模板不是 http(s)（mock）时源为 `*`。 */
export function liveFrameTarget(
	template: string,
	port: number,
	frame: string,
): { src: string; origin: string } {
	const src = portalFrameSrc(template, port, `/frames/${frame}`);
	try {
		const { origin } = new URL(src);
		return { src, origin: origin === "null" ? "*" : origin };
	} catch {
		return { src, origin: "*" };
	}
}

export function LiveFrame({
	pageId,
	src,
	origin,
	title,
	mode,
	accent,
	pins,
	onLink,
	onTimeout,
}: {
	pageId: string;
	src: string;
	origin: string;
	title: string;
	mode: SelectMode;
	accent: string;
	pins: SelectPin[];
	onLink(pageId: string, link: FrameLink | null): void;
	/** `LIVE_READY_MS` 内没收到 `ready`。 */
	onTimeout(): void;
}): ReactNode {
	const ref = useRef<HTMLIFrameElement>(null);
	const ready = useRef(false);
	const onTimeoutRef = useRef(onTimeout);
	onTimeoutRef.current = onTimeout;
	// 从挂上（开始握手）起计时：握手的跳转与 dev 服务首次编译都算在内；帧内跳转或热更整页刷新后的 ready 不再计时
	useEffect(() => {
		const timer = window.setTimeout(() => {
			if (!ready.current) onTimeoutRef.current();
		}, LIVE_READY_MS);
		return () => clearTimeout(timer);
	}, []);
	useFrameLink(ref, {
		pageId,
		origin,
		mode,
		accent,
		pins,
		onLink,
		onReady: () => {
			ready.current = true;
		},
	});
	return (
		<iframe
			ref={ref}
			data-testid="canvas-live-frame"
			title={title}
			src={src}
			sandbox={PORTAL_SANDBOX}
			allow="clipboard-write"
			referrerPolicy="no-referrer"
		/>
	);
}
