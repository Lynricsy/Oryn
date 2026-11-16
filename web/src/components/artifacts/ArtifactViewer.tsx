/**
 * artifact 查看器（计划 M14 2.3、2.5、2.6）：整页（`page`）与右面板（`panel`）共用。
 * 挂载时 `watchArtifact`（收 `artifact.version` 等观看者消息），卸载时取消。
 * 顶栏：返回（仅整页，`Esc`）、类型图标、标题、版本胶囊 `v{n}`（点开版本历史）、更多菜单（`ArtifactMenu`）；
 * 右侧版本历史、重新加载、在浏览器中打开。右面板里由 `ArtifactPanel` 用 `lead` 换掉图标与标题（换成 artifact 切换菜单）、
 * 用 `actions` 追加「放大查看」，两者合成一行工具条。页面试图跳走时帧上方出一条提示：「此页面试图打开 <主机>」。
 *
 * 版本历史（2.6）是可开关的侧栏列（宽 300）：查看器窄于 900 时覆盖在内容上，`compact` 断点下占满并在顶部给
 * 「页面 / 历史」分段切换。选一个旧版本：帧以该版本重新申请（服务端不注入运行时，只读），帧上方出状态条
 * 「版本 n / head · 时间 · 备注」与「恢复这个版本」；恢复以当前 head 为 `baseVersion`，409 时提示重试。
 */
import {
	type ReactNode,
	type RefObject,
	useEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import {
	type ArtifactVersion,
	openArtifactLink,
	useArtifact,
	useRestoreArtifactVersion,
	useUndeleteArtifact,
} from "../../api/artifact-queries";
import { ApiError } from "../../api/client";
import { kindIcon } from "../../lib/artifacts/kinds";
import { formatRelative } from "../../lib/format";
import { useBreakpoint } from "../../lib/viewport";
import { watchArtifact } from "../../realtime/socket";
import { showToast } from "../../state/toasts";
import { useSidebarShown } from "../../state/ui";
import { ChromeLead } from "../layout/WindowControls";
import { Icon } from "../ui/icons";
import { Button, describeError, IconButton, tabClass } from "../ui/primitives";
import { ArtifactMenu } from "./ArtifactMenu";
import { PageFrame } from "./PageFrame";
import { VersionHistory } from "./VersionHistory";
import {
	FrameErrorState,
	ViewerDeleted,
	ViewerLoading,
	ViewerNotFound,
} from "./viewer-states";

export type ViewerVariant = "page" | "panel";

/** 侧栏列固定宽度与「并排放得下」的查看器宽度（更窄时侧栏覆盖在内容上）。 */
export const SIDE_WIDTH = 300;
export const SIDE_INLINE_MIN = 900;

export interface ArtifactViewerProps {
	artifactId: string;
	variant: ViewerVariant;
	/** 换掉顶栏的类型图标与标题（右面板放 artifact 切换菜单）。 */
	lead?: ReactNode;
	/** 顶栏最右侧追加的按钮（右面板的「放大查看」）。 */
	actions?: ReactNode;
	/** 所在线程（右面板）：更多菜单的「打开线程」不指向它自己。 */
	threadId?: string;
	/** 进入时就打开版本历史（`/artifacts/<id>?history=1`）。 */
	initialHistory?: boolean;
	/** 整页的返回（按钮与 `Esc`）。 */
	onBack?(): void;
}

/**
 * 页面报告的被拦截地址规整成 `http(s)` 的绝对 URL；别的协议（`javascript:`、`data:`…）一律丢弃——
 * 地址来自不可信的页面，之后要交给 `window.open`。
 */
export function safeBlockedUrl(raw: string): string | null {
	try {
		const url = new URL(raw);
		return url.protocol === "http:" || url.protocol === "https:"
			? url.href
			: null;
	} catch {
		return null;
	}
}

function isNotFound(error: unknown): boolean {
	return (
		error instanceof ApiError &&
		(error.status === 404 || error.code === "not_found")
	);
}

/** 焦点在输入框、可编辑区域或有对话框 / 菜单打开时，`Esc` 不归查看器。 */
function escOwnedElsewhere(e: KeyboardEvent): boolean {
	if (document.querySelector("[role=dialog], [role=menu]")) return true;
	const el = e.target as HTMLElement | null;
	if (!el || typeof el.closest !== "function") return false;
	return !!el.closest("input, textarea, select, [contenteditable]");
}

/** 查看器自身的宽度（侧栏列并排还是覆盖）。没有 ResizeObserver 的环境按窄处理。 */
function useWidth(ref: RefObject<HTMLElement | null>): number {
	const [width, setWidth] = useState(0);
	useEffect(() => {
		const el = ref.current;
		if (!el || typeof ResizeObserver === "undefined") return;
		const ro = new ResizeObserver(([entry]) => {
			if (entry) setWidth(entry.contentRect.width);
		});
		ro.observe(el);
		return () => ro.disconnect();
	}, [ref]);
	return width;
}

export function ArtifactViewer({
	artifactId,
	variant,
	lead,
	actions,
	threadId,
	initialHistory = false,
	onBack,
}: ArtifactViewerProps): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const detail = useArtifact(artifactId);
	const undelete = useUndeleteArtifact();
	const restore = useRestoreArtifactVersion();
	const [reloadNonce, setReloadNonce] = useState(0);
	const [blocked, setBlocked] = useState<string | null>(null);
	const [openError, setOpenError] = useState<unknown>(null);
	const [history, setHistory] = useState(initialHistory);
	// compact 下侧栏打开时的分段：页面 / 历史
	const [segment, setSegment] = useState<"page" | "history">("history");
	// 正在看的旧版本；null = 跟随最新
	const [pinned, setPinned] = useState<ArtifactVersion | null>(null);
	// 类型化 artifact 的查看器在演示：帧容器铺满窗口
	const [presenting, setPresenting] = useState(false);
	const chromeLeft = !useSidebarShown() && variant === "page";
	const rootRef = useRef<HTMLDivElement>(null);
	const width = useWidth(rootRef);
	const compact = bp === "compact";
	const inline = !compact && width >= SIDE_INLINE_MIN;

	useEffect(() => watchArtifact(artifactId), [artifactId]);

	// Esc 依次：回到最新版本 → 关掉历史 → 返回（整页）
	const escRef = useRef<() => boolean>(() => false);
	escRef.current = () => {
		if (pinned) {
			setPinned(null);
			return true;
		}
		if (history) {
			setHistory(false);
			return true;
		}
		if (variant === "page" && onBack) {
			onBack();
			return true;
		}
		return false;
	};
	useEffect(() => {
		const onKey = (e: KeyboardEvent) => {
			if (e.key !== "Escape" || e.defaultPrevented || escOwnedElsewhere(e))
				return;
			if (escRef.current()) e.preventDefault();
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, []);

	const artifact = detail.data?.artifact;
	const live = artifact && artifact.deletedAt === undefined;
	const typed = artifact?.type !== undefined;
	const head = artifact?.version ?? 0;
	// 选中的是最新版本就回到跟随
	const pinnedVersion = pinned && pinned.version !== head ? pinned : null;

	const openInBrowser = (url?: string) => {
		setOpenError(null);
		if (url) {
			window.open(url, "_blank", "noopener");
			return;
		}
		openArtifactLink(artifactId, pinnedVersion?.version).then(
			(link) => window.open(link, "_blank", "noopener"),
			(err: unknown) => setOpenError(err),
		);
	};

	const toggleHistory = () => {
		setSegment("history");
		setHistory((h) => !h);
	};

	const restorePinned = async () => {
		if (!pinnedVersion || restore.isPending) return;
		try {
			const r = await restore.mutateAsync({
				id: artifactId,
				version: pinnedVersion.version,
				baseVersion: head,
			});
			if (r.kind === "conflict") {
				showToast({ tone: "err", text: t("artifacts.history.conflict") });
				return;
			}
			setPinned(null);
			showToast({
				text: t("artifacts.history.restored", {
					n: pinnedVersion.version,
					version: r.artifact.version,
				}),
			});
		} catch (e) {
			showToast({ tone: "err", text: describeError(e, t) });
		}
	};

	const panel = variant === "panel";
	const title = artifact?.title ?? "";

	let body: ReactNode;
	if (detail.isPending) body = <ViewerLoading />;
	else if (detail.isError)
		body = isNotFound(detail.error) ? (
			<ViewerNotFound />
		) : (
			<FrameErrorState
				error={detail.error}
				onRetry={() => void detail.refetch()}
			/>
		);
	else if (artifact?.deletedAt !== undefined)
		body = (
			<ViewerDeleted
				deletedAt={artifact.deletedAt}
				restoring={undelete.isPending}
				onRestore={() => undelete.mutate(artifactId)}
			/>
		);
	else
		body = (
			<PageFrame
				artifactId={artifactId}
				version={pinnedVersion?.version}
				reloadNonce={reloadNonce}
				title={t("artifacts.viewer.frameTitle", { title })}
				onBlocked={(url) => {
					const safe = safeBlockedUrl(url);
					if (safe) setBlocked(safe);
				}}
				onPresent={typed ? setPresenting : undefined}
			/>
		);

	const showSide = history && !!live;
	const sideOnly = showSide && compact && segment === "history";

	return (
		<div
			ref={rootRef}
			data-testid="artifact-viewer"
			data-variant={variant}
			className="flex h-full min-h-0 flex-col"
		>
			<div
				role="toolbar"
				aria-label={t("artifacts.viewer.toolbar")}
				data-window-drag={panel ? undefined : ""}
				className={`flex shrink-0 items-center gap-1 border-border border-b px-2 ${panel ? "h-11" : "h-12 sm:px-3"}`}
			>
				{chromeLeft && <ChromeLead />}
				{variant === "page" && onBack && (
					<IconButton
						icon="back"
						label={t("artifacts.viewer.back")}
						onClick={onBack}
					/>
				)}
				{lead ?? (
					<h1 className="flex min-w-0 items-center gap-2 px-1 font-semibold text-sm">
						{artifact && (
							<Icon
								name={kindIcon(artifact.kind)}
								size={15}
								className="shrink-0 text-faint"
							/>
						)}
						<span data-testid="artifact-title" className="truncate">
							{title}
						</span>
					</h1>
				)}
				{artifact && (
					<button
						type="button"
						data-testid="artifact-version"
						title={t("artifacts.viewer.versionTitle", { n: artifact.version })}
						aria-pressed={showSide}
						disabled={!live}
						onClick={toggleHistory}
						className="shrink-0 rounded-full border border-border px-1.5 font-mono text-2xs text-muted tabular-nums leading-5 transition-colors duration-150 enabled:hover:border-border-strong enabled:hover:text-fg"
					>
						{t("artifacts.viewer.version", { n: artifact.version })}
					</button>
				)}
				{live && (
					<ArtifactMenu
						artifact={artifact}
						place="viewer"
						threadId={threadId}
						align="left"
						onReload={() => {
							setBlocked(null);
							setReloadNonce((n) => n + 1);
						}}
						onHistory={() => {
							setSegment("history");
							setHistory(true);
						}}
					/>
				)}
				<span className="flex-1" />
				{live && (
					<>
						<IconButton
							icon="history"
							box="sm"
							size={15}
							active={showSide}
							data-testid="artifact-history-toggle"
							label={t("artifacts.history.title")}
							onClick={toggleHistory}
						/>
						<IconButton
							icon="refresh"
							box="sm"
							size={15}
							label={t("artifacts.viewer.reload")}
							onClick={() => {
								setBlocked(null);
								setReloadNonce((n) => n + 1);
							}}
						/>
						<IconButton
							icon="external"
							box="sm"
							size={15}
							label={t("artifacts.viewer.openInBrowser")}
							onClick={() => openInBrowser()}
						/>
					</>
				)}
				{actions}
			</div>
			{(blocked !== null || openError !== null) && (
				<div
					role="status"
					data-testid={blocked ? "artifact-blocked" : "artifact-open-error"}
					className="flex shrink-0 items-center gap-2 border-border border-b bg-inset px-3 py-1.5 text-sm"
				>
					<Icon
						name={blocked ? "globe" : "alert"}
						size={14}
						className="shrink-0 text-muted"
					/>
					<span className="min-w-0 flex-1 truncate">
						{blocked
							? t("artifacts.viewer.blocked", { host: new URL(blocked).host })
							: t("artifacts.viewer.openFailed", {
									reason: describeError(openError, t),
								})}
					</span>
					{blocked && (
						<Button size="sm" onClick={() => openInBrowser(blocked)}>
							{t("artifacts.viewer.openInBrowser")}
						</Button>
					)}
					<Button
						size="sm"
						variant="ghost"
						onClick={() => {
							setBlocked(null);
							setOpenError(null);
						}}
					>
						{t("common.close")}
					</Button>
				</div>
			)}
			{showSide && compact && (
				<div
					role="tablist"
					aria-label={t("artifacts.history.segments")}
					className="flex shrink-0 items-center gap-0.5 border-border border-b px-2 py-1"
				>
					{(["page", "history"] as const).map((s) => (
						<button
							key={s}
							type="button"
							role="tab"
							aria-selected={segment === s}
							className={`${tabClass(segment === s)} ${segment === s ? "bg-hover" : ""}`}
							onClick={() => setSegment(s)}
						>
							{s === "page"
								? t("artifacts.history.segmentPage")
								: t("artifacts.history.title")}
						</button>
					))}
				</div>
			)}
			<div className="relative flex min-h-0 flex-1">
				<div
					className={`flex min-h-0 min-w-0 flex-1 flex-col ${sideOnly ? "hidden" : ""}`}
				>
					{pinnedVersion && live && (
						<div
							role="status"
							data-testid="artifact-pinned"
							className="flex shrink-0 flex-wrap items-center gap-2 border-border border-b bg-inset px-3 py-1.5 text-sm"
						>
							<Icon name="history" size={14} className="shrink-0 text-muted" />
							<span className="min-w-0 flex-1 truncate">
								{t(
									pinnedVersion.note
										? "artifacts.history.pinnedNote"
										: "artifacts.history.pinned",
									{
										n: pinnedVersion.version,
										head,
										when: formatRelative(pinnedVersion.createdAt),
										note: pinnedVersion.note ?? "",
									},
								)}
							</span>
							<Button size="sm" variant="ghost" onClick={() => setPinned(null)}>
								{t("artifacts.history.backToLatest")}
							</Button>
							<Button
								size="sm"
								icon="restart"
								data-testid="artifact-restore-version"
								disabled={restore.isPending}
								onClick={() => void restorePinned()}
							>
								{t("artifacts.history.restore")}
							</Button>
						</div>
					)}
					<div
						data-testid="artifact-frame-box"
						className={
							presenting && typed && live
								? "fixed inset-0 z-50"
								: `min-h-0 flex-1 ${panel ? "p-2" : "p-2 sm:p-3"}`
						}
					>
						{body}
					</div>
				</div>
				{showSide && (!compact || segment === "history") && (
					<aside
						data-testid="artifact-side"
						data-inline={inline ? "" : undefined}
						aria-label={t("artifacts.history.title")}
						style={compact ? undefined : { width: SIDE_WIDTH }}
						className={
							compact
								? "flex min-h-0 w-full flex-col bg-panel"
								: inline
									? "flex min-h-0 shrink-0 flex-col border-border border-l bg-panel"
									: "absolute inset-y-0 right-0 z-10 flex max-w-full min-h-0 flex-col border-border border-l bg-panel shadow-xl"
						}
					>
						<VersionHistory
							artifactId={artifactId}
							head={head}
							threads={detail.data?.threads ?? []}
							selected={pinnedVersion?.version ?? head}
							onSelect={(v) => {
								setPinned(v.version === head ? null : v);
								if (compact) setSegment("page");
							}}
							onClose={() => setHistory(false)}
						/>
					</aside>
				)}
			</div>
		</div>
	);
}
