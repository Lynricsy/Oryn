/**
 * artifact 页面帧（计划 M14 2.3）：挂载时申请帧（`POST …/frames`），卸载时关掉（`DELETE /api/v1/artifact-frames/{token}`）。
 * `<iframe>` 只给 `allow-scripts allow-forms allow-downloads`，**不带 `allow-same-origin`**：页面始终在不透明源里，
 * 读不到 Oryn 的 cookie、存储与 API。帧放在与 Portal 相同的内嵌井里。
 *
 * - 跟随最新版本（`version` 缺省）：收到 `artifact.version` 后先发 `keep-scroll`，拿到 `scroll`（最多等 300 ms）后把 `src`
 *   重设为帧根，`load` 后发 `restore-scroll`，页面原地热更新且滚动位置不变。
 * - 加载超过 200 ms 才显示转圈；3000 ms 后无论是否加载完都显示内容。
 * - 帧失效（orynd 重启、空闲过期）时帧地址回 404，跨源读不到状态码；跟随最新版本的帧在 `hello` 之后 1 秒内
 *   收不到运行时的 `ready` 就当作失效，重新申请一次帧（收到过 `ready` 才会再给下一次机会，运行时缺失时不会反复申请）。
 * - 被拦截的跳转：运行时的 `blocked`（完整 URL），或父页面上 `frame-src` 的 `securitypolicyviolation`（只有源，之后重载帧）。
 * - 选择与评论（3.1、3.3）：`onLink` 交出向帧发消息的函数（帧每次重建先交 `null` 再交新的），页面的 `select` 经 `onSelect` 报出。
 */
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type {
	PageMessage,
	ParentMessageBody,
} from "../../../sandbox/runtime/messages";
import {
	type ArtifactFrame,
	closeArtifactFrame,
	openArtifactFrame,
} from "../../api/artifact-queries";
import { realtime } from "../../realtime/socket";
import { Spinner } from "../ui/primitives";
import { FrameLink } from "./frame-link";
import { FrameErrorState } from "./viewer-states";

/** artifact 帧的沙箱：恰为这三项。没有 `allow-same-origin`、`allow-popups`、`allow-top-navigation`。 */
export const ARTIFACT_SANDBOX = "allow-scripts allow-forms allow-downloads";

/** 等页面回报滚动位置的上限；超时直接重载。 */
export const KEEP_SCROLL_WAIT_MS = 300;
/** 加载超过这么久才显示转圈。 */
export const SPINNER_DELAY_MS = 200;
/** 超过这么久无论是否加载完都显示内容。 */
export const REVEAL_AFTER_MS = 3000;
/** `hello` 之后等运行时 `ready` 的上限；超时视为帧已失效。 */
export const READY_WAIT_MS = 1000;

/** 向帧里的页面发一条消息（`select-mode`、`select-pins` 等；最近一次的选择状态在帧每次加载后自动重发）。 */
export type FramePost = (body: ParentMessageBody) => void;
/** 页面报来的选中变化或定位应答。 */
export type PageSelectMessage = Extract<PageMessage, { kind: "select" }>;
/** 类型化 artifact 的查看器在评论模式下报来的点选。 */
export type AnchorPickMessage = Extract<PageMessage, { kind: "anchor-pick" }>;

export interface PageFrameProps {
	artifactId: string;
	/** 钉住某个版本（只读）；缺省 = 跟随最新版本。 */
	version?: number;
	/** 每次变化都重新申请帧（查看器的「重新加载」）。 */
	reloadNonce?: number;
	/** 帧的标题（读屏用）。 */
	title: string;
	/** 页面试图跳到别的源：`url` 是完整地址（运行时报告）或只有源（CSP 报告）。 */
	onBlocked(url: string): void;
	/** 类型化 artifact 的查看器请求进入 / 退出演示（运行时的 `present`）；缺省 = 不认这条消息。帧每次重新加载都按退出处理。 */
	onPresent?(on: boolean): void;
	/** 帧（重新）建立时交出发消息的函数，拆掉时交 `null`。 */
	onLink?(post: FramePost | null): void;
	/** 页面的 `select` 消息。 */
	onSelect?(msg: PageSelectMessage): void;
	/** 查看器的 `anchor-pick` 消息（评论模式下的点选）。 */
	onAnchorPick?(msg: AnchorPickMessage): void;
}

export function PageFrame({
	artifactId,
	version,
	reloadNonce = 0,
	title,
	onBlocked,
	onPresent,
	onLink,
	onSelect,
	onAnchorPick,
}: PageFrameProps): ReactNode {
	// 帧被判定失效时递增，重新申请
	const [attempt, setAttempt] = useState(0);
	// 已因「收不到 ready」重新申请过；之后收到 ready 才清除
	const retried = useRef(false);
	return (
		<FrameSession
			key={`${artifactId}:${version ?? 0}:${reloadNonce}:${attempt}`}
			artifactId={artifactId}
			version={version}
			title={title}
			onBlocked={onBlocked}
			onPresent={onPresent}
			onLink={onLink}
			onSelect={onSelect}
			onAnchorPick={onAnchorPick}
			onReady={() => {
				retried.current = false;
			}}
			onStale={() => {
				if (retried.current) return;
				retried.current = true;
				setAttempt((n) => n + 1);
			}}
			onRetry={() => setAttempt((n) => n + 1)}
		/>
	);
}

type Acquire =
	| { state: "pending" }
	| { state: "ready"; frame: ArtifactFrame }
	| { state: "error"; error: unknown };

/** 一次帧申请的生命周期：重新申请 = 换 key 重建。 */
function FrameSession({
	artifactId,
	version,
	title,
	onBlocked,
	onPresent,
	onLink,
	onSelect,
	onAnchorPick,
	onReady,
	onStale,
	onRetry,
}: {
	artifactId: string;
	version?: number;
	title: string;
	onBlocked(url: string): void;
	onPresent?(on: boolean): void;
	onLink?(post: FramePost | null): void;
	onSelect?(msg: PageSelectMessage): void;
	onAnchorPick?(msg: AnchorPickMessage): void;
	onReady(): void;
	onStale(): void;
	onRetry(): void;
}): ReactNode {
	const { i18n } = useTranslation();
	const [acq, setAcq] = useState<Acquire>({ state: "pending" });
	const locale = i18n.language;

	useEffect(() => {
		let live = true;
		let token: string | null = null;
		openArtifactFrame(artifactId, version, locale).then(
			(frame) => {
				if (!live) {
					void closeArtifactFrame(frame.token);
					return;
				}
				token = frame.token;
				setAcq({ state: "ready", frame });
			},
			(error: unknown) => {
				if (live) setAcq({ state: "error", error });
			},
		);
		return () => {
			live = false;
			if (token) void closeArtifactFrame(token);
		};
	}, [artifactId, version, locale]);

	if (acq.state === "error")
		return <FrameErrorState error={acq.error} onRetry={onRetry} />;
	return (
		<FrameWell>
			{acq.state === "ready" ? (
				<LiveFrame
					artifactId={artifactId}
					follow={version === undefined}
					url={acq.frame.url}
					title={title}
					onBlocked={onBlocked}
					onPresent={onPresent}
					onLink={onLink}
					onSelect={onSelect}
					onAnchorPick={onAnchorPick}
					onReady={onReady}
					onStale={onStale}
				/>
			) : (
				<DelayedSpinner />
			)}
		</FrameWell>
	);
}

/** 与 Portal 相同的内嵌井。 */
function FrameWell({ children }: { children: ReactNode }): ReactNode {
	return (
		<div className="relative h-full overflow-hidden rounded-lg border border-border bg-inset">
			{children}
		</div>
	);
}

/** 超过 `SPINNER_DELAY_MS` 才出现的转圈。 */
function DelayedSpinner(): ReactNode {
	const [shown, setShown] = useState(false);
	useEffect(() => {
		const timer = setTimeout(() => setShown(true), SPINNER_DELAY_MS);
		return () => clearTimeout(timer);
	}, []);
	if (!shown) return null;
	return (
		<div
			data-testid="artifact-frame-loading"
			className="absolute inset-0 flex items-center justify-center text-faint"
		>
			<Spinner size={18} />
		</div>
	);
}

function LiveFrame({
	artifactId,
	follow,
	url,
	title,
	onBlocked,
	onPresent,
	onLink,
	onSelect,
	onAnchorPick,
	onReady,
	onStale,
}: {
	artifactId: string;
	follow: boolean;
	url: string;
	title: string;
	onBlocked(url: string): void;
	onPresent?(on: boolean): void;
	onLink?(post: FramePost | null): void;
	onSelect?(msg: PageSelectMessage): void;
	onAnchorPick?(msg: AnchorPickMessage): void;
	onReady(): void;
	onStale(): void;
}): ReactNode {
	const ref = useRef<HTMLIFrameElement>(null);
	const [visible, setVisible] = useState(false);
	// 回调可能每次渲染都换；监听只装一次
	const cb = useRef({
		onBlocked,
		onPresent,
		onLink,
		onSelect,
		onAnchorPick,
		onReady,
		onStale,
	});
	cb.current = {
		onBlocked,
		onPresent,
		onLink,
		onSelect,
		onAnchorPick,
		onReady,
		onStale,
	};

	useEffect(() => {
		const timer = setTimeout(() => setVisible(true), REVEAL_AFTER_MS);
		return () => clearTimeout(timer);
	}, []);

	useEffect(() => {
		const el = ref.current;
		if (!el) return;
		const link = new FrameLink(el);
		let ready = false;
		let readyTimer: number | undefined;
		let scrollTimer: number | undefined;
		let waitingScroll = false;
		let restore: { x: number; y: number } | null = null;

		const reload = () => {
			ready = false;
			el.src = url;
		};
		const offs = [
			link.on("blocked", (m) => cb.current.onBlocked(m.url)),
			link.on("present", (m) => cb.current.onPresent?.(m.on)),
			link.on("select", (m) => cb.current.onSelect?.(m)),
			link.on("anchor-pick", (m) => cb.current.onAnchorPick?.(m)),
			link.on("ready", () => {
				ready = true;
				clearTimeout(readyTimer);
				cb.current.onReady();
			}),
			link.on("scroll", (m) => {
				if (!waitingScroll) return;
				waitingScroll = false;
				clearTimeout(scrollTimer);
				restore = { x: m.x, y: m.y };
				reload();
			}),
			link.onLoaded(() => {
				setVisible(true);
				// 新加载的页面不在演示态
				cb.current.onPresent?.(false);
				if (restore) {
					link.post({ kind: "restore-scroll", ...restore });
					restore = null;
				}
				if (follow && !ready) {
					clearTimeout(readyTimer);
					readyTimer = window.setTimeout(() => {
						if (!ready) cb.current.onStale();
					}, READY_WAIT_MS);
				}
			}),
		];

		// 新版本：先问滚动位置，再重载帧根
		const offVersion = follow
			? realtime.onArtifact(artifactId, (msg) => {
					if (msg.type !== "artifact.version" || waitingScroll) return;
					waitingScroll = true;
					restore = null;
					link.post({ kind: "keep-scroll" });
					scrollTimer = window.setTimeout(() => {
						waitingScroll = false;
						reload();
					}, KEEP_SCROLL_WAIT_MS);
				})
			: () => {};
		// 同意请求落定时页面已经不在等（use 已超时回 null）：重载让页面重新 use（M14 步骤 7.3）
		const offConsent = follow
			? realtime.onArtifactConsent((msg) => {
					if (
						msg.kind === "settled" &&
						msg.reload === true &&
						msg.artifactId === artifactId
					)
						reload();
				})
			: () => {};

		// 父页面 CSP 拦下的帧内跳转（只报告源）：提示后把帧带回 artifact
		const onViolation = (e: SecurityPolicyViolationEvent) => {
			if (!e.violatedDirective.startsWith("frame-src")) return;
			cb.current.onBlocked(e.blockedURI);
			reload();
		};
		document.addEventListener("securitypolicyviolation", onViolation);
		cb.current.onLink?.((body) => link.post(body));

		return () => {
			for (const off of offs) off();
			offVersion();
			offConsent();
			clearTimeout(readyTimer);
			clearTimeout(scrollTimer);
			document.removeEventListener("securitypolicyviolation", onViolation);
			cb.current.onLink?.(null);
			link.dispose();
		};
	}, [artifactId, follow, url]);

	return (
		<>
			<iframe
				ref={ref}
				data-testid="artifact-frame"
				title={title}
				src={url}
				sandbox={ARTIFACT_SANDBOX}
				allow=""
				referrerPolicy="no-referrer"
				className={`block h-full w-full border-0 ${visible ? "" : "opacity-0"}`}
			/>
			{!visible && <DelayedSpinner />}
		</>
	);
}
