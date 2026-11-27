/**
 * artifact 的更多菜单（计划 M14 2.5、2.7、4.3、5.4、8.3）：卡片的「更多」按钮、卡片右键、查看器顶栏的「更多」共用同一份条目。
 * - 打开（卡片）、分享…（卡片；查看器顶栏有自己的「分享」按钮）、重新加载（仅查看器）、在浏览器中打开、导出…（页面与画布导出原始文件；
 *   文档的子菜单是 Word、Markdown；幻灯片的子菜单是 PowerPoint、PDF；Office 类型是「下载原件」，Word 与 PowerPoint 另有 PDF，
 *   Excel 的 PDF 需要 Office 引擎而置灰；渲染不可用时 PDF 置灰并给出原因）、上传新版本…（仅查看器里的 Office 类型）、下载 PNG（仅页面类型；
 *   渲染不可用时置灰并给出原因）、重命名…、版本历史、打开线程（有创建线程且当前不在它里面）、删除。
 * - 回收站里的条目只有恢复与永久删除…。
 * - 删除未发布的 artifact 不弹框：移到「最近删除」，toast 带「撤销」；已发布的先弹确认框（默认勾选「同时取消发布」）。
 *   导出与下载 PNG 用 toast 报告进度、结果与失败（可重试）。
 */
import { type QueryClient, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { type ReactNode, type RefObject, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import {
	type Artifact,
	type ArtifactDesign,
	artifactDetailQuery,
	artifactQk,
	fetchArtifactExport,
	fetchArtifactPng,
	openArtifactLink,
	uploadArtifactVersion,
	useArtifactCapabilities,
	useDeleteArtifact,
	useUndeleteArtifact,
} from "../../api/artifact-queries";
import { ApiError, api, unwrap } from "../../api/client";
import { dispositionName, downloadBlob } from "../../lib/artifacts/download";
import { exportDocx } from "../../lib/artifacts/export-docx";
import { exportPptx } from "../../lib/artifacts/export-pptx";
import { isOfficeType, OFFICE_EXT } from "../../lib/artifacts/office/formats";
import { sendGeneratedPrompt } from "../../state/actions";
import { showToast } from "../../state/toasts";
import { uiStore } from "../../state/ui";
import {
	describeError,
	IconButton,
	Menu,
	type MenuItem,
} from "../ui/primitives";
import { DeleteDialog, PurgeDialog, RenameDialog } from "./ArtifactDialogs";
import { MockupDialog } from "./MockupDialog";
import { ArtifactShareDialog } from "./SharePopover";
export type ArtifactMenuPlace = "card" | "viewer" | "trash";

export type MenuArtifact = Pick<
	Artifact,
	"id" | "title" | "kind" | "version" | "threadId" | "type" | "deletedAt"
> &
	Partial<Pick<Artifact, "published">>;

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

/** 文档、幻灯片与 Office 类型的导出格式：`md`、`pdf`、`original` 由服务端生成，`docx`、`pptx` 在浏览器里生成。 */
export type TypedExportFormat = "docx" | "md" | "pptx" | "pdf" | "original";

/** 读版本里的一个文件的字节。 */
async function fetchArtifactFile(
	id: string,
	version: number,
	path: string,
): Promise<Uint8Array> {
	const res = await api.GET("/api/v1/artifacts/{id}/file", {
		params: { path: { id }, query: { version, path } },
		parseAs: "arrayBuffer",
	});
	return new Uint8Array(await unwrap(Promise.resolve(res)));
}

/**
 * 导出文档（Word、Markdown）、幻灯片（PowerPoint、PDF）或 Office 文件（原件、PDF）：取卡片上的最新版本，入口与图片都从这个版本读。
 * 图片转不了时用占位代替，完成的 toast 里说明有几张。
 */
export async function exportTyped(
	a: Pick<Artifact, "id" | "title" | "version" | "type">,
	format: TypedExportFormat,
	t: TFunction,
): Promise<void> {
	const id = `artifact-export:${a.id}`;
	showToast({
		id,
		busy: true,
		text: t("artifacts.export.running", { title: a.title }),
	});
	try {
		let missing = 0;
		if (format === "md" || format === "pdf" || format === "original") {
			const { blob, disposition } = await fetchArtifactExport(
				a.id,
				format,
				a.version,
			);
			downloadBlob(
				blob,
				dispositionName(disposition) ??
					`${a.title}.${format === "original" && isOfficeType(a.type) ? OFFICE_EXT[a.type] : format}`,
			);
		} else {
			const readFile = (path: string) =>
				fetchArtifactFile(a.id, a.version, path);
			const entry = new TextDecoder().decode(
				await readFile(format === "docx" ? "document.md" : "slides.json"),
			);
			const out =
				format === "docx"
					? await exportDocx({ title: a.title, markdown: entry, readFile })
					: await exportPptx({ title: a.title, deckText: entry, readFile });
			// 标题里的 / 会被当成目录分隔（同服务端）
			downloadBlob(out.blob, `${a.title.replaceAll("/", "_")}.${format}`);
			missing = out.missing.length;
		}
		showToast({
			id,
			text: missing
				? t("artifacts.export.doneMissing", { title: a.title, count: missing })
				: t("artifacts.export.done", { title: a.title }),
		});
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
				run: () => void exportTyped(a, format, t),
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

/** 导出设计画布工程（`format=project`）：下载可独立构建与部署的 zip，失败时 toast 提示并支持重试。 */
export async function exportProject(
	a: Pick<Artifact, "id" | "title" | "version">,
	t: TFunction,
): Promise<void> {
	const id = `artifact-export:${a.id}`;
	showToast({
		id,
		busy: true,
		text: t("artifacts.export.running", { title: a.title }),
	});
	try {
		const { blob, disposition } = await fetchArtifactExport(
			a.id,
			"project",
			a.version,
		);
		downloadBlob(
			blob,
			dispositionName(disposition) ??
				`${a.title.replaceAll("/", "_")}-v${a.version}-project.zip`,
		);
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
				run: () => void exportProject(a, t),
			},
		});
	}
}

/** 部署到 box（M29 D3）：给作者线程发一条提示，让 agent 按 oryn-design 第 7 节执行 eject 并部署到 box。 */
export async function deployToBox(
	a: Pick<Artifact, "id" | "title" | "threadId">,
	t: TFunction,
): Promise<void> {
	if (!a.threadId) return;
	const dir = `~/box-${a.id}`;
	const prompt = t("artifacts.export.deployPrompt", {
		id: a.id,
		title: a.title,
		dir,
	});
	try {
		await sendGeneratedPrompt(a.threadId, prompt);
		showToast({
			text: t("artifacts.export.deploySent", { title: a.title }),
		});
	} catch (e) {
		showToast({
			tone: "err",
			text: t("artifacts.export.deployFailed", {
				title: a.title,
				reason: describeError(e, t),
			}),
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
	/** 卡片的统一上下文菜单（右键、`Shift+F10`、长按）读这份菜单项：每次渲染写入最新的取值函数。 */
	itemsRef?: RefObject<(() => MenuItem[]) | null>;
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
	itemsRef,
	tabIndex,
	align = "right",
}: ArtifactMenuProps): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const qc = useQueryClient();
	const caps = useArtifactCapabilities();
	const remove = useDeleteArtifact();
	const undelete = useUndeleteArtifact();
	const [dialog, setDialog] = useState<
		"rename" | "purge" | "share" | "delete" | "mockup" | null
	>(null);
	const uploadInput = useRef<HTMLInputElement>(null);

	// 上传新版本：以卡片上的最新版本为 baseVersion，期间有了新版本时服务端回 409
	const uploadVersion = async (file: File) => {
		const id = `artifact-upload:${a.id}`;
		showToast({
			id,
			busy: true,
			text: t("artifacts.upload.running", { title: a.title }),
		});
		try {
			await uploadArtifactVersion(a.id, a.version, file);
			void qc.invalidateQueries({ queryKey: artifactQk.all });
			showToast({ id, text: t("artifacts.upload.done", { title: a.title }) });
		} catch (e) {
			showToast({
				id,
				tone: "err",
				text:
					e instanceof ApiError && e.status === 409
						? t("artifacts.upload.conflict")
						: t("artifacts.upload.failed", {
								title: a.title,
								reason: describeError(e, t),
							}),
			});
		}
	};

	const restore = async () => {
		try {
			await undelete.mutateAsync(a.id);
			showToast({ text: t("artifacts.toast.restored", { title: a.title }) });
		} catch (e) {
			showToast({ tone: "err", text: describeError(e, t) });
		}
	};

	const deletedToast = () =>
		showToast({
			text: t("artifacts.toast.deleted", { title: a.title }),
			action: {
				label: t("artifacts.toast.undo"),
				run: () => void restore(),
			},
		});

	const del = async () => {
		try {
			await remove.mutateAsync({ id: a.id });
			deletedToast();
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
	// 配置了 Office 引擎（M32）时 Office 类型的 PDF 由引擎转换，不依赖渲染器
	const convert = caps.data?.office.convert === true;
	// 卡片上不另发 /design 请求；仅当查看器缓存明确标明非设计画布时隐藏工程导出与部署
	const designCache = qc.getQueryData<ArtifactDesign>(artifactQk.design(a.id));
	const showDesignDelivery =
		a.kind === "canvas" && designCache?.design !== false;
	const pdfItem: MenuItem = {
		key: "pdf",
		label: t("artifacts.export.pdf"),
		disabled: !render,
		...(!render
			? { title: renderReason ?? t("artifacts.menu.pngUnavailable") }
			: {}),
		onSelect: () => void exportTyped(a, "pdf", t),
	};
	// 引擎转换的 PDF：始终可用
	const enginePdfItem: MenuItem = {
		key: "pdf",
		label: t("artifacts.export.pdf"),
		onSelect: () => void exportTyped(a, "pdf", t),
	};
	const originalItem: MenuItem = {
		key: "original",
		label: t("artifacts.export.original", {
			ext: isOfficeType(a.type) ? OFFICE_EXT[a.type] : "",
		}),
		onSelect: () => void exportTyped(a, "original", t),
	};
	// 类型化 artifact 的导出子菜单
	const typedExport = (): MenuItem[] => {
		switch (a.type) {
			case "document":
				return [
					{
						key: "docx",
						label: t("artifacts.export.docx"),
						onSelect: () => void exportTyped(a, "docx", t),
					},
					{
						key: "md",
						label: t("artifacts.export.md"),
						onSelect: () => void exportTyped(a, "md", t),
					},
				];
			case "slides":
				return [
					{
						key: "pptx",
						label: t("artifacts.export.pptx"),
						onSelect: () => void exportTyped(a, "pptx", t),
					},
					pdfItem,
				];
			case "docx":
			case "docm":
			case "pptx":
			case "pptm":
				return [originalItem, convert ? enginePdfItem : pdfItem];
			case "xlsx":
			case "xlsm":
				// 表格的 PDF 只能由 Office 引擎转换（M32）
				return [
					originalItem,
					convert
						? enginePdfItem
						: {
								...pdfItem,
								disabled: true,
								title: t("artifacts.export.needsEngine"),
							},
				];
			default:
				return [originalItem];
		}
	};
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
								{
									key: "share",
									icon: "share" as const,
									label: t("artifacts.share.menu"),
									onSelect: () => setDialog("share"),
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
					...(a.type === undefined && a.kind === "canvas"
						? [
								{
									key: "export",
									icon: "export" as const,
									label: t("artifacts.menu.export"),
									attrs: { "data-menu": "export" } as const,
									children: [
										{
											key: "mockup",
											icon: "image" as const,
											label: t("artifacts.export.mockup"),
											attrs: { "data-menu": "export-mockup" } as const,
											disabled: !render,
											...(!render
												? {
														title:
															renderReason ??
															t("artifacts.menu.pngUnavailable"),
													}
												: {}),
											onSelect: () => setDialog("mockup"),
										},
										...(showDesignDelivery
											? [
													{
														key: "project",
														icon: "package" as const,
														label: t("artifacts.export.project"),
														attrs: { "data-menu": "export-project" } as const,
														onSelect: () => void exportProject(a, t),
													},
												]
											: []),
										{
											key: "raw",
											icon: "download" as const,
											label: t("artifacts.export.rawZip"),
											attrs: { "data-menu": "export-raw" } as const,
											onSelect: () => void exportRaw(qc, a, t),
										},
										...(showDesignDelivery && a.threadId
											? [
													{
														key: "deploy",
														icon: "rocket" as const,
														label: t("artifacts.export.deploy"),
														attrs: { "data-menu": "deploy-box" } as const,
														onSelect: () => void deployToBox(a, t),
													},
												]
											: []),
									],
								},
							]
						: a.type === undefined
							? [
									{
										key: "export",
										icon: "export" as const,
										label: t("artifacts.menu.export"),
										attrs: { "data-menu": "export" } as const,
										onSelect: () => void exportRaw(qc, a, t),
									},
								]
							: [
									{
										key: "export",
										icon: "export" as const,
										label: t("artifacts.menu.export"),
										attrs: { "data-menu": "export" } as const,
										children: typedExport(),
									},
								]),
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
					...(place === "viewer" && isOfficeType(a.type) && !a.deletedAt
						? [
								{
									key: "upload",
									icon: "upload" as const,
									label: t("artifacts.menu.upload"),
									attrs: { "data-menu": "upload" } as const,
									onSelect: () => uploadInput.current?.click(),
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
						onSelect: () => (a.published ? setDialog("delete") : void del()),
					},
				];

	if (itemsRef) itemsRef.current = () => items;
	return (
		<>
			<Menu
				align={align}
				items={items}
				trigger={(p) => (
					<IconButton
						icon="more"
						box="sm"
						size={15}
						tabIndex={tabIndex}
						data-testid="artifact-menu"
						label={t("artifacts.menu.more")}
						{...p}
					/>
				)}
			/>
			{place === "viewer" && isOfficeType(a.type) && !a.deletedAt && (
				<input
					ref={uploadInput}
					type="file"
					accept={`.${OFFICE_EXT[a.type]}`}
					hidden
					data-testid="artifact-upload-input"
					onChange={(e) => {
						const file = e.currentTarget.files?.[0];
						e.currentTarget.value = "";
						if (file) void uploadVersion(file);
					}}
				/>
			)}
			{dialog === "rename" && (
				<RenameDialog artifact={a} onClose={() => setDialog(null)} />
			)}
			{dialog === "purge" && (
				<PurgeDialog artifact={a} onClose={() => setDialog(null)} />
			)}
			{dialog === "share" && (
				<ArtifactShareDialog artifact={a} onClose={() => setDialog(null)} />
			)}
			{dialog === "delete" && (
				<DeleteDialog
					artifact={a}
					onClose={() => setDialog(null)}
					onDeleted={deletedToast}
				/>
			)}
			{dialog === "mockup" && (
				<MockupDialog artifact={a} onClose={() => setDialog(null)} />
			)}
		</>
	);
}
