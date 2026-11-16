/**
 * artifact 查看器（计划 M14 2.3）：整页（`page`）与右面板（`panel`）共用。
 * 挂载时 `watchArtifact`（收 `artifact.version` 等观看者消息），卸载时取消。
 * 顶栏：返回（仅整页，`Esc`）、类型图标、标题、版本胶囊 `v{n}`、更多菜单（`menu` 插槽）；右侧重新加载、在浏览器中打开。
 * 右面板里由 `ArtifactPanel` 用 `lead` 换掉图标与标题（换成 artifact 切换菜单）、用 `actions` 追加「放大查看」，
 * 两者合成一行工具条。页面试图跳走时帧上方出一条提示：「此页面试图打开 <主机>」。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	openArtifactLink,
	useArtifact,
	useUndeleteArtifact,
} from "../../api/artifact-queries";
import { ApiError } from "../../api/client";
import { kindIcon } from "../../lib/artifacts/kinds";
import { watchArtifact } from "../../realtime/socket";
import { useSidebarShown } from "../../state/ui";
import { ChromeLead } from "../layout/WindowControls";
import { Icon } from "../ui/icons";
import { Button, describeError, IconButton } from "../ui/primitives";
import { PageFrame } from "./PageFrame";
import {
	FrameErrorState,
	ViewerDeleted,
	ViewerLoading,
	ViewerNotFound,
} from "./viewer-states";

export type ViewerVariant = "page" | "panel";

export interface ArtifactViewerProps {
	artifactId: string;
	variant: ViewerVariant;
	/** 顶栏的更多菜单（2.5 的 `ArtifactMenu`）。 */
	menu?: ReactNode;
	/** 换掉顶栏的类型图标与标题（右面板放 artifact 切换菜单）。 */
	lead?: ReactNode;
	/** 顶栏最右侧追加的按钮（右面板的「放大查看」）。 */
	actions?: ReactNode;
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

/** 焦点在输入框、可编辑区域或有对话框打开时，`Esc` 不归查看器。 */
function escOwnedElsewhere(e: KeyboardEvent): boolean {
	if (document.querySelector("[role=dialog]")) return true;
	const el = e.target as HTMLElement | null;
	if (!el || typeof el.closest !== "function") return false;
	return !!el.closest("input, textarea, select, [contenteditable]");
}

export function ArtifactViewer({
	artifactId,
	variant,
	menu,
	lead,
	actions,
	onBack,
}: ArtifactViewerProps): ReactNode {
	const { t } = useTranslation();
	const detail = useArtifact(artifactId);
	const undelete = useUndeleteArtifact();
	const [reloadNonce, setReloadNonce] = useState(0);
	const [blocked, setBlocked] = useState<string | null>(null);
	const [openError, setOpenError] = useState<unknown>(null);
	const chromeLeft = !useSidebarShown() && variant === "page";

	useEffect(() => watchArtifact(artifactId), [artifactId]);

	useEffect(() => {
		if (variant !== "page" || !onBack) return;
		const onKey = (e: KeyboardEvent) => {
			if (e.key !== "Escape" || e.defaultPrevented || escOwnedElsewhere(e))
				return;
			onBack();
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, [variant, onBack]);

	const artifact = detail.data?.artifact;
	const live = artifact && artifact.deletedAt === undefined;

	const openInBrowser = (url?: string) => {
		setOpenError(null);
		if (url) {
			window.open(url, "_blank", "noopener");
			return;
		}
		openArtifactLink(artifactId).then(
			(link) => window.open(link, "_blank", "noopener"),
			(err: unknown) => setOpenError(err),
		);
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
				reloadNonce={reloadNonce}
				title={t("artifacts.viewer.frameTitle", { title })}
				onBlocked={(url) => {
					const safe = safeBlockedUrl(url);
					if (safe) setBlocked(safe);
				}}
			/>
		);

	return (
		<div
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
					<span
						data-testid="artifact-version"
						title={t("artifacts.viewer.versionTitle", { n: artifact.version })}
						className="shrink-0 rounded-full border border-border px-1.5 font-mono text-2xs text-muted tabular-nums leading-5"
					>
						{t("artifacts.viewer.version", { n: artifact.version })}
					</span>
				)}
				{live && menu}
				<span className="flex-1" />
				{live && (
					<>
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
			<div className={`min-h-0 flex-1 ${panel ? "p-2" : "p-2 sm:p-3"}`}>
				{body}
			</div>
		</div>
	);
}
