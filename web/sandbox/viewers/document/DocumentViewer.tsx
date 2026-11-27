// 文档 artifact 的查看器（计划 M14 5.2）：渲染 document.md；`content` 能力可用且看的是最新版本时可以按块编辑，
// 保存成新版本。`?view=thumbnail` 只有画面。
// 评论（M31 7.4）：评论模式下点块不进入编辑，而是报出块序号与块文字；图钉按块文字重新定位后画在块左侧。

import hljs from "highlight.js/lib/common";
import {
	type KeyboardEvent,
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import {
	clipText,
	NO_PINS,
	pickPoint,
} from "../../../src/lib/artifacts/office/anchor";
import { PinDot } from "../../../src/lib/artifacts/office/pins";
import type { AnchorPin } from "../../runtime/messages";
import type { ViewerComments } from "../shared/bridge";
import { resolveAsset, type ViewMode } from "../shared/config";
import type { ViewerContent } from "../shared/content";
import { EditBar, SaveStatusBar } from "../shared/EditBar";
import type { ViewerStrings } from "../shared/strings";
import { relocateBlock, replaceBlock, splitBlocks } from "./blocks";

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

/** 图钉落到哪一块：块序号为空的是整篇的；块文字对不上任何一块的不画。 */
function placePins(
	texts: readonly string[],
	pins: readonly AnchorPin[],
): { whole: AnchorPin[]; byBlock: Map<number, AnchorPin[]> } {
	const whole: AnchorPin[] = [];
	const byBlock = new Map<number, AnchorPin[]>();
	for (const p of pins) {
		const { block, text } = p.anchor;
		if (block === null) {
			whole.push(p);
			continue;
		}
		const at = relocateBlock(texts, block, text);
		if (at === null) continue;
		const list = byBlock.get(at);
		if (list) list.push(p);
		else byBlock.set(at, [p]);
	}
	return { whole, byBlock };
}

export function DocumentViewer({
	content,
	view,
	strings,
	comments,
}: {
	content: ViewerContent;
	view: ViewMode;
	strings: ViewerStrings;
	/** 帧运行时的评论桥；没有时不画图钉、不响应点选。 */
	comments?: ViewerComments;
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

	const thumbnail = view === "thumbnail";
	// 编辑草稿时不进评论模式（块是草稿的，不是版本里的）
	const reading = !thumbnail && draft === null;
	const pick = reading && comments?.mode === "comment" ? comments.pick : null;
	const pins = reading ? (comments?.pins ?? NO_PINS) : NO_PINS;
	const locate = reading ? (comments?.locate ?? null) : null;
	// 评论模式、有图钉或要定位时按块渲染已保存的全文
	const text = doc?.text ?? "";
	const byBlock = pick !== null || pins.length > 0 || locate !== null;
	const saved = useMemo(() => {
		if (!byBlock) return null;
		const list = splitBlocks(text);
		return { list, texts: list.map((b) => clipText(b.body)) };
	}, [byBlock, text]);
	const placed = useMemo(
		() => (saved ? placePins(saved.texts, pins) : null),
		[saved, pins],
	);

	const articleRef = useRef<HTMLElement>(null);
	useEffect(() => {
		const article = articleRef.current;
		if (!locate || !saved || !article) return;
		const i =
			locate.block === null
				? null
				: relocateBlock(saved.texts, locate.block, locate.text);
		const el =
			i === null ? article : article.querySelector(`[data-block="${i}"]`);
		el?.scrollIntoView?.({ block: "center", behavior: "smooth" });
	}, [locate, saved]);

	if (content.state === "loading")
		return <p className="oryn-placeholder">{strings.loading}</p>;
	if (content.state === "error" || !doc)
		return (
			<p className="oryn-placeholder" role="alert">
				{strings.loadFailed}
			</p>
		);

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

	let body: ReactNode;
	if (draft !== null)
		body = blocks.map((b, i) =>
			active === i ? (
				<BlockEditor
					// 草稿每次变化都会重新拆块；按位置作 key，编辑框在提交前不会被换掉
					// biome-ignore lint/suspicious/noArrayIndexKey: 块没有稳定标识，位置就是身份
					key={i}
					source={b.body}
					onDone={(next) => {
						setDraft(replaceBlock(blocks, i, next));
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
		);
	else if (saved)
		body = saved.list.map((b, i) => {
			const here = placed?.byBlock.get(i);
			return (
				<div
					// biome-ignore lint/suspicious/noArrayIndexKey: 块没有稳定标识，位置就是身份
					key={i}
					data-block={i}
					className={
						pick ? "oryn-block oryn-block-comment" : "oryn-block-static"
					}
				>
					<Markdown text={b.body} components={components} />
					{pick ? (
						<button
							type="button"
							className="oryn-block-hit"
							aria-label={strings.commentBlock}
							onClick={(e) =>
								pick(
									{
										page: null,
										x: null,
										y: null,
										range: null,
										block: i,
										text: saved.texts[i] ?? "",
									},
									pickPoint(e).at,
								)
							}
						/>
					) : null}
					{here ? (
						<span className="oryn-block-pins" aria-hidden="true">
							{here.map((p) => (
								<PinDot key={p.n} pin={p} />
							))}
						</span>
					) : null}
				</div>
			);
		});
	else body = <Markdown text={doc.text} components={components} />;

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
			{!thumbnail && content.editable && draft === null && !pick && (
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
			{pick ? (
				<button
					type="button"
					className="oryn-btn oryn-doc-edit"
					onClick={(e) => pick(null, pickPoint(e).at)}
				>
					{strings.commentDocument}
				</button>
			) : null}
			{placed && placed.whole.length > 0 ? (
				<div className="oryn-doc-pins" aria-hidden="true">
					{placed.whole.map((p) => (
						<PinDot key={p.n} pin={p} />
					))}
				</div>
			) : null}
			<article ref={articleRef} className="oryn-doc">
				{body}
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
