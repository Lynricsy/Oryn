/**
 * HTML 视图（M33，docs/plan-m33.md 的 C8 / C9 / C10）：把一次 render_html 调用挂进独立源的视图帧。
 *
 * - `mount` 为假（参数流式中、尚未开始执行）时只显示骨架；为真后经 `openFrame` 申请帧（条目可能还没落库，失败时退避重试 3 次）。
 * - 首帧主题走 URL 片段（`#theme=…`），之后的主题变化经 `theme` 消息下发。
 * - 8 秒没收到 `ready` 就以 `fresh` 重新申请一次（orynd 重启或帧被淘汰），再失败显示可重试的错误态。
 * - 高度：`spec.height` 固定；否则跟随页面的 `resize`，夹在 120–1600 px，按 toolCallId 记住上次的高度，重新挂载时不跳动。
 * - 回传：每次调用只收第一条 `prompt`；已作答（含在途）时一律忽略；不可回传期间暂存第一条，可回传时再交付。
 */
import {
	type ReactNode,
	useCallback,
	useEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import {
	encodeThemeFragment,
	VIEW_MAX_HEIGHT,
	VIEW_MIN_HEIGHT,
	type ViewTheme,
	type ViewThemeVar,
} from "../../../sandbox/view/messages";
import { HTML_BLOCK, type ViewAnswer } from "../../lib/view/answer";
import type { HtmlViewSpec } from "../../lib/view/spec";
import { THEME_EVENT } from "../../state/theme";
import { Button } from "../ui/primitives";
import { ViewLink } from "./view-link";

export interface HtmlViewProps {
	toolCallId: string;
	spec: HtmlViewSpec;
	/** 可挂帧：调用已开始执行（tool_execution_start）或已成功结束。false 时只显示骨架。 */
	mount: boolean;
	/** 申请帧根地址；fresh = 旧帧失效需重新申请。没有时（如 mock）显示可重试的错误态。 */
	openFrame?: (toolCallId: string, fresh?: boolean) => Promise<string>;
	/** 已作答或在途：之后的 sendPrompt 一律忽略。 */
	answered: boolean;
	/** 能否回传。false 期间收到的第一条 prompt 暂存，变为 true 时再交付；没有 onAnswer（只读页）时丢弃。 */
	interactive: boolean;
	onAnswer?: (answer: ViewAnswer, text: string) => Promise<void>;
}

/** 等帧报 `ready` 的时长。 */
export const READY_TIMEOUT_MS = 8000;
/** 申请帧失败后的重试间隔（共重试 3 次）。 */
export const OPEN_RETRY_MS = [500, 1500, 3000] as const;
/** 跟随内容、还不知道高度时的初始高度。 */
const INITIAL_HEIGHT = 160;

/** 按 toolCallId 记住的上次高度（模块级：虚拟滚动、折叠展开后重新挂载不跳动）。 */
const lastHeights = new Map<string, number>();

/** 视图主题变量 → 应用里的令牌（与 lib/svg-theme.ts 的映射一致，另加 --bg --font --mono --radius）。 */
const THEME_SOURCE: Record<ViewThemeVar, string> = {
	"--fg": "--text",
	"--muted": "--text-muted",
	"--border": "--line",
	"--surface": "--panel",
	"--accent": "--brand",
	"--success": "--good",
	"--warning": "--caution",
	"--error": "--bad",
	"--c1": "--viz-1",
	"--c2": "--viz-2",
	"--c3": "--viz-3",
	"--c4": "--viz-4",
	"--c5": "--viz-5",
	"--c6": "--viz-6",
	"--bg": "--bg",
	"--font": "--sans",
	"--mono": "--mono",
	"--radius": "--r-md",
};

/** 当前应用主题的解析值（getComputedStyle 已把 var() 代换掉）。 */
export function currentViewTheme(): ViewTheme {
	const root = document.documentElement;
	const style = getComputedStyle(root);
	const vars: ViewTheme["vars"] = {};
	for (const [name, source] of Object.entries(THEME_SOURCE) as [
		ViewThemeVar,
		string,
	][]) {
		const v = style.getPropertyValue(source).trim();
		if (v) vars[name] = v;
	}
	return { scheme: root.dataset.theme === "dark" ? "dark" : "light", vars };
}

type Phase = "opening" | "loading" | "ready" | "error";

export function HtmlView({
	toolCallId,
	spec,
	mount,
	openFrame,
	answered,
	interactive,
	onAnswer,
}: HtmlViewProps): ReactNode {
	const { t } = useTranslation();
	const fixed = spec.height;
	const [height, setHeight] = useState(
		() => fixed ?? lastHeights.get(toolCallId) ?? INITIAL_HEIGHT,
	);
	const [src, setSrc] = useState<string | null>(null);
	const [phase, setPhase] = useState<Phase>("opening");
	// 每次递增都重新申请帧；fresh 表示这次要关掉旧帧重新申请
	const [attempt, setAttempt] = useState(0);
	const freshRef = useRef(false);
	// 8 秒没 ready 时已经重新申请过一次
	const refreshedRef = useRef(false);
	const [reloadNonce, setReloadNonce] = useState(0);
	const [sendError, setSendError] = useState<string | null>(null);
	const frameRef = useRef<HTMLIFrameElement | null>(null);

	// ---- 申请帧 ----
	// 宿主每次渲染可能给出新的 openFrame：只按「有没有」重新申请，调用时取最新的
	const openRef = useRef(openFrame);
	openRef.current = openFrame;
	const canOpen = openFrame !== undefined;
	// biome-ignore lint/correctness/useExhaustiveDependencies: attempt 递增即重新申请
	useEffect(() => {
		if (!mount) return;
		setSrc(null);
		if (!canOpen) {
			setPhase("error");
			return;
		}
		setPhase("opening");
		let cancelled = false;
		const timers: number[] = [];
		const fresh = freshRef.current;
		freshRef.current = false;
		const tryOpen = (n: number) => {
			const open = openRef.current;
			if (!open) return;
			open(toolCallId, fresh && n === 0).then(
				(url) => {
					if (cancelled) return;
					setSrc(`${url}#${encodeThemeFragment(currentViewTheme())}`);
					setPhase("loading");
				},
				() => {
					if (cancelled) return;
					const delay = OPEN_RETRY_MS[n];
					if (delay === undefined) setPhase("error");
					else timers.push(window.setTimeout(() => tryOpen(n + 1), delay));
				},
			);
		};
		tryOpen(0);
		return () => {
			cancelled = true;
			for (const timer of timers) clearTimeout(timer);
		};
	}, [mount, canOpen, toolCallId, attempt]);

	// ---- 等 ready ----
	// biome-ignore lint/correctness/useExhaustiveDependencies: 帧里换了新文档（src / reloadNonce）就重新计时
	useEffect(() => {
		if (phase !== "loading") return;
		const timer = setTimeout(() => {
			if (refreshedRef.current) {
				setPhase("error");
				return;
			}
			refreshedRef.current = true;
			freshRef.current = true;
			setAttempt((n) => n + 1);
		}, READY_TIMEOUT_MS);
		return () => clearTimeout(timer);
	}, [phase, src, reloadNonce]);

	const retry = () => {
		refreshedRef.current = false;
		freshRef.current = true;
		setAttempt((n) => n + 1);
	};

	// ---- 回传 ----
	// 最新的 props 给帧消息的回调用（ViewLink 只在帧挂上时建一次）
	const live = useRef({ answered, interactive, onAnswer });
	live.current = { answered, interactive, onAnswer };
	// 这次调用已收下一条 prompt（交付中、已交付或暂存）；交付失败时清掉，页面重新加载后可以再发
	const receivedRef = useRef(false);
	const pendingRef = useRef<string | null>(null);

	const deliver = useCallback((text: string) => {
		const submit = live.current.onAnswer;
		if (!submit) return;
		setSendError(null);
		submit({ block: HTML_BLOCK, kind: "prompt" }, text).catch(
			(err: unknown) => {
				receivedRef.current = false;
				setSendError(err instanceof Error ? err.message : String(err));
			},
		);
	}, []);

	const onPrompt = useCallback(
		(text: string) => {
			const cur = live.current;
			if (cur.answered || receivedRef.current || !cur.onAnswer) return;
			receivedRef.current = true;
			if (cur.interactive) deliver(text);
			else pendingRef.current = text;
		},
		[deliver],
	);

	useEffect(() => {
		const text = pendingRef.current;
		if (text === null) return;
		if (answered || !onAnswer) {
			pendingRef.current = null;
			return;
		}
		if (!interactive) return;
		pendingRef.current = null;
		deliver(text);
	}, [answered, interactive, onAnswer, deliver]);

	// ---- 帧通道 ----
	// biome-ignore lint/correctness/useExhaustiveDependencies: 帧元素随 src / reloadNonce（iframe 的 key）重建，通道跟着重建
	useEffect(() => {
		const frame = frameRef.current;
		if (!frame || !src) return;
		const link = new ViewLink(frame, {
			load: () => link.post({ kind: "theme", ...currentViewTheme() }),
			// 页面导航离开了视图文档：通道已作废，换成可重试的错误态（重试会换新帧）
			navigated: () => setPhase("error"),
			ready: () => setPhase("ready"),
			resize: (h) => {
				if (fixed !== undefined) return;
				const clamped = Math.round(
					Math.min(VIEW_MAX_HEIGHT, Math.max(VIEW_MIN_HEIGHT, h)),
				);
				lastHeights.set(toolCallId, clamped);
				setHeight(clamped);
			},
			prompt: onPrompt,
		});
		const onTheme = () => link.post({ kind: "theme", ...currentViewTheme() });
		window.addEventListener(THEME_EVENT, onTheme);
		return () => {
			window.removeEventListener(THEME_EVENT, onTheme);
			link.dispose();
		};
	}, [src, reloadNonce, fixed, toolCallId, onPrompt]);

	useEffect(() => {
		if (fixed !== undefined) setHeight(fixed);
	}, [fixed]);

	const title = spec.title ? (
		<h3 className="m-0 text-balance font-semibold text-base text-fg leading-6">
			{spec.title}
		</h3>
	) : null;

	if (!mount)
		return (
			<section
				aria-label={spec.title ?? t("view.html.label")}
				className="flex w-full min-w-0 flex-col gap-2"
			>
				{title}
				<div
					data-testid="html-view-skeleton"
					className="flex animate-pulse items-center justify-center rounded-lg border border-border bg-inset text-faint text-xs"
					style={{ height }}
				>
					{t("view.html.loading")}
				</div>
			</section>
		);

	return (
		<section
			aria-label={spec.title ?? t("view.html.label")}
			className="flex w-full min-w-0 flex-col gap-2"
		>
			{title}
			{phase === "error" ? (
				<div
					data-testid="html-view-error"
					className="flex flex-col items-center justify-center gap-2 rounded-lg border border-border bg-inset p-4 text-center text-muted text-xs"
					style={{ minHeight: Math.min(height, 200) }}
				>
					<span>
						{openFrame ? t("view.html.failed") : t("view.html.unavailable")}
					</span>
					<Button size="sm" icon="refresh" onClick={retry}>
						{t("view.html.retry")}
					</Button>
				</div>
			) : (
				<div
					className={`overflow-hidden rounded-lg border border-border ${phase === "ready" ? "" : "animate-pulse bg-inset"}`}
					style={{ height }}
				>
					{src && (
						<iframe
							key={`${src}:${reloadNonce}`}
							ref={frameRef}
							src={src}
							title={spec.title ?? t("view.html.label")}
							sandbox="allow-scripts"
							referrerPolicy="no-referrer"
							className="block h-full w-full border-0 bg-transparent"
						/>
					)}
				</div>
			)}
			{answered && (
				<p data-testid="html-view-sent" className="m-0 text-2xs text-muted">
					{t("view.html.sent")}
				</p>
			)}
			{sendError !== null && !answered && (
				<p
					role="alert"
					className="m-0 flex flex-wrap items-center gap-2 text-2xs text-bad"
				>
					{t("view.html.sendFailed", { error: sendError })}
					<Button
						size="sm"
						variant="ghost"
						onClick={() => {
							setSendError(null);
							setPhase("loading");
							setReloadNonce((n) => n + 1);
						}}
					>
						{t("view.html.reload")}
					</Button>
				</p>
			)}
		</section>
	);
}
