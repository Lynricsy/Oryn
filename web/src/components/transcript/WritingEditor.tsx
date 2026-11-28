/**
 * 写作块的所见即所得编辑器（Tiptap 3 / ProseMirror，Markdown 为规范存储）。全仓唯一 import Tiptap 的文件，
 * 由 WritingBlock 懒加载：转录里没有写作块时不下载编辑器。
 * - 每次编辑经 `scheduleDraftSave` 防抖存到 orynd；失焦与卸载时立即发出。
 * - 外部（其它设备、恢复原稿）改了 `markdown` 时，只在没有聚焦、也没有待发保存时同步进编辑器，不打断正在输入的人。
 * - 选中文字出现气泡菜单：粗体、斜体、链接、「要求修改」（引用进输入框，与转录引用胶囊同一路径）。
 */
import { useQueryClient } from "@tanstack/react-query";
import { Image } from "@tiptap/extension-image";
import { TaskItem, TaskList } from "@tiptap/extension-list";
import { TableKit } from "@tiptap/extension-table";
import { Markdown as MarkdownExt } from "@tiptap/markdown";
import {
	type Editor,
	EditorContent,
	useEditor,
	useEditorState,
} from "@tiptap/react";
import { BubbleMenu } from "@tiptap/react/menus";
import { StarterKit } from "@tiptap/starter-kit";
import { type ReactNode, useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import type { DraftKind } from "../../lib/draft-blocks";
import { normalizeQuote } from "../../lib/quote";
import { patchComposerDraft } from "../../state/composer-drafts";
import {
	flushDraftSaves,
	hasPendingDraftSave,
	scheduleDraftSave,
} from "../../state/draft-saves";
import type { IconName } from "../ui/icons";
import { IconButton } from "../ui/primitives";

export interface WritingEditorProps {
	threadId: string;
	entryId: string;
	blockKey: string;
	kind: DraftKind;
	title: string;
	markdown: string;
	/** 全屏层打开时把光标放到文末 */
	autoFocus?: boolean;
	/** 编辑器就绪 / 销毁时回报（写作块的复制、下载、邮件读编辑器里的最新内容，不等防抖保存） */
	onEditor?(editor: Editor | null): void;
}

/** 链接只接受 http(s) 与 mailto；其余（javascript: 等）一律拒绝。 */
const SAFE_HREF = /^(https?:|mailto:)/i;

export default function WritingEditor({
	threadId,
	entryId,
	blockKey,
	kind,
	title,
	markdown,
	autoFocus = false,
	onEditor,
}: WritingEditorProps): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	// onUpdate 闭包只在创建编辑器时绑定一次：可变的入参经 ref 读最新值
	const latest = useRef({ qc, threadId, entryId, blockKey, kind, title });
	latest.current = { qc, threadId, entryId, blockKey, kind, title };
	const editor = useEditor({
		extensions: [
			StarterKit.configure({
				underline: false,
				link: { openOnClick: false, autolink: true },
			}),
			TaskList,
			TaskItem.configure({ nested: true }),
			TableKit,
			Image,
			// breaks 与转录静态渲染的 remark-breaks 一致：邮件落款的单换行不被合并
			MarkdownExt.configure({ markedOptions: { gfm: true, breaks: true } }),
		],
		content: markdown,
		contentType: "markdown",
		immediatelyRender: true,
		shouldRerenderOnTransaction: false,
		autofocus: autoFocus ? "end" : false,
		editorProps: {
			attributes: {
				class: "md wb-editor",
				"data-writing-editor": "",
				"aria-label": title,
				role: "textbox",
				"aria-multiline": "true",
			},
		},
		onUpdate: ({ editor: e }) => {
			const p = latest.current;
			scheduleDraftSave(p.qc, p.threadId, p.entryId, p.blockKey, {
				kind: p.kind,
				title: p.title,
				text: e.getMarkdown(),
			});
		},
		onBlur: () => {
			void flushDraftSaves(latest.current.threadId);
		},
	});

	useEffect(() => {
		onEditor?.(editor);
		return () => onEditor?.(null);
	}, [editor, onEditor]);

	// 卸载（全屏切换、转录虚拟化移出视口）时立即发出待发保存
	useEffect(
		() => () => {
			void flushDraftSaves(latest.current.threadId);
		},
		[],
	);

	// 外部内容变化：没在输入、也没有待发保存时才同步，免得覆盖用户刚敲的字
	useEffect(() => {
		if (editor.isDestroyed || editor.isFocused) return;
		if (hasPendingDraftSave(threadId, entryId, blockKey)) return;
		if (editor.getMarkdown() === markdown) return;
		editor.commands.setContent(markdown, {
			contentType: "markdown",
			emitUpdate: false,
		});
	}, [editor, markdown, threadId, entryId, blockKey]);

	const state = useEditorState({
		editor,
		selector: ({ editor: e }) => ({
			bold: e.isActive("bold"),
			italic: e.isActive("italic"),
			heading: e.isActive("heading", { level: 2 }),
			bullet: e.isActive("bulletList"),
			ordered: e.isActive("orderedList"),
			task: e.isActive("taskList"),
			link: e.isActive("link"),
			canUndo: e.can().undo(),
			canRedo: e.can().redo(),
		}),
	});

	const editLink = () => {
		const prev = editor.getAttributes("link").href as string | undefined;
		const raw = window.prompt(t("writingBlock.linkPrompt"), prev ?? "https://");
		if (raw === null) return;
		const href = raw.trim();
		const chain = editor.chain().focus().extendMarkRange("link");
		if (!href) {
			chain.unsetLink().run();
			return;
		}
		if (!SAFE_HREF.test(href)) return;
		chain.setLink({ href }).run();
	};

	const askChanges = () => {
		const { from, to } = editor.state.selection;
		const text = normalizeQuote(editor.state.doc.textBetween(from, to, "\n"));
		if (!text) return;
		void flushDraftSaves(threadId);
		patchComposerDraft(threadId, { quote: { text } });
		// 收起选区：气泡菜单随之隐藏
		editor.commands.setTextSelection(to);
	};

	const tools: {
		key: string;
		icon: IconName;
		label: string;
		active?: boolean;
		disabled?: boolean;
		run(): void;
	}[] = [
		{
			key: "undo",
			icon: "undo",
			label: t("writingBlock.undo"),
			disabled: !state.canUndo,
			run: () => editor.chain().focus().undo().run(),
		},
		{
			key: "redo",
			icon: "redo",
			label: t("writingBlock.redo"),
			disabled: !state.canRedo,
			run: () => editor.chain().focus().redo().run(),
		},
		{
			key: "bold",
			icon: "textBold",
			label: t("writingBlock.bold"),
			active: state.bold,
			run: () => editor.chain().focus().toggleBold().run(),
		},
		{
			key: "italic",
			icon: "textItalic",
			label: t("writingBlock.italic"),
			active: state.italic,
			run: () => editor.chain().focus().toggleItalic().run(),
		},
		{
			key: "heading",
			icon: "heading",
			label: t("writingBlock.heading"),
			active: state.heading,
			run: () => editor.chain().focus().toggleHeading({ level: 2 }).run(),
		},
		{
			key: "bullet",
			icon: "listBullets",
			label: t("writingBlock.bulletList"),
			active: state.bullet,
			run: () => editor.chain().focus().toggleBulletList().run(),
		},
		{
			key: "ordered",
			icon: "listNumbers",
			label: t("writingBlock.orderedList"),
			active: state.ordered,
			run: () => editor.chain().focus().toggleOrderedList().run(),
		},
		{
			key: "task",
			icon: "checkSquare",
			label: t("writingBlock.taskList"),
			active: state.task,
			run: () => editor.chain().focus().toggleTaskList().run(),
		},
		{
			key: "link",
			icon: "link",
			label: t("writingBlock.link"),
			active: state.link,
			run: editLink,
		},
	];

	return (
		<>
			<div className="wb-toolbar" role="toolbar" aria-label={title}>
				{tools.map((x) => (
					<IconButton
						key={x.key}
						icon={x.icon}
						label={x.label}
						active={x.active}
						disabled={x.disabled}
						box="sm"
						size={15}
						// 按下不抢焦点：保留编辑器里的选区
						onMouseDown={(e) => e.preventDefault()}
						onClick={x.run}
					/>
				))}
			</div>
			<div className="wb-body">
				<EditorContent editor={editor} />
			</div>
			<BubbleMenu
				editor={editor}
				options={{ placement: "top", offset: 6 }}
				className="wb-bubble"
			>
				<IconButton
					icon="textBold"
					label={t("writingBlock.bold")}
					active={state.bold}
					box="xs"
					size={14}
					onMouseDown={(e) => e.preventDefault()}
					onClick={() => editor.chain().focus().toggleBold().run()}
				/>
				<IconButton
					icon="textItalic"
					label={t("writingBlock.italic")}
					active={state.italic}
					box="xs"
					size={14}
					onMouseDown={(e) => e.preventDefault()}
					onClick={() => editor.chain().focus().toggleItalic().run()}
				/>
				<IconButton
					icon="link"
					label={t("writingBlock.link")}
					active={state.link}
					box="xs"
					size={14}
					onMouseDown={(e) => e.preventDefault()}
					onClick={editLink}
				/>
				<span className="wb-bubble-sep" aria-hidden />
				<button
					type="button"
					className="wb-bubble-ask"
					data-testid="writing-ask-changes"
					onMouseDown={(e) => e.preventDefault()}
					onClick={askChanges}
				>
					{t("writingBlock.askChanges")}
				</button>
			</BubbleMenu>
		</>
	);
}
