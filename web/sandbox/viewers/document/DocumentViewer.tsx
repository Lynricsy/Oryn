// 文档 artifact 的查看器（计划 M14 5.2）：渲染 document.md；`content` 能力可用且看的是最新版本时可以按块编辑，
// 保存成新版本。`?view=thumbnail` 只有画面。

import hljs from "highlight.js/lib/common";
import {
	type KeyboardEvent,
	type ReactNode,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { resolveAsset, type ViewMode } from "../shared/config";
import type { ViewerContent } from "../shared/content";
import { EditBar, SaveStatusBar } from "../shared/EditBar";
import type { ViewerStrings } from "../shared/strings";
import { replaceBlock, splitBlocks } from "./blocks";

const REMARK_PLUGINS = [remarkGfm];

function markdownComponents(base: string): Components {
	return {
		// 外链会被运行时拦下并交给外壳提示；不开新窗口
		a: ({ node: _, href, children, ...rest }) => (
			<a {...rest} href={href} target="_self">
				{children}
			</a>
		),
		img: ({ node: _, src, alt, ...rest }) => (
			<img
				{...rest}
				src={typeof src === "string" ? resolveAsset(src, base) : undefined}
				alt={alt ?? ""}
				loading="lazy"
			/>
		),
		code: ({ node: _, className, children, ...rest }) => {
			const lang = /language-([\w+#-]+)/.exec(className ?? "")?.[1];
			if (lang && hljs.getLanguage(lang)) {
				const html = hljs.highlight(String(children).replace(/\n$/, ""), {
					language: lang,
					ignoreIllegals: true,
				}).value;
				const cls = `hljs ${className}`;
				const inner = { __html: html };
				// biome-ignore lint/security/noDangerouslySetInnerHtml: highlight.js 输出已转义
				return <code className={cls} dangerouslySetInnerHTML={inner} />;
			}
			return (
				<code {...rest} className={className}>
					{children}
				</code>
			);
		},
	};
}

function Markdown({
	text,
	components,
}: {
	text: string;
	components: Components;
}): ReactNode {
	return (
		<ReactMarkdown
			remarkPlugins={REMARK_PLUGINS}
			skipHtml
			components={components}
		>
			{text}
		</ReactMarkdown>
	);
}

export function DocumentViewer({
	content,
	view,
	strings,
}: {
	content: ViewerContent;
	view: ViewMode;
	strings: ViewerStrings;
}): ReactNode {
	const { doc } = content;
	// 编辑态的草稿全文；null = 不在编辑
	const [draft, setDraft] = useState<string | null>(null);
	const [active, setActive] = useState<number | null>(null);
	const base = doc?.base ?? "";
	const components = useMemo(() => markdownComponents(base), [base]);
	const blocks = useMemo(
		() => (draft === null ? [] : splitBlocks(draft)),
		[draft],
	);

	if (content.state === "loading")
		return <p className="oryn-placeholder">{strings.loading}</p>;
	if (content.state === "error" || !doc)
		return (
			<p className="oryn-placeholder" role="alert">
				{strings.loadFailed}
			</p>
		);

	const thumbnail = view === "thumbnail";
	const saving = content.status.kind === "saving";

	const stopEditing = () => {
		setDraft(null);
		setActive(null);
	};
	const save = async () => {
		if (draft === null) return;
		const outcome = await content.save(draft);
		if (outcome !== "error") stopEditing();
	};

	return (
		<div
			className={`oryn-doc-page${thumbnail ? " oryn-doc-thumbnail" : ""}${draft !== null ? " oryn-doc-editing" : ""}`}
		>
			{!thumbnail && (
				<SaveStatusBar
					status={content.status}
					strings={strings}
					onDismiss={content.dismiss}
				/>
			)}
			{!thumbnail && content.editable && draft === null && (
				<button
					type="button"
					className="oryn-btn oryn-doc-edit"
					onClick={() => {
						content.dismiss();
						setDraft(doc.text);
					}}
				>
					{strings.edit}
				</button>
			)}
			<article className="oryn-doc">
				{draft === null ? (
					<Markdown text={doc.text} components={components} />
				) : (
					blocks.map((b, i) =>
						active === i ? (
							<BlockEditor
								// 草稿每次变化都会重新拆块；按位置作 key，编辑框在提交前不会被换掉
								// biome-ignore lint/suspicious/noArrayIndexKey: 块没有稳定标识，位置就是身份
								key={i}
								source={b.body}
								onDone={(body) => {
									setDraft(replaceBlock(blocks, i, body));
									setActive(null);
								}}
							/>
						) : (
							// 整块上盖一个透明按钮：块里有标题、表格等，不能放进 <button>
							<div
								// biome-ignore lint/suspicious/noArrayIndexKey: 块没有稳定标识，位置就是身份
								key={i}
								className="oryn-block"
							>
								<Markdown text={b.body} components={components} />
								<button
									type="button"
									className="oryn-block-hit"
									aria-label={strings.editBlock}
									onClick={() => setActive(i)}
								/>
							</div>
						),
					)
				)}
			</article>
			{draft !== null && (
				<EditBar
					hint={strings.documentHint}
					nextVersion={doc.head + 1}
					dirty={draft !== doc.text}
					saving={saving}
					strings={strings}
					onDiscard={stopEditing}
					onSave={() => void save()}
				/>
			)}
		</div>
	);
}

/** 一块的源码编辑框：高度随内容；失焦或 `Esc` 把改动并入草稿。 */
function BlockEditor({
	source,
	onDone,
}: {
	source: string;
	onDone(body: string): void;
}): ReactNode {
	const ref = useRef<HTMLTextAreaElement>(null);
	const [value, setValue] = useState(source);

	useLayoutEffect(() => {
		const el = ref.current;
		if (!el) return;
		el.style.height = "auto";
		el.style.height = `${el.scrollHeight}px`;
	});

	useLayoutEffect(() => {
		ref.current?.focus();
	}, []);

	const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
		if (e.key !== "Escape") return;
		e.preventDefault();
		e.currentTarget.blur();
	};

	return (
		<textarea
			ref={ref}
			className="oryn-block-editor"
			value={value}
			spellCheck={false}
			onChange={(e) => setValue(e.target.value)}
			onKeyDown={onKeyDown}
			onBlur={() => onDone(value)}
		/>
	);
}
