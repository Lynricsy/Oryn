/**
 * 写作块：助手消息里 ```draft-email / draft-message / draft-doc 围栏渲染成的草稿卡片。
 * - 头栏：类型图标 + 标题（邮件写主题）+「已编辑」小标；右侧复制、全屏与「⋯」菜单（.md / .docx 下载、存为 Artifact、
 *   在邮件应用中打开、恢复原稿）。流式起草中只显示「正在起草…」。
 * - 正文：可编辑时懒加载 Tiptap 编辑器（WritingEditor），加载前与只读（分享页、流式中、没有条目 id）时按普通 Markdown 渲染。
 * - 用户改稿存在 orynd（`threads/{id}/drafts`），显示时优先于 agent 原稿；下一条用户消息由 orynd 附给 agent。
 * 本文件不 import Tiptap（只有类型），编辑器的代码只在真的出现可编辑写作块时下载。
 */
import { useQueryClient } from "@tanstack/react-query";
import type { Editor } from "@tiptap/react";
import {
	lazy,
	type ReactNode,
	Suspense,
	useCallback,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { useImportArtifact } from "../../api/artifact-queries";
import { useThreadDrafts } from "../../api/draft-queries";
import { downloadBlob } from "../../lib/artifacts/download";
import { exportDocx } from "../../lib/artifacts/export-docx";
import { copyToClipboard } from "../../lib/clipboard";
import { copyWithToast } from "../../lib/context-menu/selection-groups";
import {
	type DraftAttrs,
	type DraftKind,
	draftFileBase,
	draftTitle,
} from "../../lib/draft-blocks";
import type { DraftEditRef } from "../../lib/draft-edits";
import { isTopModal, pushModal, trapTab } from "../../lib/modal";
import { flushDraftSaves, resetDraft } from "../../state/draft-saves";
import { showToast } from "../../state/toasts";
import { Markdown } from "../markdown/Markdown";
import { Badge } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";
import {
	describeError,
	IconButton,
	Menu,
	type MenuItem,
} from "../ui/primitives";
import { useTranscriptContext } from "./context";
import "./writing-block.css";

const LazyWritingEditor = lazy(() => import("./WritingEditor"));

const KIND_ICON: Record<DraftKind, IconName> = {
	email: "envelope",
	message: "message",
	doc: "fileText",
};

/** mailto 地址的安全长度：超过时正文改走剪贴板（各平台邮件客户端对超长 mailto 的处理不一）。 */
const MAILTO_MAX = 1900;

export interface WritingBlockProps {
	/** 助手消息的转录条目 id；流式中的在途消息还没有，此时只读 */
	entryId?: string;
	/** 块键 `<contentIndex>.<ordinal>` */
	blockKey: string;
	kind: DraftKind;
	attrs: DraftAttrs;
	/** agent 原稿（围栏正文） */
	body: string;
	/** 流式起草中：围栏还没闭合 */
	composing: boolean;
}

export function WritingBlock({
	entryId,
	blockKey,
	kind,
	attrs,
	body,
	composing,
}: WritingBlockProps): ReactNode {
	const { t } = useTranslation();
	const ctx = useTranscriptContext();
	const qc = useQueryClient();
	const importArtifact = useImportArtifact();
	const readOnly = !!ctx.readOnly;
	const editable = !composing && !!entryId && !readOnly;
	const drafts = useThreadDrafts(ctx.threadId, !readOnly);
	const saved = entryId
		? drafts.data?.get(`${entryId}/${blockKey}`)
		: undefined;
	const current = saved?.text ?? body;
	// 恢复原稿时若改稿已送达过，会把原稿 PUT 回去（让 agent 知道已恢复）：此时不算「已编辑」
	const edited = saved != null && saved.text !== body;
	const title = draftTitle(kind, attrs, t);
	const editorRef = useRef<Editor | null>(null);
	const bodyRef = useRef<HTMLDivElement>(null);
	const [copied, setCopied] = useState(false);
	const [full, setFull] = useState(false);
	// 恢复原稿后重建编辑器（清掉撤销栈，内容回到原稿）
	const [generation, setGeneration] = useState(0);
	const onEditor = useCallback((e: Editor | null) => {
		editorRef.current = e;
	}, []);

	useEffect(() => {
		if (!copied) return;
		const id = setTimeout(() => setCopied(false), 1500);
		return () => clearTimeout(id);
	}, [copied]);

	/** 当前内容：编辑器在就读编辑器（含还没保存的输入），否则读已存改稿 / 原稿。 */
	const markdownNow = () => editorRef.current?.getMarkdown() ?? current;

	const copy = async () => {
		const md = markdownNow();
		const rich =
			typeof ClipboardItem !== "undefined" && !!navigator.clipboard?.write;
		if (rich) {
			const html =
				editorRef.current?.getHTML() ??
				bodyRef.current?.querySelector(".md")?.innerHTML ??
				"";
			try {
				await navigator.clipboard.write([
					new ClipboardItem({
						"text/html": new Blob([html], { type: "text/html" }),
						"text/plain": new Blob([md], { type: "text/plain" }),
					}),
				]);
				setCopied(true);
				return;
			} catch {
				// 富文本写入被拒（权限、浏览器不支持 text/html）：退回纯文本
			}
		}
		if (await copyToClipboard(md)) setCopied(true);
		else showToast({ text: t("contextMenu.copyFailed"), tone: "err" });
	};

	const base = draftFileBase(title);

	const downloadDocx = async () => {
		try {
			const out = await exportDocx({
				title,
				markdown: markdownNow(),
				readFile: () => Promise.reject(new Error("no local files")),
			});
			downloadBlob(out.blob, `${base}.docx`);
		} catch (e) {
			showToast({
				text: `${t("writingBlock.exportFailed")}: ${describeError(e, t)}`,
				tone: "err",
			});
		}
	};

	const saveArtifact = async () => {
		const name = `${base}.md`;
		const files = new FormData();
		files.append(
			name,
			new Blob([markdownNow()], { type: "text/markdown" }),
			name,
		);
		try {
			const a = await importArtifact.mutateAsync({
				files,
				threadId: ctx.threadId,
			});
			const open = ctx.openArtifact;
			showToast({
				text: t("writingBlock.savedAsArtifact"),
				...(open
					? {
							action: {
								label: t("writingBlock.openArtifact"),
								run: () => open(a.id),
							},
						}
					: {}),
			});
		} catch (e) {
			showToast({ text: describeError(e, t), tone: "err" });
		}
	};

	const openInMail = async () => {
		const head = `mailto:${encodeURIComponent(attrs.to ?? "")}?subject=${encodeURIComponent(attrs.subject ?? attrs.title ?? "")}`;
		// 用 innerText 而非 textContent：按渲染布局给出段落与列表的换行，邮件正文才可读
		const plain =
			// biome-ignore lint/performance/useDomNodeTextContent: 需要布局感知的换行
			(editorRef.current?.view.dom as HTMLElement | undefined)?.innerText ??
			// biome-ignore lint/performance/useDomNodeTextContent: 同上
			bodyRef.current?.innerText ??
			current;
		let href = `${head}&body=${encodeURIComponent(plain)}`;
		if (href.length > MAILTO_MAX) {
			href = head;
			await copy();
			showToast({ text: t("writingBlock.mailBodyCopied") });
		}
		// 经真实链接点击打开：壳里的全局链接拦截把 mailto: 交给系统邮件应用
		const a = document.createElement("a");
		a.href = href;
		a.rel = "noopener";
		document.body.appendChild(a);
		a.click();
		a.remove();
	};

	const revert = async () => {
		if (!entryId || !saved) return;
		try {
			await resetDraft(
				qc,
				ctx.threadId,
				entryId,
				blockKey,
				{ kind, title, text: body },
				saved.deliveredAt != null,
			);
			setGeneration((g) => g + 1);
		} catch (e) {
			showToast({
				text: `${t("writingBlock.saveFailed")}: ${describeError(e, t)}`,
				tone: "err",
			});
		}
	};

	const closeFull = () => {
		setFull(false);
		void flushDraftSaves(ctx.threadId);
	};

	const items: MenuItem[] = [
		{
			key: "copyMarkdown",
			icon: "copy",
			label: t("writingBlock.copyMarkdown"),
			onSelect: () => void copyWithToast(markdownNow()),
		},
		{
			key: "downloadMd",
			icon: "download",
			label: t("writingBlock.downloadMd"),
			onSelect: () =>
				downloadBlob(
					new Blob([markdownNow()], { type: "text/markdown;charset=utf-8" }),
					`${base}.md`,
				),
		},
		{
			key: "downloadDocx",
			icon: "download",
			label: t("writingBlock.downloadDocx"),
			onSelect: () => void downloadDocx(),
		},
		...(readOnly
			? []
			: [
					{
						key: "saveArtifact",
						icon: "filePlus",
						label: t("writingBlock.saveArtifact"),
						onSelect: () => void saveArtifact(),
					} satisfies MenuItem,
				]),
		...(kind === "email"
			? [
					{
						key: "openInMail",
						icon: "envelope",
						label: t("writingBlock.openInMail"),
						onSelect: () => void openInMail(),
					} satisfies MenuItem,
				]
			: []),
		...(edited && editable
			? [
					{
						key: "revert",
						icon: "undo",
						label: t("writingBlock.revert"),
						onSelect: () => void revert(),
					} satisfies MenuItem,
				]
			: []),
	];

	const staticBody = (
		<div className="wb-body">
			<Markdown text={current} streaming={composing} />
		</div>
	);

	return (
		<div
			className="wb"
			data-writing-block=""
			data-kind={kind}
			data-testid="writing-block"
		>
			<div className="wb-head">
				<Icon
					name={KIND_ICON[kind]}
					size={15}
					aria-hidden
					className="shrink-0 text-muted"
				/>
				<div className="min-w-0 flex-1">
					<div className="wb-title" data-testid="writing-block-title">
						{title}
					</div>
					{kind === "email" && attrs.to && (
						<div className="wb-sub">{attrs.to}</div>
					)}
				</div>
				{edited && !composing && (
					<Badge icon="edit">{t("writingBlock.edited")}</Badge>
				)}
				{composing ? (
					<span className="wb-composing">{t("writingBlock.composing")}</span>
				) : (
					<div className="flex shrink-0 items-center gap-0.5">
						<IconButton
							icon={copied ? "check" : "copy"}
							label={t("writingBlock.copy")}
							box="sm"
							size={15}
							className={copied ? "text-ok!" : ""}
							data-testid="writing-block-copy"
							onClick={() => void copy()}
						/>
						{editable && (
							<IconButton
								icon="maximize"
								label={t("writingBlock.fullscreen")}
								box="sm"
								size={15}
								data-testid="writing-block-fullscreen"
								onClick={() => setFull(true)}
							/>
						)}
						<Menu
							items={items}
							trigger={(p) => (
								<IconButton
									icon="more"
									label={t("writingBlock.more")}
									box="sm"
									size={15}
									{...p}
								/>
							)}
						/>
					</div>
				)}
			</div>
			<div ref={bodyRef}>
				{editable && entryId && !full ? (
					<Suspense fallback={staticBody}>
						<LazyWritingEditor
							key={generation}
							threadId={ctx.threadId}
							entryId={entryId}
							blockKey={blockKey}
							kind={kind}
							title={title}
							markdown={current}
							onEditor={onEditor}
						/>
					</Suspense>
				) : (
					staticBody
				)}
			</div>
			{full && editable && entryId && (
				<FullscreenLayer
					title={title}
					copied={copied}
					onCopy={() => void copy()}
					onClose={closeFull}
				>
					<Suspense fallback={staticBody}>
						<LazyWritingEditor
							key={generation}
							threadId={ctx.threadId}
							entryId={entryId}
							blockKey={blockKey}
							kind={kind}
							title={title}
							markdown={current}
							autoFocus
							onEditor={onEditor}
						/>
					</Suspense>
				</FullscreenLayer>
			)}
		</div>
	);
}

/** 全屏编辑层：进模态栈（其余界面 inert）、Tab 圈在层内、Esc 关闭，关闭后焦点还给打开者。 */
function FullscreenLayer({
	title,
	copied,
	onCopy,
	onClose,
	children,
}: {
	title: string;
	copied: boolean;
	onCopy(): void;
	onClose(): void;
	children: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const hostRef = useRef<HTMLDivElement>(null);
	const dialogRef = useRef<HTMLDivElement>(null);
	const onCloseRef = useRef(onClose);
	onCloseRef.current = onClose;
	useLayoutEffect(() => {
		const host = hostRef.current;
		const dialog = dialogRef.current;
		if (!host || !dialog) return;
		const opener =
			document.activeElement instanceof HTMLElement
				? document.activeElement
				: null;
		const release = pushModal(host);
		const onKey = (e: KeyboardEvent) => {
			if (!isTopModal(host)) return;
			if (e.key === "Tab") {
				trapTab(e, dialog);
				return;
			}
			if (e.key === "Escape") {
				e.preventDefault();
				onCloseRef.current();
			}
		};
		window.addEventListener("keydown", onKey);
		return () => {
			window.removeEventListener("keydown", onKey);
			release();
			if (opener?.isConnected) opener.focus();
		};
	}, []);
	return createPortal(
		<div ref={hostRef} className="fixed inset-0 z-50">
			<div
				ref={dialogRef}
				role="dialog"
				aria-modal="true"
				aria-label={title}
				data-testid="writing-block-full"
				className="wb-full"
			>
				<div className="wb-full-head">
					<div className="wb-title min-w-0 flex-1">{title}</div>
					<IconButton
						icon={copied ? "check" : "copy"}
						label={t("writingBlock.copy")}
						box="sm"
						size={15}
						className={copied ? "text-ok!" : ""}
						onClick={onCopy}
					/>
					<IconButton
						icon="minimize"
						label={t("writingBlock.closeFullscreen")}
						box="sm"
						size={15}
						onClick={onClose}
					/>
				</div>
				<div className="wb-full-scroll">
					<div className="wb wb-full-card">{children}</div>
				</div>
			</div>
		</div>,
		document.body,
	);
}

/** 用户气泡里的「附上了改过的草稿」芯片（orynd 追加的 `<oryn-draft-edits>` 块不按原文显示）。 */
export function DraftEditChips({
	drafts,
}: {
	drafts: readonly DraftEditRef[];
}): ReactNode {
	const { t } = useTranslation();
	if (drafts.length === 0) return null;
	return (
		<ul className="mt-2 flex flex-wrap gap-1.5" data-testid="draft-edit-chips">
			{drafts.map((d, i) => (
				<li
					// biome-ignore lint/suspicious/noArrayIndexKey: 芯片按块内顺序固定，标题可能重复
					key={i}
					className="inline-flex max-w-full items-center gap-1.5 rounded-md border border-border bg-panel px-2 py-1 text-xs"
				>
					<Icon name="edit" size={13} aria-hidden className="shrink-0" />
					<span className="min-w-0 truncate">
						{t("writingBlock.editedChip", {
							title: d.title || t(`writingBlock.kind.${d.kind}`),
						})}
					</span>
				</li>
			))}
		</ul>
	);
}
