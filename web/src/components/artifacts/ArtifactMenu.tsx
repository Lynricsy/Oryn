/**
 * artifact 的更多菜单（计划 M14 2.5、2.7、4.3）：卡片的「更多」按钮、卡片右键、查看器顶栏的「更多」共用同一份条目。
 * - 打开（卡片）、重新加载（仅查看器）、在浏览器中打开、导出…、下载 PNG（仅页面类型；渲染不可用时置灰并给出原因）、
 *   重命名…、版本历史、打开线程（有创建线程且当前不在它里面）、删除。
 * - 回收站里的条目只有恢复与永久删除…。
 * - 删除不弹框：移到「最近删除」，toast 带「撤销」。导出与下载 PNG 用 toast 报告进度、结果与失败（可重试）。
 */
import { type QueryClient, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { type ReactNode, type RefObject, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import {
	type Artifact,
	artifactDetailQuery,
	fetchArtifactExport,
	fetchArtifactPng,
	openArtifactLink,
	useArtifactCapabilities,
	useDeleteArtifact,
	useUndeleteArtifact,
} from "../../api/artifact-queries";
import { dispositionName, downloadBlob } from "../../lib/artifacts/download";
import { showToast } from "../../state/toasts";
import { uiStore } from "../../state/ui";
import {
	describeError,
	IconButton,
	Menu,
	type MenuItem,
} from "../ui/primitives";
import { PurgeDialog, RenameDialog } from "./ArtifactDialogs";

export type ArtifactMenuPlace = "card" | "viewer" | "trash";

export type MenuArtifact = Pick<
	Artifact,
	"id" | "title" | "kind" | "version" | "threadId" | "type" | "deletedAt"
>;

/**
 * 导出原始文件（plain artifact）：版本只有一个文件时请求 `format=html`（直接回那个文件），否则 `format=zip`。
 * 文件数取详情（缓存里有就不再请求）。
 */
export async function exportRaw(
	qc: QueryClient,
	a: Pick<Artifact, "id" | "title">,
	t: TFunction,
): Promise<void> {
	const id = `artifact-export:${a.id}`;
	showToast({
		id,
		busy: true,
		text: t("artifacts.export.running", { title: a.title }),
	});
	try {
		const detail = await qc.fetchQuery({
			...artifactDetailQuery(a.id),
			staleTime: 5_000,
		});
		const format = detail.files.length === 1 ? "html" : "zip";
		const { blob, disposition } = await fetchArtifactExport(a.id, format);
		downloadBlob(blob, dispositionName(disposition) ?? `${a.title}.${format}`);
		showToast({ id, text: t("artifacts.export.done", { title: a.title }) });
	} catch (e) {
		showToast({
			id,
			tone: "err",
			text: t("artifacts.export.failed", {
				title: a.title,
				reason: describeError(e, t),
			}),
			action: {
				label: t("artifacts.export.retry"),
				run: () => void exportRaw(qc, a, t),
			},
		});
	}
}

/** 下载 PNG：控制面以 1280×800 渲染入口页。 */
export async function downloadPng(
	a: Pick<Artifact, "id" | "title">,
	t: TFunction,
): Promise<void> {
	const id = `artifact-png:${a.id}`;
	showToast({
		id,
		busy: true,
		text: t("artifacts.png.running", { title: a.title }),
	});
	try {
		const { blob, disposition } = await fetchArtifactPng(a.id);
		downloadBlob(blob, dispositionName(disposition) ?? `${a.title}.png`);
		showToast({ id, text: t("artifacts.png.done", { title: a.title }) });
	} catch (e) {
		showToast({
			id,
			tone: "err",
			text: t("artifacts.png.failed", {
				title: a.title,
				reason: describeError(e, t),
			}),
			action: {
				label: t("artifacts.export.retry"),
				run: () => void downloadPng(a, t),
			},
		});
	}
}

export interface ArtifactMenuProps {
	artifact: MenuArtifact;
	place: ArtifactMenuPlace;
	/** 当前所在线程（右面板）：「打开线程」不指向它自己。 */
	threadId?: string;
	/** 查看器的「重新加载」。 */
	onReload?(): void;
	/** 查看器的「版本历史」（卡片上则跳到 `?history=1`）。 */
	onHistory?(): void;
	/** 外部打开菜单（卡片右键、`Shift+F10`）：挂载后写入打开函数。 */
	openRef?: RefObject<(() => void) | null>;
	/** 触发按钮的 tabIndex（卡片网格只有一个 Tab 停靠点，按钮为 -1）。 */
	tabIndex?: number;
	align?: "left" | "right";
}

export function ArtifactMenu({
	artifact: a,
	place,
	threadId,
	onReload,
	onHistory,
	openRef,
	tabIndex,
	align = "right",
}: ArtifactMenuProps): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const qc = useQueryClient();
	const caps = useArtifactCapabilities();
	const remove = useDeleteArtifact();
	const undelete = useUndeleteArtifact();
	const [dialog, setDialog] = useState<"rename" | "purge" | null>(null);

	const restore = async () => {
		try {
			await undelete.mutateAsync(a.id);
			showToast({ text: t("artifacts.toast.restored", { title: a.title }) });
		} catch (e) {
			showToast({ tone: "err", text: describeError(e, t) });
		}
	};

	const del = async () => {
		try {
			await remove.mutateAsync(a.id);
			showToast({
				text: t("artifacts.toast.deleted", { title: a.title }),
				action: {
					label: t("artifacts.toast.undo"),
					run: () => void restore(),
				},
			});
		} catch (e) {
			showToast({ tone: "err", text: describeError(e, t) });
		}
	};

	const openInBrowser = () => {
		openArtifactLink(a.id).then(
			(url) => window.open(url, "_blank", "noopener"),
			(e: unknown) =>
				showToast({
					tone: "err",
					text: t("artifacts.viewer.openFailed", {
						reason: describeError(e, t),
					}),
				}),
		);
	};

	const render = caps.data?.render === true;
	const renderReason = caps.data?.renderReason;
	const items: MenuItem[] =
		place === "trash"
			? [
					{
						key: "restore",
						icon: "restart",
						label: t("artifacts.menu.restore"),
						onSelect: () => void restore(),
					},
					{
						key: "purge",
						icon: "trash",
						danger: true,
						label: t("artifacts.menu.purge"),
						onSelect: () => setDialog("purge"),
					},
				]
			: [
					...(place === "card"
						? [
								{
									key: "open",
									icon: "shapes" as const,
									label: t("artifacts.menu.open"),
									onSelect: () => navigate(`/artifacts/${a.id}`),
								},
							]
						: []),
					...(place === "viewer" && onReload
						? [
								{
									key: "reload",
									icon: "refresh" as const,
									label: t("artifacts.viewer.reload"),
									onSelect: onReload,
								},
							]
						: []),
					{
						key: "browser",
						icon: "external",
						label: t("artifacts.viewer.openInBrowser"),
						onSelect: openInBrowser,
					},
					...(a.type === undefined
						? [
								{
									key: "export",
									icon: "export" as const,
									label: t("artifacts.menu.export"),
									attrs: { "data-menu": "export" } as const,
									onSelect: () => void exportRaw(qc, a, t),
								},
							]
						: []),
					...(a.kind === "web"
						? [
								{
									key: "png",
									icon: "image" as const,
									label: t("artifacts.menu.png"),
									disabled: !render,
									...(!render
										? {
												title:
													renderReason ?? t("artifacts.menu.pngUnavailable"),
											}
										: {}),
									onSelect: () => void downloadPng(a, t),
								},
							]
						: []),
					{
						key: "rename",
						icon: "edit",
						label: t("artifacts.menu.rename"),
						onSelect: () => setDialog("rename"),
					},
					{
						key: "history",
						icon: "history",
						label: t("artifacts.menu.history"),
						onSelect: () =>
							onHistory
								? onHistory()
								: navigate(`/artifacts/${a.id}?history=1`),
					},
					...(a.threadId && a.threadId !== threadId
						? [
								{
									key: "thread",
									icon: "message" as const,
									label: t("artifacts.menu.thread"),
									onSelect: () => {
										const tid = a.threadId as string;
										navigate(`/t/${tid}`);
										uiStore.getState().openArtifact(tid, a.id);
									},
								},
							]
						: []),
					{
						key: "delete",
						icon: "trash",
						danger: true,
						label: t("artifacts.menu.delete"),
						onSelect: () => void del(),
					},
				];

	return (
		<>
			<Menu
				align={align}
				items={items}
				trigger={(p) => {
					if (openRef) openRef.current = p.onClick;
					return (
						<IconButton
							icon="more"
							box="sm"
							size={15}
							tabIndex={tabIndex}
							data-testid="artifact-menu"
							label={t("artifacts.menu.more")}
							{...p}
						/>
					);
				}}
			/>
			{dialog === "rename" && (
				<RenameDialog artifact={a} onClose={() => setDialog(null)} />
			)}
			{dialog === "purge" && (
				<PurgeDialog artifact={a} onClose={() => setDialog(null)} />
			)}
		</>
	);
}
